# Progress Tracking

这个目录演示的是一种 **可恢复长任务 Agent** 的设计模式：让模型负责理解目标和选择动作，但把“进度事实、恢复上下文、验收证据、漂移判断”放到程序态和 SQLite 里。

它不是单纯的 todo list。当前实现分成两层：

- **v1 执行态 tracker**：保存任务列表、任务状态和每次变更后的 checkpoint。
- **v2 长任务控制面 wrapper**：包在真实 Agent 外层，把 v1 的真实状态变化转换成目标锚、账本、机械态，并在每轮计算验收/漂移摘要。

核心思想是：**Agent 可以推理下一步，但不能靠聊天记录自证自己做过什么；做过什么必须来自工具和持久化状态。**

## 要解决的问题

长任务 Agent 容易在这几个地方失控：

- 中断或重启后，模型只能翻历史对话猜进度。
- 计划和执行事实混在一起，模型可能把“打算做”说成“已经做”。
- 多轮推进后，原始目标、非目标和约束会被稀释。
- 没有结构化账本时，很难知道一次回答到底写了哪些状态、凭什么判断下一步。

这个模块的模式是把这些风险拆开处理：

- **进度事实** 由 `ProgressTracker` 和 SQLite 保存。
- **工具边界** 由 Eino ADK tools 暴露给 Agent。
- **长任务记忆** 由 v2 wrapper 从真实状态差异里生成。
- **恢复提示** 由 `ResumePacket` / `RecitationPrompt` 每轮注入。
- **验收和漂移** 由规则函数从恢复包里确定性计算。

## 分层设计

### 1. v1: 执行态 checkpoint

v1 对应最朴素的 Python `ProgressTracker`：

- `TaskStatus` / `TaskItem` 定义任务状态和任务项，见 [types.go](types.go)。
- `ProgressTracker.CreatePlan` 初始化任务列表，见 [tracker.go](tracker.go)。
- `ProgressTracker.Start` / `Complete` / `Fail` 每次只更新一个任务。
- `ProgressTracker.ResumptionContext` 生成重启后能直接交给 Agent 的进度文本。
- [sqlite_store.go](sqlite_store.go) 用 `progress_plans` / `progress_items` 保存 checkpoint。

这一层故意保持简单。它只回答三个问题：

- 现在有哪些任务？
- 每个任务是什么状态？
- 如果进程重启，下一轮该从哪里继续？

所以 v1 是“执行态事实源”，不是长任务大脑。

### 2. ADK tools: 让 Agent 写真实状态

[agent.go](agent.go) 把 v1 tracker 包成 Eino ADK 工具：

- `progress_resumption_context`：每轮行动前读取当前进度。
- `progress_create_plan`：根据用户目标创建计划。
- `progress_start_task`：把某个任务标记为进行中。
- `progress_complete_task`：写入完成结果和材料引用。
- `progress_fail_task`：写入失败原因。

Agent 场景是线下活动筹备，不是 coding。用户只给自然语言目标，例如“筹备 80 人读书会”。模型可以决定怎么拆任务、什么时候标记完成，但真正的状态变更必须经过这些工具写入 SQLite。

这就是第一层边界：**模型负责选择工具，工具负责落状态。**

### 3. v2: 长任务控制面 wrapper

v2 不替代 v1，也不让模型自由总结“我刚刚做了什么”。它在真实 Agent 外面加一层 wrapper，见 [v2_agent.go](v2_agent.go)：

```text
用户消息
-> LongHorizonTracker.ResumePacket / RecitationPrompt
-> 注入现有 Eino ADK Agent
-> Agent 调用 v1 progress tools
-> wrapper 对比 ProgressTracker 前后状态
-> LongHorizonTracker.AppendEvent / WriteMechanicalValue
-> EvaluateVerificationGate / EvaluateDrift
-> 输出原回答 + V2 checkpoint 摘要
```

这里最关键的是“对比前后状态”。v2 账本不是模型口述出来的，而是 wrapper 从 v1 `TaskItem` 的真实 diff 里写出来的：

- 新计划出现了，就追加 `progress:plan` 证据。
- 某个任务从 pending 变成 completed，就追加 `progress:item:<index>` 证据。
- 任务状态变化后，同步写入 `MechanicalValue`，形成可恢复的机械态索引。
- 最后基于恢复包跑 `EvaluateVerificationGate` 和 `EvaluateDrift`。

这就是第二层边界：**v1 是事实源，v2 是控制面；v2 只能根据事实源生成审计和恢复材料。**

## v2 状态模型

v2 的类型集中在 [v2_types.go](v2_types.go)，持久化在 [v2_store.go](v2_store.go)，操作入口在 [v2_tracker.go](v2_tracker.go)。

它把长任务拆成五类状态：

- `GoalContract`：冻结目标、成功标准、非目标和约束，防止越跑越偏。
- `Milestone`：当前阶段和验收条件，用来判断是不是可以汇报完成。
- `ProgressEvent`：append-only 账本，记录事件、决策、证据、状态读写、补偿动作和下一步。
- `MechanicalValue`：可复用的机械态真值，保存 value/ref/provenance；恢复包只暴露 key，编排器通过 `ResolveMechanicalValue` 解析真实值，避免模型复制和拼接参数。
- `DriftSignal`：漂移判断结果，描述目标相关度、证据健康和是否需要 recenter。

恢复时，`ResumePacket` 只带最小可靠上下文：

- 目标锚点。
- 当前里程碑。
- 最近 N 条账本。
- open blockers。
- 机械态 key 列表，不包含真实 value。
- 上次漂移信号。
- 下一步动作。

`RecitationPrompt` 再把这些内容压成一段复诵提示，塞回每轮 Agent 输入前面。这样模型不需要读完整聊天记录，也能被拉回原始目标和当前阶段。

执行型工具可以通过 `StatefulToolOrchestrator` 串起完整机械态闭环：

```text
StatefulToolCall Args: {"group_id":"STATE.payroll_group_id"}
-> ResolveMechanicalValue("STATE.payroll_group_id")
-> 工具收到真实参数 {"group_id":"pg_84721"}
-> 工具成功返回 {"batch_id":"pb_202606_001"}
-> MechanicalOutputBinding 写入 STATE.payroll_batch_id
-> AppendEvent 记录 StateDelta.Read/Write
```

这个闭环仍然不把真实 value 放进 `ResumePacket`。模型只看到 `mechanical_state_keys`，编排器在工具执行前解析真实值，工具成功后再把新真值写回机械态。

## 为什么 v2 要包在 v1 外面

这个设计故意没有把 v1 改造成一个超大的状态机，原因是：

- v1 的职责已经足够稳定：任务列表和 checkpoint。
- ADK tools 已经让 Agent 的真实动作落在 v1。
- v2 需要的是审计、恢复和反漂移，不应该破坏原来的任务更新路径。
- wrapper 可以同时看到 Agent 运行前后的 v1 快照，天然适合生成确定性 ledger。

所以这里采用的是 **控制面 wrapper 模式**：

```text
业务 Agent
  只关心用户消息和工具调用

v1 ProgressTracker
  保存任务级执行状态

v2 LongHorizonTracker
  保存目标锚、里程碑、账本、机械态、恢复包

wrapper
  连接三者：注入恢复上下文，观察真实状态差异，写入 v2 控制面
```

这个模式的好处是：业务 Agent 可以继续正常跑，v1 工具语义也不用变；长任务能力作为外层能力叠上去，失败时也能通过 SQLite 里的两套状态恢复。

## SQLite 命名空间

v1 和 v2 使用同一个 SQLite 文件，但用不同表和不同 id 隔离：

- v1 plan id：`<plan-id>`
- v2 task id：`<plan-id>:v2`
- v1 表：`progress_plans` / `progress_items`
- v2 表：`long_horizon_tasks` / `long_horizon_milestones` / `long_horizon_events` / `long_horizon_mechanical_values`，以及为后续持久化 gate/drift 预留的 `long_horizon_drift_signals` / `long_horizon_gate_results`

共享 SQLite 文件是为了方便一次运行同时排查“执行态”和“控制面”，不是为了让两层互相改对方的数据。

## 运行入口

命令入口在 [cmd/progress-tracking-agent/main.go](../../cmd/progress-tracking-agent/main.go)。

确定性验证，不调用模型：

```bash
GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/progress-tracking-agent \
  -prepare-only \
  -db /tmp/event-progress.sqlite \
  -plan-id event-book-club \
  -items '确认场地;发布报名页;准备签到物料'
```

真实 Agent 运行。main 会先让 Agent 生成计划，再从 SQLite tracker 查询生成了哪些计划项，并按计划 index 自动触发前三轮，所以输出里能看到“计划产生 -> 查询生成计划 -> 遍历 3 轮 -> 编辑完成 -> 看看结果”：

```bash
GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/progress-tracking-agent \
  -db /tmp/event-progress.sqlite \
  -plan-id event-book-club \
  -message-file memory/progress_tracking/examples/event_goal.txt
```

真实运行会在原有 Agent 外层自动套上 v2 长任务控制层。输出里搜索 `V2 checkpoint`，可以确认本轮 Agent 动作已经被写入 v2 ledger。

模型配置来自 `.env` 或环境变量：

```bash
OPENAI_API_KEY=...
LLM_MODEL=...
LLM_OPENAI_BASE_URL=...
```

progress tracker 的运行参数也可以放在 `.env`：

```bash
PROGRESS_TRACKING_DB=output/progress-tracking.sqlite
PROGRESS_TRACKING_PLAN_ID=event-book-club
PROGRESS_TRACKING_MESSAGE_FILE=memory/progress_tracking/examples/event_goal.txt
```

命令行 flag 优先级高于 `.env`。这里没有内置任务答案：DB 路径、plan id、用户消息、任务列表都来自命令行、文件或环境变量；代码只提供 tracker、SQLite checkpoint、ADK 工具和长任务控制面边界。

真实 Agent 模式会安装 `observability/cozeloop` 的 Eino 官方 callback；未配置 `COZELOOP_*` 时自动 no-op，不需要额外 trace flag。命令级 root span 只上报 `db_path`、`plan_id`、触发轮数和结果摘要，具体 ADK/model/tool 调用由 Eino callback 自动形成 trace。

## 当前边界

当前 v2 是确定性、test-first 的第一版：

- 它已经能冻结目标锚、生成恢复包、写 append-only ledger、维护机械态 key、跑 gate/drift。
- 它不会让 LLM 自己描述事实变更；事实变更来自 v1 tracker diff。
- 它还没有把 milestone 自动推进成完整编排器，当前重点是证明 wrapper 控制面和恢复链路成立。

如果要继续扩展，优先方向应该是让 milestone transition、gate result 持久化和补偿动作执行器变成更完整的状态机；不要把这些逻辑塞回 prompt 里。
