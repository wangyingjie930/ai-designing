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

// TestDefaultScenarioUsesNonCodingOperationalPain 验证默认场景是活动运营复盘，不是 coding demo。
func TestDefaultScenarioUsesNonCodingOperationalPain(t *testing.T) {
	traces := defaultOperationalTraces()
	if len(traces) < 4 {
		t.Fatalf("traces=%d, want at least 4", len(traces))
	}
	task := defaultRetrospectiveTask()
	for _, want := range []string{"AI 公开课", "报名转化", "复盘"} {
		if !strings.Contains(task, want) {
			t.Fatalf("default task missing %q:\n%s", want, task)
		}
	}
	for _, trace := range traces {
		if strings.Contains(strings.ToLower(trace.Task), "code") || strings.Contains(trace.Task, "代码") {
			t.Fatalf("trace should be non-coding: %+v", trace)
		}
	}
}

// TestRunAgentThroughReplayADK 验证 cmd 会灌入历史执行轨迹、抽取 lesson，并通过 ADK 返回复盘建议。
func TestRunAgentThroughReplayADK(t *testing.T) {
	oldFactory := newChatModel
	fake := &cmdReplayFakeModel{}
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
	if output.Mode != "agent" || output.RecordedTraces != 4 || output.ReflectionCount != 2 || output.LessonCount == 0 || output.AdviceChars == 0 || !output.UsedADKCustomizedData {
		t.Fatalf("output=%+v", output)
	}
	if fake.Count() < 4 {
		t.Fatalf("fake model calls=%d, want reflection/extraction/advice calls", fake.Count())
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 只记录摘要，不泄漏完整任务、错误和 lesson 文本。
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

	output, err := withRunAgentTrace(ctx, 4, 2, 2, 120, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", RecordedTraces: 4, ReflectionCount: 2, LessonCount: 2, AdviceChars: 80}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "replay_retrospective_agent_run" || endName != "replay_retrospective_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type=%T", startInput)
	}
	if input.TraceCount != 4 || input.FailureCount != 2 || input.TaskChars != 120 {
		t.Fatalf("trace input=%+v", input)
	}
	if output.RecordedTraces != 4 || output.LessonCount != 2 {
		t.Fatalf("output=%+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type=%T", endOutput)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"报名链接过期", "高意向名单", "活动前 24 小时"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

// cmdReplayFakeModel 用固定模型响应覆盖 reflection、lesson extraction 和最终建议。
type cmdReplayFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 根据 replay prompt 类型返回稳定文本，保持 cmd 测试纯本地。
func (m *cmdReplayFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	user := ""
	if len(input) > 0 {
		user = input[len(input)-1].Content
	}
	switch {
	case strings.Contains(user, "Analyze this failed execution"):
		return schema.AssistantMessage("ROOT_CAUSE: 复盘太晚\nLESSON: 关键承接链路必须提前检查\nPREVENTION: 活动前一天拉清单", nil), nil
	case strings.Contains(user, "Find cross-task patterns"):
		return schema.AssistantMessage("INSIGHT: 活动前 24 小时检查报名链路、企微承接人和直播后名单分发。", nil), nil
	case strings.Contains(user, "Review these lessons"):
		return schema.AssistantMessage("下一场复盘提醒：先查报名链路，再锁定企微承接人，并在直播后 30 分钟内分发高意向名单。", nil), nil
	default:
		return nil, errors.New("unexpected replay cmd prompt: " + user)
	}
}

// Stream 当前命令测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdReplayFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明 replay 三段链路都跑过。
func (m *cmdReplayFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
