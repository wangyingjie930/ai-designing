package critique

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestRefineRegeneratesWithCritiqueAndToolFeedback 验证主流程会把 critic 和工具反馈带入下一轮生成。
func TestRefineRegeneratesWithCritiqueAndToolFeedback(t *testing.T) {
	fake := &loopFakeModel{}
	loop, err := NewGeneratorCriticLoop(context.Background(), Config{
		GeneratorModel:   fake,
		CriticModel:      fake,
		MaxIterations:    3,
		QualityThreshold: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := loop.Refine(context.Background(), Request{
		Task: "请为 AI 公开课设计一份活动方案。",
		ToolFn: func(output string) string {
			if strings.Contains(output, "第一版") {
				return "工具检查：缺少 CTA；缺少风险兜底。"
			}
			return "工具检查：预算、渠道、CTA、风险兜底均已覆盖。"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Converged || response.Iterations != 2 || response.FinalScore != 0.93 {
		t.Fatalf("response = %+v", response)
	}
	if !strings.Contains(response.Output, "第二版") {
		t.Fatalf("output should be regenerated, got:\n%s", response.Output)
	}
	if len(response.History) != 2 || response.History[0].Approved || !response.History[1].Approved {
		t.Fatalf("history = %+v", response.History)
	}
	if !fake.SawRegenerationFeedback() {
		t.Fatalf("generator did not receive critique/tool feedback in the second round")
	}
}

// TestRefineReturnsUnconvergedAfterMaxIterations 验证多轮都不通过时返回最后一版和未收敛状态。
func TestRefineReturnsUnconvergedAfterMaxIterations(t *testing.T) {
	fake := &loopFakeModel{alwaysReject: true}
	loop, err := NewGeneratorCriticLoop(context.Background(), Config{
		GeneratorModel:   fake,
		MaxIterations:    2,
		QualityThreshold: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := loop.Refine(context.Background(), Request{Task: "请生成一份活动文案。"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Converged || response.Iterations != 2 || response.FinalScore != 0.62 {
		t.Fatalf("response = %+v", response)
	}
}

// TestADKRunnerReturnsCustomizedResponse 验证 Eino ADK Runner 能拿到核心 loop 的结构化结果。
func TestADKRunnerReturnsCustomizedResponse(t *testing.T) {
	runner, _, err := NewRunner(context.Background(), Config{
		GeneratorModel:   &loopFakeModel{},
		MaxIterations:    3,
		QualityThreshold: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	iter := runner.Query(context.Background(), "请为 AI 公开课设计一份活动方案。")
	var got *Response
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output == nil {
			continue
		}
		if response, ok := event.Output.CustomizedOutput.(*Response); ok {
			got = response
		}
	}
	if got == nil || !got.Converged || got.Iterations != 2 {
		t.Fatalf("customized response = %+v", got)
	}
}

var _ adk.Agent = (*ADKAgent)(nil)

// loopFakeModel 用固定文本模拟 generator 和 critic，避免单元测试依赖真实模型。
type loopFakeModel struct {
	mu                  sync.Mutex
	inputs              [][]*schema.Message
	generateCalls       int
	criticCalls         int
	alwaysReject        bool
	sawFeedbackInSecond bool
}

// Generate 按 prompt 类型返回活动方案草稿或结构化 critic JSON。
func (m *loopFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	user := ""
	system := ""
	if len(input) > 0 {
		system = input[0].Content
		user = input[len(input)-1].Content
	}
	switch {
	case strings.Contains(system, "活动方案生成器"):
		m.generateCalls++
		if m.generateCalls > 1 && strings.Contains(user, "缺少 CTA") && strings.Contains(user, "工具检查") {
			m.sawFeedbackInSecond = true
			return schema.AssistantMessage("第二版活动方案：明确目标人群、预算、渠道、CTA 和风险兜底。", nil), nil
		}
		return schema.AssistantMessage("第一版活动方案：介绍 AI 公开课亮点。", nil), nil
	case strings.Contains(system, "活动方案批评器"):
		m.criticCalls++
		if m.alwaysReject || m.criticCalls == 1 {
			return schema.AssistantMessage(`{"approved":false,"issues":["缺少 CTA","风险兜底不够"],"suggestions":["补充报名入口","加入雨天备用方案"],"score":0.62}`, nil), nil
		}
		return schema.AssistantMessage(`{"approved":true,"issues":[],"suggestions":[],"score":0.93}`, nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前测试不使用流式输出，只满足 Eino BaseChatModel 接口。
func (m *loopFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// SawRegenerationFeedback 返回第二轮生成是否真的收到 critic 和工具反馈。
func (m *loopFakeModel) SawRegenerationFeedback() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sawFeedbackInSecond
}
