package progresstracking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type longHorizonFakeBaseAgent struct {
	progress    *ProgressTracker
	seenMessage string
}

func (a *longHorizonFakeBaseAgent) Query(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	a.seenMessage = req.Message
	if len(a.progress.Items()) == 0 {
		if err := a.progress.CreatePlan(ctx, []string{"确认场地合同和容量", "发布报名页"}); err != nil {
			return nil, err
		}
	}
	if err := a.progress.Start(ctx, 0); err != nil {
		return nil, err
	}
	if err := a.progress.Complete(ctx, 0, "场地合同已确认，可容纳 80 人。", []string{"venue-contract.pdf"}); err != nil {
		return nil, err
	}
	return &AgentResponse{Message: "已确认场地合同和容量。"}, nil
}

func TestLongHorizonEventPlanningAgentWrapsBaseAgentAndWritesLedger(t *testing.T) {
	ctx := context.Background()
	progress, err := NewProgressTracker(ctx, Config{
		DBPath: filepath.Join(t.TempDir(), "progress.sqlite"),
		PlanID: "event-book-club",
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	defer progress.Close()
	longTracker := newTestLongHorizonTracker(t, "event-book-club-v2")
	base := &longHorizonFakeBaseAgent{progress: progress}
	agent, err := NewLongHorizonEventPlanningAgent(LongHorizonAgentConfig{
		Base:            base,
		ProgressTracker: progress,
		LongTracker:     longTracker,
	})
	if err != nil {
		t.Fatalf("NewLongHorizonEventPlanningAgent() error = %v", err)
	}

	response, err := agent.Query(ctx, AgentRequest{Message: "我要筹备一场 80 人线下读书会。"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if !strings.Contains(base.seenMessage, "Long-horizon recitation") {
		t.Fatalf("base agent did not receive v2 recitation:\n%s", base.seenMessage)
	}
	if !strings.Contains(response.Message, "V2 checkpoint") {
		t.Fatalf("response missing v2 checkpoint summary:\n%s", response.Message)
	}
	packet, err := longTracker.ResumePacket(ctx)
	if err != nil {
		t.Fatalf("ResumePacket() error = %v", err)
	}
	if packet.Goal.GoalID != "event-book-club-v2" {
		t.Fatalf("goal id = %q", packet.Goal.GoalID)
	}
	if len(packet.RecentLedger) == 0 {
		t.Fatal("recent ledger is empty")
	}
	if !containsString(packet.RecentLedger[0].EvidenceRefs, "progress:plan") {
		t.Fatalf("first ledger event should record plan evidence: %+v", packet.RecentLedger)
	}
	event := packet.RecentLedger[len(packet.RecentLedger)-1]
	if event.Event != "完成计划项: 确认场地合同和容量" {
		t.Fatalf("event = %+v", event)
	}
	if !containsString(event.EvidenceRefs, "progress:item:0") {
		t.Fatalf("event evidence refs = %+v", event.EvidenceRefs)
	}
	if strings.Join(packet.MechanicalStateKeys, ",") != "progress_item_0" {
		t.Fatalf("mechanical keys = %+v", packet.MechanicalStateKeys)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
