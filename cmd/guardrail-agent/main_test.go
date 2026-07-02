package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	guardrail "ai-designing/action/guardrail"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestPrepareOnlyShowsGuardrailPolicy 验证默认点击运行路径不需要模型参数，也能看到策略摘要。
func TestPrepareOnlyShowsGuardrailPolicy(t *testing.T) {
	output, err := runAgent(context.Background(), []string{"-prepare-only"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.AllowedTools != 3 || output.Scenario != defaultScenarioName {
		t.Fatalf("output = %+v", output)
	}
}

// TestDefaultMessageIncludesDemoCaseID 验证默认点击运行能直接进入工具链路，而不是先反问工单号。
func TestDefaultMessageIncludesDemoCaseID(t *testing.T) {
	if !strings.Contains(defaultMessage, "HS-1001") {
		t.Fatalf("defaultMessage = %q, want demo case id HS-1001", defaultMessage)
	}
}

// TestRunAgentUsesGuardrailMiddleware 验证命令入口会创建真实 ADK Agent，并通过 guardrail 拦截外发通知。
func TestRunAgentUsesGuardrailMiddleware(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdGuardrailFakeModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background(), []string{
		"-message", "客户厨房漏水，希望今天安排师傅并通知客户。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.AnswerChars == 0 || fake.calls != 3 {
		t.Fatalf("output=%+v calls=%d", output, fake.calls)
	}
	joinedToolTexts := strings.Join(fake.toolTexts, "\n")
	if !strings.Contains(joinedToolTexts, `"blocked_by":"input_filter"`) {
		t.Fatalf("tool texts = %s", joinedToolTexts)
	}
	for _, leaked := range []string{"owner@example.com", "token=svc-secret"} {
		if strings.Contains(joinedToolTexts, leaked) {
			t.Fatalf("tool text leaked %q: %s", leaked, joinedToolTexts)
		}
	}
}

// TestRunAgentTraceIsConcise 验证命令级 trace 只记录摘要，不上传完整客诉和工具输出。
func TestRunAgentTraceIsConcise(t *testing.T) {
	ctx := context.Background()
	var startInput callbacks.CallbackInput
	var endOutput callbacks.CallbackOutput
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			startInput = input
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			endOutput = output
			return ctx
		}).
		Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Name: "test_root", Type: "test", Component: "test"}, handler)

	output, err := withRunAgentTrace(ctx, 19, false, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Scenario: defaultScenarioName, QueryChars: 19, AnswerChars: 31, AllowedTools: 3}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if output.AnswerChars != 31 {
		t.Fatalf("output = %+v", output)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.QueryChars != 19 || input.ApproveExternal {
		t.Fatalf("trace input = %+v", input)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"客户厨房漏水", "owner@example.com", "token=svc-secret"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

// TestLoadModelConfigNormalizesBaseURL 验证 .env 里的本地 OpenAI-compatible 根地址会补齐 /v1。
func TestLoadModelConfigNormalizesBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	t.Setenv("LLM_OPENAI_BASE_URL", "http://localhost:8317")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("BASE_URL", "")

	config, err := loadModelConfig()
	if err != nil {
		t.Fatalf("loadModelConfig() error = %v", err)
	}
	if config.BaseURL != "http://localhost:8317/v1" {
		t.Fatalf("BaseURL = %q, want /v1 normalized", config.BaseURL)
	}
}

// TestLoadDotEnvOverridesProcessEnvironment 验证本地 demo 一切以 .env 为准，避免外层旧密钥污染运行。
func TestLoadDotEnvOverridesProcessEnvironment(t *testing.T) {
	t.Setenv("COZELOOP_API_TOKEN", "outer-token")
	t.Setenv("COZELOOP_WORKSPACE_ID", "outer-workspace")
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("COZELOOP_API_TOKEN=dotenv-token\nCOZELOOP_WORKSPACE_ID=dotenv-workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := loadDotEnv(envPath); err != nil {
		t.Fatalf("loadDotEnv() error = %v", err)
	}
	if got := os.Getenv("COZELOOP_API_TOKEN"); got != "dotenv-token" {
		t.Fatalf("COZELOOP_API_TOKEN = %q, want dotenv value", got)
	}
	if got := os.Getenv("COZELOOP_WORKSPACE_ID"); got != "dotenv-workspace" {
		t.Fatalf("COZELOOP_WORKSPACE_ID = %q, want dotenv value", got)
	}
}

// cmdGuardrailFakeModel 模拟模型查工单、尝试外发通知，再根据 guardrail 结果回复。
type cmdGuardrailFakeModel struct {
	calls     int
	toolTexts []string
}

// Generate 依次返回工具调用和最终回复，避免命令测试调用真实模型。
func (m *cmdGuardrailFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	m.captureToolMessages(input)
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_lookup_service_case",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      guardrail.LookupServiceCaseToolName,
				Arguments: `{"case_id":"HS-1001"}`,
			},
		}}), nil
	case 2:
		if !m.sawToolText("HS-1001") {
			return nil, errors.New("missing service case result")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_send_customer_notice",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      guardrail.SendCustomerNoticeToolName,
				Arguments: `{"case_id":"HS-1001","channel":"sms","message":"师傅今天 18:00 前上门"}`,
			},
		}}), nil
	case 3:
		if !m.sawToolText(`"blocked_by":"input_filter"`) {
			return nil, errors.New("missing guardrail block report")
		}
		return schema.AssistantMessage("已生成安全排期建议，外发通知已等待人工确认。", nil), nil
	default:
		return nil, errors.New("unexpected extra Generate call")
	}
}

// Stream 当前测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdGuardrailFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// captureToolMessages 收集模型可见的工具消息，用来验证 guardrail 输出。
func (m *cmdGuardrailFakeModel) captureToolMessages(messages []*schema.Message) {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.Content != "" && !containsString(m.toolTexts, message.Content) {
			m.toolTexts = append(m.toolTexts, message.Content)
		}
	}
}

// sawToolText 判断历史工具输出中是否出现指定片段。
func (m *cmdGuardrailFakeModel) sawToolText(part string) bool {
	for _, text := range m.toolTexts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
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
