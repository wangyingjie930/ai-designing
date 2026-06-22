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

// TestRunAgentPrepareOnlyUsesDefaultQuestion 验证 cmd 默认能读到非 coding 场景输入。
func TestRunAgentPrepareOnlyUsesDefaultQuestion(t *testing.T) {
	output, err := runAgent(context.Background(), []string{"-prepare-only", "-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.Scenario != defaultScenario || output.QuestionChars == 0 {
		t.Fatalf("output = %+v", output)
	}
}

// TestRunAgentCallsCOTThroughADKRunner 验证 cmd 真实调用链是 ADK Runner -> CoT Agent -> verifier。
func TestRunAgentCallsCOTThroughADKRunner(t *testing.T) {
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
		"-message", "请帮我安排周三家庭照护排班。",
		"-scenario", "家庭照护排班",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Steps != 3 || output.Issues != 0 || !output.Verified || !output.UsedADKCustomizedData {
		t.Fatalf("output = %+v", output)
	}
	if fake.Count() != 4 {
		t.Fatalf("fake model calls = %d, want 4", fake.Count())
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 不记录用户问题或推理内容。
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

	output, err := withRunAgentTrace(ctx, "家庭照护排班", 28, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Steps: 3, Issues: 0, AnswerChars: 42, Verified: true}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "cot_verifier_agent_run" || endName != "cot_verifier_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.Scenario != "家庭照护排班" || input.QuestionChars != 28 {
		t.Fatalf("trace input = %+v", input)
	}
	if output.Scenario != "家庭照护排班" || output.QuestionChars != 28 {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
}

// TestLoadQuestionFromFile 验证问题输入来自外部文件，而不是写死在代码里。
func TestLoadQuestionFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "question.txt")
	if err := os.WriteFile(path, []byte("请安排家庭复诊日程。"), 0o644); err != nil {
		t.Fatal(err)
	}
	question, err := loadQuestion(runConfig{QuestionFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if question != "请安排家庭复诊日程。" {
		t.Fatalf("question = %q", question)
	}
}

// cmdFakeModel 根据 CoT 内部 prompt 类型返回固定文本，用于验证 cmd 调用路径。
type cmdFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟 reasoner 和 verifier 两类模型调用。
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
	case strings.Contains(system, "显性推理链 Agent"):
		return schema.AssistantMessage(`{
  "steps": [
    {"step_number": 1, "content": "客户会议是硬约束，本人上午不能承担陪诊主责。", "confidence": 0.95},
    {"step_number": 2, "content": "妹妹上午可请假，最适合覆盖父亲复诊和母亲流程协助。", "confidence": 0.86},
    {"step_number": 3, "content": "孩子放学在下午，应单独安排本人或备用接送。", "confidence": 0.78}
  ],
  "final_answer": "推荐妹妹上午陪同父亲复诊并协助母亲关键流程，本人保留客户会议，下午负责孩子接送，并准备备用打车和陪诊电话。"
}`, nil), nil
	case strings.Contains(user, "校验规则"):
		if !strings.Contains(user, "原问题：") || !strings.Contains(user, "请帮我安排周三家庭照护排班。") {
			return schema.AssistantMessage("INVALID\n未提供原问题，当前步骤缺少可核验依据。", nil), nil
		}
		return schema.AssistantMessage("VALID\n该步骤没有违反硬约束。", nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前命令不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明 verifier 逐步执行。
func (m *cmdFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
