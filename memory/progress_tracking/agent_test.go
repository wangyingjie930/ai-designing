package progresstracking

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestToolsetCreatePlanProtectsExistingPlan 验证 ADK create tool 默认不会覆盖已有计划。
func TestToolsetCreatePlanProtectsExistingPlan(t *testing.T) {
	ctx := context.Background()
	tracker, err := NewProgressTracker(ctx, Config{
		DBPath: filepath.Join(t.TempDir(), "progress.sqlite"),
		PlanID: "event-ops",
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	defer tracker.Close()
	toolset := Toolset{Tracker: tracker}
	if _, err := toolset.CreatePlan(ctx, CreatePlanRequest{Items: []string{"确认场地"}}); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if _, err := toolset.CreatePlan(ctx, CreatePlanRequest{Items: []string{"重建计划"}}); err == nil {
		t.Fatal("CreatePlan() expected reset protection error")
	}
	if _, err := toolset.CreatePlan(ctx, CreatePlanRequest{Items: []string{"重建计划"}, Reset: true}); err != nil {
		t.Fatalf("CreatePlan(reset) error = %v", err)
	}
	if got := tracker.Items()[0].Description; got != "重建计划" {
		t.Fatalf("description = %q", got)
	}
}

// TestEventPlanningAgentUsesProgressTools 验证 Eino ADK Agent 会通过工具初始化计划，而不是直接返回硬编码答案。
func TestEventPlanningAgentUsesProgressTools(t *testing.T) {
	ctx := context.Background()
	tracker, err := NewProgressTracker(ctx, Config{
		DBPath: filepath.Join(t.TempDir(), "progress.sqlite"),
		PlanID: "event-ops",
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	defer tracker.Close()

	fake := &eventPlanningToolModel{}
	agent, err := NewEventPlanningAgent(ctx, AgentConfig{
		Model:         fake,
		Tracker:       tracker,
		MaxIterations: 6,
	})
	if err != nil {
		t.Fatalf("NewEventPlanningAgent() error = %v", err)
	}
	response, err := agent.Query(ctx, AgentRequest{Message: "我要筹备一场 80 人线下读书会，请先帮我建立可恢复的筹备计划。"})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if !strings.Contains(response.Message, "已建立活动筹备计划") {
		t.Fatalf("response = %q", response.Message)
	}
	items := tracker.Items()
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2; items=%+v", len(items), items)
	}
	if items[0].Description != "确认活动场地和容纳人数" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if fake.calls != 3 {
		t.Fatalf("model calls = %d, want 3", fake.calls)
	}
}

// eventPlanningToolModel 模拟模型先读取恢复上下文，再动态创建计划，最后基于工具结果回复。
type eventPlanningToolModel struct {
	calls int
}

// Generate 依次返回工具调用和最终回复，用来验证 ADK tool wiring。
func (m *eventPlanningToolModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_progress_resumption_context",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      ResumptionContextToolName,
				Arguments: `{}`,
			},
		}}), nil
	case 2:
		if firstMessageByRole(input, schema.Tool) == nil {
			return nil, errors.New("missing resume tool result")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_progress_create_plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      CreatePlanToolName,
				Arguments: `{"items":["确认活动场地和容纳人数","发布报名页并设置截止时间"]}`,
			},
		}}), nil
	case 3:
		if firstMessageByToolName(input, CreatePlanToolName) == nil {
			return nil, errors.New("missing create plan tool result")
		}
		return schema.AssistantMessage("已建立活动筹备计划，下一步先确认场地容量和合同边界。", nil), nil
	default:
		return nil, errors.New("unexpected extra Generate call")
	}
}

// Stream 当前测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *eventPlanningToolModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// firstMessageByRole 返回指定角色的第一条消息。
func firstMessageByRole(messages []*schema.Message, role schema.RoleType) *schema.Message {
	for _, message := range messages {
		if message.Role == role {
			return message
		}
	}
	return nil
}

// firstMessageByToolName 返回指定工具名对应的工具结果消息。
func firstMessageByToolName(messages []*schema.Message, toolName string) *schema.Message {
	for _, message := range messages {
		if message.Role == schema.Tool && message.ToolName == toolName {
			return message
		}
	}
	return nil
}
