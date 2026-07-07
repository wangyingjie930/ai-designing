# Progress Tracking V2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a v2 long-horizon progress tracker that models goal contracts, milestones, append-only ledger events, mechanical state provenance, resume packets, recitation prompts, drift signals, verification gates, and compensation metadata without breaking the existing v1 `memory/progress_tracking` demo.

**Architecture:** Keep the existing v1 tracker intact and add focused v2 files in the same package so the current command and ADK tools keep working. V2 is a deterministic core first: schema, SQLite persistence, tracker operations, watchdog/gate helpers, and tests. Agent wiring can later wrap these APIs without making the model responsible for factual state capture.

**Tech Stack:** Go, SQLite via `database/sql` and `github.com/mattn/go-sqlite3`, existing `memory/progress_tracking` package conventions, `go test` with `GOCACHE=/private/tmp/ai-designing-gocache`.

---

### File Structure

- Create: `memory/progress_tracking/v2_types.go`
  - V2 domain schema: `GoalContract`, `Milestone`, `ProgressEvent`, `MechanicalValue`, `DriftSignal`, `ResumePacket`, request/response structs.
- Create: `memory/progress_tracking/v2_store.go`
  - SQLite tables and persistence for the v2 state planes.
- Create: `memory/progress_tracking/v2_tracker.go`
  - Public deterministic API for initializing, loading, mutating, and restoring long-horizon state.
- Create: `memory/progress_tracking/v2_gate.go`
  - Verification gate and drift-watchdog helper functions.
- Create: `memory/progress_tracking/v2_tracker_test.go`
  - TDD coverage for resume packets, append-only ledger, mechanical provenance, gates, and drift.
- Modify: `memory/progress_tracking/README.md`
  - Add a short v2 section explaining how it differs from v1.

### Task 1: V2 Schema and Resume Packet

**Files:**
- Create: `memory/progress_tracking/v2_types.go`
- Test: `memory/progress_tracking/v2_tracker_test.go`

- [ ] **Step 1: Write the failing schema/resume test**

Add this test to `memory/progress_tracking/v2_tracker_test.go`:

```go
func TestLongHorizonTrackerResumePacketIncludesAnchorLedgerAndStateKeys(t *testing.T) {
	ctx := context.Background()
	tracker := newTestLongHorizonTracker(t, "payroll-run")

	goal := GoalContract{
		GoalID:          "payroll-shanghai-202606",
		UserGoal:        "为上海市场部准备 2026 年 6 月薪资批次，生成快照后进入人审。",
		SuccessCriteria: []string{"员工范围为上海市场部 6 月在职员工", "生成快照并完成异常核验"},
		NonGoals:        []string{"不直接发起银行付款", "不修改员工主数据"},
		Constraints:     []string{"金额和批次 id 只能从机械态读取"},
		Version:         1,
	}
	milestones := []Milestone{{
		MilestoneID:   "M1",
		Title:         "确认薪资批次边界",
		Acceptance:    []string{"部门、月份、规则来源已确认"},
		Status:        TaskStatusInProgress,
		ActiveSubgoal: "确认部门和月份",
	}}
	if err := tracker.Initialize(ctx, InitializeLongHorizonRequest{
		Goal:               goal,
		Milestones:         milestones,
		CurrentMilestoneID: "M1",
		WorkingCollection:  map[string]any{"focus": "确认 6 月上海市场部员工范围"},
		NextAction:         "创建薪资组前先核对员工范围",
	}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if err := tracker.AppendEvent(ctx, AppendProgressEventRequest{
		Event:        "冻结目标契约",
		Decision:     DecisionDefer,
		Reason:       "先确认边界，避免直接进入付款或报税",
		EvidenceRefs: []string{"user:initial-request"},
		StateDelta:   StateDelta{Write: []string{"GOAL.payroll-shanghai-202606"}},
		NextAction:   "确认薪资批次边界",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := tracker.WriteMechanicalValue(ctx, MechanicalValue{
		Key:          "payroll_group_id",
		Scope:        "company:acme/org:shanghai-sales/month:2026-06",
		ValueRef:     "STATE.payroll_group_id",
		Provider:     "create_payroll_group",
		RuntimeLayer: "plan_exec.M1.step1",
		Trust:        TrustToolOutput,
	}); err != nil {
		t.Fatalf("WriteMechanicalValue() error = %v", err)
	}

	packet, err := tracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	if packet.Goal.GoalID != goal.GoalID {
		t.Fatalf("goal id = %q, want %q", packet.Goal.GoalID, goal.GoalID)
	}
	if packet.CurrentMilestone.MilestoneID != "M1" {
		t.Fatalf("current milestone = %+v", packet.CurrentMilestone)
	}
	if len(packet.RecentLedger) != 1 || packet.RecentLedger[0].Event != "冻结目标契约" {
		t.Fatalf("recent ledger = %+v", packet.RecentLedger)
	}
	if strings.Join(packet.MechanicalStateKeys, ",") != "payroll_group_id" {
		t.Fatalf("mechanical keys = %+v", packet.MechanicalStateKeys)
	}
	if !strings.Contains(tracker.RecitationPrompt(ctx), "不直接发起银行付款") {
		t.Fatalf("recitation prompt missing non-goal:\n%s", tracker.RecitationPrompt(ctx))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./memory/progress_tracking -run TestLongHorizonTrackerResumePacketIncludesAnchorLedgerAndStateKeys -count=1
```

Expected: FAIL because `GoalContract`, `LongHorizonTracker`, and related v2 symbols do not exist.

- [ ] **Step 3: Implement minimal schema and in-memory-facing API backed by SQLite store stubs**

Create `v2_types.go` with exported domain types and `v2_tracker.go` with method signatures. The minimal implementation may return data from SQLite-backed storage once Task 2 creates tables, but the API names must match the test exactly.

- [ ] **Step 4: Run test to verify it passes**

Run the same `go test` command. Expected: PASS.

### Task 2: SQLite Persistence and Append-Only Ledger

**Files:**
- Modify: `memory/progress_tracking/v2_store.go`
- Modify: `memory/progress_tracking/v2_tracker.go`
- Test: `memory/progress_tracking/v2_tracker_test.go`

- [ ] **Step 1: Write the failing persistence test**

Add:

```go
func TestLongHorizonTrackerReloadsAppendOnlyLedger(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "long-horizon.sqlite")
	tracker, err := NewLongHorizonTracker(ctx, LongHorizonConfig{DBPath: dbPath, TaskID: "payroll-run"})
	if err != nil {
		t.Fatalf("NewLongHorizonTracker() error = %v", err)
	}
	initializePayrollTask(t, ctx, tracker)
	for _, event := range []string{"创建薪资组", "生成薪资批次"} {
		if err := tracker.AppendEvent(ctx, AppendProgressEventRequest{
			Event:        event,
			Decision:     DecisionApprove,
			EvidenceRefs: []string{"tool:" + event},
			StateDelta:   StateDelta{Write: []string{"STATE." + event}},
			NextAction:   "继续下一步",
		}); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", event, err)
		}
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reloaded, err := NewLongHorizonTracker(ctx, LongHorizonConfig{DBPath: dbPath, TaskID: "payroll-run"})
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	defer reloaded.Close()
	packet, err := reloaded.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	if got := len(packet.RecentLedger); got != 2 {
		t.Fatalf("recent ledger len = %d, want 2", got)
	}
	if packet.RecentLedger[0].Sequence >= packet.RecentLedger[1].Sequence {
		t.Fatalf("ledger is not append ordered: %+v", packet.RecentLedger)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./memory/progress_tracking -run TestLongHorizonTrackerReloadsAppendOnlyLedger -count=1
```

Expected: FAIL until SQLite tables and load paths persist v2 state.

- [ ] **Step 3: Implement v2 SQLite tables and load/save helpers**

Add tables for `long_horizon_tasks`, `long_horizon_milestones`, `long_horizon_events`, `long_horizon_mechanical_values`, `long_horizon_drift_signals`, and `long_horizon_gate_results`. Store variable fields as JSON strings and use transactions for initialization and state writes.

- [ ] **Step 4: Run test to verify it passes**

Run the same test. Expected: PASS.

### Task 3: Mechanical State Provenance, Gates, and Drift Watchdog

**Files:**
- Create: `memory/progress_tracking/v2_gate.go`
- Modify: `memory/progress_tracking/v2_tracker.go`
- Test: `memory/progress_tracking/v2_tracker_test.go`

- [ ] **Step 1: Write failing gate and drift tests**

Add:

```go
func TestVerificationGateRequiresEvidenceAndMechanicalKeys(t *testing.T) {
	packet := ResumePacket{
		Goal: GoalContract{GoalID: "payroll"},
		CurrentMilestone: Milestone{
			MilestoneID: "M2",
			Title:       "生成薪资快照",
			Acceptance:  []string{"STATE.payroll_batch_id", "evidence:tool:create_snapshot"},
		},
		RecentLedger: []ProgressEvent{{
			Event:        "创建批次",
			EvidenceRefs: []string{"tool:create_batch"},
			StateDelta:   StateDelta{Write: []string{"STATE.payroll_batch_id"}},
		}},
		MechanicalStateKeys: []string{"payroll_batch_id"},
	}
	result := EvaluateVerificationGate(packet)
	if result.Passed {
		t.Fatalf("gate unexpectedly passed: %+v", result)
	}
	if !strings.Contains(strings.Join(result.Missing, ","), "evidence:tool:create_snapshot") {
		t.Fatalf("missing evidence not reported: %+v", result)
	}

	packet.RecentLedger = append(packet.RecentLedger, ProgressEvent{Event: "生成快照", EvidenceRefs: []string{"tool:create_snapshot"}})
	result = EvaluateVerificationGate(packet)
	if !result.Passed {
		t.Fatalf("gate should pass after evidence exists: %+v", result)
	}
}

func TestDriftWatchdogRecentersOnNonGoalOrStalledMilestone(t *testing.T) {
	packet := ResumePacket{
		Goal: GoalContract{
			GoalID:   "payroll",
			NonGoals: []string{"不直接发起银行付款"},
		},
		CurrentMilestone: Milestone{MilestoneID: "M2", Title: "生成快照"},
		RecentLedger: []ProgressEvent{
			{Event: "整理薪资项名称", EvidenceRefs: nil},
			{Event: "尝试直接发起银行付款", EvidenceRefs: nil},
		},
	}
	signal := EvaluateDrift(packet)
	if signal.Level != DriftLevelPause {
		t.Fatalf("drift level = %s, want %s; signal=%+v", signal.Level, DriftLevelPause, signal)
	}
	if signal.GoalRelevance >= 1 {
		t.Fatalf("goal relevance should drop: %+v", signal)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./memory/progress_tracking -run 'TestVerificationGateRequiresEvidenceAndMechanicalKeys|TestDriftWatchdogRecentersOnNonGoalOrStalledMilestone' -count=1
```

Expected: FAIL because `EvaluateVerificationGate` and `EvaluateDrift` are missing.

- [ ] **Step 3: Implement deterministic gate and drift helpers**

Implement rule-based checks only. Gate acceptance items beginning with `STATE.` require a matching mechanical key or state delta write. Acceptance items beginning with `evidence:` require a matching ledger evidence ref. Drift pauses when a ledger event contains a normalized non-goal phrase and recent evidence is empty.

- [ ] **Step 4: Run tests to verify they pass**

Run the same test command. Expected: PASS.

### Task 4: Compensation Metadata and State Transitions

**Files:**
- Modify: `memory/progress_tracking/v2_types.go`
- Modify: `memory/progress_tracking/v2_tracker.go`
- Test: `memory/progress_tracking/v2_tracker_test.go`

- [ ] **Step 1: Write failing compensation test**

Add:

```go
func TestProgressEventStoresCompensationMetadata(t *testing.T) {
	ctx := context.Background()
	tracker := newTestLongHorizonTracker(t, "payroll-compensation")
	initializePayrollTask(t, ctx, tracker)
	if err := tracker.AppendEvent(ctx, AppendProgressEventRequest{
		Event:          "创建外部审批单",
		Decision:       DecisionApprove,
		EvidenceRefs:   []string{"tool:create_approval#1"},
		StateDelta:     StateDelta{Write: []string{"STATE.approval_id"}},
		CompensateOp:   "cancel_approval",
		IdempotencyKey: "payroll-compensation:M3:create_approval",
		NextAction:     "等待人审",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	packet, err := tracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	event := packet.RecentLedger[len(packet.RecentLedger)-1]
	if event.CompensateOp != "cancel_approval" || event.IdempotencyKey == "" {
		t.Fatalf("compensation metadata missing: %+v", event)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./memory/progress_tracking -run TestProgressEventStoresCompensationMetadata -count=1
```

Expected: FAIL until metadata is modeled and persisted.

- [ ] **Step 3: Implement compensation fields**

Add `CompensateOp` and `IdempotencyKey` to `ProgressEvent` and `AppendProgressEventRequest`, persist them in `long_horizon_events`, and load them in ledger order.

- [ ] **Step 4: Run test to verify it passes**

Run the same test. Expected: PASS.

### Task 5: README and Package Verification

**Files:**
- Modify: `memory/progress_tracking/README.md`

- [ ] **Step 1: Update README**

Add a `## V2 Long-Horizon State` section explaining:

```markdown
## V2 Long-Horizon State

V1 remains a flat SQLite checkpoint tracker for task lists. V2 adds a deterministic long-horizon state model for workflows where an Agent may drift across many steps.

V2 separates:

- `GoalContract` for the frozen anchor: success criteria, non-goals, and constraints.
- `Milestone` for the scheduling plane: current phase and acceptance conditions.
- `ProgressEvent` for the append-only ledger: event, decision, evidence, state delta, compensation metadata, and next action.
- `MechanicalValue` for the mechanical state plane: reusable business facts with provider, runtime layer, trust, and scope.
- `ResumePacket` and `RecitationPrompt` for recovery and recentering without reading full chat history.

The first v2 implementation is deterministic and test-first. It does not ask the LLM to describe factual state changes; wrappers should capture tool outputs, state diffs, and provenance, then append ledger events.
```

- [ ] **Step 2: Run package tests**

Run:

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./memory/progress_tracking ./cmd/progress-tracking-agent -count=1
```

Expected: PASS.

- [ ] **Step 3: Check unrelated user changes remain untouched**

Run:

```bash
git status --short
```

Expected: v2 files and README changed; pre-existing `memory/hierarchical_v1/examples/meal_rounds.txt` may still appear but must not be edited by this task.

### Self-Review

- Spec coverage: The plan covers the v2 schema, three-plane state split, append-only ledger, resume packet, recitation prompt, mechanical provenance, deterministic verification gate, drift signal, and compensation metadata.
- Placeholder scan: No task contains TBD/TODO/fill-later wording; test names, commands, and expected failures are explicit.
- Type consistency: Tests and implementation will use the same exported names: `LongHorizonTracker`, `GoalContract`, `Milestone`, `ProgressEvent`, `MechanicalValue`, `ResumePacket`, `EvaluateVerificationGate`, and `EvaluateDrift`.
