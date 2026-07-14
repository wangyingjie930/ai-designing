package claudepermissions

import (
	"context"
	"testing"
	"time"
)

// TestPermissionEngineMatchesClaudeCodeModes 验证非 coding 场景仍保留 Claude Code 的权限模式语义。
func TestPermissionEngineMatchesClaudeCodeModes(t *testing.T) {
	engine := NewPermissionEngine([]ToolPolicy{
		{Name: "inspect_tenant", Kind: ToolKindRead},
		{Name: "apply_feature_flag", Kind: ToolKindEdit},
		{Name: "send_change_notice", Kind: ToolKindExternal},
		{Name: "delete_tenant", Kind: ToolKindDestructive},
	})

	tests := []struct {
		name string
		mode PermissionMode
		tool string
		want PermissionBehavior
	}{
		{name: "default allows read", mode: PermissionModeDefault, tool: "inspect_tenant", want: PermissionAllow},
		{name: "default asks before edit", mode: PermissionModeDefault, tool: "apply_feature_flag", want: PermissionAsk},
		{name: "plan denies edit", mode: PermissionModePlan, tool: "apply_feature_flag", want: PermissionDeny},
		{name: "acceptEdits allows reversible edit", mode: PermissionModeAcceptEdits, tool: "apply_feature_flag", want: PermissionAllow},
		{name: "acceptEdits still asks before external effect", mode: PermissionModeAcceptEdits, tool: "send_change_notice", want: PermissionAsk},
		{name: "dontAsk turns ask into deny", mode: PermissionModeDontAsk, tool: "apply_feature_flag", want: PermissionDeny},
		{name: "bypass allows ordinary edit", mode: PermissionModeBypassPermissions, tool: "apply_feature_flag", want: PermissionAllow},
		{name: "destructive safety denial survives bypass", mode: PermissionModeBypassPermissions, tool: "delete_tenant", want: PermissionDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := engine.Evaluate(EvaluationInput{Mode: tt.mode, ToolName: tt.tool})
			if decision.Behavior != tt.want {
				t.Fatalf("behavior = %q, want %q; reason=%q", decision.Behavior, tt.want, decision.Reason)
			}
		})
	}
}

// TestExplicitRulesOverrideHookAllow 验证 PreToolUse Hook 的放行不能绕过显式 deny/ask 规则。
func TestExplicitRulesOverrideHookAllow(t *testing.T) {
	engine := NewPermissionEngine([]ToolPolicy{{Name: "apply_feature_flag", Kind: ToolKindEdit}})

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
	engine := NewPermissionEngine([]ToolPolicy{{Name: "apply_feature_flag", Kind: ToolKindEdit}})
	decision := engine.Evaluate(EvaluationInput{
		Mode:     PermissionModePlan,
		ToolName: "apply_feature_flag",
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
