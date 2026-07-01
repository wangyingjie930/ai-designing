package main

import (
	"ai-designing/reflection/skillmode"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestPrepareOnlyUsesDefaultSkillModeQuery 验证不传 message 时能直接看到默认客服场景输入。
func TestPrepareOnlyUsesDefaultSkillModeQuery(t *testing.T) {
	output, err := runAgent(context.Background(), []string{"-prepare-only", "-mode", "fork"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.SkillMode != skillmode.ModeFork {
		t.Fatalf("output = %+v", output)
	}
	if output.SkillName != "compliance_review_isolated" || output.QueryChars == 0 {
		t.Fatalf("output = %+v", output)
	}
}

// TestRunAgentUsesSkillMiddleware 验证命令入口会创建真实 ADK Runner 并调用 skill 工具。
func TestRunAgentUsesSkillMiddleware(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdSkillModeFakeModel{targetSkill: "compensation_review_with_context"}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background(), []string{
		"-mode", "fork_with_context",
		"-message", "客户第二次被临时改课，请判断是否需要补偿。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.SkillMode != skillmode.ModeForkWithContext || output.AnswerChars == 0 {
		t.Fatalf("output = %+v", output)
	}
	if fake.skillToolCalls != 1 || fake.subAgentCalls != 1 {
		t.Fatalf("fake calls skill=%d sub=%d", fake.skillToolCalls, fake.subAgentCalls)
	}
}

// TestRunAgentTraceIsConcise 验证命令级 trace 只记录摘要，不把完整客诉或 skill 内容塞进去。
func TestRunAgentTraceIsConcise(t *testing.T) {
	ctx := context.Background()
	scenario, err := skillmode.ScenarioForMode(skillmode.ModeInline)
	if err != nil {
		t.Fatal(err)
	}
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

	output, err := withRunAgentTrace(ctx, scenario, 27, func(context.Context) (runOutput, error) {
		return runOutput{
			Mode:        "agent",
			SkillMode:   skillmode.ModeInline,
			Scenario:    scenario.Title,
			SkillName:   scenario.SkillName,
			QueryChars:  27,
			AnswerChars: 31,
		}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if output.SkillName != scenario.SkillName || output.AnswerChars != 31 {
		t.Fatalf("output = %+v", output)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.SkillMode != skillmode.ModeInline || input.QueryChars != 27 {
		t.Fatalf("trace input = %+v", input)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"课程顾问 24 小时没有回复", "主管升级边界", "退款"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

// cmdSkillModeFakeModel 模拟命令入口中主 agent、skill 工具结果和专家子 agent 的交互。
type cmdSkillModeFakeModel struct {
	targetSkill    string
	skillToolCalls int
	subAgentCalls  int
}

// Generate 根据系统提示区分主 agent 与 Skill fork 子 agent。
func (m *cmdSkillModeFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	system := firstContentByRole(input, schema.System)
	if strings.Contains(system, "Skill 模式专家子 Agent") {
		m.subAgentCalls++
		return schema.AssistantMessage("专家复核：建议提供一次补偿课，并承诺主管回访。", nil), nil
	}
	if firstMessageByRole(input, schema.Tool) == nil {
		m.skillToolCalls++
		task := firstContentByRole(input, schema.User)
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_skill_mode",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "skill",
				Arguments: m.skillArguments(task),
			},
		}}), nil
	}
	return schema.AssistantMessage("已结合 Skill 结果给出客服处理建议。", nil), nil
}

// Stream 当前命令测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdSkillModeFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// skillArguments 模拟真实模型把当前用户输入随 skill 工具一起传递。
func (m *cmdSkillModeFakeModel) skillArguments(task string) string {
	args := struct {
		Skill string `json:"skill"`
		Task  string `json:"task"`
	}{
		Skill: m.targetSkill,
		Task:  task,
	}
	data, _ := json.Marshal(args)
	return string(data)
}

// firstContentByRole 返回指定角色的第一条内容。
func firstContentByRole(messages []*schema.Message, role schema.RoleType) string {
	if message := firstMessageByRole(messages, role); message != nil {
		return message.Content
	}
	return ""
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
