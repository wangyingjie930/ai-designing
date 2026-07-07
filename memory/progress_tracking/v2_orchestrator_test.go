package progresstracking

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStatefulToolOrchestratorResolvesStateRefsWritesMechanicalValuesAndLedger(t *testing.T) {
	ctx := context.Background()
	tracker := newTestLongHorizonTracker(t, "payroll-orchestrator")
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

	var executorArgs map[string]any
	orchestrator := StatefulToolOrchestrator{
		Tracker: tracker,
		Executor: StatefulToolFunc(func(ctx context.Context, toolName string, args map[string]any) (map[string]any, error) {
			if toolName != "create_payroll_batch" {
				t.Fatalf("tool name = %q", toolName)
			}
			executorArgs = args
			return map[string]any{"batch_id": "pb_202606_001"}, nil
		}),
		RuntimeLayer: "orchestrator.payroll",
	}

	result, err := orchestrator.Execute(ctx, StatefulToolCall{
		ToolName: "create_payroll_batch",
		Args: map[string]any{
			"group_id": "STATE.payroll_group_id",
			"month":    "2026-06",
		},
		Outputs: []MechanicalOutputBinding{{
			Key:        "payroll_batch_id",
			FromResult: "batch_id",
			Scope:      "tenant:acme/department:shanghai-sales/month:2026-06",
		}},
		Event:        "创建薪资批次",
		Reason:       "薪资组已确认，可以创建 6 月批次",
		EvidenceRefs: []string{"tool:create_payroll_batch"},
		NextAction:   "生成薪资快照",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if executorArgs["group_id"] != "pg_84721" {
		t.Fatalf("resolved args = %+v", executorArgs)
	}
	if strings.Join(result.StateDelta.Read, ",") != "STATE.payroll_group_id" {
		t.Fatalf("state reads = %+v", result.StateDelta.Read)
	}
	if strings.Join(result.StateDelta.Write, ",") != "STATE.payroll_batch_id" {
		t.Fatalf("state writes = %+v", result.StateDelta.Write)
	}

	batch, err := tracker.ResolveMechanicalValue(ctx, "STATE.payroll_batch_id")
	if err != nil {
		t.Fatalf("ResolveMechanicalValue(batch) error = %v", err)
	}
	if batch.Value != "pb_202606_001" || batch.Provider != "create_payroll_batch" {
		t.Fatalf("batch mechanical value = %+v", batch)
	}

	packet, err := tracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	last := packet.RecentLedger[len(packet.RecentLedger)-1]
	if strings.Join(last.StateDelta.Read, ",") != "STATE.payroll_group_id" {
		t.Fatalf("ledger read delta = %+v", last.StateDelta.Read)
	}
	if strings.Join(last.StateDelta.Write, ",") != "STATE.payroll_batch_id" {
		t.Fatalf("ledger write delta = %+v", last.StateDelta.Write)
	}
	if !containsString(packet.MechanicalStateKeys, "payroll_batch_id") {
		t.Fatalf("mechanical keys missing payroll_batch_id: %+v", packet.MechanicalStateKeys)
	}
}

func TestStatefulToolOrchestratorDoesNotWriteMechanicalStateWhenToolFails(t *testing.T) {
	ctx := context.Background()
	tracker := newTestLongHorizonTracker(t, "payroll-orchestrator-fail")
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
	orchestrator := StatefulToolOrchestrator{
		Tracker: tracker,
		Executor: StatefulToolFunc(func(ctx context.Context, toolName string, args map[string]any) (map[string]any, error) {
			return nil, errors.New("tool timeout")
		}),
	}

	_, err := orchestrator.Execute(ctx, StatefulToolCall{
		ToolName: "create_payroll_batch",
		Args:     map[string]any{"group_id": "STATE.payroll_group_id"},
		Outputs:  []MechanicalOutputBinding{{Key: "payroll_batch_id", FromResult: "batch_id"}},
		Event:    "创建薪资批次",
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want tool timeout")
	}
	if _, resolveErr := tracker.ResolveMechanicalValue(ctx, "STATE.payroll_batch_id"); resolveErr == nil {
		t.Fatal("payroll_batch_id should not be written after tool failure")
	}
	packet, err := tracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	if len(packet.RecentLedger) != 0 {
		t.Fatalf("ledger should not append on tool failure: %+v", packet.RecentLedger)
	}
}
