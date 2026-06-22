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

	"ai-designing/reasoning/compose"
)

// TestRunAgentPrepareOnlyUsesDefaultQuery 验证 cmd 默认能读到非 coding 场景输入。
func TestRunAgentPrepareOnlyUsesDefaultQuery(t *testing.T) {
	clearComposeEnv(t)
	output, err := runAgent(context.Background(), []string{"-prepare-only", "-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.Scenario != defaultScenario || output.QueryChars == 0 {
		t.Fatalf("output = %+v", output)
	}
}

// TestParseRunConfigAllowsEnvThreshold 验证路由阈值可以通过环境变量覆盖。
func TestParseRunConfigAllowsEnvThreshold(t *testing.T) {
	clearComposeEnv(t)
	t.Setenv("COMPOSE_CONFIDENCE_THRESHOLD", "0.82")
	config, err := parseRunConfig([]string{"-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if config.ConfidenceThreshold != 0.82 {
		t.Fatalf("ConfidenceThreshold = %f, want 0.82", config.ConfidenceThreshold)
	}
}

// TestRunAgentCallsComposeThroughADKRunner 验证 cmd 真实调用链是 ADK Runner -> Compose Agent。
func TestRunAgentCallsComposeThroughADKRunner(t *testing.T) {
	clearComposeEnv(t)
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
		"-message", "大一学生连续三周缺席早课，通勤变长、兼职变多、成绩下降，但只说早上起不来，应该先做哪类支持干预？",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Complexity != compose.ComplexityComplex || !output.UsedHypothesis || output.TOTRootVisits != 1 || !output.UsedADKCustomizedData {
		t.Fatalf("output = %+v", output)
	}
	if fake.Count() < 8 {
		t.Fatalf("fake model calls = %d, want complex multi-stage calls", fake.Count())
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 不记录问题原文或推理明细。
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

	output, err := withRunAgentTrace(ctx, "学生学习支持分流", 64, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Complexity: compose.ComplexityComplex, UsedHypothesis: true, AnswerChars: 42}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "compose_router_agent_run" || endName != "compose_router_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.Scenario != "学生学习支持分流" || input.QueryChars != 64 {
		t.Fatalf("trace input = %+v", input)
	}
	if output.Scenario != "学生学习支持分流" || output.QueryChars != 64 {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
}

// TestLoadQueryFromFile 验证问题输入来自外部文件，而不是写死在代码里。
func TestLoadQueryFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.txt")
	if err := os.WriteFile(path, []byte("请判断学生支持优先级。"), 0o644); err != nil {
		t.Fatal(err)
	}
	query, err := loadQuery(runConfig{QueryFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if query != "请判断学生支持优先级。" {
		t.Fatalf("query = %q", query)
	}
}

// clearComposeEnv 清理外部环境变量，保证 cmd 默认值测试不被本机 .env 或 shell 污染。
func clearComposeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"COMPOSE_SCENARIO",
		"COMPOSE_TOT_METHOD",
		"COMPOSE_TOT_MAX_DEPTH",
		"COMPOSE_TOT_NSIM",
		"COMPOSE_TOT_FOREST_SIZE",
		"COMPOSE_HYPOTHESIS_MAX_ITERATIONS",
		"COMPOSE_HYPOTHESIS_MAX_HYPOTHESES",
		"COMPOSE_HYPOTHESIS_MAX_EVIDENCE",
		"COMPOSE_CONFIDENCE_THRESHOLD",
		"COMPOSE_BEST_PATH_THRESHOLD",
	} {
		t.Setenv(key, "")
	}
}

// cmdFakeModel 根据组合 Agent 和子 Agent 的 prompt 类型返回固定文本。
type cmdFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟复杂路由、ToT 多路径、最佳路径确认和 hypothesis 反证循环。
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
	case strings.Contains(system, "复杂度路由器"):
		return schema.AssistantMessage(`{"complexity":"complex","confidence":0.88,"reason":"多根因、多风险、证据不完整","recommended_path":"parallel_exploration"}`, nil), nil
	case strings.Contains(user, "How should the thinking process continue?"):
		return schema.AssistantMessage(strings.Join([]string{
			"REFLECTION:",
			"学生支持问题需要比较多个干预路径。",
			"",
			"**Possible Options:**",
			"Option 1: 从出勤风险访谈切入",
			"Option 2: 从通勤和兼职支持切入",
			"Option 3: 从学习补救切入",
			"Option 4: TERMINATE",
		}, "\n"), nil), nil
	case strings.Contains(user, "Final Answer:"):
		return schema.AssistantMessage("先做出勤风险访谈，同时设计一周早课支持计划。", nil), nil
	case strings.Contains(user, "Rate:") || strings.Contains(system, "评价"):
		return schema.AssistantMessage("Rating: 9\n理由：能推进学生支持决策。", nil), nil
	case strings.Contains(system, "最佳路径确认器"):
		return schema.AssistantMessage(`{"confirmed":true,"confidence":0.82,"reason":"候选路径可进入验证。"}`, nil), nil
	case strings.Contains(system, "假设规划器"):
		return schema.AssistantMessage(`{"hypotheses":[
{"description":"候选答案足以作为第一步，因为它先验证退课风险并覆盖已知约束","prior":0.45},
{"description":"应该直接升级人工介入，因为所有关键信息都缺失","prior":0.30}
]}`, nil), nil
	case strings.Contains(system, "证据生成器"):
		return schema.AssistantMessage(`{"evidence":[{"description":"原问题已给出连续缺席、通勤变长、兼职变多、成绩下降四个风险信号","source":"case_brief"}]}`, nil), nil
	case strings.Contains(system, "反证评估器"):
		if strings.Contains(user, "候选答案足以作为第一步") {
			return schema.AssistantMessage(`{"effect":"supports","posterior_delta":0.50}`, nil), nil
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

// Count 返回模型调用次数，用来证明 Compose Agent 进行了多阶段内部调用。
func (m *cmdFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
