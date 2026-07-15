好。下面直接替换上一版的 7 张表；源码链接和其他说明保持不变，只新增“变量含义”列。

## 1. Tool 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| ★ Tool 调用量 | `count(tool_attempt)` | `tool_attempt`：模型发起的一次工具调用，包括成功、失败、取消和权限拒绝 | 直 |
| ★ Tool 成功率 | `success_calls / tool_attempts` | `success_calls`：正常返回结果的次数；`tool_attempts`：所有调用尝试次数 | 派 |
| Tool 执行错误率 | `execution_errors / executed_calls` | `execution_errors`：进入执行后发生错误的次数；`executed_calls`：真正进入 Tool 执行阶段的次数，不含权限拒绝 | 派 |
| Schema 合法率 | `valid_input_calls / tool_attempts` | `valid_input_calls`：参数字段、类型、必填项均符合 Schema 的次数；`tool_attempts`：所有调用尝试 | 派 |
| Unknown Tool Rate | `unknown_tool_calls / tool_attempts` | `unknown_tool_calls`：调用了不存在、未注册或未暴露 Tool 的次数 | 派 |
| Tool 取消率 | `cancelled_calls / tool_attempts` | `cancelled_calls`：被用户或系统取消的调用次数；`tool_attempts`：所有调用尝试 | 派 |
| ★ Tool P95 延迟 | `P95(duration_ms)` | `duration_ms`：一次 Tool 从开始执行到结束的毫秒数；P95 表示 95% 的调用不超过该耗时 | 直 |
| 权限阻断率 | `rejected_calls / tool_attempts` | `rejected_calls`：权限检查未通过的次数；`tool_attempts`：所有调用尝试 | 派 |
| Tool Result 大小 | `avg(tool_result_size_bytes)`、`P95(tool_result_size_bytes)` | `tool_result_size_bytes`：Tool 返回内容的字节数；`avg`：平均值；`P95`：第 95 百分位 | 直 |
| 重复调用率 | `duplicate_calls / tool_attempts` | `duplicate_calls`：同一调用链中 Tool 名和主要参数相同的重复调用；`tool_attempts`：所有调用尝试 | 派 |

## 2. 记忆指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| ★ Memory 提取成功率 | `successful_extractions / extraction_attempts` | `successful_extractions`：正常完成的记忆提取任务数；`extraction_attempts`：启动的提取任务总数 | 派 |
| Memory 提取延迟 | `P95(extraction_finished_at - extraction_started_at)` | `extraction_started_at`：开始时间；`extraction_finished_at`：结束时间 | 直 |
| Memory 保存产出率 | `memories_saved / message_count` | `memories_saved`：实际保存的记忆数量；`message_count`：本次提取处理的消息数 | 派 |
| Memory 文件大小 | `content_length` 或 `memory_tokens` | `content_length`：记忆文件字符数；`memory_tokens`：记忆内容换算后的 Token 数 | 直 |
| 记忆选择率 | `recall_with_result / recall_requests` | `recall_requests`：记忆召回执行次数；`recall_with_result`：至少选中一条记忆的召回次数 | 派 |
| ★ Recall Prefetch P95 | `P95(settled_at - fired_at)` | `fired_at`：异步召回启动时间；`settled_at`：召回完成时间 | 直 |
| Recall 消费率 | `consumed_prefetches / started_prefetches` | `started_prefetches`：启动的预取次数；`consumed_prefetches`：最终注入 Agent Context 的次数 | 派 |
| 记忆陈旧率 | `stale_memories / retrieved_memories` | `stale_memories`：超过新鲜度阈值的记忆数；`retrieved_memories`：召回的记忆总数 | 派 |
| ★ Recall Precision@K | `useful_memories_at_k / K` | `K`：返回的前 K 条记忆；`useful_memories_at_k`：其中真正帮助当前任务的记忆数 | 评 |
| 记忆冲突/污染率 | `conflicting_memories / retrieved_memories` | `conflicting_memories`：与当前事实、代码、用户要求冲突的记忆数；`retrieved_memories`：召回总数 | 评 |

## 3. 压缩指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Compaction 次数 | `count(compaction_attempt)` | `compaction_attempt`：一次上下文压缩尝试 | 直 |
| ★ Compaction 成功率 | `successful_compactions / compaction_attempts` | `successful_compactions`：成功生成摘要并替换上下文的次数；`compaction_attempts`：所有压缩尝试 | 派 |
| Compaction 失败率 | `failed_compactions / compaction_attempts` | `failed_compactions`：压缩失败次数；`compaction_attempts`：所有压缩尝试 | 派 |
| ★ Token 压缩率 | `(pre_tokens - post_tokens) / pre_tokens` | `pre_tokens`：压缩前 Token 数；`post_tokens`：压缩后真正保留的 Token 数，对应 `truePostCompactTokenCount` | 派 |
| Message 压缩率 | `(original_messages - compacted_messages) / original_messages` | `original_messages`：压缩前消息数；`compacted_messages`：压缩后摘要、附件等消息数 | 派 |
| 净节省 Token | `pre_tokens - post_tokens - overhead_tokens` | `pre_tokens`：压缩前 Token；`post_tokens`：压缩后 Token；`overhead_tokens`：生成摘要额外消耗的 Token | 派 |
| 压缩成本 | `input_tokens × 输入价 + output_tokens × 输出价 + cache_tokens × 缓存价` | `input_tokens`：摘要模型读取量；`output_tokens`：摘要生成量；`cache_tokens`：缓存读取或创建量 | 直 |
| Next-turn 重触发率 | `retrigger_count / successful_compactions` | `retrigger_count`：压缩后仍超过阈值、下轮需要再压缩的次数；`successful_compactions`：成功压缩次数 | 直 |
| 重压缩间隔 | `avg(turns_since_previous_compact)` | `turns_since_previous_compact`：距离上一次压缩经过的 Agent 轮数 | 直 |
| ★ 关键信息保留率 | `preserved_critical_facts / original_critical_facts` | `original_critical_facts`：压缩前的关键要求、ID、状态等；`preserved_critical_facts`：压缩后仍准确保留的数量 | 评 |

## 4. ReAct 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| ★ 平均 API 轮次 | `sum(num_turns) / task_count` | `num_turns`：每个任务的模型调用轮数；`task_count`：任务总数 | 直 |
| 多轮继续率 | `multi_turn_tasks / task_count` | `multi_turn_tasks`：`num_turns > 1` 的任务数；`task_count`：任务总数 | 派 |
| 每轮 Action 数 | `tool_use_count / num_turns` | `tool_use_count`：任务中的 Tool 调用数；`num_turns`：模型调用轮数 | 派 |
| Observation 利用率 | `used_tool_results / available_tool_results` | `available_tool_results`：Tool 返回结果数；`used_tool_results`：后续决策或最终答案真正使用的结果数 | 评 |
| 重复 Action 率 | `repeated_actions / all_actions` | `repeated_actions`：Tool 名和主要参数重复的 Action 数；`all_actions`：所有 Tool Action 数 | 派 |
| 无进展循环率 | `no_progress_tasks / multi_turn_tasks` | `no_progress_tasks`：多轮执行却没有新增证据或状态变化的任务数；`multi_turn_tasks`：多轮任务数 | 评 |
| 错误恢复率 | `recovered_tasks / tasks_with_tool_error` | `tasks_with_tool_error`：发生过 Tool Error 的任务；`recovered_tasks`：出错后最终仍成功的任务 | 派 |
| Max-turn 触顶率 | `max_turn_tasks / task_count` | `max_turn_tasks`：因达到最大轮数被迫停止的任务；`task_count`：任务总数 | 直 |
| 过早停止率 | `premature_tasks / reported_completed_tasks` | `reported_completed_tasks`：Agent 报告完成的任务；`premature_tasks`：实际仍有要求未完成的任务 | 评 |
| ★ 任务完成率 | `successful_tasks / task_count` | `successful_tasks`：通过用户要求或验收标准的任务；`task_count`：任务总数 | 评 |
| 单成功任务成本 | `total_cost / successful_tasks` | `total_cost`：所有任务的模型和工具总成本；`successful_tasks`：成功任务数 | 派 |

## 5. Skill 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Skill 可用数量 | `sum(skills_loaded_per_session) / session_count` | `skills_loaded_per_session`：每个 Session 加载的 Skill 数；`session_count`：Session 总数 | 直 |
| Skill Budget 占用 | `skill_listing_tokens / context_budget_tokens` | `skill_listing_tokens`：Skill 名称和描述占用的 Token；`context_budget_tokens`：为 Skill 列表预留的预算 | 直 |
| ★ Skill 调用率 | `tasks_invoking_skill / eligible_tasks` | `eligible_tasks`：本应该使用 Skill 的任务；`tasks_invoking_skill`：实际调用 Skill 的任务 | 派 |
| 调用来源分布 | `invocations_from_source / all_skill_invocations` | `invocations_from_source`：来自用户、Claude 主动调用或嵌套调用的次数；`all_skill_invocations`：Skill 调用总数 | 直 |
| 执行模式分布 | `mode_invocations / all_skill_invocations` | `mode_invocations`：inline、fork 或 remote 模式的调用次数；`all_skill_invocations`：Skill 调用总数 | 直 |
| ★ Discovery→Invocation 转化率 | `discovered_and_invoked / discovered_skills` | `discovered_skills`：动态发现的 Skill 数；`discovered_and_invoked`：发现后又被真正调用的数量 | 派 |
| Skill 参数校验失败率 | `validation_failed / invocation_attempts` | `validation_failed`：不存在、禁用、类型不正确等校验失败次数；`invocation_attempts`：Skill 调用尝试总数 | 派 |
| Remote Skill 缓存命中率 | `remote_cache_hits / remote_skill_loads` | `remote_cache_hits`：从缓存加载的次数；`remote_skill_loads`：Remote Skill 加载总数 | 直 |
| Remote Skill 加载 P95 | `P95(remote_load_latency_ms)` | `remote_load_latency_ms`：一次 Remote Skill 加载所需的毫秒数 | 直 |
| Skill 执行成功率 | `successful_invocations / valid_invocations` | `valid_invocations`：通过校验并开始执行的调用；`successful_invocations`：正常执行完成的调用 | 派 |
| ★ Skill 指令遵循率 | `passed_constraints / all_constraints` | `all_constraints`：Skill 中需要遵守的关键约束数；`passed_constraints`：实际遵守的数量 | 评 |
| Skill Success Lift | `success_rate_with_skill - success_rate_without_skill` | `success_rate_with_skill`：使用 Skill 的任务成功率；`success_rate_without_skill`：不使用 Skill 的对照组成功率 | 评 |

## 6. 权限指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Permission 决策量 | `count(permission_decision)` | `permission_decision`：一次 allow、reject 或 ask 权限判断 | 直 |
| ★ Permission Prompt Rate | `prompted_calls / permission_checked_calls` | `prompted_calls`：弹出用户确认的调用数；`permission_checked_calls`：经过权限判断的调用总数 | 派 |
| 权限通过率 | `accept_decisions / all_decisions` | `accept_decisions`：允许执行的决策数；`all_decisions`：允许和拒绝决策总数 | 派 |
| 权限拒绝率 | `reject_decisions / all_decisions` | `reject_decisions`：拒绝执行的决策数；`all_decisions`：所有终态决策 | 派 |
| ★ 用户等待 P95 | `P95(permission_decision_at - prompt_started_at)` | `prompt_started_at`：权限弹窗出现时间；`permission_decision_at`：用户完成选择的时间 | 直 |
| 决策来源分布 | `decisions_from_source / all_decisions` | `source`：config、classifier、hook、临时允许、永久允许、用户拒绝等决策来源 | 直 |
| 永久授权率 | `permanent_accepts / user_accepts` | `permanent_accepts`：用户选择永久允许的次数；`user_accepts`：用户手动允许的总次数 | 派 |
| Hook 干预率 | `hook_decisions / all_decisions` | `hook_decisions`：由 Permission Hook 决定允许或拒绝的次数；`all_decisions`：所有权限决策 | 派 |
| Classifier 决策率 | `classifier_decisions / all_decisions` | `classifier_decisions`：由权限分类器自动判断的次数；它只反映使用量，不代表判断正确 | 派 |
| 连续拒绝降级率 | `fallback_prompts / automatic_denial_sequences` | `automatic_denial_sequences`：连续自动拒绝的调用序列；`fallback_prompts`：达到阈值后改为询问用户的次数 | 派 |
| ★ 权限导致任务终止率 | `permission_failed_tasks / tasks_with_permission_checks` | `permission_failed_tasks`：因必要 Tool 被拒绝而失败的任务；`tasks_with_permission_checks`：发生过权限检查的任务 | 派 |

## 7. Subagent 指标

| 指标 | 计算口径 | 变量含义 | 类型 |
|---|---|---|---|
| Subagent 启动量 | `count(subagent_started)` | `subagent_started`：一个 Subagent 真正开始运行；可按类型、模型、同步/异步拆分 | 直 |
| ★ Subagent 完成率 | `completed_subagents / started_subagents` | `completed_subagents`：正常完成并返回结果的数量；`started_subagents`：启动总数 | 派 |
| Subagent 终止率 | `terminated_subagents / started_subagents` | `terminated_subagents`：因取消、超时或错误终止的数量；`started_subagents`：启动总数 | 派 |
| ★ Subagent P95 延迟 | `P95(finished_at - started_at)` | `started_at`：开始时间；`finished_at`：完成或终止时间 | 直 |
| Subagent Token | `sum(total_tokens) / completed_subagents` | `total_tokens`：每个 Subagent 消耗的 Token；`completed_subagents`：正常完成数量 | 直 |
| Subagent Tool 数 | `sum(total_tool_uses) / completed_subagents` | `total_tool_uses`：Subagent 内部 Tool 调用次数；`completed_subagents`：正常完成数量 | 直 |
| 消息/输出规模 | `sum(response_char_count) / completed_subagents` | `response_char_count`：Subagent 最终输出字符数；也可用 `assistant_message_count` 统计消息量 | 直 |
| Async 占比 | `async_subagents / started_subagents` | `async_subagents`：后台异步运行的 Subagent；`started_subagents`：全部启动数量 | 直 |
| Fork 占比 | `fork_subagents / started_subagents` | `fork_subagents`：继承父 Agent 上下文的 Fork Agent；`started_subagents`：全部启动数量 | 直 |
| Resume Rate | `resume_invocations / all_subagent_invocations` | `resume_invocations`：继续已有 Subagent 的次数；`all_subagent_invocations`：新建和继续调用总数 | 直 |
| 后台化比例 | `foreground_to_background / foreground_started` | `foreground_started`：最初以前台方式运行的数量；`foreground_to_background`：执行中转入后台的数量 | 派 |
| ★ 委派收益 | `success_rate_with_subagent - success_rate_without_subagent` | `success_rate_with_subagent`：使用 Subagent 的成功率；`success_rate_without_subagent`：不使用 Subagent 的对照组成功率 | 评 |