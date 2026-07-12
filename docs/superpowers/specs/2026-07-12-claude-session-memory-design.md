# Claude Code 风格 Session Memory 核心链路设计

## 目标

在 `memory/claude_auto_memory` 现有长期自动记忆闭环中增加 Claude Code 风格的 Session Memory，使系统能够：

- 在长会话中后台维护结构化任务摘要，不阻塞主回答。
- 上下文接近阈值时，用 Session Memory 替换已摘要的旧消息，同时保留最近消息。
- 使用同一个 `session-id` 重启后恢复任务现场。
- 继续让 Auto Memory 独立沉淀跨会话的稳定偏好和项目知识。
- 提供可降低阈值的三轮或多轮 CLI 演示，适合面试讲解。

本次不追求逐行复刻 Claude Code，也不增加 Redis、SQLite、向量库、完整工具循环或生产级分布式一致性。

## 核心边界

Auto Memory 与 Session Memory 是并行系统，不互相晋升或复制：

- Auto Memory 保存 `user/feedback/project/reference` 等长期稳定信息。
- Session Memory 保存当前会话的任务目标、进度、关键文件、错误修正和下一步。
- 两者读取完整 Transcript；只有 Session Compactor 修改发给主模型的 Context View。
- Compact Summary 不是用户原始消息，禁止进入 Auto Memory 提取输入。

## 为什么必须先拆分 Transcript 与 Context

当前 `Runner.history` 同时承担完整会话和模型上下文，`Extractor` 使用消息数量下标作为游标。Session Memory 引入 Compact 后，旧消息会从模型上下文中被删除，数字游标可能大于压缩后的消息数量，导致 Auto Memory 无法继续增量抽取。

因此 Runner 必须维护两个视图：

```text
Transcript
  - 完整、只追加、持久化
  - Auto Memory 和 Session Memory 的事实输入

Context View
  - 发给主模型
  - 可以替换为 Session Summary + 最近消息
  - 不作为长期抽取游标的事实源
```

## 数据模型

### ConversationMessage

每条消息增加稳定标识和来源类型：

```go
type ConversationMessage struct {
    ID            string      `json:"id"`
    Role          Role        `json:"role"`
    Content       string      `json:"content"`
    Kind          MessageKind `json:"kind"`
    ToolCallCount int         `json:"tool_call_count,omitempty"`
}
```

`MessageKind` 至少包含：

- `normal`：用户和主 Agent 的真实消息。
- `compact_summary`：Session Memory 生成的压缩摘要，仅进入 Context View。

Auto Memory 的消息数量游标改成 `lastExtractedMessageID`。游标只在模型提取和主题写入成功后推进；失败时保留，下一次重试。

### SessionMemoryConfig

配置包含：

- `MinimumTokensToInit`：首次生成摘要的上下文 token 阈值，默认 10000。
- `MinimumTokensBetweenUpdates`：后续更新至少增长的 token，默认 5000。
- `ToolCallsBetweenUpdates`：辅助触发阈值，默认 3。
- `CompactTokens`：触发 Context Compact 的阈值。
- `MinimumRecentMessages`：Compact 后至少保留的最近消息数量。
- `ExtractionWaitTimeout`：Compact 等待后台摘要更新的上限，默认 15 秒。

CLI 可以覆盖阈值以稳定演示，生产默认值保持接近 Claude Code。

### SessionState

状态文件保存：

```json
{
  "last_summarized_message_id": "message-uuid",
  "tokens_at_last_update": 12000,
  "initialized": true
}
```

将摘要边界落盘是本实现为稳定 Resume 增加的工程措施；不把它解释为 Claude Code 的逐行等价实现。

## 存储布局

```text
<memory-root>/
├── MEMORY.md
├── <private-topic>.md
├── team/
│   ├── MEMORY.md
│   └── <team-topic>.md
└── sessions/
    └── <session-id>/
        ├── transcript.jsonl
        └── session-memory/
            ├── summary.md
            └── state.json
```

规则：

- `session-id` 必须经过安全校验，只允许受控字符，禁止路径穿越。
- Transcript 使用 JSONL 追加，保留完整消息顺序。
- Summary 与 State 使用同目录临时文件加 rename 原子写入。
- Auto Memory Store 不读取或扫描 `sessions/` 目录。

## Session Summary 模板

摘要保留稳定栏目：

```markdown
# 会话标题
# 当前状态
# 任务要求
# 重要文件与函数
# 工作流程
# 错误与修正
# 系统结构
# 经验结论
# 关键结果
# 工作日志
```

Session Summarizer 接收：

- 当前 Summary。
- `lastSummarizedMessageID` 之后的真实 Transcript 消息。
- Summary 文件的固定结构约束。

模型必须返回完整的新 Summary。存储层验证所有固定标题仍然存在后才原子替换文件并推进 UUID 边界。空结果、结构损坏或模型错误都不会推进边界。

## 组件职责

### TranscriptStore

- 追加带 UUID 的真实消息到 `transcript.jsonl`。
- 启动时恢复完整 Transcript。
- 拒绝重复 UUID、非法角色和 `compact_summary` 落盘。

### SessionStore

- 初始化 Summary 模板和 State。
- 原子读取、校验和更新 Summary/State。
- 对 Session 目录执行路径和符号链接保护。

### SessionMemoryUpdater

- 根据 token 增长、工具调用次数和自然对话边界判断是否更新。
- 只把 `normal` 消息交给 `SessionSummarizer`。
- 更新成功后记录 `lastSummarizedMessageID` 和 token 水位。

### SessionScheduler

- 回答完成后 fire-and-forget 调度更新。
- 同时最多运行一个摘要任务。
- 繁忙时只保留最新 Transcript 快照，完成后执行一次 trailing update。
- 提供 `Wait`，仅供 Compact、测试和进程退出边界使用。

### SessionCompactor

- 仅在 Context View 达到 Compact 阈值时运行。
- 最多等待 15 秒让正在运行的 Session Memory 更新完成。
- Summary 不存在、仍为空模板或边界非法时返回无操作结果，让调用方继续使用现有 Context。
- 边界 UUID 存在时，使用 Summary 替换该 UUID 及之前的旧消息。
- 保留边界之后的消息，并向前扩展以满足 `MinimumRecentMessages`。
- Resume 时边界缺失但 Summary 有效，则使用 Summary 加 Transcript 尾部消息重建 Context。

### Runner

Runner 持有：

- `transcript`：完整消息，只追加。
- `contextMessages`：当前模型上下文，可压缩。
- Auto Memory Recaller 和 ExtractionScheduler。
- Session Memory Updater、Scheduler 和 Compactor。

Runner 不直接实现摘要、存储或裁剪细节，只负责业务顺序。

## 运行时数据流

### 正常一轮

```text
用户输入
  -> 生成 UUID
  -> 追加 TranscriptStore
  -> 追加 transcript/contextMessages
  -> SessionCompactor.MaybeCompact(contextMessages)
  -> Auto Memory Recall
  -> Main Agent Generate(compacted context + recalled memory)
  -> 回答生成 UUID 并追加 Transcript/context
  -> SessionScheduler.Schedule(transcript snapshot)
  -> AutoMemoryScheduler.Schedule(transcript snapshot)
  -> 立即返回回答
```

Session Memory 和 Auto Memory 的后台失败都只形成 warning，不回滚已经返回的主回答。

### Compact

```text
Context 达到阈值
  -> 等待当前 Session 更新，最多 15 秒
  -> 读取 summary.md + state.json
  -> 校验 lastSummarizedMessageID
  -> contextMessages = compact_summary + 最近消息
  -> Transcript 保持不变
```

### Resume

```text
使用相同 session-id 启动
  -> 加载 transcript.jsonl
  -> 加载 summary.md + state.json
  -> 根据摘要边界构造 compact_summary + 最近消息
  -> 后续新消息继续追加原 Transcript
```

### 退出

交互主链路不等待两个后台系统。CLI 脚本、测试和进程退出时统一 Drain Auto Memory，并 Wait Session Memory，确保演示输出和落盘稳定。

## Auto Memory 集成规则

- Auto Memory Extractor 从完整 Transcript 读取新增 `normal` 消息。
- 使用 UUID 游标，不依赖当前 Context View 长度。
- 忽略 `compact_summary`，防止当前任务摘要被误存为长期偏好。
- Auto Memory 是否写入仍由四种类型与 private/team 作用域决定。
- Session Summary 不自动晋升为 Auto Memory；同一段真实对话可以由两个独立模型分别抽取不同价值的信息。

## 错误处理与降级

- Transcript 追加失败：当前轮返回错误，不调用模型，避免内存和磁盘状态分叉。
- Session Summary 模型失败：保留旧 Summary 和旧 UUID 边界，下一次重试。
- Summary 结构校验失败：拒绝覆盖，记录 warning。
- Compact 等待超时：使用最后一次成功 Summary；没有有效 Summary 时不 Compact。
- UUID 边界在当前 Context 中找不到：Resume 时使用摘要加尾部消息；普通运行时记录 warning 并不裁剪。
- Auto Memory 或 Session Memory 后台失败互不影响，也不阻塞主回答。
- State 写入失败时不报告摘要更新成功；Summary 与 State 更新需要由 SessionStore 保持一致的提交顺序和可恢复性。

## CLI

增加参数：

- `-session-id`：指定会话；为空时生成新 ID。
- `-resume`：要求加载既有 Session，不存在时返回错误。
- `-session-init-tokens`：覆盖首次摘要阈值。
- `-session-update-tokens`：覆盖增量摘要阈值。
- `-compact-tokens`：覆盖 Compact 阈值。
- `-session-recent-messages`：Compact 后最低保留消息数。

CLI trace 只输出低敏边界：

```text
session_id=interview-demo resumed=false
session_updated=true summarized_through=<short-id>
compacted=true before=12 after=5
auto_memory_written=1
```

不输出提取 Prompt、完整 Summary 或长期记忆正文。

## 代码组织

新增文件：

- `session_types.go`：Session 配置、状态、结果和模型接口。
- `transcript_store.go`：JSONL Transcript 追加与恢复。
- `session_store.go`：Summary/State 初始化、校验和原子写入。
- `session_prompts.go`：中文模板与摘要 Prompt。
- `session_memory.go`：阈值判断与增量更新。
- `session_scheduler.go`：后台单实例、latest-wins 和 Wait。
- `session_compactor.go`：摘要边界验证和 Context 裁剪。

修改文件：

- `types.go`：消息 UUID、Kind 和工具调用计数。
- `extractor.go`：Auto Memory 数字游标改 UUID 游标。
- `runner.go`：拆分 Transcript 与 Context 并装配双后台链路。
- `llm.go`：增加隔离的 Session Summarizer 适配器。
- CLI `main.go`：Session 参数、Resume、双 Drain 和边界 trace。
- `README.md`：补充 Session Memory 运行语义和演示命令。

所有新增公开结构、函数和关键生命周期分支添加中文用途注释；Prompt 使用中文。

## 测试与验收

### 单元测试

- Transcript 能追加并按原顺序恢复，重复 UUID 被拒绝。
- Auto Memory UUID 游标只提取新增真实消息。
- Compact Summary 不进入 Auto Memory 提取输入。
- Session 首次阈值和增量阈值判断正确。
- Summary 更新成功才推进 UUID 和 token 水位。
- Summary 结构损坏时拒绝写入。
- SessionScheduler 不阻塞调用方，繁忙时只保留最新快照。
- Compact 使用 Summary 替换旧消息并保留最近消息。
- Compact 边界缺失时安全降级。
- Resume 能从 Transcript、Summary 和 State 重建 Context。

### 集成测试

- 本轮主回答先返回，两个后台模型仍阻塞时主链路不被阻塞。
- Session Compact 后 Auto Memory 不重复提取旧消息。
- Auto Memory 用户偏好与 Session 当前进度能同时进入主 Agent。
- 使用同一 `session-id` 创建第二个 Runner 后能够继续前一任务。
- Session 更新失败不影响 Auto Memory，Auto Memory 失败也不影响 Session 更新。

### CLI 演示验收

使用低阈值运行多轮场景，证明：

1. 前两轮生成并更新 `summary.md`。
2. Context 达到阈值后发生 Compact。
3. 主 Agent 仍能回答当前任务进度。
4. 退出后使用相同 `session-id -resume` 能继续任务。
5. 用户长期偏好仍从 Auto Memory 独立召回。

## 明确不做

- Redis/数据库 Checkpoint。
- 多进程同时写同一 Session。
- 完整 Tool Use/Tool Result 配对裁剪；当前只保留工具调用计数扩展位。
- Claude Code 所有远程特性开关和遥测。
- Session Summary 自动晋升为长期记忆。
- 逐 token 精确计数；MVP 使用可替换 TokenEstimator，默认采用稳定估算。
- 对旧版无 UUID Transcript 的兼容迁移；当前模块仍是面试实验，可直接使用新会话目录。

## 完成定义

只有同时满足以下条件才算实现 Session Memory，而不是普通聊天历史：

- Session Summary 后台持续更新且不阻塞主回答。
- Summary 具有 UUID 摘要边界，失败不推进。
- Context 可以被摘要加最近消息替换，Transcript 不丢失。
- 同一 Session 能在进程重启后恢复。
- Auto Memory 与 Session Memory 的存储、Prompt、游标和失败边界保持隔离。
