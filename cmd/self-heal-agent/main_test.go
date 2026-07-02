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

// TestDefaultSupportSOPFailureShowsBusinessPain 验证默认输入表达的是非 coding 的客服 SOP 痛点。
func TestDefaultSupportSOPFailureShowsBusinessPain(t *testing.T) {
	failure := defaultSupportSOPFailure()
	for _, want := range []string{"客服补偿 SOP", "升级边界", "一线客服"} {
		if !strings.Contains(failure.ErrorText, want) {
			t.Fatalf("failure missing %q: %+v", want, failure)
		}
	}
	if failure.Kind != "support_sop_gap" || failure.Severity < 2 {
		t.Fatalf("failure = %+v", failure)
	}
}

// TestRunAgentSelfHealsSupportSOP 验证命令入口会通过 ADK Runner 跑完整自愈链路并修复业务 SOP。
func TestRunAgentSelfHealsSupportSOP(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdSelfHealFakeModel{}
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
	if output.Status != "fixed" || output.Iterations != 1 || !output.UsedADKCustomizedData {
		t.Fatalf("output = %+v", output)
	}
	if output.PolicyVersion != 2 || output.RefundWindowHours != 24 || !output.EscalationEnabled || output.CompensationLimit != 200 {
		t.Fatalf("policy summary = %+v", output)
	}
	if fake.Count() != 3 {
		t.Fatalf("fake model calls = %d, want 3", fake.Count())
	}
}

// TestSupportSOPScenarioRollsBackUnsafePatch 验证非 coding 场景里过度补偿这类业务回归会触发回滚。
func TestSupportSOPScenarioRollsBackUnsafePatch(t *testing.T) {
	scenario := newSupportSOPScenario()
	commitID, err := scenario.Apply(context.Background(), unsafeFixProposal())
	if err != nil {
		t.Fatal(err)
	}
	failure, err := scenario.Verify(context.Background(), unsafeFixProposal())
	if err != nil {
		t.Fatal(err)
	}
	if failure == nil || failure.Kind != "support_sop_regression" || failure.Severity < 4 {
		t.Fatalf("failure = %+v", failure)
	}
	if err := scenario.Rollback(context.Background(), commitID); err != nil {
		t.Fatal(err)
	}
	summary := scenario.PolicySummary()
	if summary.PolicyVersion != 1 || summary.RefundWindowHours != 0 || summary.EscalationEnabled {
		t.Fatalf("summary after rollback = %+v", summary)
	}
}

// TestRunAgentTraceIsConcise 验证命令 trace 只携带摘要字段，不泄露完整 SOP 文本和补丁正文。
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

	output, err := withRunAgentTrace(ctx, runAgentTraceInput{
		Scenario:        "support_sop_self_heal",
		FailureKind:     "support_sop_gap",
		AffectedFiles:   1,
		MaxIterations:   3,
		FailureSeverity: 2,
	}, func(context.Context) (runOutput, error) {
		return runOutput{
			Scenario:              "support_sop_self_heal",
			Status:                "fixed",
			Iterations:            1,
			Commits:               1,
			AnswerChars:           128,
			UsedADKCustomizedData: true,
			PolicyVersion:         2,
			RefundWindowHours:     24,
			EscalationEnabled:     true,
			CompensationLimit:     200,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if startName != "self_heal_agent_run" || endName != "self_heal_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	if output.Status != "fixed" || output.PolicyVersion != 2 {
		t.Fatalf("output = %+v", output)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"客服补偿 SOP 缺少升级边界", "refund_window_hours=24", "一线客服"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

// cmdSelfHealFakeModel 根据自愈节点的系统提示返回固定诊断、补丁和评审结果。
type cmdSelfHealFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟 Eino ChatModel 在诊断、补丁生成和风险评审三个节点中的输出。
func (m *cmdSelfHealFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	system := ""
	if len(input) > 0 {
		system = input[0].Content
	}
	switch {
	case strings.Contains(system, "自愈诊断器"):
		return schema.AssistantMessage("根因：客服补偿 SOP 缺少补偿窗口、升级边界和上限，导致一线无法独立处理。", nil), nil
	case strings.Contains(system, "自愈修复生成器"):
		return schema.AssistantMessage(`{"summary":"补齐客服补偿 SOP","fix_diff":"refund_window_hours=24; escalation_enabled=true; compensation_limit=200"}`, nil), nil
	case strings.Contains(system, "自愈风险评审器"):
		return schema.AssistantMessage(`{"block":false,"reason":"补偿上限和升级边界明确，风险可控"}`, nil), nil
	default:
		return nil, errors.New("unexpected prompt")
	}
}

// Stream 当前命令不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdSelfHealFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，证明诊断、修复生成和评审都经过了 Eino 模型节点。
func (m *cmdSelfHealFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
