package claudepermissions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestSaaSAgentResumesApprovedToolCall 验证真实 ADK Agent 会暂停写工具、接收更新参数并恢复原调用。
func TestSaaSAgentResumesApprovedToolCall(t *testing.T) {
	executor := NewInMemorySaaSExecutor()
	fakeModel := &saasFakeModel{}
	agent, err := NewSaaSAgent(context.Background(), SaaSAgentConfig{
		Model:    fakeModel,
		Mode:     PermissionModeDefault,
		Executor: executor,
		PermissionRequestHooks: []PermissionRequestHook{PermissionRequestHookFunc(func(ctx context.Context, request PermissionRequest) (*PermissionResponse, error) {
			return &PermissionResponse{
				Behavior:             PermissionAllow,
				UpdatedArgumentsJSON: strings.ReplaceAll(request.ArgumentsJSON, `"enabled":true`, `"enabled":false`),
				Reason:               "值班人员批准但改为关闭",
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := agent.Query(context.Background(), SaaSRequest{Message: "把 TENANT-42 的 invoice_v2 开关打开"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message != "变更已按审批后的参数执行。" {
		t.Fatalf("response = %q", response.Message)
	}
	snapshot := executor.Snapshot()
	if snapshot.FeatureFlags["TENANT-42"]["invoice_v2"] {
		t.Fatalf("feature flag = true, want approval-updated false; snapshot=%+v", snapshot)
	}
	if !fakeModel.sawToolOutput("changed") {
		t.Fatalf("model did not receive tool output: %+v", fakeModel.toolOutputs)
	}
}

// saasFakeModel 模拟模型发起一次生产开关变更，再根据工具结果给出最终回复。
type saasFakeModel struct {
	calls       int
	toolOutputs []string
}

// Generate 返回确定性的工具调用和最终回复，避免测试访问真实模型。
func (m *saasFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	m.captureToolOutputs(input)
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-apply-flag",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      ApplyFeatureFlagToolName,
				Arguments: `{"tenant_id":"TENANT-42","flag":"invoice_v2","enabled":true}`,
			},
		}}), nil
	case 2:
		if !m.sawToolOutput("changed") {
			return nil, errors.New("missing apply_feature_flag output")
		}
		return schema.AssistantMessage("变更已按审批后的参数执行。", nil), nil
	default:
		return nil, errors.New("unexpected extra model call")
	}
}

// Stream 当前测试不需要流式输出，只满足 Eino 模型接口。
func (m *saasFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// captureToolOutputs 收集模型收到的工具消息。
func (m *saasFakeModel) captureToolOutputs(messages []*schema.Message) {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.Content != "" && !containsText(m.toolOutputs, message.Content) {
			m.toolOutputs = append(m.toolOutputs, message.Content)
		}
	}
}

// sawToolOutput 判断任一工具消息是否包含目标片段。
func (m *saasFakeModel) sawToolOutput(fragment string) bool {
	for _, output := range m.toolOutputs {
		if strings.Contains(output, fragment) {
			return true
		}
	}
	return false
}

// containsText 判断字符串切片是否已经包含目标值。
func containsText(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
