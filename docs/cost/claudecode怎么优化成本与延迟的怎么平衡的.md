## 学习目标

理解 Claude Code 如何同时控制：

- Token/API 成本
- 用户等待时间
- 复杂任务的完成质量

## 先记这个

Claude Code 的核心策略不是“全部换成便宜模型”，而是：

> 主循环保能力，辅助任务用小模型；重复上下文靠缓存，过大上下文逐级压缩；等待时间靠流式、并发和预取隐藏。

它优化的是“完成整个任务的总成本”，不是单次请求的最低价格。主模型太弱导致反复调用、错误修改，最终反而更贵。

## 知识框架

```text
成本与延迟优化
├── 模型预算：Sonnet / Opus / Haiku / Effort
├── 上下文预算：缓存 / 工具按需加载 / 大结果外置 / 压缩
├── 时间调度：流式输出 / 工具并发 / 后台预取
└── 硬约束：最大金额 / 最大轮数 / 降级与冷却
```

## 结构化笔记

| 层面 | Claude Code 的做法 | 节省什么 | 如何保护质量 |
|---|---|---|---|
| 主模型 | 多数用户默认 Sonnet；Max、Team Premium 默认 Opus | 避免所有请求都用 Opus | 主循环仍使用足够强的模型 |
| 辅助模型 | 标题、工具摘要等固定小任务调用 Haiku，关闭 thinking、不给工具 | 模型单价和推理成本 | 只把低风险、窄任务交给 Haiku |
| Prompt Cache | 缓存 system prompt、工具定义和历史前缀 | 重复输入成本、通常也减少首 Token 延迟 | 保证缓存前缀字节稳定 |
| 上下文治理 | 工具定义延迟加载、大输出落盘、旧结果清理、自动压缩 | 输入 Token 和超长上下文延迟 | 保存完整输出路径，最近信息尽量保留 |
| 执行调度 | 流式生成时启动工具；安全工具并发 | 墙钟时间 | 写操作和非并发安全工具串行 |
| 用户控制 | `/model`、`/effort`、`/fast`、预算上限 | 根据场景取舍 | 把明显的价格/质量选择交给用户 |

### 1. 模型分层，而不是每轮自动猜模型

当前源码中，没有发现一个统一的“任务复杂度评分器”，每轮自动在 Haiku、Sonnet、Opus 之间切换。

默认模型主要根据用户套餐决定：多数用户是 Sonnet，Max 和 Team Premium 是 Opus；用户可以显式切换模型。[model.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/model/model.ts:169)

但固定的小任务会走 `queryHaiku()`：

- 使用 small/fast model
- 关闭 thinking
- 不提供工具
- 默认不启用 Prompt Cache

见 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:3241)。

还有一个很典型的可选模式 `opusplan`：

- Plan 阶段使用 Opus
- 普通执行阶段使用 Sonnet

这相当于把贵模型集中在“做关键决策”的阶段。

### 2. Prompt Cache 是成本优化主力

源码中的价格表显示，缓存读取价格大约是普通输入的十分之一：

- Sonnet：普通输入 `$3/Mtok`，缓存读取 `$0.3/Mtok`
- Opus 4.6：普通输入 `$5/Mtok`，缓存读取 `$0.5/Mtok`
- Haiku 4.5：普通输入 `$1/Mtok`，缓存读取 `$0.1/Mtok`

但缓存写入约为普通输入的 1.25 倍。因此它的取舍是：

> 第一轮稍贵，后续多轮大幅便宜。

见 [modelCost.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/modelCost.ts:35)。

Claude Code 为了提高命中率，会刻意保持 system prompt、工具、模型、消息前缀和 thinking 配置稳定；派生任务也会尽量复用主线程的缓存前缀。[forkedAgent.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/forkedAgent.ts:46)

### 3. 尽量少把内容送进模型

它不是简单截断，而是“保留句柄，需要时再读取”：

- MCP 等工具定义通过 Tool Search 按需加载，避免每轮携带全部 JSON Schema。[toolSearch.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/toolSearch.ts:154)
- 超大工具结果保存到磁盘，只给模型约 2KB 预览和文件路径。
- 同一条消息内工具结果超预算时，优先外置最大的结果。[toolResultStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/toolResultStorage.ts:739)
- 接近上下文上限时才触发 Auto Compact，并预留输出和恢复空间。[autoCompact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/autoCompact.ts:62)

完整压缩仍使用主循环模型，但关闭 thinking，并限制工具集合。这意味着它愿意为“摘要质量”付一点成本，同时减少不必要的深度推理。[compact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/compact.ts:1292)

### 4. 延迟主要靠“重叠执行”

模型返回 `tool_use` 后，不一定等整段响应完全结束才开始工具执行；Streaming Tool Executor 会尽早启动工具。[query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:837)

多个并发安全工具——通常是 Read、Grep、Glob 等只读操作——可以并发执行，默认并发上限为 10；修改类或非安全工具保持串行。[toolOrchestration.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolOrchestration.ts:19)

此外，记忆检索、技能发现、工具摘要等辅助工作会在主模型流式输出和工具执行期间后台运行；如果还没完成，就跳过而不是阻塞当前轮次。[attachments.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/attachments.ts:2334)

### 5. Fast Mode 是“花钱买延迟”

这是最直白的成本—延迟交换。

当前源码中 Opus 4.6：

- 普通模式：输入 `$5`、输出 `$25/Mtok`
- Fast Mode：输入 `$30`、输出 `$150/Mtok`

价格约为六倍。因此 `/fast` 不是省钱功能，而是明确的低延迟溢价。[modelCost.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/modelCost.ts:62)

相比之下，`effort=low/medium/high/max` 控制思考深度。源码对 Opus 推荐 `medium`，理由正是平衡速度、智能和限额使用。[effort.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/effort.ts:260)

## 示例推演

用户要求“排查并修复一个跨模块 Bug”：

1. Sonnet/Opus 负责理解问题和决定修改方案。
2. 多个 `Grep`、`Read` 并发执行。
3. 大日志只发送预览，完整内容保存到文件。
4. 下一轮复用 Prompt Cache，不重新计算整个历史前缀。
5. 工具摘要等非关键任务由 Haiku 在后台完成。
6. 只有上下文接近上限时，才执行有损摘要压缩。

所以它的平衡原则可以概括为：

> 不轻易降低关键决策的智能，而是优先消灭重复 Token、无关上下文和串行等待。

## 常见误区

- `/fast` 会更省钱：错误，它是显著加价换速度。
- Auto Mode 会自动选择模型：当前源码里的 Auto Mode 主要是权限决策分类，不是通用模型路由。
- Compact 越频繁越省钱：不一定。压缩本身也调用模型，而且可能损失细节，所以只在上下文压力出现时触发。
- 并发越多越好：修改操作并发容易产生冲突，因此只并发经过 `isConcurrencySafe` 判断的工具。

## 主动回忆卡片

- Claude Code 最主要的成本优化是什么？  
  Prompt Cache、上下文减量，以及辅助任务使用小模型。

- 最主要的延迟优化是什么？  
  流式执行、安全工具并发、后台预取。

- 它如何避免“为了便宜损失质量”？  
  主循环保留 Sonnet/Opus，大结果可回读，最近上下文优先保留，有损压缩最后才做。