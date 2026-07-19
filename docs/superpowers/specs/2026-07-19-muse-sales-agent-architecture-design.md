# Muse 销售对话 Agent 架构设计

## 1. 背景与目标

`muse` 是一个基于 CloudWeGo Eino ADK `v0.9.6` 的独立、可运行参考实现。它理解并复用 `silicon-sage` 的拟人销售/学习服务业务，但不依赖或复制 `silicon-sage` 的代码、数据库和外部服务。

当前 `silicon-sage` 的主链路本质上是固定串行 Pipeline：Planner 读取原始输入并决定路由，可选 Reviewer 审核计划，Thinker 生成内容，Writer 生成消息气泡。这个方向适合短链路、强业务约束的销售对话，但存在以下架构缺口：

- Reviewer 由 Planner 自己决定是否调用，且只看到 Planner 摘要；
- Planner 在推理阶段直接修改 Profile/TODO，重试可能重放副作用；
- 下游角色无法回看原始输入，角色间是有损自由文本接力；
- 工具 Schema 是模型提示，不是执行端授权；
- 最终 Bubbles、Artifacts、Run 终态和消息投递之间缺少一致性边界；
- 缺少业务阶段 Checkpoint，进程崩溃后无法确定从哪个阶段恢复；
- Prompt、Skill、模型和策略版本没有在单次运行中冻结。

本设计不追求动态 Agent Swarm，而是构建：

> 确定性 Orchestrator + 受控 Eino ADK Agent 节点 + 条件审核 + 幂等业务写入 + 原子终态收口 + 双层 Checkpoint。

成功标准：

1. 普通低风险消息仍走低成本 Planner → Thinker → Writer 主路径；
2. 风险路径无法绕过计划审核或最终审核；
3. Planner 可以直接更新 Profile/TODO，但模型重试、进程崩溃和恢复不会重复应用同一更新；
4. 已完成阶段和已成功 Tool Write 不会在恢复时重复执行；
5. 所有模型、Prompt、Skill、Policy 和 Tool 权限版本可追踪、可回放；
6. 默认测试不访问真实模型或网络；
7. `update_profile` 和 `update_todo` 保留为 Planner 可直接调用的写工具，并具备服务端校验、CAS、幂等 Tool Ledger 和审计能力。

## 2. 范围

### 2.1 本期范围

- 被动消息回复主链路；
- Raw Input Guard；
- Planner、Thinker、Writer；
- 条件触发的 Plan Policy Gate 和 Final Reviewer；
- 角色级工具权限与只读知识工具；
- Planner 直接调用的 `update_profile`、`update_todo` 写工具；
- 证据驱动的 ProfilePatch、受状态机约束的 TodoTransition，以及防重放 Tool Ledger；
- Ownership、CAS、Transactional Outbox；
- Eino Interrupt/Resume Checkpoint；
- SQLite 业务阶段 Checkpoint 与崩溃恢复；
- Eino ADK Runner、Fake Model 测试、可选真实模型 CLI；
- CozeLoop 低敏观测；
- 可版本化 Skill 解析与运行快照。

### 2.2 非目标

- 不接入 `silicon-sage` 生产数据库或外部服务；
- 不实现主动触达 Activator 和定时调度；
- 不实现真实微信/短信发送，Delivery 由本地模拟器完成；
- 不实现长期记忆压缩和向量数据库；
- 不实现 Supervisor、动态 Agent Handoff 或并行 Agent Swarm；
- 不在本期接入 Eino TurnLoop 抢占；通过 Ownership/CAS 模拟新消息抢占，保留未来接入点；
- 不保存或展示模型 Chain-of-Thought。

## 3. 方案选择

### 3.1 备选方案

1. **纯 SequentialAgent**：实现简单，但条件审核、rewrite、Checkpoint 和终态收口边界不自然。
2. **确定性 Orchestrator + 受控 Agent 节点**：外层 Go 状态机掌控顺序、风险路由、恢复和终态收口；模型节点只负责判断与生成。
3. **Supervisor + 动态 Handoff**：灵活，但难以保证审核和终态收口一定执行，成本和不可预测性不适合当前业务。

### 3.2 结论

采用方案 2。`MuseAgent` 是自定义 Eino ADK 根 Agent，并实现 `adk.ResumableAgent` 以承接有意触发的 Interrupt/Resume；Planner、Thinker、Writer 和两个语义 Reviewer 由受控模型适配器实现。Raw Input Guard、ReviewRouter、OutputValidator、OwnershipGate、Checkpoint 和 RunFinalizer 均为确定性 Go 组件。

## 4. 总体架构

```text
RunService / cmd/muse-agent
  -> Eino ADK Runner + CheckPointStore + CozeLoop
      -> MuseAgent（确定性 Orchestrator）
          -> SnapshotLoader
          -> RawInputGuard
          -> Planner（ReAct：可直接调用 update_profile / update_todo）
          -> ReloadMutableContext（重载 Profile / TODO）
          -> ReviewRouter
              -> low risk: skip PlanPolicyGate
              -> medium/high risk: PlanPolicyGate
          -> Thinker（受限只读 Tool Loop）
          -> Writer
          -> OutputValidator
          -> OutputReviewRouter
              -> low risk: skip FinalReviewer
              -> medium/high risk: FinalReviewer
                  -> approve
                  -> rewrite Thinker/Writer，最多一次
                  -> block/transfer
          -> OwnershipGate
          -> RunFinalizer
              -> complete reply / block / transfer / stop-contact
              -> OutboxMessage（需要投递时）
          -> DeliveryWorker
```

外层流程固定。Planner 只能调用两个经过服务端校验的业务写工具，Thinker 只能进行有限的只读工具调用；模型不能自由跳过审核或直接结束 Run。

## 5. 核心数据契约

### 5.1 ContextSnapshot

一次运行开始时冻结 InitialSnapshot：

- `RunID`、`SessionID`、`MessageID`、Ownership Token；
- 原始文本和多模态引用；
- Profile、TODO、短期记忆、长期记忆摘要；
- 各状态版本号；
- Prompt、Skill、Policy、Tool Policy 和模型版本；
- 当前业务阶段与时间上下文。

Planner 写工具成功后，Orchestrator 重载 Profile/TODO 并创建新的 EffectiveSnapshot；InitialSnapshot 和原始输入保持不变。Reviewer、Thinker、Writer 只读取 EffectiveSnapshot，Checkpoint 同时保留初始版本和写后版本，避免把“重载最新状态”实现成原地修改共享对象。

### 5.2 RunManifest

`RunManifest` 保存：

- 每个角色实际生效的模型标识；
- Prompt ID、版本和 checksum；
- Skill 名称、版本和 checksum；
- Policy 和 Tool Policy 版本；
- 合约版本与代码版本；
- 开始时间和运行模式。

一次运行中不重新解析“最新版本”。恢复时继续使用原 Manifest；版本不存在或 checksum 不匹配时终止恢复。

### 5.3 Plan

Planner 输出严格结构：

- `Intent`、`Facts`、`Guidance`；
- `KnowledgeQueries`；
- `ResponseGoal`；
- `RiskHints`、`Uncertainties`；
- `SkipReason`。

Planner 只注册 `update_profile`、`update_todo` 两个业务写工具，以及只读的 Eino `skill` 元工具；业务写工具直接提交且返回 committed version。Planner 不生成通用待执行动作；转人工、停止触达和拦截由 Guard 或 Reviewer 的类型化结果决定。

### 5.4 FinalOutcome 与 OutboxMessage

Orchestrator 根据 RawInputGuard、PlanVerdict、FinalVerdict 和最终 Bubbles 生成唯一的 `FinalOutcome`：

- `Type`：`reply/block/transfer/stop_contact`；
- `ReasonCode` 和审核依据引用；
- `Bubbles`：只有 reply 或安全确认回复需要；
- `ExpectedOwnership` 及 Profile/TODO committed version。

`FinalOutcome` 不是任意动作列表，也不能携带工具名或自由组合的副作用。Planner 不能直接创建它；RunFinalizer 只接受这四种固定结果。

需要投递最终结果时，RunFinalizer 同事务创建 `OutboxMessage`：

- `MessageID`：由 `RunID + outcome + ordinal` 稳定生成；
- `Type`：`reply/handoff_notice/stop_confirmation/todo_rule_task`；
- 结构化 Payload；
- `Status`：`pending/sending/sent/failed`；
- retry 次数、下次执行时间和最近错误摘要。

Profile/TODO 数据写入不走 FinalOutcome 或 Outbox；它们由 Planner Tool 在规划阶段直接提交。只有 TODO 更新衍生出的异步规则任务会与 TODO、TodoLog、Tool Ledger 同事务写成 `todo_rule_task` Outbox，避免原实现中脱离 Run 的 goroutine 丢任务。Outbox 只记录已经决定、等待可靠投递的消息或内部任务，不负责模型推理和业务状态规划。

### 5.5 ProfilePatch：update_profile 工具参数

`silicon-sage` 的 Planner 会从对话中补充用户称呼、职业、需求场景、目标、痛点、紧迫度、截止时间、当前水平、薄弱项、目标水平、考试历史、每日学习投入、偏好时段、设备、当前 SKU、线索来源和是否入群。`muse` 保留这些业务字段；Planner 通过 `update_profile` 直接提交字段级 Patch，但不能提交整个 Profile 或全量覆盖数据库。

Planner 只能生成字段级 Patch：

```text
ProfilePatch
  expected_version
  idempotency_scope: message_id + field
  changes[]
    field
    operation: set | clear
    value
    evidence_refs[]
    source: explicit_user | verified_context | model_inference
    confidence
```

约束：

- 每个字段必须在服务端 allowlist 中；
- `note/current_sku/join_group/deadline/exam_history/daily_budget` 等事实字段必须有用户原话或可信上下文证据，不能只靠模型推测；
- 推断型字段必须保留 evidence、source 和 confidence；
- `status/abuse_count` 不属于 Planner 权限，只能由 Policy 或人工流程提出；
- 空字符串不会隐式清空字段，清空必须使用显式 `clear`；
- 同一 Patch 不允许重复字段；
- Tool Handler 按字段合并并检查 Profile 版本，不进行全量覆盖；
- Tool Handler 在同一事务中写 Profile 和 Tool Ledger；同一 MessageID 对同一字段只能成功写入一次；
- Planner 重试后再次提交相同字段时，先返回 Tool Ledger 中的历史结果，不重复更新；参数发生冲突时返回 idempotency conflict。

证据充分、字段合法的常规 ProfilePatch 由 `update_profile` Tool Handler 直接写入，不触发 PlanPolicyGate；缺少证据或字段越权直接拒绝。写入成功后 Orchestrator 重载 Profile，使 Thinker 在同一轮使用新画像。

### 5.6 TodoTransition：update_todo 工具参数

`silicon-sage` 的 TODO 由 `phase/stage/task/step/status` 和追加日志组成。`muse` 允许 Planner 调用 `update_todo` 直接写入，但 Tool 参数必须表达从当前节点到目标节点的 Transition，不能任意覆盖字段：

```text
TodoTransition
  expected_version
  idempotency_scope: message_id
  from: phase / stage / task / step
  to: phase / stage / task / step
  status
  reason
  evidence_refs[]
```

服务端 `TodoWorkflow` 从冻结的 TODO Skill/SOP 编译出允许的节点和边。`update_todo` Tool Handler 必须校验：

- `from` 与 Snapshot 当前 TODO 完全一致；
- `to` 是 SOP 中存在的节点；
- 当前节点允许走向目标节点，不允许跳阶段；
- 状态变化满足节点前置条件；
- step 切换时不会保留旧 step；
- 每次 Transition 都生成 append-only TodoLog；
- TODO 更新、TodoLog、Tool Ledger 和由此产生的规则任务写入同一事务/Outbox，不启动脱离 Run 的后台 goroutine；
- 同一 MessageID 只允许成功提交一个 TodoTransition；Planner 重试时复用历史 Tool Result，不重复推进。

合法 TodoTransition 由 `update_todo` Tool Handler 直接写入。非法跳转由确定性状态机直接拒绝，不交给 Reviewer 放行。写入成功后 Orchestrator 重载 TODO 和阶段 Prompt，使 Thinker 使用最新进度。

### 5.7 Draft、Bubble 与 Verdict

- Thinker 输出 `Draft`：事实、建议、核心对话内容和引用；
- Writer 输出 `[]Bubble` 和 Artifact Draft；
- PlanVerdict：`approve/rewrite/block/transfer`；
- FinalVerdict：`approve/rewrite/block/transfer`、违规项和 rewrite guidance。

空字段、未知枚举、未知 Bubble 类型和互相矛盾的字段均视为合约错误，不能默认通过。

## 6. 角色与控制组件

### 6.1 RawInputGuard

第一版完全使用确定性 Go 规则，不调用模型。它处理：

- 已拉黑、冻结或人工接管状态；
- 空输入和格式上限；
- 明确的停止联系；
- 明确要求人工；
- 明确 Prompt Injection 和禁止内容。

输出为 `allow/safe_reply/transfer/block/stop_contact`。Orchestrator 将终止类结果转换为固定 FinalOutcome；Guard 不直接写数据库或调用外部渠道。

### 6.2 Planner

Planner 读取完整 Snapshot，输出 Plan，并可在 Eino ReAct Tool Loop 中直接调用 `update_profile`、`update_todo`。它不能调用 Timer、Blacklist、Transfer 或其他外部副作用工具。

当用户对话产生新的画像事实或业务进度时，Planner 分别调用 `update_profile(ProfilePatch)`、`update_todo(TodoTransition)`。没有证据时不更新 Profile；没有合法状态变化时不更新 TODO。

Planner 最终输出非法时只重试 Planner 一次。已经成功的 Profile/TODO Tool Call 通过 MessageID 作用域的 Tool Ledger 返回历史结果，不重复写数据库。

### 6.3 ReviewRouter

Plan Policy Gate 不是常态化调用。ProfilePatch/TodoTransition 的硬约束已经由 Tool Handler 在写入前校验；ReviewRouter 根据 Planner 最终计划、Tool Ledger 和原始输入判断是否需要语义审核。

以下任一条件触发 Plan Policy Gate：

- 原始输入或 Plan 表达了人工接管、停止触达等需要语义确认的控制信号；
- Tool Ledger 显示 Planner 尝试了被拒绝的画像推断或 TODO 跳转；
- Plan 包含价格、名额、交付或服务承诺；
- 原始输入包含拒绝信号，但 Plan 仍继续营销；
- RiskLevel 非 low；
- Plan 存在不确定项或合约冲突。

普通信息咨询，以及已经通过 Tool Handler 校验的常规 Profile/TODO 更新，在无其他风险信号时跳过。字段越权、无证据事实和非法 TODO 跳转已经被 Tool Handler 拒绝，Reviewer 只能处理语义风险，不能撤销或绕过数据库硬约束。

### 6.4 PlanPolicyGate

这是对原 `silicon-sage` Reviewer 合理意图的保留：Planner 先制定计划，Policy Gate 再审核计划。

它同时读取原始输入、Snapshot、Plan 和冻结的 Policy，不再只读取 Planner 摘要。它调用一次语义模型，不注册工具，不修改状态。调用失败或输出非法时，高风险计划不能继续。

### 6.5 Thinker

Thinker 负责业务内容。它可以通过 ToolGateway 调用 `search_knowledge` 等只读工具；不能调用状态修改、外发、拉黑或转人工工具。

工具失败以结构化 Observation 返回，Thinker 可以生成明确降级回复。工具超时不会触发整条 Pipeline 重跑。

### 6.6 Writer

Writer 把 Draft 转为 Bubble 和 Artifact Draft，不注册工具。Writer 不重新解释用户意图，也不新增价格、名额、身份或服务承诺。

如果后续评测证明 Writer 没有独立收益，可以合并到 Thinker；本期保留它以验证职责边界。

### 6.7 OutputValidator 与 FinalReviewer

OutputValidator 始终执行确定性检查：非空、Bubble 类型、长度、内部字段泄露和引用一致性。

以下任一条件触发 FinalReviewer：

- Plan 经过 PlanPolicyGate；
- 最终输出包含价格、名额、交付、身份或服务承诺；
- 有 Artifact Draft 或人工接管等控制结果；
- OutputValidator 检出需要语义判断的风险；
- Plan 或输出风险等级为中高风险。

FinalReviewer 同时读取原始输入、Plan、最终 Bubbles 和 Artifact Draft，不注册工具。`rewrite` 最多一次，仅重跑 Thinker/Writer 和必要的 FinalReviewer；不重跑 Planner 或已完成工具调用。

`block` 和 `transfer` 不是 Reviewer 的直接副作用。Reviewer 只返回 Verdict，Orchestrator 将其映射成固定 FinalOutcome；通过 OwnershipGate 后，由 RunFinalizer 将 Run 收口为对应终态，并在需要通知时写入 Outbox。

## 7. Tool 与 Skill

### 7.1 ToolGateway

ToolGateway 按 `{role, run_id, tenant, tool}` deny-by-default 校验。模型看到的 Tool Schema 不是授权依据；执行端必须再次验证。

本期权限：

| 角色 | 工具 |
| --- | --- |
| Planner | `skill`、`update_profile`、`update_todo` |
| PlanPolicyGate | 无 |
| Thinker | `skill`、`search_knowledge` |
| Writer | 无 |
| FinalReviewer | 无 |

工具调用必须有调用 ID、超时、参数 Schema、审计事件和幂等语义。未展示或未授权的工具即使由模型返回也必须拒绝。`skill` 是 Eino Skill Middleware 提供的只读元工具，只能从本 Run 冻结的 Backend 读取 Skill，不得借此增加业务工具权限。

`update_profile` 和 `update_todo` 是 Planner 可直接调用的写 Tool。权限只授予 Planner；Tool Handler 内部完成字段/状态机校验、CAS、幂等 Tool Ledger 和审计。Reviewer、Thinker、Writer 即使返回同名 Function Call，也会被执行端拒绝。

### 7.2 SkillResolver

Skill 不是无版本 Prompt 文本，而是不可变发布物：

- 名称、版本、checksum；
- 适用角色和业务阶段；
- Instruction；
- 可申请的工具能力；
- 输入输出合约版本。

Run 开始时解析 Skill alias 并冻结具体版本到 RunManifest。Skill 申请的工具能力必须与服务端 Role Policy 取交集，Skill 不能自行授予权限。

本期只实现 inline Skill，不创建动态专家子 Agent；接入 Eino Skill Middleware 的 registry-backed 模式，复用仓库现有的不可变 artifact + alias 思路。

## 8. Checkpoint、恢复与抢占

### 8.1 双层 Checkpoint

**Eino ADK Checkpoint** 使用 `CheckPointStore`、`WithCheckPointID` 和 `Runner.Resume`，服务于 StatefulInterrupt、人工审批和可恢复 Cancel。

**业务阶段 Checkpoint** 在每个成功阶段后持久化 RunState，服务于进程崩溃恢复。Eino ADK 不会自动为每个普通模型阶段保存这种业务进度，因此两者不能互相替代。

### 8.2 阶段状态

```text
CREATED
SNAPSHOT_READY
INPUT_ALLOWED
PLANNER_RUNNING
PLANNER_TOOL_WRITES_COMMITTED
PLAN_READY
PLAN_APPROVED | PLAN_REVIEW_SKIPPED
DRAFT_READY
OUTPUT_READY
FINAL_APPROVED | FINAL_REVIEW_SKIPPED
FINALIZING

终态：COMPLETED | BLOCKED | TRANSFERRED | STOPPED | PREEMPTED | FAILED
```

Outbox 的 `pending/sending/sent/failed` 是独立投递状态，不作为 Run 阶段。每个 Planner 写工具在业务数据事务内同步写 Tool Ledger；Orchestrator 随后保存 Tool Result 和 committed version。Checkpoint 保存初始 Snapshot、写后重载的 Profile/TODO、Tool Ledger 引用、Manifest、Plan、Verdict、Draft、Bubbles、FinalOutcome、rewrite 次数、阶段版本、状态和 heartbeat；不保存 Chain-of-Thought。

### 8.3 恢复规则

- Planner 模型调用中崩溃：重新运行 Planner；重复 Tool Call 先查 MessageID 级 Tool Ledger并返回历史结果，不重复写库；
- 其他模型调用中崩溃：从前一个完成阶段恢复，只重跑当前纯模型阶段；
- 阶段已完成：读取 Checkpoint 后从下一阶段继续；
- FINALIZING：读取 Run 终态和 Outbox；Run 已是终态则不重复 Finalize，否则用相同 FinalOutcome 重试原子事务；
- COMPLETED/TRANSFERRED/STOPPED：不再运行模型，DeliveryWorker 独立处理未完成 Outbox；
- BLOCKED：不再运行模型，也不产生普通回复 Outbox；
- PREEMPTED：禁止恢复和 Finalize；
- Manifest checksum 不匹配：恢复失败，不使用最新版本悄悄替换；
- 过期 RUNNING heartbeat 由 RecoveryWorker 认领，使用 lease 防止多 Worker 同时恢复。

### 8.4 客户端断开与新消息

RunService 创建独立 Run Worker，HTTP 请求上下文或进程内 CLI 等待上下文取消时，不把取消信号直接传给 Worker；调用方使用 RunID 查询状态。只有显式 Cancel 才取消 Run。若整个 CLI/服务进程退出，则按进程崩溃处理，下次启动从 SQLite Checkpoint 恢复，而不是假设后台 goroutine 仍存活。

新消息抢占旧 Run 时，旧 Run 标记 PREEMPTED。OwnershipGate 和 CAS 阻止旧进程随后提交；新 Run 使用最新 Snapshot。

### 8.5 存储

生产形态接口使用 SQLite 参考实现，单元测试使用 MemoryStore。统一 Repository 负责事务边界，Checkpoint、Planner Tool、RunFinalizer 和 Delivery 只依赖各自所需的窄接口；SQLite 同时保存：

- Eino ADK binary checkpoint；
- 业务 RunCheckpoint；
- heartbeat 与 lease；
- Run 终态转换记录；
- OutboxMessage。

Checkpoint 带 schema version。生产迁移时需增加加密、TTL、租户隔离和清理任务；本地参考实现不把原始内容写入 CozeLoop 根 Trace。

## 9. Run Finalization 与 Outbox

Planner 阶段可能已经通过 `update_profile`、`update_todo` 提交 Profile/TODO；这些写入不会因后续 Reviewer、Thinker 或 Writer 失败而回滚。RunFinalizer 不解释或分发任意业务动作，它只接受一个固定 FinalOutcome，并结束一次 Run：

1. OwnershipGate 校验 Run 仍拥有当前 Session；
2. 校验当前 Profile/TODO 版本与 Planner Tool Result 中的 committed version 一致；
3. `CompleteReply`：将 Run 标记 `COMPLETED`，并在同一事务写最终回复 Outbox；
4. `CompleteTransfer`：将 Run 标记 `TRANSFERRED`，并在同一事务写人工通知 Outbox；
5. `CompleteBlocked`：将 Run 标记 `BLOCKED`，不创建普通回复 Outbox；
6. `CompleteStopContact`：记录固定的停止触达终态并取消本地待触达项，需要确认回复时写 Outbox；
7. DeliveryWorker 使用稳定 MessageID 幂等发送，失败只重试 Outbox，不重新运行 Agent。

RunFinalizer 没有 `Action[]`、动作注册表或通用 Dispatcher，也不写 Profile/TODO。它只更新 Run 终态及该终态要求的固定会话控制字段，并按需写 Outbox。相同 `RunID + FinalOutcome.Type` 只能 Finalize 一次；终态更新和 Outbox 插入处于同一事务。Planner Tool Write 使用 MessageID 级 Tool Ledger 防重放；最终 Ownership/CAS 冲突把 Run 标记 PREEMPTED，不发送旧回复，但不回滚此前已经成功且幂等提交的 Profile/TODO 更新。

如果事务提交前崩溃，Run 仍未进入终态，恢复后使用同一 FinalOutcome 重试；如果事务提交后、消息发送前崩溃，Outbox 保持 `pending`，DeliveryWorker 继续发送；如果发送后、标记 `sent` 前崩溃，则由稳定 MessageID 和渠道幂等能力抑制重复消息。

## 10. 错误与预算策略

- 整体 Run 默认预算 60 秒；
- Planner 12 秒、PlanPolicyGate 8 秒、Thinker 15 秒、Writer 10 秒、FinalReviewer 8 秒；
- 单次 Tool 默认 5 秒，Finalize 默认 3 秒；
- Planner、Thinker、Writer 各只允许一次纯模型重试；
- PlanPolicyGate/FinalReviewer 在风险路径失败时不能 fail-open；
- rewrite 最多一次；
- Invalid contract 视为阶段失败，不通过空字段默认放行；
- Read-only Tool 错误可降级；Planner 写 Tool 不在 Executor 内盲目重试，模型重试或恢复时必须先查询 Tool Ledger；终态控制只能通过 RunFinalizer 的固定方法执行。

## 11. 观测与隐私

Eino callback 与 CozeLoop 记录：

- RunID、阶段、实际模型、版本和耗时；
- Tool 名称、权限结论、耗时和成功状态；
- ReviewRouter 风险原因和 Verdict；
- retry、rewrite、checkpoint、resume、preempt、finalize、outbox 状态；
- token 和调用次数。

根 Trace 不记录完整用户文本、Profile、Memory、Prompt、Skill 正文或完整模型回复。完整恢复数据只进入本地 Checkpoint Store；日志使用长度、哈希、枚举和脱敏摘要。

## 12. 包结构

```text
muse/
├── agent.go
├── orchestrator.go
├── state.go
├── contracts.go
├── domain/
│   ├── profile.go
│   ├── todo.go
│   ├── profile_validator.go
│   └── todo_workflow.go
├── run/
│   ├── service.go
│   ├── worker.go
│   └── finalizer.go
├── policy/
│   ├── input_guard.go
│   ├── review_router.go
│   ├── plan_gate.go
│   ├── output_router.go
│   └── output_validator.go
├── roles/
│   ├── planner.go
│   ├── thinker.go
│   ├── writer.go
│   └── reviewer.go
├── tools/
│   ├── gateway.go
│   ├── knowledge.go
│   ├── update_profile.go
│   ├── update_todo.go
│   └── tool_ledger.go
├── skills/
│   ├── manifest.go
│   └── registry.go
├── checkpoint/
│   ├── adk_store.go
│   └── recovery.go
├── delivery/
│   └── outbox.go
└── repository/
    ├── store.go
    ├── memory.go
    └── sqlite.go

cmd/muse-agent/
├── main.go
├── trace.go
└── main_test.go
```

每个文件只承担一项主要职责。业务规则、模型适配、持久化和命令入口不混在根 Agent 文件中；复杂恢复、事务和权限边界使用简洁中文注释解释原因。

## 13. CLI

```bash
go run ./cmd/muse-agent -prepare-only
go run ./cmd/muse-agent -message "课程几点开始？"
go run ./cmd/muse-agent -resume-run <run-id>
go run ./cmd/muse-agent -list-recoverable
```

`-prepare-only` 不调用模型，输出角色、Skill、Tool Policy、Checkpoint Store 和预算摘要。真实模型从仓库现有 `.env` 变量读取；默认测试不读取真实密钥。

## 14. 测试与验收

测试使用 Fake ChatModel 和真实 Eino ADK Runner，必须覆盖：

1. 普通课程咨询跳过 PlanPolicyGate 和 FinalReviewer；
2. 委婉拒绝 + 继续营销计划触发 PlanPolicyGate；
3. 明确停止联系由 RawInputGuard 直接处理；
4. Reviewer block 时不产生最终回复 Outbox，但不回滚 Planner 已成功提交的 Profile/TODO；transfer 只通过 `RunFinalizer.CompleteTransfer` 收口一次并写人工通知 Outbox；
5. FinalReviewer rewrite 只重跑 Thinker/Writer，Planner 只执行一次；
6. 未授权工具调用在执行端被拒绝；
7. 相同 RunID/FinalOutcome 只 Finalize 一次；
8. CAS 冲突标记 PREEMPTED，不产生 Outbox；
9. SQLite 关闭并重开后，可从每个阶段 Checkpoint 恢复；
10. Eino StatefulInterrupt 保存 Checkpoint，重建 Runner 后可以 Resume；
11. COMPLETED 后崩溃只恢复 Outbox 投递，不调用模型；
12. 客户端断开不取消后台 Run，显式 Cancel 才终止；
13. Manifest checksum 不匹配时拒绝恢复；
14. CozeLoop 根 Trace 不包含测试中的敏感原文；
15. FINALIZING 阶段崩溃后，Run 已进入终态时不重复 Finalize；仍未进入终态时使用相同 FinalOutcome 安全重试；
16. 用户明确提供职业、目标等画像事实时，Planner 调用 update_profile；低风险且证据充分的 Patch 不调用 PlanPolicyGate，Tool Ledger 保证只提交一次；
17. Planner 推测未被用户确认的 SKU、考试成绩或入群状态时，update_profile Tool Handler 拒绝 ProfilePatch；
18. 合法同阶段 TodoTransition 由 update_todo 直接提交并追加一条 TodoLog，不调用 PlanPolicyGate；
19. TODO 跨阶段跳转、from 版本不一致或目标节点不在 SOP 时，update_todo Tool Handler 在模型审核前直接拒绝；
20. Planner 最终 JSON 重试和进程恢复会复用 Tool Ledger，ProfilePatch 和 TodoTransition 不重复提交；Reviewer block 不回滚已经成功的更新。

默认命令：

```bash
go test ./muse/... ./cmd/muse-agent -count=1
```

最终验收原则：

> 低风险路径不增加无必要的 Reviewer 调用；风险路径不可绕过审核；任意阶段失败或崩溃后都能确定恢复点；恢复不重复已完成模型阶段，不重复副作用，也不发送过期回复。
