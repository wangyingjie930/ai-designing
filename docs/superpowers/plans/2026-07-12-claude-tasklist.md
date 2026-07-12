# Claude Code Task List Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a file-backed Claude Code V2-style Task List with four real model tools and integrate it into the existing Auto Memory/Session Memory CLI as the mechanical progress source of truth.

**Architecture:** A new `memory/claude_tasklist` package owns task JSON files, dependency updates, Eino tool schemas, and the ADK ReAct loop. The command layer adapts the compacted `ConversationMessage` view into that agent. `memory/claude_auto_memory` only gains a structured answer result so tool-call counts reach Transcript and Session Memory thresholds.

**Tech Stack:** Go 1.24, CloudWeGo Eino v0.9.6 ADK, `toolutils.InferTool`, JSON files with atomic rename, standard-library tests.

## Global Constraints

- Preserve existing comments; add Chinese purpose comments before all new public structs/functions.
- Keep Codex-authored prompts in Chinese.
- Do not reuse `memory/progress_tracking`. Task JSON is progress truth; memory prose never mutates it.
- Do not add database, embeddings, UI watcher, hooks, team lifecycle, or cross-process locking.
- Preserve the user's unstaged `defaultOneClickSessionConfig` and its test.
- Do not stage pre-existing user hunks in the two CLI files; inspect diffs before every commit.
- Follow red-green-refactor for every behavior.
- Verify with `GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache`.

---

### Task 1: File-backed Task Store

**Files:**

- Create: `memory/claude_tasklist/types.go`
- Create: `memory/claude_tasklist/store.go`
- Create: `memory/claude_tasklist/store_test.go`

**Interfaces:**

- `NewStore(root, taskListID string) (*Store, error)`
- `(*Store).Create(context.Context, TaskCreateRequest) (*Task, error)`
- `(*Store).Get(context.Context, string) (*Task, error)`
- `(*Store).List(context.Context) ([]Task, error)`
- `(*Store).ListSummaries(context.Context) ([]TaskSummary, error)`
- `(*Store).Update(context.Context, TaskUpdateRequest) (*TaskUpdateResult, error)`
- `(*Store).Counts(context.Context) (TaskCounts, error)`
- `(*Store).Dir() string`

- [ ] **Step 1: Write failing persistence and monotonic-ID tests**

```go
func TestStorePersistsTasksAndNeverReusesDeletedIDs(t *testing.T) {
    ctx := context.Background()
    root := t.TempDir()
    store, err := NewStore(root, "interview")
    if err != nil { t.Fatal(err) }

    first, err := store.Create(ctx, TaskCreateRequest{
        Subject: "梳理源码", Description: "核对 Claude Code Task 工具链", ActiveForm: "正在梳理源码",
    })
    if err != nil { t.Fatal(err) }
    if first.ID != "1" || first.Status != TaskStatusPending { t.Fatalf("first=%+v", first) }

    reopened, _ := NewStore(root, "interview")
    loaded, err := reopened.Get(ctx, "1")
    if err != nil || loaded.Subject != "梳理源码" { t.Fatalf("loaded=%+v err=%v", loaded, err) }

    deleted := TaskUpdateStatusDeleted
    _, _ = reopened.Update(ctx, TaskUpdateRequest{TaskID: "1", Status: &deleted})
    second, _ := reopened.Create(ctx, TaskCreateRequest{Subject: "实现工具", Description: "实现四工具"})
    if second.ID != "2" { t.Fatalf("second id=%q", second.ID) }
}
```

Also test empty/unsafe IDs (`../escape`, `a/b`) and a symlinked task directory.

- [ ] **Step 2: Run RED**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./memory/claude_tasklist -run 'TestStorePersists|TestNewStoreRejects' -count=1
```

Expected: compilation fails because Store and task types do not exist.

- [ ] **Step 3: Implement stable types and basic storage**

```go
type TaskStatus string
const (
    TaskStatusPending TaskStatus = "pending"
    TaskStatusInProgress TaskStatus = "in_progress"
    TaskStatusCompleted TaskStatus = "completed"
)
type TaskUpdateStatus string
const (
    TaskUpdateStatusPending TaskUpdateStatus = "pending"
    TaskUpdateStatusInProgress TaskUpdateStatus = "in_progress"
    TaskUpdateStatusCompleted TaskUpdateStatus = "completed"
    TaskUpdateStatusDeleted TaskUpdateStatus = "deleted"
)
type Task struct {
    ID string `json:"id"`
    Subject string `json:"subject"`
    Description string `json:"description"`
    ActiveForm string `json:"activeForm,omitempty"`
    Owner string `json:"owner,omitempty"`
    Status TaskStatus `json:"status"`
    Blocks []string `json:"blocks"`
    BlockedBy []string `json:"blockedBy"`
    Metadata map[string]any `json:"metadata,omitempty"`
}
```

Define summary/count/request/result types now. Use pointers for optional Status/Owner. Store fields are root/listID/dir plus `sync.Mutex`. `NewStore` validates ID, creates `<root>/tasks/<id>`, rejects symlinks, and initializes `.highwatermark`. Atomic helpers write mode `0600` temp files, Sync, Close, then Rename.

- [ ] **Step 4: Run persistence GREEN**

Run Step 2; expect PASS.

- [ ] **Step 5: Write failing dependency tests**

```go
func TestStoreMaintainsDependenciesAndFiltersCompletedBlockers(t *testing.T) {
    ctx := context.Background()
    store, _ := NewStore(t.TempDir(), "dependencies")
    first, _ := store.Create(ctx, TaskCreateRequest{Subject: "设计", Description: "确定边界"})
    second, _ := store.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "编写代码"})
    _, err := store.Update(ctx, TaskUpdateRequest{TaskID: second.ID, AddBlockedBy: []string{first.ID}})
    if err != nil { t.Fatal(err) }
    a, _ := store.Get(ctx, first.ID)
    b, _ := store.Get(ctx, second.ID)
    if !slices.Equal(a.Blocks, []string{second.ID}) || !slices.Equal(b.BlockedBy, []string{first.ID}) {
        t.Fatalf("a=%+v b=%+v", a, b)
    }
    completed := TaskUpdateStatusCompleted
    _, _ = store.Update(ctx, TaskUpdateRequest{TaskID: first.ID, Status: &completed})
    summaries, _ := store.ListSummaries(ctx)
    if len(summaries[1].BlockedBy) != 0 { t.Fatalf("summaries=%+v", summaries) }
}
```

Also test self/missing dependency atomic failure, delete cleanup, metadata null deletion, and numeric ID sorting.

- [ ] **Step 6: Run RED, implement validated batch updates, then GREEN**

Under the Store mutex: load all tasks, validate the complete request before mutation, clone maps/slices, update reciprocal `blocks/blockedBy`, and persist changed tasks. Delete cleans every reciprocal reference. List filters completed blockers and sorts numeric IDs.

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./memory/claude_tasklist -count=1
```

Expected first failure, then PASS.

- [ ] **Step 7: Format and commit Store**

```bash
gofmt -w memory/claude_tasklist
git diff --check -- memory/claude_tasklist
git add memory/claude_tasklist/types.go memory/claude_tasklist/store.go memory/claude_tasklist/store_test.go
git commit -m "feat: add Claude task list store"
```

---

### Task 2: Four Eino Tools and JSON Schema

**Files:**

- Create: `memory/claude_tasklist/tools.go`
- Create: `memory/claude_tasklist/tools_test.go`
- Create: `memory/claude_tasklist/tool_schema_test.go`
- Modify: `memory/claude_tasklist/types.go`

**Interfaces:** exact tool names and `BuildTools(*Store) ([]tool.BaseTool, error)`.

- [ ] **Step 1: Write failing Toolset CRUD test**

```go
func TestToolsetCreatesReadsUpdatesAndListsTasks(t *testing.T) {
    ctx := context.Background()
    store, _ := NewStore(t.TempDir(), "tools")
    ts := Toolset{Store: store}
    created, err := ts.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "实现工具"})
    if err != nil || created.Task.Status != TaskStatusPending { t.Fatalf("created=%+v err=%v", created, err) }
    status := TaskUpdateStatusInProgress
    updated, _ := ts.Update(ctx, TaskUpdateRequest{TaskID: created.Task.ID, Status: &status})
    got, _ := ts.Get(ctx, TaskGetRequest{TaskID: created.Task.ID})
    listed, _ := ts.List(ctx, TaskListRequest{})
    if updated.Task.Status != TaskStatusInProgress || got.Task.Subject != "实现" || len(listed.Tasks) != 1 {
        t.Fatalf("updated=%+v got=%+v listed=%+v", updated, got, listed)
    }
}
```

Add deleted and empty-update cases.

- [ ] **Step 2: Run RED**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./memory/claude_tasklist -run TestToolset -count=1
```

Expected: compile failure because Toolset is missing.

- [ ] **Step 3: Implement Toolset and exact InferTool constructors**

```go
const (
    TaskCreateToolName = "TaskCreate"
    TaskGetToolName = "TaskGet"
    TaskUpdateToolName = "TaskUpdate"
    TaskListToolName = "TaskList"
)
```

Build all four using `toolutils.InferTool`. List returns summaries, Get full details, Create pending, Update mutations.

- [ ] **Step 4: Run Toolset GREEN**

Run Step 2; expect PASS.

- [ ] **Step 5: Write failing Schema regression**

Assert Create required fields/descriptions, Get/Update require taskId, Update exposes enum `pending,in_progress,completed,deleted`, and List has non-nil empty-object schema.

```go
TaskID string `json:"taskId" jsonschema:"required" jsonschema_description:"要读取或更新的任务 ID"`
Status *TaskUpdateStatus `json:"status,omitempty" jsonschema:"enum=pending,enum=in_progress,enum=completed,enum=deleted" jsonschema_description:"新状态；deleted 表示删除任务"`
```

- [ ] **Step 6: Run Schema RED, complete tags, run GREEN**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./memory/claude_tasklist -run TestTaskToolsExposeCompleteJSONSchema -count=1
```

- [ ] **Step 7: Format and commit tools**

```bash
gofmt -w memory/claude_tasklist
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./memory/claude_tasklist -count=1
git add memory/claude_tasklist
git commit -m "feat: expose Claude task tools"
```

---

### Task 3: Eino Task Agent ReAct Loop

**Files:**

- Create: `memory/claude_tasklist/prompts.go`
- Create: `memory/claude_tasklist/agent.go`
- Create: `memory/claude_tasklist/agent_test.go`

**Interfaces:** `AgentConfig`, `AgentResult`, `NewAgent`, `Agent.Run`, and `DefaultInstruction()` containing `[CLAUDE_TASK_AGENT]`.

- [ ] **Step 1: Write failing ReAct test**

Fake sequence: TaskList, TaskCreate, TaskUpdate in_progress, final. Assert all four schemas are bound, ToolCallCount is 3, and Store contains one in-progress task.

- [ ] **Step 2: Run RED**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./memory/claude_tasklist -run TestAgentRunsTaskToolLoop -count=1
```

- [ ] **Step 3: Implement Agent**

```go
agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name: "claude_task_agent",
    Description: "使用文件任务列表跟踪复杂工作的 Claude Code 风格主 Agent。",
    Instruction: DefaultInstruction(),
    Model: config.Model,
    MaxIterations: maxIterations,
    ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools}},
})
```

`Run` rejects empty messages, iterates `runner.Run`, returns event errors, counts assistant ToolCalls, and keeps the last non-empty assistant content. No final text returns `agent finished without assistant output`. Prompt contains the approved task rules in Chinese.

- [ ] **Step 4: Run GREEN and add recovery/failure tests**

Prove second Run updates task 1 without recreation, prior successful writes survive later errors, and no-final error is exact.

- [ ] **Step 5: Format, verify, commit**

```bash
gofmt -w memory/claude_tasklist
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./memory/claude_tasklist -count=1
git add memory/claude_tasklist
git commit -m "feat: run Claude task agent loop"
```

---

### Task 4: Carry Tool-call Metadata Through Auto Memory

**Files:** modify `types.go`, `llm.go`, `llm_test.go`, `runner.go`, `runner_test.go` in `memory/claude_auto_memory`.

**Interfaces:** add `ChatResponse{Content, ToolCallCount}` and `TurnResult.ToolCallCount`; keep `TurnResult.Answer` unchanged for existing callers.

- [ ] **Step 1: Write failing Runner metadata test**

Return `ChatResponse{Content:"已推进任务。", ToolCallCount:3}` and assert both `runner.Transcript()[1].ToolCallCount == 3` and `result.ToolCallCount == 3`.

- [ ] **Step 2: Run RED**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./memory/claude_auto_memory -run 'TestRunnerPersistsMainAgentToolCallCount|TestLLMChatAgentInjectsMemory' -count=1
```

- [ ] **Step 3: Implement structured response**

```go
type ChatResponse struct {
    Content string
    ToolCallCount int
}
type ChatAgent interface {
    Generate(context.Context, []ConversationMessage, string) (ChatResponse, error)
}
```

LLM adapter returns zero count. Runner validates content/count, copies count to assistant Transcript and `TurnResult.ToolCallCount`, and preserves `TurnResult.Answer`. Update package fakes.

- [ ] **Step 4: Run GREEN and commit**

```bash
gofmt -w memory/claude_auto_memory
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./memory/claude_auto_memory -count=1
git add memory/claude_auto_memory/types.go memory/claude_auto_memory/llm.go memory/claude_auto_memory/llm_test.go memory/claude_auto_memory/runner.go memory/claude_auto_memory/runner_test.go
git commit -m "feat: persist main agent tool counts"
```

---

### Task 5: CLI Adapter, Task Lifecycle, and Resume

**Files:**

- Create: `cmd/claude-auto-memory-agent/task_chat.go`, `task_chat_test.go`
- Modify: `cmd/claude-auto-memory-agent/main.go`, `main_test.go`
- Modify: `memory/claude_auto_memory/examples/interview_rounds.txt`

- [ ] **Step 1: Write failing adapter test**

Fake runner captures Eino messages and returns `AgentResult{Content:"回答", ToolCallCount:2}`. Assert memory context and compact summary are system messages, normal history preserves roles, and ChatResponse preserves count.

- [ ] **Step 2: Run RED**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./cmd/claude-auto-memory-agent -run TestTaskChatAgent -count=1
```

- [ ] **Step 3: Implement adapter**

```go
type taskRunner interface {
    Run(context.Context, []*schema.Message) (claudetasklist.AgentResult, error)
}
type taskChatAgent struct { runner taskRunner }
```

Generate validates runner/roles, injects optional memory system context, maps compact summary to system context, and returns `claudeautomemory.ChatResponse`.

- [ ] **Step 4: Run adapter GREEN**

- [ ] **Step 5: Write failing CLI integration tests**

Preserve existing one-click test. Assert Prepare-only creates `.highwatermark`; scripted model recognizes `[CLAUDE_TASK_AGENT]` and calls Task tools; runOutput contains state/tool counts; a second `-resume` reads existing IDs without recreation.

- [ ] **Step 6: Run CLI RED**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./cmd/claude-auto-memory-agent -run 'TestRunAgentPrepareOnly|TestRunAgentUsesOneModel|TestRunAgentResumesTaskList' -count=1
```

- [ ] **Step 7: Integrate CLI**

Initialize Task Store before Prepare-only, print `tasks_dir`, construct Task Agent with shared model/max iterations 12, wrap it as main ChatAgent, pass Store to runRounds, print counts, and populate runOutput fields. Preserve `defaultOneClickSessionConfig` exactly. Rewrite the three-round example to create a plan, advance it, then list tasks plus recalled convention.

- [ ] **Step 8: Run targeted GREEN**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./memory/claude_tasklist ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1
```

- [ ] **Step 9: Format and preserve overlapping user hunks**

```bash
gofmt -w cmd/claude-auto-memory-agent/*.go
git diff --check -- cmd/claude-auto-memory-agent memory/claude_auto_memory/examples/interview_rounds.txt
git diff -- cmd/claude-auto-memory-agent/main.go cmd/claude-auto-memory-agent/main_test.go
```

Do not commit overlapping CLI files; leave them for final handoff.

---

### Task 6: README, Full Verification, and Review

- [ ] **Step 1: Run failing docs contract**

```bash
for term in 'TaskCreate' 'TaskGet' 'TaskUpdate' 'TaskList' '机械进度真相' 'tasks/<task-list-id>'; do
  rg -q "$term" memory/claude_auto_memory/README.md || { echo "missing:$term"; exit 1; }
done
```

- [ ] **Step 2: Update README**

Document the three independent state layers, Task path/tree, four tools, traces, Resume, and single-process/no-team boundary. Explicitly say `summary.md` is not Task truth. Preserve existing Auto/Session Memory sections.

- [ ] **Step 3: Run docs and targeted GREEN**

```bash
gofmt -w memory/claude_tasklist/*.go memory/claude_auto_memory/*.go cmd/claude-auto-memory-agent/*.go
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache \
  go test ./memory/claude_tasklist ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1
git diff --check
```

- [ ] **Step 4: Run full repository tests**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-tasklist-gocache go test ./... -count=1
```

If an unrelated environment package fails, record exact evidence, rerun targeted packages, and do not modify unrelated code.

- [ ] **Step 5: Inspect Prepare-only persistence**

```bash
tmp_root="$(mktemp -d /private/tmp/claude-tasklist-verify.XXXXXX)"
go run ./cmd/claude-auto-memory-agent -prepare-only -memory-dir "$tmp_root" -session-id verify -env-file /private/tmp/missing.env
find "$tmp_root" -maxdepth 4 -type f | sort
```

Expected: indices, Session Summary/State, and `tasks/verify/.highwatermark`.

- [ ] **Step 6: Request and receive code review**

Use `superpowers:requesting-code-review`; for accepted findings use `superpowers:receiving-code-review` and add a regression test first.

- [ ] **Step 7: Verify before completion**

Use `superpowers:verification-before-completion`. Freshly rerun targeted tests and `git diff --check`, inspect status, and report files, integration, tests, preserved user modifications, and uncommitted overlaps.
