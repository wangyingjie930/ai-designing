这里不是线程池，而是基于 JavaScript `Promise + AsyncGenerator` 实现的异步并发。

完整链路：

```text
模型一次返回多个 tool_use
→ 判断每个工具是否 concurrency-safe
→ 连续的安全工具组成一个并发批次
→ 不安全工具单独组成批次
→ 按批次依次执行
→ 批次内部通过 Promise.race 并发消费结果
```

## 1. 模型一次产生多个 `tool_use`

在模型流式输出中，Claude Code 收集所有工具调用：

```ts
const msgToolUseBlocks = message.message.content.filter(
  content => content.type === 'tool_use',
) as ToolUseBlock[]

toolUseBlocks.push(...msgToolUseBlocks)
```

见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:826)。

例如模型一次返回：

```text
Read(a.ts)
Read(b.ts)
Edit(c.ts)
Read(d.ts)
Bash("git status")
```

这些都会进入 `toolUseBlocks`。

## 2. 每个 Tool 自己声明能否并发

所有 Tool 都有：

```ts
isConcurrencySafe(input): boolean
isReadOnly(input): boolean
```

接口定义见 [Tool.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/Tool.ts:379)。

例如 `Read` 永远可以并发：

```ts
isConcurrencySafe() {
  return true
},

isReadOnly() {
  return true
},
```

见 [FileReadTool.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/FileReadTool/FileReadTool.ts:373)。

Bash 则根据具体命令判断：

```ts
isConcurrencySafe(input) {
  return this.isReadOnly?.(input) ?? false
},

isReadOnly(input) {
  const result = checkReadOnlyConstraints(input, ...)
  return result.behavior === 'allow'
},
```

见 [BashTool.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/BashTool/BashTool.tsx:434)。

因此：

```text
Bash("git status")  → 可以并发
Bash("rm -rf tmp")  → 不允许并发
```

需要纠正一个细节：

> `concurrency-safe` 不完全等于只读。它最终由 Tool 自己声明，例如带锁、内部保证并发一致性的状态工具，也可能返回 `true`。

## 3. 将工具调用切成批次

核心代码在 [toolOrchestration.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolOrchestration.ts:91)：

```ts
const isConcurrencySafe = parsedInput?.success
  ? (() => {
      try {
        return Boolean(
          tool?.isConcurrencySafe(parsedInput.data),
        )
      } catch {
        return false
      }
    })()
  : false

if (
  isConcurrencySafe &&
  acc[acc.length - 1]?.isConcurrencySafe
) {
  acc[acc.length - 1]!.blocks.push(toolUse)
} else {
  acc.push({
    isConcurrencySafe,
    blocks: [toolUse],
  })
}
```

规则是：

- 输入 Schema 校验失败：不并发。
- `isConcurrencySafe()` 抛异常：不并发。
- 连续的安全调用：放进同一个批次。
- 非安全调用：单独一个批次，成为并发屏障。

前面的例子会被划分为：

```text
Batch 1，并发：
├── Read(a.ts)
└── Read(b.ts)

Batch 2，串行：
└── Edit(c.ts)

Batch 3，并发：
├── Read(d.ts)
└── Bash("git status")
```

注意：Batch 3 必须等 Batch 2 完成后才开始。

## 4. 安全批次怎样真正并发

安全批次进入：

```ts
runToolsConcurrently(...)
```

实现：

```ts
yield* all(
  toolUseMessages.map(async function* (toolUse) {
    yield* runToolUse(
      toolUse,
      assistantMessage,
      canUseTool,
      toolUseContext,
    )
  }),
  getMaxToolUseConcurrency(),
)
```

见 [toolOrchestration.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolOrchestration.ts:152)。

默认最大并发数是 10：

```ts
function getMaxToolUseConcurrency(): number {
  return (
    parseInt(
      process.env.CLAUDE_CODE_MAX_TOOL_USE_CONCURRENCY || '',
      10,
    ) || 10
  )
}
```

可以通过环境变量修改：

```bash
CLAUDE_CODE_MAX_TOOL_USE_CONCURRENCY=5
```

## 5. `all()` 如何合并多个异步工具

真正的并发调度器在 [generators.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/generators.ts:31)：

```ts
const waiting = [...generators]
const promises = new Set()

while (
  promises.size < concurrencyCap &&
  waiting.length > 0
) {
  const gen = waiting.shift()!
  promises.add(next(gen))
}
```

它先启动不超过并发上限的 AsyncGenerator，然后：

```ts
const {
  done,
  value,
  generator,
  promise,
} = await Promise.race(promises)
```

谁先产生结果，就先处理谁：

```ts
if (!done) {
  promises.add(next(generator))
  yield value
} else if (waiting.length > 0) {
  const nextGen = waiting.shift()!
  promises.add(next(nextGen))
}
```

因此它本质上是：

```text
启动最多 10 个异步工具
→ Promise.race 等待最先有输出的工具
→ 立刻 yield 这个输出
→ 继续拉取该工具的下一条输出
→ 某个工具完成后，从等待队列补一个
```

不是多线程计算，而是 Node/Bun 事件循环并发等待：

- 网络请求；
- 文件 IO；
- 子进程；
- MCP 调用；
- 工具进度事件。

## 6. 为什么并发时不直接修改共享 Context

并发工具返回的 `contextModifier` 不会立即应用，而是先按 `toolUseID` 暂存：

```ts
queuedContextModifiers[toolUseID].push(
  modifyContext,
)
```

并发批次全部完成后，再按照模型产生工具调用的原始顺序应用：

```ts
for (const block of blocks) {
  const modifiers = queuedContextModifiers[block.id]

  for (const modifier of modifiers) {
    currentContext = modifier(currentContext)
  }
}
```

见 [toolOrchestration.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolOrchestration.ts:30)。

这避免了：

```text
Tool A 和 Tool B 同时完成
→ 同时修改共享 Context
→ 最终状态取决于谁先完成
```

换成：

```text
工具可以并发执行
→ 状态变更延迟提交
→ 按原始 tool_use 顺序确定性合并
```

## 7. 新版还有 StreamingToolExecutor

开启流式工具执行后，工具甚至不必等模型整条消息生成完；一个 `tool_use` block 流出来后就立即入队：

```ts
for (const toolBlock of msgToolUseBlocks) {
  streamingToolExecutor.addTool(
    toolBlock,
    message,
  )
}
```

见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:837)。

其调度条件是：

```ts
return (
  executingTools.length === 0 ||
  (
    isConcurrencySafe &&
    executingTools.every(t => t.isConcurrencySafe)
  )
)
```

见 [StreamingToolExecutor.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/StreamingToolExecutor.ts:126)。

含义是：

```text
当前没有工具执行
→ 任何工具都能启动

当前已经有安全工具执行
+ 新工具也安全
→ 可以同时启动

当前存在不安全工具
或新工具不安全
→ 必须等待独占执行
```

流式版本还会：

- 进度消息立即返回；
- 最终结果按工具接收顺序输出；
- Bash 并发调用失败时取消相关 sibling Bash；
- Read/WebFetch 失败不会取消其他独立读取。

一句话总结：

> Claude Code 先由每个 Tool 的 `isConcurrencySafe(input)` 做动态安全判断，再把连续安全调用组成并发批次，通过 AsyncGenerator 和 `Promise.race` 限流执行；不安全调用充当串行屏障，共享状态则延迟并按原始工具顺序合并。