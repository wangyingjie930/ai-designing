package tooldispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestRenewalRiskAgentLoadsDeferredToolsBeforeUse 验证 Agent 先用 tool_search 加载工具，再调用续费处理工具。
func TestRenewalRiskAgentLoadsDeferredToolsBeforeUse(t *testing.T) {
	fake := &renewalRiskFakeModel{}
	agent, err := NewRenewalRiskAgent(context.Background(), RenewalRiskAgentConfig{Model: fake})
	if err != nil {
		t.Fatal(err)
	}

	response, err := agent.Query(context.Background(), RenewalRiskRequest{Message: "客户 ACME-42 本月要续费，最近用量下降并多次反馈发票问题，请给客户成功经理一份处理建议。"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Message, "续费风险") || !strings.Contains(response.Message, "客户成功经理") {
		t.Fatalf("response = %q", response.Message)
	}
	if fake.calls != 5 {
		t.Fatalf("Generate calls = %d, want 5", fake.calls)
	}
	if names := toolNames(fake.toolsByCall[0]); !sameStringSet(names, []string{"tool_search"}) {
		t.Fatalf("first model tools = %v, want only tool_search", names)
	}
	if names := toolNames(fake.toolsByCall[1]); !containsAllStrings(names, []string{
		LoadAccountSnapshotToolName,
		CheckRenewalContractToolName,
		DraftRetentionPlaybookToolName,
	}) {
		t.Fatalf("second model tools = %v, want selected renewal tools", names)
	}
	joined := strings.Join(fake.toolTexts, "\n")
	for _, want := range []string{"ACME-42", "enterprise", "90 天续费挽留方案"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("tool messages missing %q:\n%s", want, joined)
		}
	}
}

// TestNewRenewalRiskToolsIncludesDistractors 验证工具库包含无关工具，用来体现动态检索减少工具暴露面。
func TestNewRenewalRiskToolsIncludesDistractors(t *testing.T) {
	tools, err := NewRenewalRiskTools()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, info.Name)
	}
	if len(names) < 5 {
		t.Fatalf("tool names = %v, want renewal tools plus distractors", names)
	}
	if !containsAllStrings(names, []string{
		LoadAccountSnapshotToolName,
		CheckRenewalContractToolName,
		DraftRetentionPlaybookToolName,
		EstimateExpansionPotentialToolName,
		BookOnsiteWorkshopToolName,
	}) {
		t.Fatalf("tool names = %v", names)
	}
}

// renewalRiskFakeModel 模拟模型按 Eino dynamic toolsearch 流程搜索工具、调用工具并最终总结。
type renewalRiskFakeModel struct {
	calls       int
	toolsByCall [][]*schema.ToolInfo
	toolTexts   []string
}

// Generate 用调用次数驱动固定工具链，避免单测访问真实模型。
func (m *renewalRiskFakeModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	options := model.GetCommonOptions(nil, opts...)
	m.toolsByCall = append(m.toolsByCall, options.Tools)
	m.captureToolMessages(input)

	switch m.calls {
	case 1:
		if !containsString(toolNames(options.Tools), "tool_search") {
			return nil, errors.New("tool_search is not visible before dynamic selection")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_tool_search",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "tool_search",
				Arguments: `{"query":"select:load_account_snapshot,check_renewal_contract,draft_retention_playbook"}`,
			},
		}}), nil
	case 2:
		if !m.sawToolText("draft_retention_playbook") {
			return nil, errors.New("missing tool_search result")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_account",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      LoadAccountSnapshotToolName,
				Arguments: `{"account_id":"ACME-42"}`,
			},
		}}), nil
	case 3:
		if !m.sawToolText("用量下降") {
			return nil, errors.New("missing account snapshot")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_contract",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      CheckRenewalContractToolName,
				Arguments: `{"account_id":"ACME-42"}`,
			},
		}}), nil
	case 4:
		if !m.sawToolText("enterprise") {
			return nil, errors.New("missing renewal contract")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_playbook",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      DraftRetentionPlaybookToolName,
				Arguments: `{"account_id":"ACME-42","risk_signal":"用量下降且发票问题未闭环","days_to_renewal":27}`,
			},
		}}), nil
	case 5:
		if !m.sawToolText("90 天续费挽留方案") {
			return nil, errors.New("missing retention playbook")
		}
		return schema.AssistantMessage("续费风险较高：建议客户成功经理先修复发票阻塞，再用 90 天续费挽留方案推动高层复盘。", nil), nil
	default:
		return nil, errors.New("unexpected extra Generate call")
	}
}

// Stream 当前测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *renewalRiskFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// captureToolMessages 收集模型下一轮能看到的工具消息。
func (m *renewalRiskFakeModel) captureToolMessages(messages []*schema.Message) {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.Content != "" && !containsString(m.toolTexts, message.Content) {
			m.toolTexts = append(m.toolTexts, message.Content)
		}
	}
}

// sawToolText 判断历史工具消息中是否出现指定片段。
func (m *renewalRiskFakeModel) sawToolText(part string) bool {
	for _, text := range m.toolTexts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

// toolNames 提取模型本轮可见的工具名。
func toolNames(infos []*schema.ToolInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info != nil {
			names = append(names, info.Name)
		}
	}
	return names
}

// containsAllStrings 判断 values 是否包含全部 expected。
func containsAllStrings(values []string, expected []string) bool {
	for _, want := range expected {
		if !containsString(values, want) {
			return false
		}
	}
	return true
}

// containsString 判断切片中是否已经存在目标字符串。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// sameStringSet 判断两个字符串集合是否一致。
func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	return containsAllStrings(left, right) && containsAllStrings(right, left)
}
