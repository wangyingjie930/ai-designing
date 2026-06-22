package hypothesis

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestAgentUsesModelRoles 验证 Agent 会通过模型完成 planner、generator、evaluator 三段调用。
func TestAgentUsesModelRoles(t *testing.T) {
	fake := &agentFakeModel{}
	startedNames := make([]string, 0)
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info != nil {
				startedNames = append(startedNames, info.Name)
			}
			return ctx
		}).
		Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Name: "test_root", Type: "test", Component: "test"}, handler)
	agent, err := NewAgent(context.Background(), Config{
		Model:         fake,
		Scenario:      "社区活动报名下滑诊断",
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := agent.Diagnose(ctx, Request{Problem: "报名表新增字段后报名下降。"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Outcome.Converged || response.Tree.SurvivorCount != 1 || response.FinalAnswer == "" {
		t.Fatalf("response = %+v", response)
	}
	if fake.Count() != 5 {
		t.Fatalf("model calls = %d, want 5", fake.Count())
	}
	nameCounts := map[string]int{}
	for _, name := range startedNames {
		nameCounts[name]++
	}
	if nameCounts["hypothesis_planner"] != 1 || nameCounts["hypothesis_generator"] != 2 || nameCounts["hypothesis_evaluator"] != 2 {
		t.Fatalf("started names = %#v", startedNames)
	}
}

// agentFakeModel 根据 hypothesis 内部 prompt 类型返回固定 JSON。
type agentFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟三个模型角色，并按 Eino 组件习惯触发 callback 以验证 RunInfo 命名。
func (m *agentFakeModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx = callbacks.EnsureRunInfo(ctx, "AgentFakeModel", components.ComponentOfChatModel)
	ctx = callbacks.OnStart(ctx, "agent_fake_model_generate")
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	system := ""
	user := ""
	if len(input) > 0 {
		system = input[0].Content
		user = input[len(input)-1].Content
	}
	var message *schema.Message
	switch {
	case strings.Contains(system, "假设规划器"):
		message = schema.AssistantMessage(`{"hypotheses":[
{"description":"海报标题降低了亲子体验感","prior":0.35},
{"description":"报名表新增字段造成流程摩擦","prior":0.45},
{"description":"学校运动会撞期分流了家长","prior":0.20}
]}`, nil)
	case strings.Contains(system, "证据生成器"):
		message = schema.AssistantMessage(`{"evidence":[{"description":"报名表新增身份证号和紧急联系人两个必填字段","source":"case_brief"}]}`, nil)
	case strings.Contains(system, "反证评估器"):
		if strings.Contains(user, "报名表新增字段造成流程摩擦") {
			message = schema.AssistantMessage(`{"effect":"supports","posterior_delta":0.55}`, nil)
		} else {
			message = schema.AssistantMessage(`{"effect":"refutes","posterior_delta":-0.35}`, nil)
		}
	default:
		message = schema.AssistantMessage(`{"effect":"neutral","posterior_delta":0}`, nil)
	}
	callbacks.OnEnd(ctx, "agent_fake_model_done")
	return message, nil
}

// Stream 当前测试不使用流式输出，只满足 Eino BaseChatModel 接口。
func (m *agentFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明三个角色都被执行过。
func (m *agentFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
