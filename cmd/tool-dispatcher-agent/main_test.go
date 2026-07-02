package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tooldispatcher "ai-designing/action/tool_dispatcher"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestPrepareOnlyShowsToolDispatcherSummary 验证默认预检路径不需要模型参数。
func TestPrepareOnlyShowsToolDispatcherSummary(t *testing.T) {
	envPath := writeTestDotEnv(t, "")
	output, err := runAgent(context.Background(), []string{"-env-file", envPath, "-prepare-only"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.DynamicTools < 5 || output.Scenario != defaultScenarioName {
		t.Fatalf("output = %+v", output)
	}
}

// TestRunAgentUsesDynamicToolDispatcher 验证命令入口会通过 tool_search 加载续费风险工具。
func TestRunAgentUsesDynamicToolDispatcher(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdToolDispatcherFakeModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	envPath := writeTestDotEnv(t, strings.Join([]string{
		"OPENAI_API_KEY=test-key",
		"LLM_MODEL=test-model",
	}, "\n"))
	output, err := runAgent(context.Background(), []string{
		"-env-file", envPath,
		"-message", "ACME-42 续费前用量下降，还卡着发票问题，请给 CSM 处理建议。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.AnswerChars == 0 || fake.calls != 5 || output.LoadedTools != 3 {
		t.Fatalf("output=%+v calls=%d", output, fake.calls)
	}
	if names := toolNames(fake.toolsByCall[0]); !sameStringSet(names, []string{"tool_search"}) {
		t.Fatalf("first model tools = %v", names)
	}
}

// TestRunAgentPrefersDotEnvOverProcessEnvAndFlags 验证 tool_dispatcher 运行配置以 .env 文件为准。
func TestRunAgentPrefersDotEnvOverProcessEnvAndFlags(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdToolDispatcherFakeModel{}
	var captured modelConfig
	newChatModel = func(_ context.Context, config modelConfig) (model.BaseChatModel, error) {
		captured = config
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	dotenvMessage := "DOTENV-42 续费风险来自 .env，请以这个输入为准。"
	envPath := writeTestDotEnv(t, strings.Join([]string{
		"OPENAI_API_KEY=dotenv-key",
		"LLM_MODEL=dotenv-model",
		"LLM_OPENAI_BASE_URL=http://dotenv.example",
		"TOOL_DISPATCHER_MESSAGE=" + dotenvMessage,
	}, "\n"))

	t.Setenv("OPENAI_API_KEY", "process-key")
	t.Setenv("LLM_MODEL", "process-model")
	output, err := runAgent(context.Background(), []string{
		"-env-file", envPath,
		"-message", "FLAG-99 这条命令行输入不应覆盖 .env。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.APIKey != "dotenv-key" || captured.Model != "dotenv-model" || captured.BaseURL != "http://dotenv.example/v1" {
		t.Fatalf("captured model config = %+v", captured)
	}
	if output.QueryChars != len([]rune(dotenvMessage)) {
		t.Fatalf("query chars = %d, want dotenv message chars %d", output.QueryChars, len([]rune(dotenvMessage)))
	}
}

// writeTestDotEnv 写入测试专用 .env，避免受仓库根目录真实 .env 影响。
func writeTestDotEnv(t *testing.T, content string) string {
	t.Helper()
	envPath := filepath.Join(t.TempDir(), ".env")
	if strings.TrimSpace(content) != "" {
		content += "\n"
	}
	content += "COZELOOP_ENABLED=false\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return envPath
}

// TestRunAgentTraceIsConcise 验证 trace 只带摘要，不上传完整客户消息或工具结果。
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

	output, err := withRunAgentTrace(ctx, 31, 6, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Scenario: defaultScenarioName, QueryChars: 31, AnswerChars: 40, DynamicTools: 5, LoadedTools: 3}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if output.AnswerChars != 40 {
		t.Fatalf("output = %+v", output)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.QueryChars != 31 || input.DynamicTools != 6 {
		t.Fatalf("trace input = %+v", input)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"ACME-42 续费前用量下降", "90 天续费挽留方案"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

// cmdToolDispatcherFakeModel 模拟模型使用 tool_search 后再调用续费风险工具。
type cmdToolDispatcherFakeModel struct {
	calls       int
	toolsByCall [][]*schema.ToolInfo
	toolTexts   []string
}

// Generate 用固定调用序列验证 cmd 真实走 ADK tool dispatcher 链路。
func (m *cmdToolDispatcherFakeModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	options := model.GetCommonOptions(nil, opts...)
	m.toolsByCall = append(m.toolsByCall, options.Tools)
	m.captureToolMessages(input)

	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_tool_search",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "tool_search",
				Arguments: `{"query":"select:load_account_snapshot,check_renewal_contract,draft_retention_playbook"}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_account",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      tooldispatcher.LoadAccountSnapshotToolName,
				Arguments: `{"account_id":"ACME-42"}`,
			},
		}}), nil
	case 3:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_contract",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      tooldispatcher.CheckRenewalContractToolName,
				Arguments: `{"account_id":"ACME-42"}`,
			},
		}}), nil
	case 4:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_playbook",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      tooldispatcher.DraftRetentionPlaybookToolName,
				Arguments: `{"account_id":"ACME-42","risk_signal":"用量下降且发票问题未闭环","days_to_renewal":27}`,
			},
		}}), nil
	case 5:
		return schema.AssistantMessage("续费风险较高，建议 CSM 按工具给出的挽留方案推进。", nil), nil
	default:
		return nil, errors.New("unexpected extra Generate call")
	}
}

// Stream 当前测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdToolDispatcherFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// captureToolMessages 记录模型可见的工具消息。
func (m *cmdToolDispatcherFakeModel) captureToolMessages(messages []*schema.Message) {
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && message.Content != "" && !containsString(m.toolTexts, message.Content) {
			m.toolTexts = append(m.toolTexts, message.Content)
		}
	}
}

// toolNames 提取本轮模型可见工具。
func toolNames(infos []*schema.ToolInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info != nil {
			names = append(names, info.Name)
		}
	}
	return names
}

// containsString 判断切片中是否包含目标字符串。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// containsAllStrings 判断 values 是否包含 expected 中的全部字符串。
func containsAllStrings(values []string, expected []string) bool {
	for _, want := range expected {
		if !containsString(values, want) {
			return false
		}
	}
	return true
}

// sameStringSet 判断两个字符串集合是否一致。
func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	return containsAllStrings(left, right) && containsAllStrings(right, left)
}
