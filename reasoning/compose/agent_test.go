package compose

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"ai-designing/reasoning/tot"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestSolveSimpleRoutesDirect 验证简单问题只走 direct response，不触发重推理路径。
func TestSolveSimpleRoutesDirect(t *testing.T) {
	agent, err := NewAgent(context.Background(), Config{
		Model:    &composeFakeModel{},
		Scenario: "学生学习支持分流",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := agent.Solve(context.Background(), Request{Query: "图书馆几点关门？"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Decision.Complexity != ComplexitySimple || response.FinalAnswer == "" || response.UsedHypothesis {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Path) != 1 || response.Path[0].Kind != PathDirect {
		t.Fatalf("path = %+v", response.Path)
	}
}

// TestSolveModerateUsesCOTWithoutHypothesis 验证中等问题走单路径 CoT，校验通过且置信度足够时直接回答。
func TestSolveModerateUsesCOTWithoutHypothesis(t *testing.T) {
	agent, err := NewAgent(context.Background(), Config{
		Model:    &composeFakeModel{},
		Scenario: "学生学习支持分流",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := agent.Solve(context.Background(), Request{Query: "帮我安排下周三奖学金补交和小组展示顺序。"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Decision.Complexity != ComplexityModerate || response.COT == nil || response.UsedHypothesis {
		t.Fatalf("response = %+v", response)
	}
	if !strings.Contains(response.FinalAnswer, "先补交奖学金材料") {
		t.Fatalf("final answer = %q", response.FinalAnswer)
	}
}

// TestSolveComplexUsesTOTAndHypothesis 验证复杂问题会先走 ToT 多路径探索，再进入 hypothesis 反证循环。
func TestSolveComplexUsesTOTAndHypothesis(t *testing.T) {
	fake := &composeFakeModel{}
	agent, err := NewAgent(context.Background(), Config{
		Model:    fake,
		Scenario: "学生学习支持分流",
		TOTConfig: tot.Config{
			ReasonConfig: tot.ReasonConfig{
				Method:   tot.MethodMCTS,
				MaxDepth: 1,
				NSim:     1,
			},
			Rand: rand.New(rand.NewSource(1)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := agent.Solve(context.Background(), Request{Query: "大一学生连续三周缺席早课，通勤变长、兼职变多、成绩下降，但只说早上起不来，应该先做哪类支持干预？"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Decision.Complexity != ComplexityComplex || response.TOT == nil || response.Hypothesis == nil || !response.UsedHypothesis {
		t.Fatalf("response = %+v", response)
	}
	if response.Escalated || !strings.Contains(response.FinalAnswer, "先做出勤风险访谈") {
		t.Fatalf("final answer = %q escalated=%t", response.FinalAnswer, response.Escalated)
	}
	if fake.Count() < 8 {
		t.Fatalf("fake model calls = %d, want complex multi-stage calls", fake.Count())
	}
}

// TestADKRunnerEmitsCustomizedOutput 验证 compose 可以作为 Eino ADK Agent 被 Runner 调度。
func TestADKRunnerEmitsCustomizedOutput(t *testing.T) {
	runner, _, err := NewRunner(context.Background(), Config{
		Model:    &composeFakeModel{},
		Scenario: "学生学习支持分流",
	})
	if err != nil {
		t.Fatal(err)
	}
	iter := runner.Query(context.Background(), "图书馆几点关门？")
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
	if got == nil || got.Decision.Complexity != ComplexitySimple {
		t.Fatalf("customized response = %+v", got)
	}
}

// composeFakeModel 根据组合层和三个子 Agent 的 prompt 类型返回固定结构化结果。
type composeFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟 router/direct/CoT/ToT/hypothesis 全链路，测试控制流而不是模型能力。
func (m *composeFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	system := ""
	user := ""
	if len(input) > 0 {
		system = input[0].Content
		user = input[len(input)-1].Content
	}
	switch {
	case strings.Contains(system, "复杂度路由器"):
		return m.routeReply(user), nil
	case strings.Contains(system, "直接响应 Agent"):
		return schema.AssistantMessage("图书馆今天 22:00 关门，请以学校当天公告为准。", nil), nil
	case strings.Contains(system, "显性推理链 Agent"):
		return schema.AssistantMessage(`{"steps":[
{"step_number":1,"content":"奖学金材料有截止风险，应先完成补交。","confidence":0.91},
{"step_number":2,"content":"小组展示需要保留彩排时间，适合放在材料提交后。","confidence":0.86}
],"final_answer":"建议上午先补交奖学金材料，下午完成小组展示彩排，晚上整理展示分工。"} `, nil), nil
	case strings.Contains(user, "校验规则"):
		return schema.AssistantMessage("VALID\n步骤与原问题约束一致。", nil), nil
	case strings.Contains(user, "How should the thinking process continue?"):
		return schema.AssistantMessage(strings.Join([]string{
			"REFLECTION:",
			"这个学生支持问题存在多个可能根因，需要比较干预路径。",
			"",
			"**Possible Options:**",
			"Option 1: 从出勤风险和退课预警角度设计访谈",
			"Option 2: 从通勤变化和兼职排班角度设计支持",
			"Option 3: 从成绩下降和学习策略角度设计补救",
			"Option 4: TERMINATE",
		}, "\n"), nil), nil
	case strings.Contains(user, "Final Answer:"):
		return schema.AssistantMessage("先做出勤风险访谈，确认通勤、兼职和睡眠是否形成叠加压力；同时给出一周早课支持计划。", nil), nil
	case strings.Contains(user, "Rate:") || strings.Contains(system, "评价"):
		return schema.AssistantMessage("Rating: 9\n理由：覆盖了主要风险和下一步行动。", nil), nil
	case strings.Contains(system, "最佳路径确认器"):
		return schema.AssistantMessage(`{"confirmed":true,"confidence":0.84,"reason":"候选答案覆盖了出勤风险、通勤兼职和短期支持计划。"}`, nil), nil
	case strings.Contains(system, "假设规划器"):
		return schema.AssistantMessage(`{"hypotheses":[
{"description":"候选答案足以作为第一步，因为它先验证退课风险并覆盖通勤兼职睡眠约束","prior":0.45},
{"description":"应该直接升级人工介入，因为所有关键信息都缺失","prior":0.30}
]}`, nil), nil
	case strings.Contains(system, "证据生成器"):
		return schema.AssistantMessage(`{"evidence":[{"description":"原问题已给出连续三周缺席、通勤变长、兼职变多、成绩下降四个风险信号","source":"case_brief"}]}`, nil), nil
	case strings.Contains(system, "反证评估器"):
		if strings.Contains(user, "候选答案足以作为第一步") {
			return schema.AssistantMessage(`{"effect":"supports","posterior_delta":0.50}`, nil), nil
		}
		return schema.AssistantMessage(`{"effect":"refutes","posterior_delta":-0.35}`, nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前单测不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *composeFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，确认复杂路径确实运行了多个内部阶段。
func (m *composeFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}

// routeReply 按用户问题内容返回不同复杂度，方便覆盖三条路由分支。
func (m *composeFakeModel) routeReply(user string) *schema.Message {
	switch {
	case strings.Contains(user, "图书馆"):
		return schema.AssistantMessage(`{"complexity":"simple","confidence":0.92,"reason":"事实型低风险问题","recommended_path":"direct_response"}`, nil)
	case strings.Contains(user, "奖学金"):
		return schema.AssistantMessage(`{"complexity":"moderate","confidence":0.86,"reason":"需要单路径安排和校验","recommended_path":"chain_of_thought"}`, nil)
	default:
		return schema.AssistantMessage(`{"complexity":"complex","confidence":0.88,"reason":"多根因、多风险、证据不完整","recommended_path":"parallel_exploration"}`, nil)
	}
}

var _ adk.Agent = (*ADKAgent)(nil)
