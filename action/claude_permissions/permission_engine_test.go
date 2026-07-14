package claudepermissions

import (
	"context"
	"testing"
	"time"
)

// TestPermissionEngineMatchesClaudeCodeModes 验证非 coding 场景仍保留 Claude Code 的权限模式语义。
func TestPermissionEngineMatchesClaudeCodeModes(t *testing.T) {
	engine := NewPermissionEngine([]ToolPolicy{
		{Name: "inspect_tenant"},
		{Name: "apply_feature_flag"},
		{Name: "send_change_notice"},
		{Name: "delete_tenant"},
	})

	tests := []struct {
		name  string
		mode  PermissionMode
		tool  string
		check ToolPermissionCheckResult
		want  PermissionBehavior
	}{
		{name: "default accepts tool allow", mode: PermissionModeDefault, tool: "inspect_tenant", check: ToolPermissionCheckResult{Behavior: PermissionAllow}, want: PermissionAllow},
		{name: "default keeps tool ask", mode: PermissionModeDefault, tool: "apply_feature_flag", check: ToolPermissionCheckResult{Behavior: PermissionAsk}, want: PermissionAsk},
		{name: "plan respects tool deny", mode: PermissionModePlan, tool: "apply_feature_flag", check: ToolPermissionCheckResult{Behavior: PermissionDeny}, want: PermissionDeny},
		{name: "acceptEdits accepts tool allow", mode: PermissionModeAcceptEdits, tool: "apply_feature_flag", check: ToolPermissionCheckResult{Behavior: PermissionAllow}, want: PermissionAllow},
		{name: "acceptEdits keeps external safety ask", mode: PermissionModeAcceptEdits, tool: "send_change_notice", check: ToolPermissionCheckResult{Behavior: PermissionAsk, BypassImmune: true}, want: PermissionAsk},
		{name: "dontAsk turns tool ask into deny", mode: PermissionModeDontAsk, tool: "apply_feature_flag", check: ToolPermissionCheckResult{Behavior: PermissionAsk}, want: PermissionDeny},
		{name: "bypass allows ordinary tool ask", mode: PermissionModeBypassPermissions, tool: "apply_feature_flag", check: ToolPermissionCheckResult{Behavior: PermissionAsk}, want: PermissionAllow},
		{name: "tool deny survives bypass", mode: PermissionModeBypassPermissions, tool: "delete_tenant", check: ToolPermissionCheckResult{Behavior: PermissionDeny}, want: PermissionDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := engine.Evaluate(EvaluationInput{Mode: tt.mode, ToolName: tt.tool, ToolCheck: tt.check})
			if decision.Behavior != tt.want {
				t.Fatalf("behavior = %q, want %q; reason=%q", decision.Behavior, tt.want, decision.Reason)
			}
		})
	}
}

// TestExplicitRulesOverrideHookAllow 验证 PreToolUse Hook 的放行不能绕过显式 deny/ask 规则。
func TestExplicitRulesOverrideHookAllow(t *testing.T) {
	engine := NewPermissionEngine([]ToolPolicy{{Name: "apply_feature_flag"}})

	deny := engine.Evaluate(EvaluationInput{
		Mode:          PermissionModeDefault,
		ToolName:      "apply_feature_flag",
		ArgumentsJSON: `{"tenant_id":"TENANT-LOCKED"}`,
		HookDecision:  PermissionAllow,
		Rules: []PermissionRule{{
			Behavior:         PermissionDeny,
			ToolName:         "apply_feature_flag",
			ArgumentContains: "TENANT-LOCKED",
			Reason:           "受管租户禁止自动变更",
		}},
	})
	if deny.Behavior != PermissionDeny {
		t.Fatalf("deny behavior = %q, want %q", deny.Behavior, PermissionDeny)
	}

	ask := engine.Evaluate(EvaluationInput{
		Mode:          PermissionModeBypassPermissions,
		ToolName:      "apply_feature_flag",
		ArgumentsJSON: `{"tenant_id":"TENANT-PROD"}`,
		HookDecision:  PermissionAllow,
		Rules: []PermissionRule{{
			Behavior:         PermissionAsk,
			ToolName:         "apply_feature_flag",
			ArgumentContains: "TENANT-PROD",
			Reason:           "生产租户必须人工确认",
		}},
	})
	if ask.Behavior != PermissionAsk {
		t.Fatalf("ask behavior = %q, want %q", ask.Behavior, PermissionAsk)
	}
}

// TestPlanModeCannotBeBypassedByAllowRule 验证 plan 模式仍是只读边界，普通 allow 规则不能开启写操作。
func TestPlanModeCannotBeBypassedByAllowRule(t *testing.T) {
	engine := NewPermissionEngine([]ToolPolicy{{Name: "apply_feature_flag"}})
	decision := engine.Evaluate(EvaluationInput{
		Mode:     PermissionModePlan,
		ToolName: "apply_feature_flag",
		ToolCheck: ToolPermissionCheckResult{
			Behavior: PermissionDeny,
			Reason:   "规划模式下不执行功能开关变更",
		},
		Rules: []PermissionRule{{
			Behavior: PermissionAllow,
			ToolName: "apply_feature_flag",
		}},
	})
	if decision.Behavior != PermissionDeny {
		t.Fatalf("behavior = %q, want %q", decision.Behavior, PermissionDeny)
	}
}

// TestPermissionBrokerPausesAndResumesToolCall 验证请求发出后调用暂停，外层决策后从同一请求继续。
func TestPermissionBrokerPausesAndResumesToolCall(t *testing.T) {
	broker := NewPermissionBroker(1)
	result := make(chan PermissionResponse, 1)
	errCh := make(chan error, 1)

	go func() {
		response, err := broker.Request(context.Background(), PermissionRequest{
			ToolName:      "apply_feature_flag",
			ArgumentsJSON: `{"tenant_id":"TENANT-42","flag":"invoice_v2","enabled":true}`,
			Reason:        "可逆生产变更需要确认",
		})
		if err != nil {
			errCh <- err
			return
		}
		result <- response
	}()

	var request PermissionRequest
	select {
	case request = <-broker.Requests():
	case <-time.After(time.Second):
		t.Fatal("permission request was not published")
	}
	if request.ID == "" {
		t.Fatal("permission request id is empty")
	}

	accepted := broker.Resolve(PermissionResponse{
		RequestID:            request.ID,
		Behavior:             PermissionAllow,
		UpdatedArgumentsJSON: `{"tenant_id":"TENANT-42","flag":"invoice_v2","enabled":false}`,
		Reason:               "值班人员改为关闭开关",
	})
	if !accepted {
		t.Fatal("first permission response was not accepted")
	}
	if broker.Resolve(PermissionResponse{RequestID: request.ID, Behavior: PermissionDeny}) {
		t.Fatal("second permission response unexpectedly won")
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	case response := <-result:
		if response.Behavior != PermissionAllow {
			t.Fatalf("response behavior = %q", response.Behavior)
		}
		if response.UpdatedArgumentsJSON == "" {
			t.Fatal("updated arguments were not returned")
		}
	case <-time.After(time.Second):
		t.Fatal("permission request did not resume")
	}
}
