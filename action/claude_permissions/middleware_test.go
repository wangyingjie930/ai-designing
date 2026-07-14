package claudepermissions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TestMiddlewareFiltersBlanketDeniedTools 验证整工具 deny 会在模型调用前移除工具定义。
func TestMiddlewareFiltersBlanketDeniedTools(t *testing.T) {
	middleware, err := NewMiddleware(MiddlewareConfig{
		Tools: []ToolPolicy{
			{Name: "inspect_tenant", Checker: fixedToolChecker(PermissionAllow)},
			{Name: "delete_tenant", Checker: fixedToolChecker(PermissionDeny)},
		},
		Rules: []PermissionRule{{Behavior: PermissionDeny, ToolName: "delete_tenant"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{{Name: "inspect_tenant"}, {Name: "delete_tenant"}}}
	_, rewritten, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rewritten.ToolInfos) != 1 || rewritten.ToolInfos[0].Name != "inspect_tenant" {
		t.Fatalf("visible tools = %+v", rewritten.ToolInfos)
	}
}

// TestMiddlewarePausesUpdatesExecutesAndPostProcesses 验证同一工具调用经审批更新参数后恢复，并进入 PostToolUse。
func TestMiddlewarePausesUpdatesExecutesAndPostProcesses(t *testing.T) {
	broker := NewPermissionBroker(1)
	middleware, err := NewMiddleware(MiddlewareConfig{
		Tools:  []ToolPolicy{{Name: "apply_feature_flag", Checker: fixedToolChecker(PermissionAsk)}},
		Mode:   PermissionModeDefault,
		Broker: broker,
		PreToolUseHooks: []PreToolUseHook{PreToolUseHookFunc(func(ctx context.Context, input PreToolUseInput) (PreToolUseResult, error) {
			return PreToolUseResult{UpdatedArgumentsJSON: strings.ReplaceAll(input.ArgumentsJSON, "TENANT-OLD", "TENANT-42")}, nil
		})},
		PostToolUseHooks: []PostToolUseHook{PostToolUseHookFunc(func(ctx context.Context, input PostToolUseInput) (string, error) {
			return input.Output + "|post-audited", nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		request := <-broker.Requests()
		broker.Resolve(PermissionResponse{
			RequestID:            request.ID,
			Behavior:             PermissionAllow,
			UpdatedArgumentsJSON: strings.ReplaceAll(request.ArgumentsJSON, `"enabled":true`, `"enabled":false`),
			Reason:               "值班人员批准并修正参数",
		})
	}()

	var executedArguments string
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
		executedArguments = arguments
		return "changed", nil
	}, &adk.ToolContext{Name: "apply_feature_flag"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := wrapped(context.Background(), `{"tenant_id":"TENANT-OLD","flag":"invoice_v2","enabled":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(executedArguments, "TENANT-42") || !strings.Contains(executedArguments, `"enabled":false`) {
		t.Fatalf("executed arguments = %s", executedArguments)
	}
	if output != "changed|post-audited" {
		t.Fatalf("output = %q", output)
	}
}

// TestMiddlewareReturnsStructuredDenialWithoutExecuting 验证拒绝作为工具结果返回，让模型有机会调整计划。
func TestMiddlewareReturnsStructuredDenialWithoutExecuting(t *testing.T) {
	middleware, err := NewMiddleware(MiddlewareConfig{
		Tools: []ToolPolicy{{Name: "apply_feature_flag", Checker: fixedToolChecker(PermissionDeny)}},
		Mode:  PermissionModePlan,
	})
	if err != nil {
		t.Fatal(err)
	}
	executed := false
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
		executed = true
		return "changed", nil
	}, &adk.ToolContext{Name: "apply_feature_flag"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := wrapped(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("denied tool was executed")
	}
	var report ToolExecutionReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("output is not a report: %s", output)
	}
	if report.Executed || report.Behavior != PermissionDeny || report.BlockedBy != "permission" {
		t.Fatalf("report = %+v", report)
	}
}

// TestMiddlewareRunsStopHookOnFinalAssistantMessage 验证 Agent 结束前会把最终回复交给 Stop Hook。
func TestMiddlewareRunsStopHookOnFinalAssistantMessage(t *testing.T) {
	seen := make(chan string, 1)
	middleware, err := NewMiddleware(MiddlewareConfig{
		Tools: []ToolPolicy{{Name: "inspect_tenant", Checker: fixedToolChecker(PermissionAllow)}},
		StopHooks: []StopHook{StopHookFunc(func(ctx context.Context, message string) error {
			seen <- message
			if strings.Contains(message, "secret") {
				return errors.New("最终回复包含内部字段")
			}
			return nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.AssistantMessage("tenant secret", nil)}}
	_, err = middleware.AfterAgent(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "最终回复") {
		t.Fatalf("error = %v", err)
	}
	select {
	case message := <-seen:
		if message != "tenant secret" {
			t.Fatalf("message = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("stop hook was not called")
	}
}

// fixedToolChecker 为中间件既有测试提供固定的工具级权限结果。
func fixedToolChecker(behavior PermissionBehavior) ToolPermissionChecker {
	return ToolPermissionCheckerFunc(func(context.Context, ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
		return ToolPermissionCheckResult{Behavior: behavior}, nil
	})
}
