package guardrail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestHomeServiceAgentUsesGuardrailSandwich 验证非 coding Agent 会查工单、脱敏输出，并拦截未审批外发动作。
func TestHomeServiceAgentUsesGuardrailSandwich(t *testing.T) {
	fakeModel := &homeServiceFakeModel{}
	agent, err := NewHomeServiceAgent(context.Background(), HomeServiceAgentConfig{
		Model: fakeModel,
	})
	if err != nil {
		t.Fatalf("NewHomeServiceAgent() error = %v", err)
	}

	response, err := agent.Query(context.Background(), HomeServiceRequest{
		Message: "客户反馈厨房漏水，希望今天安排师傅并通知客户。",
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if fakeModel.calls != 3 {
		t.Fatalf("model calls = %d, want 3", fakeModel.calls)
	}
	for _, leaked := range []string{"owner@example.com", "token=svc-secret"} {
		if strings.Contains(response.Message, leaked) || fakeModel.sawToolText(leaked) {
			t.Fatalf("sensitive value leaked %q, response=%s, toolTexts=%v", leaked, response.Message, fakeModel.toolTexts)
		}
	}
	if !strings.Contains(strings.Join(fakeModel.toolTexts, "\n"), `"blocked_by":"input_filter"`) {
		t.Fatalf("tool texts = %+v, want guardrail block report", fakeModel.toolTexts)
	}
	if !strings.Contains(response.Message, "已拦截外发通知") {
		t.Fatalf("response = %s", response.Message)
	}
}

// homeServiceFakeModel 模拟模型先查工单，再尝试外发通知，最后基于 guardrail 结果回复。
type homeServiceFakeModel struct {
	calls     int
	toolTexts []string
}

// Generate 依次返回工具调用和最终回复，用来验证 ADK tool wiring 与 guardrail。
func (m *homeServiceFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	m.captureToolMessages(input)
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_lookup_service_case",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      LookupServiceCaseToolName,
				Arguments: `{"case_id":"HS-1001"}`,
			},
		}}), nil
	case 2:
		if !m.sawToolText("HS-1001") {
			return nil, errors.New("missing lookup service case result")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_send_customer_notice",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      SendCustomerNoticeToolName,
				Arguments: `{"case_id":"HS-1001","channel":"sms","message":"师傅今天 18:00 前上门"}`,
			},
		}}), nil
	case 3:
		if !m.sawToolText(`"blocked_by":"input_filter"`) {
			return nil, errors.New("missing guardrail block report")
		}
		return schema.AssistantMessage("已完成排期建议；已拦截外发通知，需客服确认后再发送。", nil), nil
	default:
		return nil, errors.New("unexpected extra Generate call")
	}
}

// Stream 当前测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *homeServiceFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// captureToolMessages 记录模型能看到的工具消息文本，用来检查敏感信息是否已经脱敏。
func (m *homeServiceFakeModel) captureToolMessages(messages []*schema.Message) {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.Content != "" && !containsString(m.toolTexts, message.Content) {
			m.toolTexts = append(m.toolTexts, message.Content)
		}
	}
}

// sawToolText 判断模型历史里是否出现过指定片段。
func (m *homeServiceFakeModel) sawToolText(part string) bool {
	for _, text := range m.toolTexts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

// containsString 判断切片中是否已有相同字符串，避免重复记录同一条工具消息。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
