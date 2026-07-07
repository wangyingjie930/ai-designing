package progresstracking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// newTestLongHorizonTracker 创建独立 SQLite tracker，保证每个测试互不污染。
func newTestLongHorizonTracker(t *testing.T, taskID string) *LongHorizonTracker {
	t.Helper()
	tracker, err := NewLongHorizonTracker(context.Background(), LongHorizonConfig{
		DBPath: filepath.Join(t.TempDir(), "long-horizon.sqlite"),
		TaskID: taskID,
	})
	if err != nil {
		t.Fatalf("NewLongHorizonTracker() error = %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return tracker
}

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
		Value:        "pg_84721",
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

func TestLongHorizonTrackerResolvesMechanicalStateRefsWithoutLeakingValuesToResumePacket(t *testing.T) {
	ctx := context.Background()
	tracker := newTestLongHorizonTracker(t, "payroll-resolve")
	initializePayrollTask(t, ctx, tracker)

	if err := tracker.WriteMechanicalValue(ctx, MechanicalValue{
		Key:          "payroll_group_id",
		Scope:        "tenant:acme/department:shanghai-sales/month:2026-06",
		ValueRef:     "STATE.payroll_group_id",
		Value:        "pg_84721",
		Provider:     "create_payroll_group",
		RuntimeLayer: "orchestrator.tool.create_payroll_group",
		Trust:        TrustToolOutput,
	}); err != nil {
		t.Fatalf("WriteMechanicalValue() error = %v", err)
	}

	resolved, err := tracker.ResolveMechanicalValue(ctx, "STATE.payroll_group_id")
	if err != nil {
		t.Fatalf("ResolveMechanicalValue() error = %v", err)
	}
	if resolved.Value != "pg_84721" {
		t.Fatalf("resolved value = %q, want pg_84721", resolved.Value)
	}
	if resolved.Provider != "create_payroll_group" || resolved.Trust != TrustToolOutput {
		t.Fatalf("resolved provenance missing: %+v", resolved)
	}

	packet, err := tracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	if strings.Join(packet.MechanicalStateKeys, ",") != "payroll_group_id" {
		t.Fatalf("mechanical keys = %+v", packet.MechanicalStateKeys)
	}
	if strings.Contains(strings.Join(packet.MechanicalStateKeys, ","), "pg_84721") {
		t.Fatalf("resume packet leaked mechanical value: %+v", packet.MechanicalStateKeys)
	}
}

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

func TestLongHorizonTrackerAppendEventUpdatesNextAction(t *testing.T) {
	ctx := context.Background()
	tracker := newTestLongHorizonTracker(t, "payroll-next-action")
	initializePayrollTask(t, ctx, tracker)
	if err := tracker.AppendEvent(ctx, AppendProgressEventRequest{
		Event:      "确认薪资边界",
		Decision:   DecisionApprove,
		NextAction: "创建薪资组",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	packet, err := tracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	if packet.NextAction != "创建薪资组" {
		t.Fatalf("next action = %q, want 创建薪资组", packet.NextAction)
	}
}

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

func initializePayrollTask(t *testing.T, ctx context.Context, tracker *LongHorizonTracker) {
	t.Helper()
	if err := tracker.Initialize(ctx, InitializeLongHorizonRequest{
		Goal: GoalContract{
			GoalID:          "payroll-shanghai-202606",
			UserGoal:        "为上海市场部准备 2026 年 6 月薪资批次，生成快照后进入人审。",
			SuccessCriteria: []string{"员工范围为上海市场部 6 月在职员工", "生成快照并完成异常核验"},
			NonGoals:        []string{"不直接发起银行付款", "不修改员工主数据"},
			Constraints:     []string{"金额和批次 id 只能从机械态读取"},
			Version:         1,
		},
		Milestones: []Milestone{{
			MilestoneID:   "M1",
			Title:         "确认薪资批次边界",
			Acceptance:    []string{"部门、月份、规则来源已确认"},
			Status:        TaskStatusInProgress,
			ActiveSubgoal: "确认部门和月份",
		}},
		CurrentMilestoneID: "M1",
		WorkingCollection:  map[string]any{"focus": "确认 6 月上海市场部员工范围"},
		NextAction:         "创建薪资组前先核对员工范围",
	}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
}
