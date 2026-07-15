# Agent 层指标

> 口径基于当前 Claude Code 源码。只保留源码已有事件、消息字段，或能由它们稳定计算的指标；未实现的空模块、依赖人工评价的概念指标和语义不完整的指标已删除。
>
> 类型：`直` 表示直接对现有事件或字段聚合；`派` 表示由多个现有事件或字段计算。

## 1. Tool 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| ★ 模型 Tool 调用量 | `count(assistant.content[type = 'tool_use'])` | 一次 `tool_use` 内容块代表模型发起的一次 Tool 调用；包含后续未进入实际执行的调用 | 直 |
| Tool 实际执行量 | `count(OTel tool_result)` | `tool_result`：进入 `tool.call()` 后成功返回或抛出非 `AbortError` 的终态事件；不含 Schema 失败、权限拒绝和取消 | 直 |
| ★ Tool 执行成功率 | `count(tool_result.success = 'true') / count(tool_result)` | `success`：实际执行终态；该口径只评价进入 `tool.call()` 的调用 | 派 |
| Schema 校验失败率 | `count(tengu_tool_use_error.error = 'InputValidationError') / 模型 Tool 调用量` | `InputValidationError`：`tool.inputSchema.safeParse()` 未通过 | 派 |
| Unknown Tool Rate | `count(tengu_tool_use_error.error starts_with 'No such tool available') / 模型 Tool 调用量` | 表示模型调用了当前可用 Tool 列表中不存在、且不能通过旧别名兼容的 Tool | 派 |
| ★ Tool 执行 P95 延迟 | `P95(tool_result.duration_ms)` | `duration_ms`：从调用 `tool.call()` 前开始计时，到成功返回或非取消异常结束；不含权限等待和 PreToolUse Hook | 直 |
| Tool Result 字符长度 | `avg(tengu_tool_use_success.toolResultSizeBytes)`、`P95(...)` | 字段名虽为 `toolResultSizeBytes`，源码实际使用 JavaScript 字符串 `.length`，因此这里按字符长度解释；只在成功事件上记录 | 直 |

## 2. 记忆指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Memory 提取尝试量 | `count(tengu_extract_memories_extraction) + count(tengu_extract_memories_error)` | 两类事件分别表示提取流程正常结束和进入异常分支 | 派 |
| ★ Memory 提取成功率 | `count(tengu_extract_memories_extraction) / Memory 提取尝试量` | 成功表示提取流程正常结束，不要求 `memories_saved > 0` | 派 |
| Memory 提取 P95 延迟 | `P95(duration_ms)`，事件取成功与失败事件并集 | `duration_ms`：本次提取流程从启动到结束的耗时 | 直 |
| Memory 保存产出率 | `sum(memories_saved) / sum(message_count)` | `memories_saved`：写入的主题记忆文件数，不含索引文件 `MEMORY.md`；`message_count`：本次处理的新消息数 | 派 |
| MEMORY.md 字符长度 | `avg(tengu_memdir_loaded.content_length)`、`P95(...)` | `content_length` 只对应入口文件 `MEMORY.md`；源码通过字符串 `.length` 计算，字段不是 UTF-8 字节数 | 直 |
| 记忆目录文件数 | `avg(tengu_memdir_loaded.total_file_count)` | `total_file_count`：入口文件所在记忆目录的直属文件数；目录读取失败时该字段缺失 | 直 |
| Recall Prefetch 启动量 | `count(tengu_memdir_prefetch_collected)` | 每个已创建的 Prefetch handle 在释放时记录一次；未满足启动条件的请求不会产生该事件 | 直 |
| ★ Recall Prefetch 生命周期 P95 | `P95(tengu_memdir_prefetch_collected.latency_ms)` | 已完成时为启动到完成耗时；尚未完成或被取消时为启动到 handle 释放的生命周期，不统一等于召回延迟 | 直 |
| Recall 到达消费点率 | `count(consumed_on_iteration >= 0) / Recall Prefetch 启动量` | `consumed_on_iteration >= 0` 只表示查询循环到达消费点，不保证召回结果非空或真正注入了记忆 | 派 |
| 首轮消费前完成率 | `count(hidden_by_first_iteration = true) / Recall Prefetch 启动量` | 该字段为真等价于 Prefetch 已完成且 `consumed_on_iteration = 0` | 派 |

## 3. 压缩指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| 自动压缩成功量 | `count(tengu_auto_compact_succeeded)` | 自动压缩成功后记录；同一次 Full Compaction 还会产生 `tengu_compact`，两者不能相加当作尝试量 | 直 |
| Full Compaction 成功量 | `count(tengu_compact)` | 完整上下文压缩成功事件，包含自动压缩和手动完整压缩 | 直 |
| Partial Compaction 成功量 | `count(tengu_partial_compact)` | 消息选择器触发的局部压缩成功事件 | 直 |
| 已记录 Compaction 失败量 | `count(tengu_compact_failed) + count(tengu_partial_compact_failed)` | 只覆盖源码明确记录的失败分支，不能视为所有压缩尝试的完整失败数 | 派 |
| ★ 消息载荷 Token 压缩率 | `avg((preCompactTokenCount - truePostCompactTokenCount) / preCompactTokenCount)`，事件取 `tengu_compact` | `truePostCompactTokenCount` 是压缩后消息载荷的粗略估算，不含完整 System Prompt、Tools 和 User Context | 派 |
| Compaction API Token | `avg(compactionInputTokens)`、`avg(compactionOutputTokens)`、`avg(compactionCacheReadTokens)`、`avg(compactionCacheCreationTokens)` | 这些字段是生成压缩摘要这次 API 调用的用量，不是压缩后上下文大小 | 直 |
| Payload 超阈值预测率 | `count(tengu_compact.willRetriggerNextTurn = true) / count(tengu_compact)` | 表示压缩后的消息载荷估算已超过自动压缩阈值；`false` 也可能因附加 System Prompt 等内容在下轮再次触发 | 派 |
| 重压缩迭代间隔 | `avg(turnsSincePreviousCompact)`，仅统计 `turnsSincePreviousCompact >= 0` | 表示距上次压缩经过的 Agent 查询循环迭代数，不是用户对话轮数或 API 请求次数 | 直 |

## 4. ReAct 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| ★ API 请求尝试量 | `count(tengu_api_success) + count(tengu_api_error)`，按目标 `querySource` 过滤 | 一次事件对应一次 API 请求尝试；必须用 `querySource` 区分主循环、Side Query、Compaction、Subagent 等来源 | 派 |
| API 请求成功率 | `count(tengu_api_success) / API 请求尝试量`，保持相同 `querySource` 过滤 | 只表示 API 请求是否正常完成，不表示用户任务完成 | 派 |
| 每次成功 API 响应的 Tool Action 数 | `模型 Tool 调用量 / count(tengu_api_success)`，保持相同查询范围 | 衡量成功模型响应平均产生多少个 `tool_use`；不能使用 `QueryEngine.turnCount` 代替 API 请求数 | 派 |
| SDK 技术成功率 | `count(result.subtype = 'success') / count(result)` | `result`：一次 SDK 查询的最终结果消息；技术成功不等于用户验收成功 | 派 |
| Max-turn 触顶率 | `count(result.subtype = 'error_max_turns') / count(result)` | 表示 SDK 查询因达到最大查询循环迭代数而结束 | 派 |
| Tool 错误后技术恢复率 | `count(存在 tool_result.is_error 且最终 result.subtype = 'success' 的 SDK 查询) / count(存在 tool_result.is_error 的 SDK 查询)` | 表示出现 Tool 错误后查询仍以 SDK 技术成功结束，不评价答案质量 | 派 |
| 单次 SDK 技术成功成本 | `sum(成功 SDK 查询内 tengu_api_success.costUSD) / 成功 SDK 查询数` | `costUSD` 是模型 API 成本；不包含外部 Tool 自身费用，且不能直接累加 SDK result 的累计 `total_cost_usd` | 派 |

## 5. Skill 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Session 启动 Skill 可用数量 | `count(tengu_skill_loaded)`，按 Session 分组 | 每个 Session 启动时，每个可用的 Prompt Skill 记录一次；不包含非 Prompt 类型命令 | 直 |
| Skill Listing 字符预算 | `max(tengu_skill_loaded.skill_budget)`，按 Session 分组 | 同一 Session 的各 Skill 事件重复携带同一预算；源码按上下文窗口的 1% Token 再乘 4 字符估算，字段本身是字符预算 | 直 |
| ★ 模型 Skill 调用量 | `count(tengu_skill_tool_invocation)` | 模型通过 Skill Tool 调用本地 Skill 时记录；当前可用本地执行路径为 `inline` 和 `fork` | 直 |
| Skill 执行模式分布 | `count(execution_context = mode) / 模型 Skill 调用量` | `execution_context`：当前外部构建可达的值为 `inline` 或 `fork`；Remote Skill 依赖的模块在当前源码快照中为空实现，不纳入指标 | 派 |
| Skill 嵌套调用占比 | `count(invocation_trigger = 'nested-skill') / 模型 Skill 调用量` | `nested-skill` 表示 Skill 在更深层查询上下文中触发；顶层模型主动调用为 `claude-proactive` | 派 |
| Skill Tool 技术成功率 | `count(tengu_tool_use_success.toolName = 'Skill') / count(tengu_tool_use_success/error.toolName = 'Skill')` | 只衡量 Skill Tool 是否正常处理完成；不衡量 Skill 指令遵循、适用性或任务收益；取消不进入分母 | 派 |

## 6. 权限指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Permission 终态决策量 | `count(OTel tool_decision)` | 每个事件都是最终 `accept` 或 `reject`；`ask` 是中间行为，不是该事件的终态值 | 直 |
| ★ Permission Prompt Rate | `count(权限分析事件包含 waiting_for_user_permission_ms) / count(权限分析终态事件)` | 等待字段只在真正向用户展示确认并得到终态决定时记录；自动允许或拒绝不带该字段 | 派 |
| 权限通过率 | `count(tool_decision.decision = 'accept') / Permission 终态决策量` | `accept`：最终允许执行 | 派 |
| 权限拒绝率 | `count(tool_decision.decision = 'reject') / Permission 终态决策量` | `reject`：最终拒绝执行 | 派 |
| ★ 用户等待 P95 | `P95(waiting_for_user_permission_ms)` | 从权限提示开始到用户完成选择的耗时；只统计实际弹出提示的决策 | 直 |
| 决策来源分布 | `count(tool_decision.source = source) / Permission 终态决策量` | `source` 可包括 `config`、`classifier`、`hook`、`user_temporary`、`user_permanent`、`user_abort`、`user_reject` 等 | 派 |
| 永久授权率 | `count(tengu_tool_use_granted_in_prompt_permanent) / count(tengu_tool_use_granted_in_prompt_permanent + tengu_tool_use_granted_in_prompt_temporary)` | 只比较用户在权限提示中选择的永久允许和临时允许，不混入配置、Hook 或 Classifier 自动允许 | 派 |
| Hook 决策率 | `count(tool_decision.source = 'hook') / Permission 终态决策量` | 表示最终决定来自 Permission Hook 的比例 | 派 |
| Classifier 决策率 | `count(tool_decision.source = 'classifier') / Permission 终态决策量` | 表示最终决定来自权限分类器的比例，不代表分类正确率 | 派 |
| 自动拒绝阈值触发量 | `count(tengu_auto_mode_denial_limit_exceeded)` | 自动模式累计或连续拒绝达到阈值时记录 | 直 |
| CLI 降级询问占比 | `count(tengu_auto_mode_denial_limit_exceeded.mode = 'cli') / count(tengu_auto_mode_denial_limit_exceeded)` | `cli` 路径在触发后降级为询问用户；`headless` 路径直接抛出 `AbortError` | 派 |

## 7. Subagent 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Subagent 选择量 | `count(tengu_agent_tool_selected)` | Agent 定义和模型已选定后、真正运行前记录；它是启动尝试量，不能严格解释为已经开始运行 | 直 |
| Subagent 完成事件量 | `count(tengu_agent_tool_completed)` | Subagent 正常生成可返回结果后记录 | 直 |
| ★ 完成事件覆盖率 | `count(tengu_agent_tool_completed) / count(tengu_agent_tool_selected)` | 表示选择事件中有完成事件的比例；部分非取消异常没有统一终态事件，因此不能直接命名为真实完成率 | 派 |
| 已记录终止率 | `count(tengu_agent_tool_terminated) / count(tengu_agent_tool_selected)` | 终止事件主要覆盖用户取消、`AbortError`、后台任务终止和部分合成终止路径，不覆盖所有运行错误 | 派 |
| ★ Subagent 终态 P95 延迟 | `P95(duration_ms)`，事件取 `tengu_agent_tool_completed` 与 `tengu_agent_tool_terminated` 并集 | `duration_ms`：从 Subagent 运行起点到正常完成或已记录终止的耗时 | 直 |
| 完成时上下文 Token | `avg(tengu_agent_tool_completed.total_tokens)` | `total_tokens` 来自最后一条 Assistant 消息的 usage，表示最终 API 上下文用量，不是整个 Subagent 的累计 Token 消耗 | 直 |
| Subagent Tool 数 | `avg(tengu_agent_tool_completed.total_tool_uses)` | `total_tool_uses`：Subagent 消息中出现的 Tool Use 总数 | 直 |
| 最终文本块数量 | `avg(tengu_agent_tool_completed.response_char_count)` | 字段名虽叫 `response_char_count`，源码实际赋值为最终文本内容块数组的 `.length`，因此这里按文本块数量解释 | 直 |
| Agent 消息数量 | `avg(tengu_agent_tool_completed.assistant_message_count)` | 字段名虽叫 `assistant_message_count`，源码实际使用 `agentMessages.length`，包含 Subagent 消息数组中的所有消息类型 | 直 |
| Async 选择占比 | `count(tengu_agent_tool_selected.is_async = true) / count(tengu_agent_tool_selected)` | `is_async`：选择时已确定在后台异步运行 | 派 |
| Fork 选择占比 | `count(tengu_agent_tool_selected.is_fork = true) / count(tengu_agent_tool_selected)` | `is_fork`：选择时走继承父上下文的 Fork 路径 | 派 |
| Resume 调用占比 | `count(API 事件 invocationKind = 'resume') / count(API 事件 invocationKind in ['spawn', 'resume'])` | `invocationKind` 只稀疏记录在每次 spawn/resume 边界的第一条 `tengu_api_success/error` 上，需限定 `invokingRequestId` 非空 | 派 |
