package guardrail

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TestPolicyRequiresHumanForHighRiskTool 验证高风险外部动作必须走人工审批。
func TestPolicyRequiresHumanForHighRiskTool(t *testing.T) {
	ctx := context.Background()
	policy, err := NewPolicy(DefaultSafetyPolicy())
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	var approvalRequest ApprovalRequest
	approver := ApproverFunc(func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
		approvalRequest = req
		return ApprovalDecision{Approved: false, Reason: "客户尚未确认"}, nil
	})

	verdict, err := policy.Evaluate(ctx, "send_customer_notice", `{"message":"明天上门"}`, DefaultRiskClassifier{}, approver)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if verdict.Approved || verdict.RiskLevel != RiskLevelHigh {
		t.Fatalf("verdict = %+v", verdict)
	}
	if approvalRequest.ToolName != "send_customer_notice" || approvalRequest.RiskLevel != RiskLevelHigh {
		t.Fatalf("approval request = %+v", approvalRequest)
	}
}

// TestRedactorMasksSensitiveOutput 验证工具输出回到模型上下文前会脱敏。
func TestRedactorMasksSensitiveOutput(t *testing.T) {
	policy, err := NewPolicy(DefaultSafetyPolicy())
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	output := policy.Redact(`客户邮箱 owner@example.com，token=abc123，请勿外泄。`)

	for _, leaked := range []string{"owner@example.com", "token=abc123"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("output leaked %q: %s", leaked, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("output = %s, want redacted marker", output)
	}
}

// TestMiddlewareFiltersToolInfosBeforeModel 验证模型前只暴露白名单工具。
func TestMiddlewareFiltersToolInfosBeforeModel(t *testing.T) {
	mw, err := NewMiddleware(MiddlewareConfig{
		Policy: SafetyPolicy{AllowedTools: []string{"lookup_service_case"}},
	})
	if err != nil {
		t.Fatalf("NewMiddleware() error = %v", err)
	}
	state := &adk.ChatModelAgentState{
		ToolInfos: []*schema.ToolInfo{
			{Name: "lookup_service_case"},
			{Name: "cancel_service_contract"},
		},
	}

	_, next, err := mw.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState() error = %v", err)
	}
	if len(next.ToolInfos) != 1 || next.ToolInfos[0].Name != "lookup_service_case" {
		t.Fatalf("tool infos = %+v", next.ToolInfos)
	}
}

// TestMiddlewareBlocksToolCallAndRedactsAllowedOutput 验证执行前兜底拦截和执行后脱敏。
func TestMiddlewareBlocksToolCallAndRedactsAllowedOutput(t *testing.T) {
	mw, err := NewMiddleware(MiddlewareConfig{
		Policy: SafetyPolicy{
			AllowedTools: []string{"lookup_service_case"},
			AutoApprove:  []RiskLevel{RiskLevelLow},
			SensitivePatterns: []string{
				`(?i)token\s*[:=]\s*\S+`,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewMiddleware() error = %v", err)
	}

	blocked, err := mw.WrapInvokableToolCall(context.Background(), func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		return "should not run", nil
	}, &adk.ToolContext{Name: "cancel_service_contract", CallID: "blocked-1"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall(blocked) error = %v", err)
	}
	blockedOutput, err := blocked(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("blocked endpoint error = %v", err)
	}
	if !strings.Contains(blockedOutput, `"executed":false`) || !strings.Contains(blockedOutput, `"blocked_by":"input_filter"`) {
		t.Fatalf("blocked output = %s", blockedOutput)
	}

	allowed, err := mw.WrapInvokableToolCall(context.Background(), func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		return `{"note":"token=abc123"}`, nil
	}, &adk.ToolContext{Name: "lookup_service_case", CallID: "ok-1"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall(allowed) error = %v", err)
	}
	output, err := allowed(context.Background(), `{"case_id":"A1001"}`)
	if err != nil {
		t.Fatalf("allowed endpoint error = %v", err)
	}
	if strings.Contains(output, "token=abc123") || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("allowed output = %s", output)
	}
}
