package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestRunAgentPrepareOnlyUsesDefaultProblem 验证 cmd 默认能读到非 coding 场景输入。
func TestRunAgentPrepareOnlyUsesDefaultProblem(t *testing.T) {
	output, err := runAgent(context.Background(), []string{"-prepare-only", "-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.Scenario != defaultScenario || output.ProblemChars == 0 || output.MaxIterations != defaultMaxIterations || output.MaxHypotheses != defaultMaxHypotheses || output.MaxEvidence != defaultMaxEvidence {
		t.Fatalf("output = %+v", output)
	}
}

// TestParseRunConfigAllowsEnvMaxIterations 验证环境变量可以覆盖默认迭代上限。
func TestParseRunConfigAllowsEnvMaxIterations(t *testing.T) {
	t.Setenv("HYPOTHESIS_MAX_ITERATIONS", "4")
	config, err := parseRunConfig([]string{"-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxIterations != 4 {
		t.Fatalf("MaxIterations = %d, want 4", config.MaxIterations)
	}
}

// TestParseRunConfigAllowsEnvQuantityLimits 验证假设和证据数量也可以通过环境变量调整。
func TestParseRunConfigAllowsEnvQuantityLimits(t *testing.T) {
	t.Setenv("HYPOTHESIS_MAX_HYPOTHESES", "3")
	t.Setenv("HYPOTHESIS_MAX_EVIDENCE", "2")
	config, err := parseRunConfig([]string{"-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxHypotheses != 3 || config.MaxEvidence != 2 {
		t.Fatalf("limits = hypotheses:%d evidence:%d", config.MaxHypotheses, config.MaxEvidence)
	}
}

// TestRunAgentCallsHypothesisThroughADKRunner 验证 cmd 真实调用链是 ADK Runner -> Hypothesis Agent。
func TestRunAgentCallsHypothesisThroughADKRunner(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdFakeModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "社区活动报名下降，报名表新增了多个必填字段。",
		"-scenario", "社区活动报名下滑诊断",
		"-max-iterations", "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Hypotheses != 2 || output.Survivors != 1 || !output.Converged || !output.UsedADKCustomizedData {
		t.Fatalf("output = %+v", output)
	}
	if fake.Count() != 5 {
		t.Fatalf("fake model calls = %d, want 5", fake.Count())
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 不记录问题原文或证据明细。
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

	output, err := withRunAgentTrace(ctx, "社区活动报名下滑诊断", 36, 2, 2, 1, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", IterationsUsed: 1, Hypotheses: 2, Survivors: 1, Confirmed: 1, Converged: true, AnswerChars: 42}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "hypothesis_agent_run" || endName != "hypothesis_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.Scenario != "社区活动报名下滑诊断" || input.ProblemChars != 36 || input.MaxIterations != 2 || input.MaxHypotheses != 2 || input.MaxEvidence != 1 {
		t.Fatalf("trace input = %+v", input)
	}
	if output.Scenario != "社区活动报名下滑诊断" || output.ProblemChars != 36 || output.MaxIterations != 2 || output.MaxHypotheses != 2 || output.MaxEvidence != 1 {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
}

// TestLoadProblemFromFile 验证问题输入来自外部文件，而不是写死在代码里。
func TestLoadProblemFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "problem.txt")
	if err := os.WriteFile(path, []byte("请诊断线下课程报名下降。"), 0o644); err != nil {
		t.Fatal(err)
	}
	problem, err := loadProblem(runConfig{ProblemFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if problem != "请诊断线下课程报名下降。" {
		t.Fatalf("problem = %q", problem)
	}
}

// cmdFakeModel 根据 Hypothesis 内部 prompt 类型返回固定文本，用于验证 cmd 调用路径。
type cmdFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟 planner、generator、evaluator 三类模型调用。
func (m *cmdFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
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
	case strings.Contains(system, "假设规划器"):
		return schema.AssistantMessage(`{"hypotheses":[
{"description":"海报标题降低了亲子体验感","prior":0.35},
{"description":"报名表新增字段造成流程摩擦","prior":0.45},
{"description":"学校运动会撞期分流了家长","prior":0.20}
]}`, nil), nil
	case strings.Contains(system, "证据生成器"):
		return schema.AssistantMessage(`{"evidence":[{"description":"报名表新增身份证号和紧急联系人两个必填字段","source":"case_brief"}]}`, nil), nil
	case strings.Contains(system, "反证评估器"):
		if strings.Contains(user, "报名表新增字段造成流程摩擦") {
			return schema.AssistantMessage(`{"effect":"supports","posterior_delta":0.55}`, nil), nil
		}
		return schema.AssistantMessage(`{"effect":"refutes","posterior_delta":-0.35}`, nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前命令不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明 Hypothesis Agent 进行了多角色内部调用。
func (m *cmdFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
