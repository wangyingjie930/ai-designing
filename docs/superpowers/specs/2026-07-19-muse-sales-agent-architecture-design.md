# Muse 销售对话 Agent 架构设计

## 1. 背景与目标

`muse` 是一个基于 CloudWeGo Eino ADK `v0.9.6` 的独立、可运行参考实现。它理解并复用 `silicon-sage` 的拟人销售/学习服务业务，但不依赖或复制 `silicon-sage` 的代码、数据库和外部服务。

当前 `silicon-sage` 的主链路本质上是固定串行 Pipeline：Planner 读取原始输入并决定路由，可选 Reviewer 审核计划，Thinker 生成内容，Writer 生成消息气泡。这个方向适合短链路、强业务约束的销售对话，但存在以下架构缺口：

- Reviewer 由 Planner 自己决定是否调用，且只看到 Planner 摘要；
- Planner 在推理阶段直接修改 Profile/TODO，重试可能重放副作用；
- 下游角色无法回看原始输入，角色间是有损自由文本接力；
- 工具 Schema 是模型提示，不是执行端授权；
- 最终 Bubbles、Artifacts 和待提交动作没有形成审核闭环；
- 缺少业务阶段 Checkpoint，进程崩溃后无法确定从哪个阶段恢复；
- Prompt、Skill、模型和策略版本没有在单次运行中冻结。

本设计不追求动态 Agent Swarm，而是构建：

> 确定性 Orchestrator + 受控 Eino ADK Agent 节点 + 条件审核 + 延迟提交 + 双层 Checkpoint。

成功标准：

1. 普通低风险消息仍走低成本 Planner → Thinker → Writer 主路径；
2. 风险路径无法绕过计划审核或最终审核；
3. 模型阶段失败、重试或进程崩溃不会提前修改业务状态；
4. 已完成阶段不会在恢复时重复执行；
5. 所有模型、Prompt、Skill、Policy 和 Tool 权限版本可追踪、可回放；
6. 默认测试不访问真实模型或网络。

## 2. 范围

### 2.1 本期范围

- 被动消息回复主链路；
- Raw Input Guard；
- Planner、Thinker、Writer；
- 条件触发的 Plan Policy Gate 和 Final Reviewer；
- 角色级工具权限与只读知识工具；
- Profile/TODO 等业务修改的 ProposedAction 模型；
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

1. **纯 SequentialAgent**：实现简单，但条件审核、rewrite、Checkpoint 和 Commit 边界不自然。
2. **确定性 Orchestrator + 受控 Agent 节点**：外层 Go 状态机掌控顺序、风险路由、恢复和提交；模型节点只负责判断与生成。
3. **Supervisor + 动态 Handoff**：灵活，但难以保证审核和 Commit 一定执行，成本和不可预测性不适合当前业务。

### 3.2 结论

采用方案 2。`MuseAgent` 是自定义 Eino ADK 根 Agent，并实现 `adk.ResumableAgent` 以承接有意触发的 Interrupt/Resume；Planner、Thinker、Writer 和两个语义 Reviewer 由受控模型适配器实现。Raw Input Guard、ReviewRouter、OutputValidator、OwnershipGate、Checkpoint 和 CommitCoordinator 均为确定性 Go 组件。

## 4. 总体架构

```text
RunService / cmd/muse-agent
  -> Eino ADK Runner + CheckPointStore + CozeLoop
      -> MuseAgent（确定性 Orchestrator）
          -> SnapshotLoader
          -> RawInputGuard
          -> Planner
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
          -> CommitCoordinator
              -> ProposedActions
              -> OutboxMessage
              -> ActionAudit
          -> DeliveryWorker
```

外层流程固定；阶段内只有 Thinker 可以进行有限的只读工具调用。模型不能自由跳过审核、选择 Commit 或调用写工具。

## 5. 核心数据契约

### 5.1 ContextSnapshot

一次运行开始时冻结：

- `RunID`、`SessionID`、`MessageID`、Ownership Token；
- 原始文本和多模态引用；
- Profile、TODO、短期记忆、长期记忆摘要；
- 各状态版本号；
- Prompt、Skill、Policy、Tool Policy 和模型版本；
- 当前业务阶段与时间上下文。

后续角色只能读取 Snapshot。任何阶段都不能覆盖原始输入。

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
- `ProposedActions`；
- `RiskHints`、`Uncertainties`；
- `SkipReason`。

Planner 不注册写工具。`ProposedActions` 只是待验证命令，不产生业务副作用。

### 5.4 ProposedAction

每个动作包含：

- `ActionID`：由 `RunID + stage + ordinal` 稳定生成；
- `Type`：更新 Profile、更新 TODO、转人工、停止触达、创建 Outbox 等；
- 结构化 Payload；
- RiskLevel；
- 预期状态版本；
- Policy Evidence。

只有 CommitCoordinator 可以执行 ProposedAction。

### 5.5 Draft、Bubble 与 Verdict

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

输出为 `allow/safe_reply/transfer/block`。它可以产生 ProposedAction，但不能直接写状态。

### 6.2 Planner

Planner 是纯规划节点，读取完整 Snapshot，输出 Plan。它不直接执行更新 Profile、TODO、Timer、Blacklist 或 Transfer。

Planner 输出非法时只重试 Planner 一次。重试前不得执行任何业务副作用。

### 6.3 ReviewRouter

Plan Policy Gate 不是常态化调用。ReviewRouter 使用确定性字段判断是否需要语义审核。

以下任一条件触发 Plan Policy Gate：

- ProposedAction 包含状态修改、外部动作、定时触达或人工接管；
- Plan 包含价格、名额、交付或服务承诺；
- 原始输入包含拒绝信号，但 Plan 仍继续营销；
- RiskLevel 非 low；
- Plan 存在不确定项或合约冲突。

普通信息咨询、无写动作且无风险信号时跳过。

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

OutputValidator 始终执行确定性检查：非空、Bubble 类型、长度、内部字段泄露、ProposedAction 引用一致性。

以下任一条件触发 FinalReviewer：

- Plan 经过 PlanPolicyGate；
- 最终输出包含价格、名额、交付、身份或服务承诺；
- 有 Artifact Draft 或外部动作；
- OutputValidator 检出需要语义判断的风险；
- 本轮包含中高风险 ProposedAction。

FinalReviewer 同时读取原始输入、Plan、最终 Bubbles、Artifact Draft 和 ProposedActions，不注册工具。`rewrite` 最多一次，仅重跑 Thinker/Writer 和必要的 FinalReviewer；不重跑 Planner 或已完成工具调用。

`block` 和 `transfer` 也不是 Reviewer 的直接副作用：`block` 终止普通生成；`transfer` 产生 `TransferToHuman` ProposedAction，仍须经过 OwnershipGate 和 CommitCoordinator，才能更新会话并写入人工通知 Outbox。

## 7. Tool 与 Skill

### 7.1 ToolGateway

ToolGateway 按 `{role, run_id, tenant, tool}` deny-by-default 校验。模型看到的 Tool Schema 不是授权依据；执行端必须再次验证。

本期权限：

| 角色 | 工具 |
| --- | --- |
| Planner | 无 |
| PlanPolicyGate | 无 |
| Thinker | `search_knowledge` |
| Writer | 无 |
| FinalReviewer | 无 |

工具调用必须有调用 ID、超时、参数 Schema、审计事件和幂等语义。未展示或未授权的工具即使由模型返回也必须拒绝。

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
PLAN_READY
PLAN_APPROVED | PLAN_REVIEW_SKIPPED
DRAFT_READY
OUTPUT_READY
FINAL_APPROVED | FINAL_REVIEW_SKIPPED
COMMITTING
COMMITTED
OUTBOX_SENT

终态：BLOCKED | TRANSFERRED | PREEMPTED | FAILED
```

Checkpoint 保存 Snapshot、Manifest、Plan、Verdict、Draft、Bubbles、ProposedActions、rewrite 次数、阶段版本、状态和 heartbeat；不保存 Chain-of-Thought。

### 8.3 恢复规则

- 在某模型调用中崩溃：从前一个完成阶段恢复，只重跑当前纯模型阶段；
- 阶段已完成：读取 Checkpoint 后从下一阶段继续；
- COMMITTING：先按 RunID 查询 CommitRecord 和 Outbox；记录存在则收敛为 COMMITTED，不存在则用相同幂等键重试整个事务；
- COMMITTED：不再运行模型，只处理 Outbox；
- PREEMPTED：禁止恢复和提交；
- Manifest checksum 不匹配：恢复失败，不使用最新版本悄悄替换；
- 过期 RUNNING heartbeat 由 RecoveryWorker 认领，使用 lease 防止多 Worker 同时恢复。

### 8.4 客户端断开与新消息

RunService 创建独立 Run Worker，HTTP 请求上下文或进程内 CLI 等待上下文取消时，不把取消信号直接传给 Worker；调用方使用 RunID 查询状态。只有显式 Cancel 才取消 Run。若整个 CLI/服务进程退出，则按进程崩溃处理，下次启动从 SQLite Checkpoint 恢复，而不是假设后台 goroutine 仍存活。

新消息抢占旧 Run 时，旧 Run 标记 PREEMPTED。OwnershipGate 和 CAS 阻止旧进程随后提交；新 Run 使用最新 Snapshot。

### 8.5 存储

生产形态接口使用 SQLite 参考实现，单元测试使用 MemoryStore。SQLite 同时保存：

- Eino ADK binary checkpoint；
- 业务 RunCheckpoint；
- heartbeat 与 lease；
- ProposedAction 和 ActionAudit；
- OutboxMessage。

Checkpoint 带 schema version。生产迁移时需增加加密、TTL、租户隔离和清理任务；本地参考实现不把原始内容写入 CozeLoop 根 Trace。

## 9. Commit 与 Outbox

生成阶段没有业务写操作。Final Review 通过后：

1. OwnershipGate 校验 Run 仍拥有当前 Session；
2. 校验 Snapshot 中 Profile/TODO 的版本；
3. CommitCoordinator 在同一事务中执行 ProposedActions；
4. 同一事务写 ActionAudit 和 OutboxMessage；
5. DeliveryWorker 使用稳定 MessageID 幂等发送；
6. 发送失败只重试 Outbox，不重新运行 Agent。

相同 `RunID + ActionID` 只能提交一次。CAS 冲突把 Run 标记 PREEMPTED，不发送旧回复。

## 10. 错误与预算策略

- 整体 Run 默认预算 60 秒；
- Planner 12 秒、PlanPolicyGate 8 秒、Thinker 15 秒、Writer 10 秒、FinalReviewer 8 秒；
- 单次 Tool 默认 5 秒，Commit 默认 3 秒；
- Planner、Thinker、Writer 各只允许一次纯模型重试；
- PlanPolicyGate/FinalReviewer 在风险路径失败时不能 fail-open；
- rewrite 最多一次；
- Invalid contract 视为阶段失败，不通过空字段默认放行；
- Read-only Tool 错误可降级；写动作不存在运行期重试，只在 Commit 中通过幂等事务执行。

## 11. 观测与隐私

Eino callback 与 CozeLoop 记录：

- RunID、阶段、实际模型、版本和耗时；
- Tool 名称、权限结论、耗时和成功状态；
- ReviewRouter 风险原因和 Verdict；
- retry、rewrite、checkpoint、resume、preempt、commit、outbox 状态；
- token 和调用次数。

根 Trace 不记录完整用户文本、Profile、Memory、Prompt、Skill 正文或完整模型回复。完整恢复数据只进入本地 Checkpoint Store；日志使用长度、哈希、枚举和脱敏摘要。

## 12. 包结构

```text
muse/
├── agent.go
├── orchestrator.go
├── state.go
├── contracts.go
├── run/
│   ├── service.go
│   └── worker.go
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
│   └── knowledge.go
├── skills/
│   ├── manifest.go
│   └── registry.go
├── checkpoint/
│   ├── store.go
│   ├── memory_store.go
│   ├── sqlite_store.go
│   └── recovery.go
├── commit/
│   ├── coordinator.go
│   └── outbox.go
└── repository/
    └── memory.go

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
4. Reviewer block 时不产生业务修改；transfer 只通过 CommitCoordinator 提交一次 TransferToHuman 和人工通知 Outbox；
5. FinalReviewer rewrite 只重跑 Thinker/Writer，Planner 只执行一次；
6. 未授权工具调用在执行端被拒绝；
7. 相同 RunID/ActionID 只提交一次；
8. CAS 冲突标记 PREEMPTED，不产生 Outbox；
9. SQLite 关闭并重开后，可从每个阶段 Checkpoint 恢复；
10. Eino StatefulInterrupt 保存 Checkpoint，重建 Runner 后可以 Resume；
11. COMMITTED 后崩溃只恢复 Outbox，不调用模型；
12. 客户端断开不取消后台 Run，显式 Cancel 才终止；
13. Manifest checksum 不匹配时拒绝恢复；
14. CozeLoop 根 Trace 不包含测试中的敏感原文。
15. COMMITTING 阶段崩溃后，CommitRecord 已存在时不重复事务；不存在时使用相同幂等键安全重试。

默认命令：

```bash
go test ./muse/... ./cmd/muse-agent -count=1
```

最终验收原则：

> 低风险路径不增加无必要的 Reviewer 调用；风险路径不可绕过审核；任意阶段失败或崩溃后都能确定恢复点；恢复不重复已完成模型阶段，不重复副作用，也不发送过期回复。
