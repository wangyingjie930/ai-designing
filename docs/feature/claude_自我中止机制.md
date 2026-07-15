# Claude Code 的自我中止机制

## 学习目标

读完后应该能够说清：

- Claude Code 陷入工具调用循环时，会不会自动发现并中止。
- 正常结束、轮数上限、费用上限、局部超时和人工中断的区别。
- `--max-turns` 中的 `turn` 究竟如何计数。
- `--max-budget-usd` 能限制什么，不能限制什么。

## 先记这个

### 1 分钟总览

结论：**Claude Code 有局部中止和资源上限，但没有在交互式主循环中默认开启的“语义死循环检测器”。**

```text
模型不再调用工具
-> 正常结束

模型持续调用工具
-> 交互模式下默认没有 maxTurns 硬上限

无人值守执行
-> 可以用 --max-turns 和 --max-budget-usd 设置双保险

用户发现异常
-> 按 Esc，通过 AbortController 中断当前执行链
```

### 复述一句话版本

> Claude Code 有超时、重试上限、轮数上限、费用上限和人工中断，但交互式主循环没有默认的语义死循环检测；是否停止仍主要依赖模型不再调用工具。

## 知识框架

```text
Claude Code 的中止机制
-> 正常结束
   -> 模型不再输出 tool_use
-> 硬上限
   -> --max-turns
   -> --max-budget-usd
-> 局部卡死保护
   -> Bash 超时/转后台
   -> API 流无数据 watchdog
   -> API 重试上限
-> 人工中断
   -> Esc / AbortController
-> 关键边界
   -> 没有通用的重复行为识别
   -> 局部超时 ≠ 整个任务必然结束
   -> 重试/恢复 ≠ 完整自愈
```

## 1. 主循环正常如何停止

Claude Code 的主循环可以简化为：

```text
调用模型
-> 模型输出 tool_use
-> 执行工具
-> 把 tool_result 放回上下文
-> 再次调用模型
```

源码不依赖 `stop_reason === "tool_use"` 判断是否继续，因为这个字段并不总是可靠。它在流式输出期间收集真实的 `tool_use` block：

- 出现 `tool_use`：`needsFollowUp = true`，执行工具并继续下一轮。
- 没有 `tool_use`：执行 Stop Hook 等收尾逻辑后返回 `completed`。

见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:551) 和 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1357)。

因此，默认情况下的“主动停止”实际上是模型做出的决定：模型认为任务完成，于是不再输出工具调用。

## 2. 为什么这不等于“自动死循环检测”

假设模型不断执行：

```text
Read 文件
-> 判断信息不足
-> 再次 Read 同一文件
-> 仍然判断信息不足
-> 继续 Read
```

当前交互式主循环中没有通用的：

```text
sameActionCount > N
clarificationNotImproving === true
semanticLoopDetected === true
```

交互式 REPL 调用 `query()` 时没有传入 `maxTurns`，见 [REPL.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/screens/REPL.tsx:2825)。而主循环的轮数保护只在 `maxTurns` 存在时才生效，见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1704)。

系统提示词确实要求模型：

- 不要盲目重试相同操作。
- 先诊断失败原因。
- 调查后仍然真正卡住时，再向用户求助。

见 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:233)。

但这属于对模型的软约束，不是程序层强制保证。

## 3. `--max-turns 20` 是什么

```bash
claude -p --max-turns 20 "执行任务"
```

它表示：**在非交互模式下，最多进行 20 个 Agentic Turn。**

### 3.1 `turn` 不是一轮用户对话

这里的 `turn` 是一次主循环模型调用轮次，可以近似理解为一次 API sampling round。

它不等于每一次底层 HTTP 请求尝试。同一个 Agentic Turn 内发生的网络重试、流式转非流式 fallback 等恢复动作，通常不会单独增加 `turnCount`。

```text
第 1 轮：模型决定调用 Read
          -> 执行 Read

第 2 轮：模型根据 Read 结果调用 Grep + Read
          -> 执行这批工具

第 3 轮：模型决定编辑文件
          -> 执行 Edit

第 4 轮：模型运行测试
          -> 执行 Bash

第 5 轮：模型不再调用工具，给出总结
```

这个例子一共是 5 个 turn。

同一次模型输出中即使包含多个工具调用，也只会在整批工具结果收集完、准备递归进入下一轮时，把 `turnCount` 加一。见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1678)。

### 3.2 到达第 20 轮时会发生什么

`turnCount` 从 1 开始。每次已经得到工具结果、即将进入下一次模型调用时，计算：

```ts
const nextTurnCount = turnCount + 1

if (maxTurns && nextTurnCount > maxTurns) {
  return { reason: 'max_turns' }
}
```

见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:276) 和 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1678)。

因此，如果第 20 轮模型仍然调用了工具：

1. 这批工具会先执行。
2. Claude Code 准备进入第 21 轮时发现超过上限。
3. 主循环返回 `max_turns`。
4. 不会再调用模型生成第 21 轮的最终总结。

所以 `--max-turns` 保证“最多再跑多少轮”，不保证被截停时已经有完整的最终答案。

## 4. `--max-budget-usd 2` 是什么

```bash
claude -p --max-budget-usd 2 "执行任务"
```

它表示：**Claude Code 记录的本次非交互运行累计模型 API 费用达到 2 美元后停止。**

源码判断条件是：

```ts
if (maxBudgetUsd !== undefined && getTotalCost() >= maxBudgetUsd) {
  return
}
```

见 [QueryEngine.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/QueryEngine.ts:971)。

### 4.1 它不是绝对精确的预付费闸门

它是在执行过程中检查已累计的费用，不是在发起每次 API 请求前精确预测这次请求的最终费用。

例如：

```text
发起当前请求前：$1.95
当前请求结束后：$2.06
检查到累计费用 >= $2
-> 停止
```

因此最终费用可能略高于配置值。

### 4.2 它不限制什么

`--max-budget-usd` 不是：

- 墙钟运行时间上限。
- Token 数量上限。
- Bash 命令运行时间上限。
- 所有外部 MCP 服务费用的通用上限。

它保护的是 Claude Code 内部记录的模型 API 调用成本。

## 5. 两个参数一起使用

```bash
claude -p --max-turns 20 --max-budget-usd 2 "执行任务"
```

它的语义是：

```text
正常完成
   或
达到 20 个 Agentic Turn
   或
累计模型 API 费用达到 $2

-> 谁先达到，就因谁停止
```

例如：

| 实际执行情况 | 结果 |
| --- | --- |
| 第 8 轮正常完成，花费 `$0.40` | 正常返回，两个上限都不触发 |
| 第 20 轮仍然需要继续，花费 `$0.80` | 因 `max_turns` 停止 |
| 第 7 轮费用累计到 `$2.04` | 因 `max_budget_usd` 停止 |

`20` 和 `2` 只是示例值，不是 Claude Code 的默认限制。

### 5.1 为什么必须有 `-p`

`-p` / `--print` 表示非交互输出模式，适合脚本和无人值守任务。

CLI 对这两个参数的描述都明确写着 `only works with --print`，见 [main.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/main.tsx:982)。

普通交互式 Claude Code 不会因为这个示例自动得到 20 轮、`$2` 的默认限制。

## 6. 局部卡死保护

### 6.1 Bash 命令超时

Bash 命令的默认超时时间是 120 秒，默认最大可配置时间是 600 秒，见 [timeouts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/timeouts.ts:1)。

但超时并不总是直接杀掉命令：

- 允许自动后台化的命令，超时时可能被转为后台任务。
- 不允许自动后台化的命令，超时时才会收到 `SIGTERM`。

见 [ShellCommand.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/ShellCommand.ts:135) 和 [BashTool.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/BashTool/BashTool.tsx:965)。

所以“Bash 工具不再阻塞主循环”不一定等于“子进程已经停止”。

### 6.2 API 流无数据 watchdog

源码包含流式输出 watchdog：如果连续一段时间收不到 chunk，主动释放流资源，然后转入非流式 fallback / retry。

- 默认阈值：90 秒。
- 但是受 `CLAUDE_ENABLE_STREAM_WATCHDOG` 环境变量控制，不能表述成所有构建中默认必然开启。
- 触发后主要是切换请求模式并重试，不是立即终止整个任务。

见 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:1868) 和 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:2308)。

### 6.3 API 重试上限

普通 API 重试默认上限是 10 次，见 [withRetry.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/withRetry.ts:52)。

但特殊的无人值守重试模式可以对 `429` / `529` 无限重试，以长退避和心跳维持运行，见 [withRetry.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/withRetry.ts:91)。

因此“有重试”和“有统一自我中止”是两件事。

## 7. 用户按 Esc 时怎么中断

用户按 Esc 后，交互界面会：

1. 暂停 proactive 模式。
2. 保留已经流式生成的部分文本。
3. 对当前 `AbortController` 调用 `abort('user-cancel')`。
4. 清理当前权限请求或提问队列。

见 [REPL.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/screens/REPL.tsx:2138)。

`query` 主循环分别在模型流输出结束处和工具执行结束处检查 `signal.aborted`，然后补齐必要的中断 `tool_result` / 提示消息并返回。见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1011) 和 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1484)。

这是可靠的人工中断通道，不是 Claude Code 自己识别出了循环。

## 8. 机制对照表

| 场景 | 机制 | 是否自动终止整个任务 |
| --- | --- | --- |
| 模型认为任务完成 | 不再输出 `tool_use` | 是，正常结束 |
| 模型重复调用相同工具 | 提示词要求不要盲目重试 | 不保证，属于软约束 |
| 非交互执行超过轮数上限 | `--max-turns` | 是 |
| 非交互执行达到费用上限 | `--max-budget-usd` | 是，但可能略微超额 |
| Bash 命令长时间不返回 | 超时杀死或转后台 | 不一定，只是解除主循环阻塞 |
| API 流长时间没有 chunk | watchdog + fallback/retry | 不一定 |
| 用户按 Esc | `AbortController.abort()` | 是，但是人工中断 |

## 9. 常见误区

### 误区一：有 `--max-turns`，所以交互模式默认不会无限循环

不对。`--max-turns` 是 `--print` 非交互模式的可选参数，交互式 REPL 没有默认传入该值。

### 误区二：`--max-turns 20` 等于最多调用 20 个工具

不对。它限制的是模型调用轮次，一轮中可能没有工具，也可能有多个工具。

它也不是最多 20 次底层 HTTP 请求；同一 Agentic Turn 中可能发生额外的网络重试或 fallback。

### 误区三：`--max-budget-usd 2` 保证绝不会超过 `$2.00`

不对。它检查的是已经累计的费用，最后一次调用可能让总费用略高于阈值。

### 误区四：流式 watchdog 触发后，任务一定终止

不对。它优先解决的是某次流式请求卡住，后续可能切换成非流式请求并重试。

### 误区五：有超时和重试就等于完整自愈

不对。超时、重试、fallback 和人工中断是恢复原语，不代表系统已经具备了语义级的循环识别、自动验证和自动回滚闭环。

## 10. 面试回答思路

可以按下面的顺序回答：

```text
先给结论
-> 有局部中止，没有默认通用语义死循环检测

再讲正常主循环
-> tool_use -> tool_result -> 下一轮
-> 不再有 tool_use 时完成

再讲硬保护
-> 非交互模式可配 maxTurns / maxBudgetUsd

再讲局部保护
-> Bash 超时、流式 watchdog、有限重试

最后讲人工逃生通道
-> Esc -> AbortController
```

30 秒回答：

> Claude Code 不是默认带有通用的语义死循环检测器。它的主循环依赖模型是否继续输出 `tool_use`；不再调用工具时正常结束。它另外提供非交互模式的 `--max-turns` 和 `--max-budget-usd`、Bash 超时、API watchdog、重试上限和 Esc 中断。所以应该把它描述为“多层局部保护”，而不是“默认完整自愈”。

## 11. 复习检查清单

- [ ] 能说清主循环为什么依赖 `tool_use` 决定是否继续。
- [ ] 能解释交互式 REPL 为什么没有默认 `maxTurns`。
- [ ] 能区分用户对话轮次、Agentic Turn 和工具调用次数。
- [ ] 能解释 `--max-turns 20` 触发时为什么可能没有最终总结。
- [ ] 能解释 `--max-budget-usd 2` 为什么可能略微超额。
- [ ] 能区分“转后台”和“杀掉进程”。
- [ ] 能说清 watchdog / retry 为什么不等于整个任务终止。

## 12. 主动回忆卡片

### 卡片 1：Claude Code 如何判断主循环正常结束？

答：本轮模型没有产生 `tool_use` block，且 Stop Hook 等收尾逻辑不要求继续时，返回 `completed`。

### 卡片 2：`--max-turns 20` 是最多调用 20 次工具吗？

答：不是。它限制的是模型调用轮次；一轮可以包含多个工具调用。

### 卡片 3：`--max-budget-usd 2` 能保证最终费用不超过 `$2.00` 吗？

答：不能绝对保证。它是在执行过程中检查累计费用，最后一次 API 调用可能让总额略高于阈值。

### 卡片 4：为什么说 Bash 超时不一定是任务终止？

答：允许自动后台化的命令在超时后可能被转为后台任务，主循环恢复响应，但子进程仍可能继续运行。

### 卡片 5：Claude Code 有默认语义死循环检测吗？

答：在当前检查的交互式主循环中没有看到通用的重复行为识别和默认轮数上限；主要依赖模型收敛、局部保护和用户 Esc 中断。
