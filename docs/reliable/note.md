

## 学习目标

面试中能讲清楚：Claude Code 为什么比“裸 LLM 调工具”可靠、可靠性由哪些机制组成、它又有哪些边界。

## 先记这个

### 1 分钟总览

```text
Claude Code 的可靠性
≠ 模型每次都能回答正确
= 把不稳定的 LLM 放进一个可控制、可验证、可恢复的 Agent Runtime

事前：Schema + Plan + Permission + PreToolUse Hook
事中：工具执行闭环 + 并发隔离 + 错误回灌
事后：PostToolUse + Stop Hook + 独立验证
长期：Transcript + TaskList + Compaction + Session Memory
故障：Retry + Model Fallback + Resume + Checkpoint/Rewind
```

记忆口诀：

> 先限制，后执行；失败可见，结果验证；状态落盘，随时恢复。

### 面试一句话版本

Claude Code 不假设模型永远正确，而是通过工具权限、生命周期 Hook、真实执行反馈、独立验证、会话持久化、上下文压缩、重试回退和文件检查点，把模型的不确定性限制在一个可观测、可恢复的工程闭环中。

---

## 来源概览

| 来源 | 用途 | 限制 |
| --- | --- | --- |
| 当前仓库源码 | 判断真实运行链路 | 包含内部及 feature-gated 能力 |
| 仓库中文文档 | 帮助定位特性 | 结论仍以源码为准 |
| 之前的源码研究 | 提供检索线索 | 本次重新核对了关键路径 |

## 知识框架

```text
Claude Code 可靠性
├── 1. 行为可控：权限、Hook、Schema、Plan
├── 2. 执行闭环：tool_use → tool_result → 再推理
├── 3. 结果可信：测试、Stop Hook、Verification Agent
├── 4. 长任务连续：TaskList、Transcript、Resume、Compaction
├── 5. 故障恢复：Retry、Fallback、Checkpoint、Rewind
└── 6. 可观测性：Transcript、错误记录、工具事件、执行耗时
```

## 结构化笔记

### 1. 工具执行闭环：失败不是终点，而是下一轮输入

Claude Code 的主循环是 ReAct 风格：

```text
模型生成 tool_use
→ Runtime 校验并执行工具
→ 生成 tool_result
→ 把成功结果或错误重新加入上下文
→ 模型根据真实结果继续修正
```

工具异常不会被吞掉，而是包装成 `is_error: true` 的 `tool_result` 返回主循环，因此模型能看到真实错误并继续修复。[toolExecution.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolExecution.ts:1691)

工具结果会进入下一轮状态，循环持续到没有工具调用、Hook 阻止继续或者达到限制。[query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1678)

面试重点：

> Claude Code 的正确性不是一次生成得到的，而是通过“执行—观察—修正”逐步收敛的。

---

### 2. 权限与 Hook：减少错误的破坏半径

每次工具执行大致经过：

```text
输入 Schema 校验
→ PreToolUse Hook
→ Permission 决策
→ 工具执行
→ PostToolUse / PostToolUseFailure Hook
→ Stop / TaskCompleted Hook
```

权限系统支持 `allow / ask / deny`，并区分 `default`、`plan`、`dontAsk`、`bypassPermissions` 等模式。[permissions.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/types/permissions.ts:16)

关键细节：

- `deny` 和显式 `ask` 规则在 bypass 判断之前执行。
- 一些安全路径检查不会被 `bypassPermissions` 绕过。
- `dontAsk` 会把需要询问的操作转换成拒绝。
- Hook 返回 `allow`，也不能绕过显式 deny/ask 规则。

源码明确写了：“Hook allow 不会绕过 settings 中的 deny/ask”。[toolHooks.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolHooks.ts:321)

面试表达：

> Permission 决定“能不能做”，Hook 决定“在生命周期节点还能附加哪些企业规则”，两者不是同一个东西。

---

### 3. 并发可靠性：只并行安全操作

Claude Code 不会把所有工具盲目并行：

- 被认定为 concurrency-safe 的只读操作可以并行。
- 非只读、可能改变状态的操作按顺序执行。
- 如果安全性判断本身抛异常，默认按“不安全并发”处理。

对应实现见 [toolOrchestration.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolOrchestration.ts:19)。

这解决的是典型竞态问题：

```text
并行读取多个文件：可以
并行修改同一份状态：默认不做
无法判断是否安全：保守串行
```

---

### 4. Verification Agent：实现者和验证者分离

当该能力开启时，Verification Agent 不是阅读代码后说“看起来没问题”，而是要求：

- 实际运行 build、test、lint、typecheck。
- 前端实际点击，后端实际请求，CLI 检查输出和退出码。
- 至少执行一个边界、并发、幂等或异常输入测试。
- 每项检查必须包含真实命令和实际输出。
- 最后只能给出 `PASS / FAIL / PARTIAL`。
- 验证 Agent 禁止修改项目文件，避免自己修、自己验。

源码中的目标非常直接：不是确认实现有效，而是主动尝试破坏它。[verificationAgent.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/AgentTool/built-in/verificationAgent.ts:10)

但面试一定要补充边界：

> 当前源码中 Verification Agent 受 build feature 和远程开关控制，并不是所有外部版本都默认强制启用。[builtInAgents.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/AgentTool/builtInAgents.ts:64)

---

### 5. 会话持久化与恢复

Claude Code 在进入模型请求前，就先把用户消息写入 transcript。即使进程在 API 返回前被杀掉，这次会话也仍然可以恢复。[QueryEngine.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/QueryEngine.ts:436)

恢复时还会做一致性清理：

- 移除没有对应结果的残缺 `tool_use`。
- 清理孤立 thinking 消息。
- 过滤无效权限模式。
- 检测是否在一轮执行中被中断。
- 必要时注入“从中断位置继续”的元消息。
- 恢复 plan、skills、文件历史和 session metadata。

对应实现见 [conversationRecovery.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/conversationRecovery.ts:158)。

另外，TaskList 是独立的结构化执行状态：

```text
pending → in_progress → completed
owner
blocks
blockedBy
```

任务以 JSON 文件落盘，并通过文件锁处理多个 Agent 的并发更新。[tasks.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/tasks.ts:69)

最容易混淆的一组：

| 机制 | 保存什么 |
| --- | --- |
| Transcript | 完整会话事件和消息 |
| TaskList | 结构化任务进度和依赖 |
| Session Memory | 当前会话的语义摘要 |
| Plan 文件 | 准备怎么实施 |
| Checkpoint | 文件修改前后的可恢复状态 |

---

### 6. 上下文可靠性：不是简单截断

当上下文变长时，Claude Code 按层处理：

```text
大工具结果落盘
→ snip 删除低价值历史
→ microcompact 处理旧工具结果
→ context collapse 局部折叠
→ autocompact 生成会话摘要
```

压缩后的上下文不只有摘要，还可以继续携带：

- 保留的近期消息；
- 最近读取的文件；
- Plan 和 Plan Mode 状态；
- 已调用的 Skills；
- 后台 Agent 状态；
- Hook 结果。

最终顺序为：

```text
compact boundary
→ summary
→ preserved recent messages
→ attachments
→ hook results
```

见 [compact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/compact.ts:325)。

大工具输出也不是直接丢弃，而是保存到 session 的 `tool-results` 目录，模型上下文里只保留路径和预览，需要时再读取完整内容。[toolResultStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/toolResultStorage.ts:137)

这是一种典型的：

> 热数据放上下文，冷数据保存句柄，需要时再加载。

---

### 7. API 故障与长时间运行

API 层默认支持：

- 默认最多重试 10 次；
- 500ms 起步的指数退避；
- 随机抖动，避免请求同时重试；
- 401/部分 403 场景刷新认证；
- 连接重置时重建客户端；
- 连续 529 后，在配置了 fallback model 时触发模型回退；
- 上下文溢出时调整输出 Token 或触发压缩恢复。

源码见 [withRetry.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/withRetry.ts:52)。

无人值守的持续重试、心跳和最长五分钟退避同样存在，但属于 feature/env 控制能力，不能说成所有会话默认无限重试。

面试重点：

> Retry 解决的是网络、限流和服务容量问题，不能解决业务逻辑错误；业务错误仍需工具反馈、验证和修复闭环。

---

### 8. Checkpoint 与 Rewind：把失败成本降下来

Claude Code 会为跟踪到的文件建立快照，并关联到用户消息。用户可以选择历史消息执行 rewind：

```text
选择 messageId
→ 找到对应文件快照
→ 比较当前文件与备份
→ 恢复被修改文件
→ 删除当时尚不存在的新增文件
```

实现见 [fileHistory.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/fileHistory.ts:198)。

但它不是 Git：

- 只覆盖 Claude Code 跟踪到的文件。
- 不提供 Git commit 那样完整的仓库历史语义。
- 回退主要由用户或上层流程触发。

---

## 示例推演

假设 Claude Code 修改接口后测试失败：

```text
1. 模型提出编辑文件
2. Schema + PreToolUse + Permission 判断是否允许
3. 编辑前建立文件快照
4. 执行编辑
5. PostToolUse Hook 可以自动格式化或检查
6. 运行测试，测试错误作为 tool_result 返回
7. 主 Agent 根据真实错误继续修改
8. Stop Hook 或 Verification Agent 再次验证
9. 如果进程崩溃，通过 transcript + resume 继续
10. 如果修改方向完全错误，用户执行 rewind
```

这体现了三层可靠性：

```text
预防：权限、Schema、Plan
发现：工具错误、测试、Hook、Verification
恢复：Retry、Resume、Checkpoint、Rewind
```

## 常见误区

| 误区 | 正确理解 |
| --- | --- |
| Claude Code 可靠是因为模型更聪明 | 更关键的是模型外部 Runtime |
| 有重试就是自愈 | 重试只处理暂时性故障 |
| 有 Hook 就一定可靠 | Hook 需要正确配置，Hook 自身也可能失败 |
| Compaction 等于长期记忆 | Compaction 主要解决当前上下文连续性 |
| Checkpoint 等于 Git | 它是会话级文件恢复机制 |
| Verification Agent 所有版本默认强制 | 当前源码中仍受 feature gate 控制 |
| Claude Code 有完整自动自愈 | 有修复与回退原语，但没有默认的全自动回滚状态机 |
| bypassPermissions 完全没有限制 | 显式 deny/ask 和部分安全检查仍可优先拦截 |

## 90 秒面试答案

> 我认为 Claude Code 的可靠性主要来自模型外部的工程闭环。首先，它不是让模型直接执行命令，而是通过结构化工具、输入 Schema、权限模式和 PreToolUse Hook 控制动作边界。其次，它采用 ReAct 式执行循环，工具的成功结果和真实错误都会重新进入上下文，让模型根据运行结果修正，而不是凭空判断。再次，它通过 PostToolUse、PostToolUseFailure、Stop 和 TaskCompleted Hook 加入格式化、测试和完成条件检查；部分版本还提供独立 Verification Agent，把实现者和验证者分离。对于长任务，它使用 transcript、TaskList、Session Memory、compaction 和大型工具结果落盘维持状态；对于网络和服务故障，则使用指数退避、认证刷新和模型回退。最后，通过 checkpoint、resume 和 rewind 降低中断及错误修改的恢复成本。不过它并不是完整的自动自愈系统，因为验证失败到自动回滚再重试的链路并没有默认完整闭合。

## 复习检查清单

- [ ] 能用“预防、发现、恢复”解释可靠性。
- [ ] 能画出 `tool_use → tool_result → 再推理`。
- [ ] 能区分 Permission、Hook 和 Verification。
- [ ] 能区分 Transcript、TaskList、Session Memory、Checkpoint。
- [ ] 能解释 Compaction 为什么不是简单截断。
- [ ] 能说明 Retry 为什么不等于业务自愈。
- [ ] 能说出 Claude Code 自愈能力缺少的最后一环。

## 主动回忆卡片

### 1. Claude Code 可靠性的核心是什么？

不是让模型永不犯错，而是让错误可限制、可发现、可反馈、可恢复。

### 2. 工具失败后为什么还能继续？

失败会成为 `is_error: true` 的 `tool_result`，重新进入主循环供模型诊断。

### 3. Permission 和 Hook 有什么区别？

Permission 决定动作是否允许；Hook 在生命周期节点执行额外策略、校验或上下文注入。

### 4. Claude Code 有完整自愈吗？

没有。它有失败 Hook、修复循环、验证和文件回退，但缺少默认的“验证失败→自动回滚→重试或人工接管”完整状态机。

### 5. Compaction 如何保证连续性？

用摘要替换旧历史，同时保留近期消息以及文件、计划、Skill、后台 Agent 等关键附件。

### 6. 最值得借鉴的可靠性设计是什么？

不要要求 LLM 自己证明自己正确；应把实现、验证和恢复拆成独立机制。




## 补充

### 529是什么状态码

529：平台自定义的“服务器整体过载”。

### verify agent的调用时机

system prompt中规定
以下任务完成实现后、向用户汇报完成前，主 Agent 必须调用 Verification Agent：
修改了 3 个或更多文件；
后端/API 变更；
基础设施变更；
无论代码是主 Agent、fork 还是其他 subagent 实现的。

然后就使用了tool, 触发subagent

### tool的并发是怎么实现的

