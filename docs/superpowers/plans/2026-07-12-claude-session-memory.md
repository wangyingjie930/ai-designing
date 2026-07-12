# Claude Code 风格 Session Memory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `memory/claude_auto_memory` 增加后台 Session Summary、UUID 摘要边界、Context Compact 和基于 `session-id` 的 Resume，同时保持 Auto Memory 独立运行。

**Architecture:** Runner 将完整只追加的 Transcript 与可压缩的 Context View 分离；Auto Memory 和 Session Memory 都消费 Transcript，Session Compactor 只替换 Context View。Session Summary 与状态按 session 目录原子落盘，后台失败不阻塞主回答。

**Tech Stack:** Go 1.24、CloudWeGo Eino `BaseChatModel`、`github.com/google/uuid`、JSONL、Markdown、Go 标准库测试。

## Global Constraints

- 新增公开结构、函数和关键生命周期分支必须有中文用途注释。
- 新增模型 Prompt 必须使用中文。
- 不增加数据库、Redis、向量库或新的第三方依赖。
- Auto Memory 与 Session Memory 使用独立 Prompt、存储、游标和调度器。
- Compact Summary 不能进入 Auto Memory 提取输入。
- 后台 Session 更新失败不阻塞主回答，也不推进 UUID 摘要边界。
- 默认阈值接近 Claude Code：首次 10000 tokens、后续增长 5000 tokens、工具调用阈值 3、等待上限 15 秒。
- 每个任务遵循先失败测试、再最小实现、最后相关包回归的 TDD 顺序。

---

### Task 1: 消息 UUID、Transcript Store 与 Auto Memory UUID 游标

**Files:**
- Modify: `memory/claude_auto_memory/types.go`
- Modify: `memory/claude_auto_memory/extractor.go`
- Modify: `memory/claude_auto_memory/extractor_test.go`
- Create: `memory/claude_auto_memory/transcript_store.go`
- Create: `memory/claude_auto_memory/transcript_store_test.go`

**Interfaces:**
- Produces: `MessageKind`, `MessageKindNormal`, `MessageKindCompactSummary`。
- Produces: `NewConversationMessage(role Role, content string) ConversationMessage`。
- Produces: `NewTranscriptStore(root, sessionID string) (*TranscriptStore, error)`。
- Produces: `Append(context.Context, ConversationMessage) error` 和 `Load(context.Context) ([]ConversationMessage, error)`。
- Changes: `Extractor.Cursor()` 返回最后成功处理的消息 UUID 字符串。

- [ ] **Step 1: 为消息类型、Transcript 和 UUID 游标编写失败测试**

```go
func TestTranscriptStoreAppendAndLoad(t *testing.T) {
    store, err := NewTranscriptStore(t.TempDir(), "session-1")
    if err != nil { t.Fatal(err) }
    first := NewConversationMessage(RoleUser, "开始任务")
    second := NewConversationMessage(RoleAssistant, "收到")
    if err := store.Append(context.Background(), first); err != nil { t.Fatal(err) }
    if err := store.Append(context.Background(), second); err != nil { t.Fatal(err) }
    loaded, err := store.Load(context.Background())
    if err != nil || len(loaded) != 2 || loaded[0].ID != first.ID { t.Fatalf("loaded=%+v err=%v", loaded, err) }
}

func TestExtractorUsesUUIDCursorAndIgnoresCompactSummary(t *testing.T) {
    history := []ConversationMessage{
        NewConversationMessage(RoleUser, "记住中文注释"),
        {ID: uuid.NewString(), Role: RoleUser, Content: "会话摘要", Kind: MessageKindCompactSummary},
        NewConversationMessage(RoleAssistant, "好的"),
    }
    result := extractor.ExtractNew(context.Background(), history)
    if result.ProcessedMessages != 2 || extractor.Cursor() != history[2].ID { t.Fatalf("result=%+v", result) }
}
```

- [ ] **Step 2: 运行测试确认因缺少新类型和 Store 而失败**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -run 'TestTranscriptStore|TestExtractorUsesUUID' -count=1`

Expected: FAIL，提示 `NewTranscriptStore`、`MessageKindCompactSummary` 或新的 `Cursor` 契约不存在。

- [ ] **Step 3: 实现消息 UUID、Transcript JSONL 和 UUID 增量选择**

```go
type MessageKind string

const (
    MessageKindNormal         MessageKind = "normal"
    MessageKindCompactSummary MessageKind = "compact_summary"
)

type ConversationMessage struct {
    ID            string      `json:"id"`
    Role          Role        `json:"role"`
    Content       string      `json:"content"`
    Kind          MessageKind `json:"kind"`
    ToolCallCount int         `json:"tool_call_count,omitempty"`
}

func NewConversationMessage(role Role, content string) ConversationMessage {
    return ConversationMessage{ID: uuid.NewString(), Role: role, Content: strings.TrimSpace(content), Kind: MessageKindNormal}
}
```

Transcript Store 必须校验 session ID、拒绝 compact summary、拒绝重复 UUID，并用 `O_APPEND|O_CREATE|O_WRONLY` 追加一行 JSON。Extractor 从 `lastExtractedMessageID` 后收集 `normal` 消息；游标不存在时安全处理完整快照，成功后推进到快照最后一个真实消息 UUID。

- [ ] **Step 4: 运行 Task 1 测试和包回归**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS。

- [ ] **Step 5: 提交 Task 1**

```bash
git add memory/claude_auto_memory/types.go memory/claude_auto_memory/extractor.go memory/claude_auto_memory/extractor_test.go memory/claude_auto_memory/transcript_store.go memory/claude_auto_memory/transcript_store_test.go
git commit -m "feat: add transcript and UUID memory cursor"
```

### Task 2: Session Store、模板校验与 LLM Summarizer

**Files:**
- Create: `memory/claude_auto_memory/session_types.go`
- Create: `memory/claude_auto_memory/session_store.go`
- Create: `memory/claude_auto_memory/session_store_test.go`
- Create: `memory/claude_auto_memory/session_prompts.go`
- Modify: `memory/claude_auto_memory/llm.go`
- Modify: `memory/claude_auto_memory/llm_test.go`

**Interfaces:**
- Produces: `SessionMemoryConfig`、`DefaultSessionMemoryConfig()`。
- Produces: `SessionState`、`SessionSnapshot`、`SessionSummarizer`。
- Produces: `NewSessionStore(root, sessionID string) (*SessionStore, error)`。
- Produces: `Load(context.Context) (SessionSnapshot, error)` 和 `Commit(context.Context, summary string, state SessionState) error`。
- Produces: `NewLLMSessionSummarizer(model.BaseChatModel) (*LLMSessionSummarizer, error)`。

- [ ] **Step 1: 编写 Session Store 与模型隔离失败测试**

```go
func TestSessionStoreCommitAndLoad(t *testing.T) {
    store, err := NewSessionStore(t.TempDir(), "session-1")
    if err != nil { t.Fatal(err) }
    summary := strings.Replace(defaultSessionMemoryTemplate, "# 当前状态\n", "# 当前状态\n正在实现 Session Memory。\n", 1)
    state := SessionState{LastSummarizedMessageID: "message-1", TokensAtLastUpdate: 12000, Initialized: true}
    if err := store.Commit(context.Background(), summary, state); err != nil { t.Fatal(err) }
    loaded, err := store.Load(context.Background())
    if err != nil || loaded.State.LastSummarizedMessageID != "message-1" { t.Fatalf("loaded=%+v err=%v", loaded, err) }
}

func TestSessionStoreRejectsBrokenTemplate(t *testing.T) {
    store, _ := NewSessionStore(t.TempDir(), "session-1")
    err := store.Commit(context.Background(), "# 当前状态\n内容", SessionState{})
    if err == nil { t.Fatal("expected template validation error") }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -run 'TestSessionStore|TestLLMSession' -count=1`

Expected: FAIL，提示 Session 类型、Store 或 Summarizer 不存在。

- [ ] **Step 3: 实现 Session Store、固定中文模板和模型适配器**

```go
type SessionSummarizer interface {
    Summarize(ctx context.Context, currentSummary string, messages []ConversationMessage) (string, error)
}

type SessionState struct {
    LastSummarizedMessageID string `json:"last_summarized_message_id"`
    TokensAtLastUpdate      int    `json:"tokens_at_last_update"`
    Initialized             bool   `json:"initialized"`
}
```

`Commit` 先校验十个固定标题，再分别原子写入 `summary.md` 和 `state.json`。`LLMSessionSummarizer` 使用 `[SESSION_MEMORY_UPDATE]` 中文 Prompt，输入当前摘要和新增真实消息，返回完整 Markdown，不复用 Auto Memory Prompt。

- [ ] **Step 4: 运行相关测试和包回归**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS。

- [ ] **Step 5: 提交 Task 2**

```bash
git add memory/claude_auto_memory/session_types.go memory/claude_auto_memory/session_store.go memory/claude_auto_memory/session_store_test.go memory/claude_auto_memory/session_prompts.go memory/claude_auto_memory/llm.go memory/claude_auto_memory/llm_test.go
git commit -m "feat: add session memory store and summarizer"
```

### Task 3: Session Memory 阈值更新器与后台调度器

**Files:**
- Create: `memory/claude_auto_memory/session_memory.go`
- Create: `memory/claude_auto_memory/session_memory_test.go`
- Create: `memory/claude_auto_memory/session_scheduler.go`
- Create: `memory/claude_auto_memory/session_scheduler_test.go`

**Interfaces:**
- Produces: `TokenEstimator` 和 `RoughTokenEstimator`。
- Produces: `NewSessionMemoryUpdater(store *SessionStore, summarizer SessionSummarizer, estimator TokenEstimator, config SessionMemoryConfig) (*SessionMemoryUpdater, error)`。
- Produces: `Update(context.Context, []ConversationMessage) SessionUpdateResult`。
- Produces: `NewSessionScheduler(*SessionMemoryUpdater) (*SessionScheduler, error)`、`Schedule`、`Wait`。

- [ ] **Step 1: 编写阈值、失败不推进和 latest-wins 测试**

```go
func TestSessionUpdaterAdvancesOnlyAfterSuccessfulCommit(t *testing.T) {
    updater := newTestUpdater(t, &fakeSessionSummarizer{err: errors.New("model unavailable")}, fixedEstimator(12000))
    messages := []ConversationMessage{NewConversationMessage(RoleUser, "任务"), NewConversationMessage(RoleAssistant, "回答")}
    failed := updater.Update(context.Background(), messages)
    if len(failed.Warnings) != 1 { t.Fatalf("failed=%+v", failed) }
    snapshot, _ := updater.store.Load(context.Background())
    if snapshot.State.LastSummarizedMessageID != "" { t.Fatalf("state=%+v", snapshot.State) }
}

func TestSessionSchedulerReturnsBeforeBlockedSummary(t *testing.T) {
    scheduler := newBlockingSessionScheduler(t)
    returned := make(chan struct{})
    go func() { scheduler.Schedule(context.Background(), testMessages()); close(returned) }()
    select { case <-returned: case <-time.After(time.Second): t.Fatal("Schedule blocked") }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -run 'TestSessionUpdater|TestSessionScheduler' -count=1`

Expected: FAIL，提示 Updater 或 Scheduler 不存在。

- [ ] **Step 3: 实现阈值和后台单实例**

`RoughTokenEstimator` 对消息内容使用稳定 rune 估算。Updater 首次达到 `MinimumTokensToInit` 后初始化；后续要求 token 增长达到 `MinimumTokensBetweenUpdates`，并结合 `ToolCallsBetweenUpdates` 或最后 assistant 无工具调用的自然停顿。Scheduler 复用 Auto Memory 的单实例、latest-wins、trailing update 模式，但维护独立状态和结果。

- [ ] **Step 4: 运行相关测试和 race 检查**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test -race ./memory/claude_auto_memory -run 'TestSessionUpdater|TestSessionScheduler' -count=1`

Expected: PASS，且无 data race。

- [ ] **Step 5: 提交 Task 3**

```bash
git add memory/claude_auto_memory/session_memory.go memory/claude_auto_memory/session_memory_test.go memory/claude_auto_memory/session_scheduler.go memory/claude_auto_memory/session_scheduler_test.go
git commit -m "feat: update session memory in background"
```

### Task 4: Session Compact 与 Resume Context 重建

**Files:**
- Create: `memory/claude_auto_memory/session_compactor.go`
- Create: `memory/claude_auto_memory/session_compactor_test.go`

**Interfaces:**
- Produces: `NewSessionCompactor(store *SessionStore, scheduler *SessionScheduler, estimator TokenEstimator, config SessionMemoryConfig) (*SessionCompactor, error)`。
- Produces: `MaybeCompact(context.Context, []ConversationMessage, bool) CompactResult`，最后一个 bool 表示 Resume 模式。
- `CompactResult` 包含 `Messages`、`Compacted`、`Before`、`After`、`Warnings`。

- [ ] **Step 1: 编写正常 Compact、边界缺失降级和 Resume 测试**

```go
func TestSessionCompactorReplacesSummarizedPrefix(t *testing.T) {
    messages := testMessagesWithIDs("m1", "m2", "m3", "m4")
    seedSessionSnapshot(t, store, summary, SessionState{LastSummarizedMessageID: "m2", Initialized: true})
    result := compactor.MaybeCompact(context.Background(), messages, false)
    if !result.Compacted || result.Messages[0].Kind != MessageKindCompactSummary { t.Fatalf("result=%+v", result) }
    if result.Messages[len(result.Messages)-1].ID != "m4" { t.Fatalf("messages=%+v", result.Messages) }
}

func TestSessionCompactorResumeUsesSummaryAndTailWhenBoundaryMissing(t *testing.T) {
    result := compactor.MaybeCompact(context.Background(), transcript, true)
    if !result.Compacted || result.Messages[0].Kind != MessageKindCompactSummary { t.Fatalf("result=%+v", result) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -run 'TestSessionCompactor' -count=1`

Expected: FAIL，提示 Compactor 不存在。

- [ ] **Step 3: 实现等待、边界校验和最近消息保留**

Compact 前调用 `SessionScheduler.Wait`，等待上限来自配置。普通运行时边界不存在则 warning + no-op；Resume 模式允许使用有效 Summary 加 Transcript 尾部重建 Context。Compact 只生成新的消息切片，不修改输入 Transcript。

- [ ] **Step 4: 运行 Compactor 与包回归测试**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS。

- [ ] **Step 5: 提交 Task 4**

```bash
git add memory/claude_auto_memory/session_compactor.go memory/claude_auto_memory/session_compactor_test.go
git commit -m "feat: compact context with session memory"
```

### Task 5: Runner 双通道集成与可恢复 Session

**Files:**
- Modify: `memory/claude_auto_memory/runner.go`
- Modify: `memory/claude_auto_memory/runner_test.go`
- Modify: `memory/claude_auto_memory/types.go`

**Interfaces:**
- Produces: `RunnerConfig`，包含 `SessionID` 和 Session 依赖。
- Changes: `NewRunner` 保留兼容包装；增加 `NewRunnerWithSession(...)` 装配完整链路。
- Produces: `SessionID()`、`Transcript()`、`ContextMessages()`、`WaitSession()`。
- Changes: `TurnResult` 增加 `Compacted` 和 Session warnings，不暴露 Summary 正文。

- [ ] **Step 1: 编写双后台、Compact 后不重复提取和 Resume 集成测试**

```go
func TestRunnerUsesSessionSummaryAndAutoMemoryTogether(t *testing.T) {
    runner := newSessionRunner(t, sessionID, lowThresholdConfig())
    runAndDrainTurns(t, runner, "记住中文注释", "开始实现 Session Memory")
    result, err := runner.RunTurn(context.Background(), "继续刚才的任务")
    if err != nil { t.Fatal(err) }
    if !strings.Contains(chat.lastContext, "compact_summary") || !strings.Contains(chat.lastMemoryContext, "中文注释") {
        t.Fatalf("context=%s memory=%s", chat.lastContext, chat.lastMemoryContext)
    }
}

func TestRunnerResumeRestoresSessionContext(t *testing.T) {
    first := newSessionRunner(t, "resume-demo", lowThresholdConfig())
    runAndDrainTurns(t, first, "当前任务是改 Session Memory")
    second := resumeSessionRunner(t, "resume-demo", lowThresholdConfig())
    if len(second.ContextMessages()) == 0 || len(second.Transcript()) == 0 { t.Fatal("session was not restored") }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory -run 'TestRunnerUsesSession|TestRunnerResume' -count=1`

Expected: FAIL，提示 Session Runner 接口不存在。

- [ ] **Step 3: 重构 Runner 编排顺序**

```text
append user transcript/context
-> maybe compact context
-> auto recall
-> main answer
-> append assistant transcript/context
-> schedule session update
-> schedule auto extraction
-> return answer
```

Transcript 追加失败时不调用主模型。主模型失败时不追加 assistant，也不调度两个后台系统。`Drain` 保留 Auto Memory 语义，`WaitSession` 单独返回 Session 更新结果，避免混淆两个生命周期。

- [ ] **Step 4: 运行 Runner、包测试和 race 检查**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test -race ./memory/claude_auto_memory -count=1`

Expected: PASS，且无 data race。

- [ ] **Step 5: 提交 Task 5**

```bash
git add memory/claude_auto_memory/runner.go memory/claude_auto_memory/runner_test.go memory/claude_auto_memory/types.go
git commit -m "feat: integrate auto and session memory"
```

### Task 6: CLI、三角色扩展、README 与完整验收

**Files:**
- Modify: `cmd/claude-auto-memory-agent/main.go`
- Modify: `cmd/claude-auto-memory-agent/main_test.go`
- Modify: `memory/claude_auto_memory/README.md`

**Interfaces:**
- CLI flags: `-session-id`、`-resume`、`-session-init-tokens`、`-session-update-tokens`、`-compact-tokens`、`-session-recent-messages`。
- `buildRunner` 装配第四个隔离角色 `LLMSessionSummarizer`。
- `runOutput` 增加低敏 Session 统计：Session ID、更新次数、Compact 次数和 Resume 状态。

- [ ] **Step 1: 编写 CLI Session 参数、模型隔离和 Resume 失败测试**

```go
func TestRunAgentBuildsSessionMemoryRole(t *testing.T) {
    output, err := runAgent(context.Background(), []string{
        "-env-file", envPath, "-memory-dir", memoryDir, "-rounds-file", roundsPath,
        "-session-id", "interview", "-session-init-tokens", "1", "-session-update-tokens", "1", "-compact-tokens", "20",
    })
    if err != nil { t.Fatal(err) }
    if output.SessionID != "interview" || scripted.sessionCalls == 0 { t.Fatalf("output=%+v calls=%d", output, scripted.sessionCalls) }
}

func TestRunAgentResumeRequiresExistingSession(t *testing.T) {
    _, err := runAgent(context.Background(), []string{"-resume", "-session-id", "missing", "-memory-dir", t.TempDir()})
    if err == nil { t.Fatal("expected missing session error") }
}
```

- [ ] **Step 2: 运行 CLI 测试确认失败**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./cmd/claude-auto-memory-agent -count=1`

Expected: FAIL，提示新 flags、输出字段或 Session 模型角色不存在。

- [ ] **Step 3: 实现 CLI 装配和低敏 trace，更新 README**

共享 `BaseChatModel` 通过 `[SESSION_MEMORY_UPDATE]` Marker 承担独立 Session Summarizer 角色。脚本每轮先输出主回答，再等待两个后台系统，使下一轮演示稳定消费上一轮 Summary 和 Auto Memory。README 增加新建会话、低阈值 Compact 和 `-resume` 示例，并明确交互主链路不等待后台任务。

- [ ] **Step 4: 格式化并运行完整测试**

Run: `gofmt -w memory/claude_auto_memory/*.go cmd/claude-auto-memory-agent/*.go`

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1`

Expected: PASS。

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test -race ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1`

Expected: PASS，且无 data race。

- [ ] **Step 5: 运行 Prepare-only 和 Git 检查**

Run: `go run ./cmd/claude-auto-memory-agent -prepare-only -memory-dir /private/tmp/claude-auto-session-memory-prepare -session-id interview`

Expected: 输出 private/team 索引以及 Session 路径，不调用模型。

Run: `git diff --check && git status --short`

Expected: `git diff --check` 无输出；状态只包含本计划内文件。

- [ ] **Step 6: 提交 Task 6**

```bash
git add cmd/claude-auto-memory-agent/main.go cmd/claude-auto-memory-agent/main_test.go memory/claude_auto_memory/README.md
git commit -m "feat: expose Claude session memory demo"
```

### Task 7: 最终验证与交付核对

**Files:**
- Verify only: `memory/claude_auto_memory/**`
- Verify only: `cmd/claude-auto-memory-agent/**`
- Verify only: `docs/superpowers/specs/2026-07-12-claude-session-memory-design.md`

**Interfaces:**
- Consumes: 前六个任务的所有公开接口和 CLI。
- Produces: 无新接口，只形成可复现验证证据。

- [ ] **Step 1: 对照 Spec 检查每项完成定义**

检查后台更新、UUID 边界、Context/Transcript 分离、Resume、双记忆隔离和失败降级均有对应测试。

- [ ] **Step 2: 运行完整目标包测试**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1`

Expected: PASS。

- [ ] **Step 3: 运行 race 测试**

Run: `env GOCACHE=/private/tmp/ai-designing-session-memory-gocache go test -race ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1`

Expected: PASS，且无 data race。

- [ ] **Step 4: 检查提交和工作区**

Run: `git log --oneline -8 && git status --short`

Expected: 能看到每个独立任务提交；工作区为空。
