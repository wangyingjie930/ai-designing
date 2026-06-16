package compaction

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSupportCompactorLevel2KeepsErrorsAndAnchor 验证 L2 压缩保留错误并更新 Anchor。
func TestSupportCompactorLevel2KeepsErrorsAndAnchor(t *testing.T) {
	cfg := testConfig(220)
	compactor := NewSupportCompactor(cfg)
	turns := []Turn{
		NewTurn(RoleUser, TurnKindMessage, "客户反馈支付一直 502，订单 ORD-1 无法完成", WithTokens(80)),
		NewTurn(RoleAssistant, TurnKindAction, "查询订单 ORD-1 和支付网关状态", WithTokens(80)),
		NewTurn(RoleToolResult, TurnKindObservation, strings.Repeat("gateway log ok ", 120), WithTokens(700), WithHandle("log://pay/ord-1")),
		NewTurn(RoleToolResult, TurnKindObservation, "Traceback: timeout in /pay/gateway.go:88 status=502", WithTokens(80), WithHandle("log://pay/error")),
		NewTurn(RoleAssistant, TurnKindDecision, "判断不是用户本地网络问题", WithTokens(20)),
		NewTurn(RoleUser, TurnKindMessage, "客户还在线，要求尽快恢复", WithTokens(20)),
	}

	result, event, err := compactor.Compact(context.Background(), supportContext(), turns)
	if err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	if event == nil || event.Level != 2 {
		t.Fatalf("expected level 2 event, got %#v", event)
	}
	if !event.AllErrorsRepresented() {
		t.Fatalf("expected errors represented: %#v", event)
	}
	if !strings.Contains(joinTurnContent(result), "Traceback: timeout") {
		t.Fatalf("protected error was not kept: %#v", result)
	}
	if got := compactor.Anchor().ExcludedApproaches; len(got) == 0 || got[0] != "不要只让用户重试" {
		t.Fatalf("expected excluded approach in anchor, got %#v", got)
	}
}

// TestSupportCompactorLevel3RecommendsHandoff 验证极限压缩会触发交接信号。
func TestSupportCompactorLevel3RecommendsHandoff(t *testing.T) {
	cfg := testConfig(80)
	compactor := NewSupportCompactor(cfg)
	turns := []Turn{
		NewTurn(RoleUser, TurnKindMessage, "P1 客户无法支付", WithTokens(160)),
		NewTurn(RoleToolResult, TurnKindError, "FAILED test payment retry path A", WithTokens(70), WithHandle("err://a")),
		NewTurn(RoleToolResult, TurnKindError, "Traceback timeout path B", WithTokens(70), WithHandle("err://b")),
		NewTurn(RoleToolResult, TurnKindError, "HTTP 503 path C", WithTokens(70), WithHandle("err://c")),
		NewTurn(RoleToolResult, TurnKindError, "panic path D", WithTokens(70), WithHandle("err://d")),
		NewTurn(RoleAssistant, TurnKindDecision, "准备升级二线", WithTokens(30)),
		NewTurn(RoleUser, TurnKindMessage, "SLA 只剩二十分钟", WithTokens(30)),
	}

	result, event, err := compactor.Compact(context.Background(), supportContext(), turns)
	if err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	if event == nil || event.Level != 3 {
		t.Fatalf("expected level 3 event, got %#v", event)
	}
	if !event.HandoffRecommended {
		t.Fatalf("expected handoff recommendation")
	}
	if !event.AllErrorsRepresented() {
		t.Fatalf("expected all errors represented: %#v", event)
	}
	if !strings.Contains(joinTurnContent(result), "Old errors represented") {
		t.Fatalf("expected old error summary, got %#v", result)
	}
}

// testConfig 返回可预测的测试压缩配置。
func testConfig(target int) Config {
	return Config{
		ContextBudget:                 1000,
		TargetTokens:                  target,
		PreserveRecent:                2,
		LongObservationTokenThreshold: 20,
		MaxRecentErrors:               2,
		BaseTriggerRatio:              0.50,
		MediumRiskTriggerRatio:        0.50,
		HighRiskTriggerRatio:          0.50,
		Now:                           func() time.Time { return time.Unix(0, 0).UTC() },
		AnchorUpdater: AnchorUpdaterFunc(func(_ context.Context, current Anchor, _ []Turn, _ SupportContext) (Anchor, error) {
			return current.Merge(Anchor{
				Intent:             "恢复客户支付工单",
				ChangesMade:        []string{"查过订单和支付网关"},
				DecisionsTaken:     []string{"502 来自支付网关"},
				ExcludedApproaches: []string{"不要只让用户重试"},
				NextSteps:          []string{"检查支付网关实例并准备交接"},
			}), nil
		}),
	}
}

// supportContext 返回测试用 P1 工单上下文。
func supportContext() SupportContext {
	return SupportContext{
		TicketID:       "T-100",
		CustomerID:     "C-9",
		ProductLine:    "pay",
		Severity:       "P1",
		SLADeadlineISO: "2026-06-12T22:00:00Z",
	}
}

// joinTurnContent 拼接压缩结果，方便断言关键证据是否存在。
func joinTurnContent(turns []Turn) string {
	var parts []string
	for _, turn := range turns {
		parts = append(parts, turn.Content)
	}
	return strings.Join(parts, "\n")
}
