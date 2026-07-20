## 先记这个

Claude Code 的长期记忆召回，本质是：

```text
Markdown 文件存储
→ 扫描文件的 frontmatter
→ 旁路 Sonnet 选择最多 5 条相关记忆
→ 读取正文
→ 作为隐藏的 <system-reminder> 注入主模型上下文
```

它不是向量数据库，也没有 embedding / cosine similarity。它采用的是“LLM 充当召回路由器”。

## 源码调用链

### 1. 记忆存在哪里

默认目录是：

```text
~/.claude/projects/<项目根目录编码>/memory/
```

项目标识使用 canonical Git root，因此同一个仓库的不同 worktree 可以共享记忆。路径解析见 [paths.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/paths.ts:200)。

每条记忆是一个 Markdown 文件，常见 frontmatter 大致是：

```md
---
name: testing preference
description: 用户希望集成测试连接真实数据库
type: feedback
---

集成测试必须使用真实数据库……
```

支持四种类型：`user`、`feedback`、`project`、`reference`，见 [memoryTypes.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/memoryTypes.ts:14)。

### 2. 用户输入后启动异步召回

每个用户 turn 只启动一次：

```ts
startRelevantMemoryPrefetch(messages, toolUseContext)
```

入口在 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:297)，实际实现在 [attachments.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/attachments.ts:2361)。

它会：

- 取最后一条真正的用户消息，跳过系统注入的 `isMeta` 消息。
- 默认搜索当前项目的 auto-memory。
- 如果输入里 `@agent-xxx`，则只搜索该 Agent 的独立记忆目录。
- 与主模型生成、工具执行并行运行，不阻塞当前请求。

### 3. 扫描候选记忆

`scanMemoryFiles()` 会：

- 递归查找 `.md` 文件。
- 排除 `MEMORY.md`。
- 每个文件只读取前 30 行。
- 提取 `type`、`description`、修改时间。
- 按修改时间倒序。
- 最多保留最近 200 个文件。

实现见 [memoryScan.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/memoryScan.ts:35)。

最终给召回模型的清单类似：

```text
- [feedback] testing.md (2026-07-18T10:00:00Z): 用户要求集成测试使用真实数据库
- [project] auth-rewrite.md (...): 认证重构的业务背景
```

### 4. 用旁路 Sonnet 选择记忆

`findRelevantMemories()` 调用一个 `sideQuery`，模型使用默认 Sonnet，只看：

- 当前用户问题
- 文件名
- 类型
- 修改时间
- `description`
- 最近成功使用过的工具

然后通过 JSON Schema 返回最多 5 个文件名：

```json
{
  "selected_memories": [
    "testing.md",
    "auth-rewrite.md"
  ]
}
```

提示词明确要求“没有明显帮助就返回空数组”，见 [findRelevantMemories.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/findRelevantMemories.ts:18)；模型调用在同文件的 [第 97 行](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/findRelevantMemories.ts:97)。

所以这里的“相关性”是 Sonnet 判断出来的，不是关键词匹配分数。

### 5. 读取正文并注入主模型

选中后才读取正文，并限制：

- 每轮最多 5 个文件。
- 每个最多 200 行。
- 每个最多 4 KB。
- 当前 compact 区间累计最多 60 KB。

见 [attachments.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/attachments.ts:268) 和 [正文读取逻辑](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/attachments.ts:2279)。

随后转换成隐藏消息：

```text
<system-reminder>
Memory (saved yesterday): /path/to/testing.md

记忆正文……
</system-reminder>
```

转换发生在 [messages.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/messages.ts:3708)。因此所谓“想起来”，实际上是 CLI 把记忆重新塞进了模型上下文。

## 两套召回模式

代码里由 `tengu_moth_copse` 特性开关控制：

| 模式 | 召回方式 |
|---|---|
| 旧模式 | 启动时把 `MEMORY.md` 索引整体注入上下文，Claude 根据索引决定是否继续 `Read` 具体文件 |
| 新模式 | 不注入 `MEMORY.md`，扫描所有记忆 frontmatter，由旁路 Sonnet 自动选择正文 |

`MEMORY.md` 的加载在 [claudemd.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/claudemd.ts:979)，新模式下过滤掉索引的逻辑在 [claudemd.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/claudemd.ts:1137)。

## 一个很值得注意的时序

新版召回虽然在主模型请求前启动，但召回结果不会进入主模型的第一次 API 调用。

它会等主模型完成一轮工具调用后再检查：

```ts
if (pendingMemoryPrefetch.settledAt !== null) {
  // 注入 relevant_memories
}
```

见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1592)。

从控制流看，如果主模型首轮没有工具调用、直接回答结束，会在 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1062) 提前返回，召回结果可能不会参与这次回答。这个设计主要是为了把召回延迟藏在工具执行时间里，更适合多步 Agent 任务。

还有一个明显边界：代码用“是否包含空白字符”判断是不是单词输入：

```ts
if (!input || !/\s/.test(input.trim())) {
  return undefined
}
```

所以像你这句没有空格的中文输入，在新版 prefetch 路径下会被直接跳过。源码注释说是过滤“单词提示”，但这个判断对连续中文并不友好。

## 容易混淆的三种“记忆”

- `auto memory`：跨会话、项目级持久记忆，使用上面这套相关性召回。
- `CLAUDE.md`：项目指令。根目录内容启动时加载；读取深层文件后，再按路径懒加载对应目录的 `CLAUDE.md`，不是语义检索。
- `Session Memory`：当前任务的持续摘要，主要服务于 compact 和恢复会话。后台 forked agent 定期更新 Markdown，压缩时把摘要放回上下文，并不是每个问题都进行相关性召回。

复述一句话：Claude Code 把长期记忆保存成项目级 Markdown，通过一个旁路 Sonnet读取文件描述并选择最多五条相关记忆，再把正文包装成隐藏系统提醒注入主模型上下文。