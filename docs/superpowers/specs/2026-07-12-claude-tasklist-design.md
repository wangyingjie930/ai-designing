# Claude Code 风格 Task List 进度跟踪设计

## 目标

在现有 `memory/claude_auto_memory` 与 `cmd/claude-auto-memory-agent` 闭环旁新增 Claude Code 风格的 Task List，使长任务同时拥有三种边界清楚、职责不同的状态：

```text
Auto Memory    跨会话稳定知识
Session Memory 当前会话的叙述性恢复摘要
Task List      可机械读写的任务进度真相
```

最终实现必须满足：

1. 主 Agent 能通过 `TaskCreate`、`TaskGet`、`TaskUpdate`、`TaskList` 四个真实模型工具管理进度。
2. 任务使用文件持久化，使用相同 `session-id -resume` 后仍能读取未完成任务。
3. Task List 是进度真相源；`summary.md` 可以概括进度，但不能反向覆盖任务状态。
4. 现有 Auto Memory、Session Memory、Compact、Resume 和用户正在修改的 CLI 默认阈值行为保持兼容。
5. 默认 CLI 能用确定性测试证明工具调用、任务落盘和恢复，而不是只增加数据结构或演示代码。

## 方案选择

### 采用：独立 `memory/claude_tasklist` 包，由现有 CLI 装配

Task 类型、文件存储和四个工具位于独立包。`cmd/claude-auto-memory-agent` 增加一个薄适配器，把 Compact 后的会话上下文和 Auto Memory 召回结果交给 Task Agent 的 Eino ADK 工具循环。

优点：

- 记忆与进度仍是两个独立机制，符合 Claude Code 的真实职责边界。
- Task List 可以单独测试和复用，不依赖 Auto Memory 的 Markdown topic 结构。
- CLI 可以组合三条状态链，面试时能讲清楚“知识、摘要、机械进度”的差别。

### 不采用：直接把 Task 字段塞进 `claude_auto_memory`

这会让 `summary.md`、长期记忆和机械任务共享一个包与生命周期。模型生成摘要可能被误解为任务真相，错误处理和 Resume 边界也会相互污染。

### 不采用：直接复用 `memory/progress_tracking`

`progress_tracking` 使用 SQLite plan、0-based item 和 create/start/complete/fail 语义。Claude Code Task List 使用独立 JSON task、字符串 ID、owner、blocks/blockedBy，以及 `TaskCreate/Get/Update/List` 工具面。强行复用会产生大量语义转换，代码更难用于源码对照。

## 范围

### 实现范围

- Claude Code V2 风格四工具：`TaskCreate`、`TaskGet`、`TaskUpdate`、`TaskList`。
- 三种稳定状态：`pending`、`in_progress`、`completed`；`TaskUpdate` 额外接受 `deleted` 作为删除动作。
- 任务字段：`id`、`subject`、`description`、`activeForm`、`owner`、`status`、`blocks`、`blockedBy`、`metadata`。
- 单调递增字符串 ID；删除后不复用旧 ID。
- 依赖关系双向维护；删除任务时清理其他任务中的依赖引用。
- Task List 以 `session-id` 作为默认 `task-list-id`，为未来 team task list 保留显式 ID 边界。
- Eino ADK 工具循环接收现有完整 Context View，并把本轮工具调用数量写回 assistant transcript 消息。
- Prepare-only 初始化并输出任务目录；普通 CLI 每轮输出低敏任务状态计数。
- JSON Schema 回归测试，确保四个工具的必填字段、状态枚举和中文参数描述对模型可见。

### 不实现范围

- Claude Code 的终端 React UI、TaskList watcher、spinner 和后台通知组件。
- TeamCreate/TeamDelete、跨进程 teammate 抢占和自动领取下一任务。
- 多进程文件锁、分布式锁和跨设备同步；MVP 保持与当前 Auto/Session Store 一致的单进程运行边界。
- TaskCreated、TaskCompleted 等 hook 系统。
- 旧版 `TodoWrite` 兼容分支；本实现直接采用当前交互模式的 V2 Task 工具面。
- 根据摘要文本自动推断并改写任务状态。
- 自动把所有简单问答拆成任务；只有确实需要多步骤跟踪时才使用 Task 工具。

## 包与文件职责

新增包：

```text
memory/claude_tasklist/
├── types.go              # Task、状态、请求响应和 Agent 结果
├── store.go              # JSON 文件、ID 水位、依赖维护和原子写入
├── tools.go              # 四个 Eino 工具及确定性 Toolset 方法
├── agent.go              # Eino ChatModelAgent、完整消息输入和结果收集
├── prompts.go            # 中文任务使用规则
├── store_test.go
├── tools_test.go
├── tool_schema_test.go
└── agent_test.go
```

CLI 新增：

```text
cmd/claude-auto-memory-agent/
├── task_chat.go          # ConversationMessage 到 Eino Message 的薄适配器
└── task_chat_test.go
```

修改：

- `memory/claude_auto_memory/types.go`：让主回答返回正文和工具调用次数。
- `memory/claude_auto_memory/llm.go`：普通无工具 ChatAgent 适配新返回结构。
- `memory/claude_auto_memory/runner.go`：把工具调用次数写入 assistant Transcript。
- `cmd/claude-auto-memory-agent/main.go`：初始化 Task Store/Agent、Prepare-only 输出和每轮任务计数。
- `cmd/claude-auto-memory-agent/main_test.go`：把原主回答角色升级为 Task Agent，并增加 CLI 工具闭环断言；召回、提取、Session 摘要和主回答仍保持四种隔离 Prompt。
- `memory/claude_auto_memory/README.md`：加入三状态层、落盘结构、工具循环和运行说明。
- `memory/claude_auto_memory/examples/interview_rounds.txt`：加入能够真实触发任务创建、推进与恢复的长任务场景，同时保留记忆召回价值。

每个新增公开类型、结构体和函数使用中文用途注释；模型 Prompt 使用中文。现有注释全部保留。

## 数据模型

```go
type TaskStatus string

const (
    TaskStatusPending    TaskStatus = "pending"
    TaskStatusInProgress TaskStatus = "in_progress"
    TaskStatusCompleted  TaskStatus = "completed"
)

type Task struct {
    ID          string         `json:"id"`
    Subject     string         `json:"subject"`
    Description string         `json:"description"`
    ActiveForm  string         `json:"activeForm,omitempty"`
    Owner       string         `json:"owner,omitempty"`
    Status      TaskStatus     `json:"status"`
    Blocks      []string       `json:"blocks"`
    BlockedBy   []string       `json:"blockedBy"`
    Metadata    map[string]any `json:"metadata,omitempty"`
}
```

规则：

- 创建任务时固定为 `pending`、空 owner、空依赖。
- `subject` 和 `description` 去除首尾空白后不能为空。
- `activeForm` 是任务进行中时展示的现在进行式文案，不参与状态判断。
- `owner` 是可选 Agent 名称；空字符串表示取消分配。
- `blocks` 与 `blockedBy` 必须引用同一 Task List 中的既有任务，禁止引用自身。
- `TaskList` 只返回未完成 blocker；已 completed 的 blocker 仍保留在完整 Task 记录中，但不阻挡执行。
- `metadata` 作为扩展字段原样保存；MVP 不用 metadata 承载核心状态。

## 存储布局

```text
<memory-root>/
├── MEMORY.md
├── team/
├── sessions/<session-id>/...
└── tasks/
    └── <task-list-id>/
        ├── .highwatermark
        ├── 1.json
        ├── 2.json
        └── 3.json
```

`task-list-id` 默认等于现有 `session-id`。路径只允许字母、数字、下划线和连字符，禁止绝对路径、`..`、路径分隔符和符号链接越界。

`Store` 使用进程内互斥锁串行化一次 Task List 的读改写操作。任务文件和 `.highwatermark` 使用同目录临时文件加 rename 原子替换。创建任务时先读取任务文件最大数字 ID 与水位文件，取两者最大值加一。删除时先推进水位，再删除任务并清理依赖，确保 ID 不会复用。

## 四个工具

### TaskCreate

输入：`subject`、`description`、可选 `activeForm`、可选 `metadata`。

行为：创建 `pending` 任务并返回完整 Task。创建失败不留下半写任务。

### TaskGet

输入：`taskId`。

行为：返回完整 Task，包括 description、owner、全部 blocks/blockedBy 和 metadata。任务不存在时返回明确错误。

### TaskUpdate

输入：`taskId`，以及至少一个可选更新字段：`subject`、`description`、`activeForm`、`status`、`owner`、`addBlocks`、`addBlockedBy`、`metadata`。

行为：

- `status=deleted` 删除任务并清理依赖。
- 其他 status 必须属于三种稳定状态。
- `addBlocks=A` 同时让当前任务的 `blocks` 增加 A，并让 A 的 `blockedBy` 增加当前任务。
- `addBlockedBy=A` 执行相反方向的同一双向更新。
- 任一依赖引用非法时整次更新失败，不写入部分结果。
- metadata 采用键级合并；值为 `null` 时删除该键。

### TaskList

无输入。

行为：按数字 ID 排序返回 `id`、`subject`、`status`、`owner` 和尚未完成的 `blockedBy`。完整描述由 `TaskGet` 提供，避免每轮把所有正文塞给模型。

## Agent 与现有 Runner 集成

`claude_tasklist.Agent` 使用 Eino `adk.NewChatModelAgent` 注册四个工具。它提供：

```go
type AgentResult struct {
    Content       string
    ToolCallCount int
}

func (a *Agent) Run(ctx context.Context, messages []*schema.Message) (AgentResult, error)
```

`Agent.Run` 调用 `adk.Runner.Run(ctx, messages)`，因此每轮直接使用现有 Runner 已经恢复或 Compact 后的完整 Context View，而不是在 Task Agent 内部再维护一份会话历史。

命令层 `taskChatAgent` 实现 `claude_auto_memory.ChatAgent`：

1. 以 Task Agent 固定中文 instruction 作为工具规则。
2. 将 Auto Memory 召回正文作为独立 `<memory_context>` system message。
3. 将 `ConversationMessage` 映射为 Eino user/assistant message；compact summary 作为 system context，不伪装成真实用户消息。
4. 调用 `claude_tasklist.Agent.Run`。
5. 返回最终 assistant 正文和本轮 `ToolCallCount`。

Auto Memory Runner 把该计数写入最终 assistant `ConversationMessage`，供现有 Session Memory 的 `ToolCallsBetweenUpdates` 阈值使用。工具调用和工具结果不写入长期 Auto Memory topic；Task JSON 已经是这些机械状态的事实源。

## Task Agent 使用规则

固定中文 instruction 包含：

- 复杂、多步骤或用户明确要求跟踪的工作才创建任务；简单问答不创建。
- 开始多步骤工作或恢复任务时先调用 `TaskList`，避免重复创建。
- 新工作用 `TaskCreate` 拆成可验证动作，不能把尚未完成的结果写成任务结论。
- 开始某项工作前立即标记 `in_progress`；完成后立即标记 `completed`，不能最后批量完成。
- 通常只保持一个 `in_progress`，除非真实并行执行。
- 被未完成依赖阻塞的任务不能声称正在执行或已经完成。
- 任务状态必须通过工具更新，不能只在最终自然语言回答里宣称完成。
- 最终回答可以概括进度，但不能让摘要或回答替代 Task JSON。

## 运行时数据流

```text
用户输入
  -> Transcript 追加 user
  -> SessionCompactor 产生当前 Context View
  -> Auto Memory Recall
  -> taskChatAgent 转换完整上下文并注入 memory_context
  -> Eino Task Agent
       -> TaskList / TaskCreate / TaskUpdate / TaskGet
       -> Task Store 原子落盘
       -> 模型继续推理直到最终回答
  -> Transcript 追加 assistant + tool_call_count
  -> Session Memory 后台摘要
  -> Auto Memory 后台提取真实 user/assistant 对话
  -> CLI 输出回答和 Task 状态计数
```

Resume 时不根据 Transcript 重放 Task 工具。Task Agent 直接读取 `<memory-root>/tasks/<session-id>/` 的现状；Transcript 和 Session Summary 只负责恢复语义上下文。

## 错误处理

- Task Store 初始化失败：命令启动失败，不降级成“嘴上跟踪进度”。
- Task 工具参数非法：工具返回错误，Eino 循环允许模型读取错误并修正参数。
- Task 不存在：Get/Update 返回包含 task ID 的明确错误。
- 依赖任务不存在或自引用：整次 Update 失败，所有任务文件保持原状。
- 原子写入失败：保留原文件；创建不会推进成功结果，更新不会返回伪成功。
- Agent 达到最大迭代次数或没有最终文本：当前轮返回错误；已经成功执行的 Task 工具写入保留，因为它们是已经发生的显式状态变更。
- Transcript 写 assistant 失败：现有 Runner 返回错误；已经落盘的任务变更保留，Resume 后 Task List 仍可读取，Session Summary 不冒充回滚机制。
- Auto/Session Memory 后台失败：不回滚 Task 状态，也不把 Task Store 错误混入记忆索引。

## CLI 与可观察输出

Prepare-only 新增：

```text
tasks_dir=<memory-root>/tasks/<session-id>
```

普通每轮回答后新增低敏摘要：

```text
tasks=pending:2,in_progress:1,completed:1
```

不输出任务 description、metadata、模型工具参数、完整 Session Summary 或长期记忆正文。

`runOutput` 增加最终 `PendingTasks`、`InProgressTasks`、`CompletedTasks` 与累计 `TaskToolCalls`，用于命令集成测试，不用于替代 Task Store。

## 测试与验收

### Store 单元测试

- 创建任务得到从 `1` 开始的字符串 ID，并持久化完整 JSON。
- 重建 Store 后可以读取已有任务。
- 删除任务后下一个 ID 继续递增，不复用旧 ID。
- addBlocks/addBlockedBy 双向更新且去重。
- 非法依赖整次失败，不产生部分写入。
- 删除任务会清理其他任务中的 blocks/blockedBy。
- TaskList 按数字 ID 排序，并过滤已完成 blocker。
- 路径穿越和符号链接越界被拒绝。

### 工具与 Schema 测试

- 四个工具名与 Claude Code 一致。
- Create 默认写入 pending。
- Update 可以推进 pending -> in_progress -> completed，也可以 deleted。
- Get 返回完整任务，List 返回低成本摘要。
- `tool.Info(ctx).ParamsOneOf.ToJSONSchema()` 暴露 required、status enum 和中文 description。

### Agent 测试

- Fake 模型先 TaskList、再创建任务、再把首项标记 in_progress，最后根据工具结果回答。
- Fake 模型下一轮读取同一 Store，把首项标记 completed，并看到后续任务解除阻塞。
- `AgentResult.ToolCallCount` 等于真实模型工具调用数量。
- Agent 接收到 Compact Summary、最近消息和 Auto Memory context，但 Task 工具只操作独立 Store。

### CLI 集成测试

- Prepare-only 同时创建 Memory、Session 与 Task 目录，不需要 API key。
- 同一个 fake model 承担召回、长期提取、Session Summary 和 Task 主 Agent 四种隔离 Prompt；Task 主 Agent 内部执行多次模型调用不被误算成新的模型角色。
- 三轮场景创建计划、推进状态、沉淀长期记忆并触发 Compact。
- 第二次使用相同 session ID 和 `-resume` 后读取既有 Task List，不重复创建任务。
- 用户现有 `defaultOneClickSessionConfig` 修改保持生效，相关测试继续通过。

### 完成标准

以下命令全部通过：

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./memory/claude_tasklist ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1

env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./... -count=1

git diff --check
```

README 的最终运行链路必须明确表达：Task List 是机械进度真相，Session Memory 是恢复摘要，Auto Memory 是稳定知识；三者不能互相冒充。
