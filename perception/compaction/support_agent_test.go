package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// TestSupportAgentRunsADKWithCompactedPrompt 验证 Agent 通过 ADK Runner 使用压缩历史。
func TestSupportAgentRunsADKWithCompactedPrompt(t *testing.T) {
	ctx := context.Background()
	fake := &fakeChatModel{responses: []string{"我已看到支付网关 502 线索，会继续检查网关实例并准备交接。"}}
	cfg := testConfig(220)
	store := NewInMemorySessionStore(cfg)
	agent, err := NewSupportAgent(ctx, SupportAgentConfig{
		Model:         fake,
		Store:         store,
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("new support agent failed: %v", err)
	}
	agent.AddTurn(supportContext(), NewTurn(RoleUser, TurnKindMessage, "客户反馈支付一直 502，订单 ORD-1 无法完成", WithTokens(80)))
	agent.AddTurn(supportContext(), NewTurn(RoleAssistant, TurnKindAction, "查询订单 ORD-1 和支付网关状态", WithTokens(80)))
	agent.AddTurn(supportContext(), NewTurn(RoleToolResult, TurnKindObservation, strings.Repeat("gateway log ok ", 120), WithTokens(700), WithHandle("log://pay/ord-1")))
	agent.AddTurn(supportContext(), NewTurn(RoleToolResult, TurnKindObservation, "Traceback: timeout in /pay/gateway.go:88 status=502", WithTokens(80), WithHandle("log://pay/error")))
	agent.AddTurn(supportContext(), NewTurn(RoleAssistant, TurnKindDecision, "判断不是用户本地网络问题", WithTokens(20)))
	agent.AddTurn(supportContext(), NewTurn(RoleUser, TurnKindMessage, "客户还在线，要求尽快恢复", WithTokens(20)))

	resp, err := agent.Query(ctx, SupportRequest{
		Context: supportContext(),
		Message: "现在进展如何？",
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if resp.CompactionEvent == nil || resp.CompactionEvent.Level != 2 {
		t.Fatalf("expected level 2 compaction, got %#v", resp.CompactionEvent)
	}
	if !resp.PromptView.TokenPressure.ShouldCompact {
		t.Fatalf("expected token pressure to trigger compaction: %#v", resp.PromptView.TokenPressure)
	}
	if !strings.Contains(resp.Message, "支付网关 502") {
		t.Fatalf("unexpected response: %s", resp.Message)
	}

	inputs := fake.Inputs()
	if len(inputs) != 1 {
		t.Fatalf("expected one ADK model call, got %d", len(inputs))
	}
	prompt := joinMessages(inputs[0])
	for _, want := range []string{
		"ticket_id: T-100",
		"EXCLUDED (do not retry): 不要只让用户重试",
		"Traceback: timeout",
		"现在进展如何？",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	turns := agent.SessionTurns(supportContext())
	if got := turns[len(turns)-1].Content; got != resp.Message {
		t.Fatalf("expected final assistant turn saved, got %q", got)
	}
}

// TestSupportAgentSkipsCompactionBelowTokenLimit 验证 token 未超阈值时 middleware 不触发压缩。
func TestSupportAgentSkipsCompactionBelowTokenLimit(t *testing.T) {
	ctx := context.Background()
	fake := &fakeChatModel{responses: []string{"我会继续跟进支付状态。"}}
	cfg := testConfig(900)
	var anchorUpdates int
	cfg.AnchorUpdater = AnchorUpdaterFunc(func(_ context.Context, current Anchor, _ []Turn, _ SupportContext) (Anchor, error) {
		anchorUpdates++
		return current, nil
	})
	store := NewInMemorySessionStore(cfg)
	agent, err := NewSupportAgent(ctx, SupportAgentConfig{
		Model:         fake,
		Store:         store,
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("new support agent failed: %v", err)
	}
	agent.AddTurn(supportContext(), NewTurn(RoleUser, TurnKindMessage, "客户反馈支付偶发失败，等待继续排查", WithTokens(80)))

	resp, err := agent.Query(ctx, SupportRequest{
		Context: supportContext(),
		Message: "现在需要做什么？",
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if resp.CompactionEvent != nil {
		t.Fatalf("expected no compaction event, got %#v", resp.CompactionEvent)
	}
	if anchorUpdates != 0 {
		t.Fatalf("expected anchor updater not called, got %d", anchorUpdates)
	}
	if resp.PromptView.TokenPressure.ShouldCompact || resp.PromptView.TokenPressure.ExceedsTrigger {
		t.Fatalf("expected token pressure below threshold: %#v", resp.PromptView.TokenPressure)
	}

	inputs := fake.Inputs()
	if len(inputs) != 1 {
		t.Fatalf("expected one ADK model call, got %d", len(inputs))
	}
	if prompt := joinMessages(inputs[0]); !strings.Contains(prompt, "客户反馈支付偶发失败") {
		t.Fatalf("expected raw history kept in prompt:\n%s", prompt)
	}
}

// TestSupportAgentEmitsCallbacksForQueryAndMiddleware 验证 CozeLoop 依赖的 Eino callback 能覆盖自定义中间件。
func TestSupportAgentEmitsCallbacksForQueryAndMiddleware(t *testing.T) {
	ctx := context.Background()
	var starts []string
	var ends []string
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			if info != nil {
				starts = append(starts, info.Name)
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
			if info != nil {
				ends = append(ends, info.Name)
			}
			return ctx
		}).
		Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Name: "test_root", Type: "test", Component: "test"}, handler)

	fake := &fakeChatModel{responses: []string{"我会继续跟进支付状态。"}}
	agent, err := NewSupportAgent(ctx, SupportAgentConfig{
		Model:           fake,
		CompactorConfig: testConfig(900),
		MaxIterations:   2,
	})
	if err != nil {
		t.Fatalf("new support agent failed: %v", err)
	}
	agent.AddTurn(supportContext(), NewTurn(RoleUser, TurnKindMessage, "客户反馈支付偶发失败，等待继续排查", WithTokens(80)))

	_, err = agent.Query(ctx, SupportRequest{
		Context: supportContext(),
		Message: "现在需要做什么？",
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	for _, want := range []string{"support_agent_query", promptMiddlewareName} {
		if !stringSliceContains(starts, want) {
			t.Fatalf("callback starts missing %q: %#v", want, starts)
		}
		if !stringSliceContains(ends, want) {
			t.Fatalf("callback ends missing %q: %#v", want, ends)
		}
	}
}

// joinMessages 拼接模型输入消息，方便断言 prompt 内容。
func joinMessages(messages []*schema.Message) string {
	var parts []string
	for _, msg := range messages {
		parts = append(parts, msg.Content)
	}
	return strings.Join(parts, "\n")
}

// stringSliceContains 判断字符串切片是否包含目标值。
func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
