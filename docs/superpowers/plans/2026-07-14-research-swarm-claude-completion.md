# Research Swarm Claude Code Completion Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. This plan is executed inline because subagent dispatch is not authorized for this task.

**Goal:** Replace automatic `task_completed/artifact_ready` forwarding with explicit teammate result messages plus runtime-owned idle notifications.

**Architecture:** SQLite remains the durable mailbox/task/artifact boundary. Models own task status and explicit result reporting; worker runtime owns running-to-idle lifecycle notifications; leader consumes normal messages and idle notifications as distinct inputs.

**Tech Stack:** Go 1.25, CloudWeGo Eino ADK, SQLite, existing `action/research_swarm` package.

## Global Constraints

- Preserve SQLite and external worker processes.
- Do not add test files or new test cases; only update existing assertions required by the protocol change.
- Keep direct shutdown as the simplified lifecycle boundary.
- Model-visible recipients use teammate names; stable `name@team` IDs remain internal.
- Preserve existing Chinese comments and add concise Chinese purpose comments for new structs/functions.

---

### Task 1: Separate mailbox protocol and name routing

**Files:**
- Modify: `action/research_swarm/types.go`
- Modify: `action/research_swarm/store.go`
- Modify: `action/research_swarm/tools.go`

**Interfaces:**
- Produces: `IdleNotification`, `TeammateMessage`, `Store.GetTask`, `newSendMessageTool`.
- Consumes: existing `MailboxMessage`, `Member`, `AgentID`, and SQLite store.

- [ ] **Step 1: Replace automatic completion types with explicit message and idle payloads**

Define compact model-facing/runtime payloads:

```go
type TeammateMessage struct {
    From    string `json:"from"`
    Summary string `json:"summary,omitempty"`
    Message string `json:"message"`
}

type IdleNotification struct {
    Type              string `json:"type"`
    AgentName         string `json:"agent_name"`
    IdleReason        string `json:"idle_reason"`
    CompletedTaskID   int64  `json:"completed_task_id,omitempty"`
    CompletedStatus   string `json:"completed_status,omitempty"`
    Summary           string `json:"summary,omitempty"`
}
```

Remove `MessageKindTaskCompleted` and `TaskCompletionEvent`. Update `LeaderDirectorInput` to carry either `Message *TeammateMessage` or `Idle *IdleNotification`.

- [ ] **Step 2: Add exact task lookup for lifecycle decisions**

Add:

```go
func (s *Store) GetTask(ctx context.Context, taskID int64) (ResearchTask, error)
```

Return `sql.ErrNoRows` unchanged so callers can distinguish missing tasks from storage failures.

- [ ] **Step 3: Refactor send_message into a reusable name-based tool**

Change the model schema to:

```go
type sendMessageRequest struct {
    To      string `json:"to" jsonschema:"required"`
    Summary string `json:"summary,omitempty"`
    Message string `json:"message" jsonschema:"required"`
}
```

Implement `newSendMessageTool(config ToolConfig)` and reuse it from both worker common tools and leader tools. Resolve `req.To` with `AgentID(req.To, config.TeamName)`, verify the member exists in the current team, and persist `MessageKindNotification` with a `TeammateMessage` payload.

- [ ] **Step 4: Format and inspect Task 1**

Run:

```bash
gofmt -w action/research_swarm/types.go action/research_swarm/store.go action/research_swarm/tools.go
git diff --check -- action/research_swarm/types.go action/research_swarm/store.go action/research_swarm/tools.go
```

Expected: both commands exit 0.

### Task 2: Make worker results explicit and idle lifecycle automatic

**Files:**
- Modify: `action/research_swarm/agent.go`
- Modify: `action/research_swarm/worker.go`

**Interfaces:**
- Consumes: `Store.GetTask`, `newSendMessageTool`, `IdleNotification` from Task 1.
- Produces: `notifyLeaderIdle`, explicit deterministic role messages, and a worker loop that remains alive after idle.

- [ ] **Step 1: Update role instructions**

Require every role to follow this order: persist artifact, call `update_task`, call `send_message(to="report_director")`, then finish the turn. Remove text claiming a downstream teammate was notified automatically.

- [ ] **Step 2: Make the offline role model call send_message explicitly**

After each role's `update_task` call, add one `send_message` tool call with a short summary and artifact reference. Keep the final assistant message informational only.

- [ ] **Step 3: Remove runtime-owned task completion**

Delete the unconditional `Store.UpdateTask(... completed ...)` in `runWorkerMessage`. After the ADK iteration ends, read the task state, mark the member idle, and emit one `idle_notification`.

Map task status as follows:

```go
func completedStatusForTask(status TaskStatus) string {
    switch status {
    case TaskStatusCompleted:
        return "resolved"
    case TaskStatusFailed:
        return "failed"
    default:
        return ""
    }
}
```

- [ ] **Step 4: Emit failure idle notification before returning an Agent error**

If the ADK iteration fails, update the task to failed, mark the member failed, send an idle notification with `idle_reason="failed"`, then return the original error.

- [ ] **Step 5: Format and inspect Task 2**

Run:

```bash
gofmt -w action/research_swarm/agent.go action/research_swarm/worker.go
git diff --check -- action/research_swarm/agent.go action/research_swarm/worker.go
```

Expected: both commands exit 0.

### Task 3: Feed explicit messages and idle state into leader

**Files:**
- Modify: `action/research_swarm/director.go`
- Modify: `action/research_swarm/leader.go`

**Interfaces:**
- Consumes: `TeammateMessage`, `IdleNotification`, `Store.GetTask`, `newSendMessageTool`.
- Produces: a director that advances only from explicit teammate messages and a completion gate based on report + completed writer task + consumed writer idle.

- [ ] **Step 1: Expose spawn_teammate and send_message to leader**

Return both tools from `NewLeaderTools`. Update the instruction so the director treats explicit messages as business results and idle notifications as availability only.

- [ ] **Step 2: Update deterministic director routing**

Replace `directorInput.Event.AgentName` checks with sender checks on `directorInput.Message.From`. Idle-only inputs return a wait/availability response and do not spawn the next teammate.

- [ ] **Step 3: Parse mailbox payloads by semantic type**

In `leaderInputFromMailbox`, first try `IdleNotification` with `type="idle_notification"`; otherwise decode `TeammateMessage`. Preserve sender identity from `MailboxMessage.FromAgent` when older or malformed content omits it.

- [ ] **Step 4: Replace final artifact event gate**

Track a consumed writer idle notification. Before returning success, require:

```text
final report section exists
writer idle notification has completed_task_id
that task currently has status completed
```

- [ ] **Step 5: Format and inspect Task 3**

Run:

```bash
gofmt -w action/research_swarm/director.go action/research_swarm/leader.go
git diff --check -- action/research_swarm/director.go action/research_swarm/leader.go
```

Expected: both commands exit 0.

### Task 4: Update existing assertions and verify the package

**Files:**
- Modify only if required: `action/research_swarm/leader_worker_test.go`
- Modify only if required: `action/research_swarm/tools_test.go`

**Interfaces:**
- Consumes: completed implementation from Tasks 1-3.
- Produces: existing test suite expectations aligned with the new protocol; no new tests or test files.

- [ ] **Step 1: Update stale assertions only**

Replace references to `task_completed/artifact_ready` and leader-only spawn tools with expectations for explicit notification messages, idle notification, and `spawn_teammate + send_message`. Do not add new test functions.

- [ ] **Step 2: Run the existing offline-safe focused tests**

Run:

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run 'Test(Leader|RunLeader|CreateTeam|SpawnTeammate|Worker|RoleTools|SendMessageToolSchema|Store|FakeSearchClient)' -count=1
```

Expected: `ok ai-designing/action/research_swarm`.

- [ ] **Step 3: Prove old completion protocol is absent**

Run:

```bash
rg -n 'task_completed|artifact_ready|MessageKindTaskCompleted|TaskCompletionEvent' action/research_swarm
```

Expected: no matches.

- [ ] **Step 4: Run final static verification**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; status contains only the intended research swarm changes plus the user's pre-existing unrelated changes.
