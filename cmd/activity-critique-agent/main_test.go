package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestDefaultActivityRequestIsDirectBusinessInput 验证默认输入就是活动运营需求，不需要命令行参数拼装。
func TestDefaultActivityRequestIsDirectBusinessInput(t *testing.T) {
	task := defaultActivityRequest()
	for _, want := range []string{"AI 公开课", "报名转化", "目标人群", "预算"} {
		if !strings.Contains(task, want) {
			t.Fatalf("default task missing %q:\n%s", want, task)
		}
	}
}

// TestActivityChecklistToolReportsMissingFields 验证确定性工具能把缺失项反馈给 critic。
func TestActivityChecklistToolReportsMissingFields(t *testing.T) {
	feedback := activityChecklistTool(`{
  "goal": "提升报名转化",
  "audience": "",
  "budget": {"amount": 0, "items": []},
  "channels": [],
  "timeline": [],
  "cta": "提升转化",
  "risk_plan": []
}`)
	for _, want := range []string{"预算缺少具体金额", "渠道缺少具体触点", "CTA 缺少明确动作", "风险兜底缺少备用动作"} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("feedback missing %q:\n%s", want, feedback)
		}
	}

	feedback = activityChecklistTool(`{
  "goal": "提升报名转化",
  "audience": "AI 产品经理、企业培训负责人",
  "budget": {"amount": 5000, "items": ["海报设计", "社群分发"]},
  "channels": ["社群", "公众号"],
  "timeline": ["预热", "直播", "直播后跟进"],
  "cta": "扫码报名",
  "risk_plan": ["候补直播", "讲师备份"]
}`)
	if !strings.Contains(feedback, "均已覆盖") {
		t.Fatalf("feedback = %s", feedback)
	}
}

// TestActivityChecklistToolRejectsVagueLabels 验证工具不会因为出现栏目名就误判为已覆盖。
func TestActivityChecklistToolRejectsVagueLabels(t *testing.T) {
	feedback := activityChecklistTool(`{
  "goal": "提升报名转化",
  "audience": "待定",
  "budget": {"amount": 0, "items": ["待定"]},
  "channels": ["渠道待定"],
  "timeline": ["时间待定"],
  "cta": "提升转化",
  "risk_plan": ["注意风险"]
}`)
	for _, want := range []string{"预算缺少具体金额", "渠道缺少具体触点", "CTA 缺少明确动作", "风险兜底缺少备用动作"} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("feedback missing %q:\n%s", want, feedback)
		}
	}
}

// TestActivityChecklistToolRequiresStructuredPlan 验证工具层依赖结构化方案，不用全文关键词冒充字段校验。
func TestActivityChecklistToolRequiresStructuredPlan(t *testing.T) {
	feedback := activityChecklistTool("目标人群为 AI 产品经理；预算 5000 元；渠道包含社群和公众号；CTA 是扫码报名；风险兜底包含候补直播。")
	if !strings.Contains(feedback, "不是结构化活动方案") {
		t.Fatalf("feedback = %s", feedback)
	}
}

// TestRunAgentDirectInputThroughADKRunner 验证 main_test 可以直接点跑，不传一堆参数。
func TestRunAgentDirectInputThroughADKRunner(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdFakeModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Iterations != 2 || !output.Converged || output.FinalScore != 0.93 || !output.UsedADKCustomizedData {
		t.Fatalf("output = %+v", output)
	}
	if fake.Count() != 4 {
		t.Fatalf("fake model calls = %d, want 4", fake.Count())
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 只记录摘要，不把活动需求或完整方案塞进上报。
func TestRunAgentTraceIsConcise(t *testing.T) {
	ctx := context.Background()
	var startName string
	var endName string
	var startInput callbacks.CallbackInput
	var endOutput callbacks.CallbackOutput
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info != nil {
				startName = info.Name
			}
			startInput = input
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info != nil {
				endName = info.Name
			}
			endOutput = output
			return ctx
		}).
		Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Name: "test_root", Type: "test", Component: "test"}, handler)

	output, err := withRunAgentTrace(ctx, 187, 3, 0.9, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Iterations: 2, FinalScore: 0.93, Converged: true, AnswerChars: 128}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "activity_critique_agent_run" || endName != "activity_critique_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.TaskChars != 187 || input.MaxIterations != 3 || input.QualityThreshold != 0.9 {
		t.Fatalf("trace input = %+v", input)
	}
	if output.TaskChars != 187 || output.Iterations != 2 || !output.Converged {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"AI 公开课", "扫码报名", "risk_plan"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

// cmdFakeModel 根据 critique 内部 prompt 类型返回固定文本，用于验证 cmd 调用路径。
type cmdFakeModel struct {
	mu            sync.Mutex
	inputs        [][]*schema.Message
	generateCalls int
	criticCalls   int
}

// Generate 模拟 generator 和 critic 两类模型调用。
func (m *cmdFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
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
			return schema.AssistantMessage(`{
  "goal": "提升报名转化",
  "audience": "AI 产品经理、企业培训负责人",
  "budget": {"amount": 5000, "items": ["海报设计", "社群分发", "讲师物料"]},
  "channels": ["社群", "公众号", "销售朋友圈"],
  "timeline": ["活动前 3 天预热", "直播当天答疑", "直播后销售跟进"],
  "cta": "扫码报名",
  "risk_plan": ["候补直播", "讲师备份"]
}`, nil), nil
		}
		return schema.AssistantMessage(`{
  "goal": "介绍 AI 公开课亮点",
  "audience": "",
  "budget": {"amount": 0, "items": []},
  "channels": [],
  "timeline": [],
  "cta": "提升转化",
  "risk_plan": []
}`, nil), nil
	case strings.Contains(system, "活动方案批评器"):
		m.criticCalls++
		if m.criticCalls == 1 {
			return schema.AssistantMessage(`{"approved":false,"issues":["缺少 CTA","预算和渠道不清晰"],"suggestions":["补充扫码报名入口","补充预算和分发渠道"],"score":0.62}`, nil), nil
		}
		return schema.AssistantMessage(`{"approved":true,"issues":[],"suggestions":[],"score":0.93}`, nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前命令不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明 generator 和 critic 都执行过。
func (m *cmdFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
