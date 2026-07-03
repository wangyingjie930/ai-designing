package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestSalesTurnLoopPreemptsAndMergesInputs 验证第二句到来时，第一轮输出作废，但第一句输入会并入下一轮。
func TestSalesTurnLoopPreemptsAndMergesInputs(t *testing.T) {
	fake := newBlockingSalesModel()
	agent, err := newSalesChatAgent(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runSalesConversation(context.Background(), agent, salesConversationConfig{
		FirstMessage:    "客户说预算只有三万，先别催单。",
		SecondMessage:   "补充：客户其实最关心交付周期，明天下午要给老板汇报。",
		SecondDelay:     10 * time.Millisecond,
		PreemptTimeout:  10 * time.Millisecond,
		CompletionLimit: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	inputs := fake.userInputs()
	if len(inputs) < 2 {
		t.Fatalf("model inputs = %v, want at least two calls", inputs)
	}
	if !strings.Contains(inputs[0], "预算只有三万") || strings.Contains(inputs[0], "交付周期") {
		t.Fatalf("first input was not the original single message:\n%s", inputs[0])
	}
	if !strings.Contains(inputs[1], "预算只有三万") || !strings.Contains(inputs[1], "交付周期") {
		t.Fatalf("second input did not merge both user messages:\n%s", inputs[1])
	}
	if result.Preemptions != 1 || result.DiscardedTurns != 1 || result.MergedMessages != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Answer != "收到补充，我会按预算和交付周期重新组织销售推进话术。" {
		t.Fatalf("answer = %q", result.Answer)
	}
}

// TestSalesTurnLoopSessionAllowsExternalRepeatedInterrupts 验证外层可以按自己的节奏连续打断当前销售 turn。
func TestSalesTurnLoopSessionAllowsExternalRepeatedInterrupts(t *testing.T) {
	fake := newMultiInterruptSalesModel(3)
	agent, err := newSalesChatAgent(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}
	session := newSalesTurnLoopSession(context.Background(), agent, salesTurnLoopSessionConfig{
		PreemptTimeout: 10 * time.Millisecond,
	})
	defer session.Stop()

	if _, err := session.PushUserMessage("第一句：客户说预算只有三万。"); err != nil {
		t.Fatal(err)
	}
	fake.waitForCall(t, 1)
	if ack, err := session.PushUserMessage("第二句：客户补充最关心交付周期。"); err != nil {
		t.Fatal(err)
	} else {
		waitAck(t, ack)
	}
	fake.waitForCall(t, 2)
	if ack, err := session.PushUserMessage("第三句：客户明天下午要给老板汇报。"); err != nil {
		t.Fatal(err)
	} else {
		waitAck(t, ack)
	}

	result, err := session.WaitAnswer(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	inputs := fake.userInputs()
	if len(inputs) < 3 {
		t.Fatalf("model inputs = %v, want at least three calls", inputs)
	}
	finalInput := inputs[2]
	for _, want := range []string{"预算只有三万", "交付周期", "给老板汇报"} {
		if !strings.Contains(finalInput, want) {
			t.Fatalf("final input missing %q:\n%s", want, finalInput)
		}
	}
	if result.Preemptions != 2 || result.DiscardedTurns != 2 || result.MergedMessages != 3 {
		t.Fatalf("result = %+v", result)
	}
}

// TestSalesTurnLoopMergesWhenFirstTurnFinishesBeforeSecondInput 验证模型很快时，cmd 仍会丢弃未展示的第一轮并合并下一轮。
func TestSalesTurnLoopMergesWhenFirstTurnFinishesBeforeSecondInput(t *testing.T) {
	fake := &fastSalesModel{}
	agent, err := newSalesChatAgent(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runSalesConversation(context.Background(), agent, salesConversationConfig{
		FirstMessage:    "客户说预算只有三万，先别催单。",
		SecondMessage:   "补充：客户其实最关心交付周期。",
		SecondDelay:     20 * time.Millisecond,
		PreemptTimeout:  10 * time.Millisecond,
		CompletionLimit: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	inputs := fake.userInputs()
	if len(inputs) < 2 {
		t.Fatalf("model inputs = %v, want at least two calls", inputs)
	}
	if !strings.Contains(inputs[1], "预算只有三万") || !strings.Contains(inputs[1], "交付周期") {
		t.Fatalf("second input did not merge fast first turn:\n%s", inputs[1])
	}
	if result.Preemptions != 0 || result.DiscardedTurns != 1 || result.MergedMessages != 2 {
		t.Fatalf("result = %+v", result)
	}
}

// TestRunAgentPrepareOnly 不依赖模型配置即可看到 TurnLoop demo 的默认输入。
func TestRunAgentPrepareOnly(t *testing.T) {
	envPath := writeTurnLoopTestDotEnv(t, "")
	output, err := runAgent(context.Background(), []string{"-env-file", envPath, "-prepare-only"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.Scenario != defaultScenarioName || output.InputMessages != 2 {
		t.Fatalf("output = %+v", output)
	}
}

// TestRunAgentUsesTurnLoopWithFakeModel 验证 cmd 真实入口会走 TurnLoop 抢占合并路径。
func TestRunAgentUsesTurnLoopWithFakeModel(t *testing.T) {
	oldFactory := newChatModel
	fake := newBlockingSalesModel()
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	envPath := writeTurnLoopTestDotEnv(t, strings.Join([]string{
		"OPENAI_API_KEY=test-key",
		"LLM_MODEL=test-model",
		"TURNLOOP_FIRST_MESSAGE=客户说预算只有三万，先别催单。",
		"TURNLOOP_SECOND_MESSAGE=补充：客户其实最关心交付周期，明天下午要给老板汇报。",
		"TURNLOOP_SECOND_DELAY_MS=10",
		"TURNLOOP_PREEMPT_TIMEOUT_MS=10",
		"TURNLOOP_COMPLETION_LIMIT_MS=2000",
	}, "\n"))

	output, err := runAgent(context.Background(), []string{"-env-file", envPath})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Preemptions != 1 || output.MergedMessages != 2 || output.AnswerChars == 0 {
		t.Fatalf("output = %+v", output)
	}
}

// TestRunAgentTraceIsConcise 验证根 trace 只记录长度和抢占摘要，不上传完整销售话术。
func TestRunAgentTraceIsConcise(t *testing.T) {
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
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Name: "test_root", Type: "test", Component: "test"}, handler)

	output, err := withRunAgentTrace(ctx, runAgentTraceInput{
		FirstMessageChars:  12,
		SecondMessageChars: 14,
		PreemptTimeoutMS:   200,
	}, func(context.Context) (runOutput, error) {
		return runOutput{
			Mode:           "agent",
			Scenario:       defaultScenarioName,
			InputMessages:  2,
			MergedMessages: 2,
			Preemptions:    1,
			AnswerChars:    30,
		}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if output.Preemptions != 1 {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := startInput.(runAgentTraceInput); !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
	traceText := fmt.Sprintf("%+v\n%+v", startInput, endOutput)
	for _, leaked := range []string{"预算只有三万", "交付周期"} {
		if strings.Contains(traceText, leaked) {
			t.Fatalf("trace leaked %q:\n%s", leaked, traceText)
		}
	}
}

func writeTurnLoopTestDotEnv(t *testing.T, content string) string {
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

type blockingSalesModel struct {
	mu     sync.Mutex
	inputs []string
	calls  int
}

func newBlockingSalesModel() *blockingSalesModel {
	return &blockingSalesModel{}
}

func (m *blockingSalesModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("Generate should not be used; TurnLoop demo enables streaming")
}

func (m *blockingSalesModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.inputs = append(m.inputs, lastUserContent(input))
	m.mu.Unlock()

	if call == 1 {
		reader, _ := schema.Pipe[*schema.Message](1)
		return reader, nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("收到补充，我会按预算和交付周期重新组织销售推进话术。", nil),
	}), nil
}

func (m *blockingSalesModel) userInputs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.inputs...)
}

func lastUserContent(messages []*schema.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.User {
			return messages[i].Content
		}
	}
	return ""
}

var _ model.BaseChatModel = (*blockingSalesModel)(nil)
var _ adk.TypedAgent[*schema.Message] = (*adk.ChatModelAgent)(nil)

func waitAck(t *testing.T, ack <-chan struct{}) {
	t.Helper()
	if ack == nil {
		return
	}
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for preempt ack")
	}
}

type multiInterruptSalesModel struct {
	mu            sync.Mutex
	inputs        []string
	calls         int
	finalCall     int
	callStartedCh chan int
}

func newMultiInterruptSalesModel(finalCall int) *multiInterruptSalesModel {
	return &multiInterruptSalesModel{
		finalCall:     finalCall,
		callStartedCh: make(chan int, finalCall+2),
	}
}

func (m *multiInterruptSalesModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("Generate should not be used; TurnLoop demo enables streaming")
}

func (m *multiInterruptSalesModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.inputs = append(m.inputs, lastUserContent(input))
	m.mu.Unlock()
	m.callStartedCh <- call

	if call < m.finalCall {
		reader, _ := schema.Pipe[*schema.Message](1)
		return reader, nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("我会合并所有补充信息，重新给出像真人销售一样的推进话术。", nil),
	}), nil
}

func (m *multiInterruptSalesModel) waitForCall(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		m.mu.Lock()
		calls := m.calls
		m.mu.Unlock()
		if calls >= want {
			return
		}
		select {
		case <-m.callStartedCh:
		case <-deadline:
			t.Fatalf("timed out waiting for model call %d, got %d", want, calls)
		}
	}
}

func (m *multiInterruptSalesModel) userInputs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.inputs...)
}

var _ model.BaseChatModel = (*multiInterruptSalesModel)(nil)

type fastSalesModel struct {
	mu     sync.Mutex
	inputs []string
	calls  int
}

func (m *fastSalesModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("Generate should not be used; TurnLoop demo enables streaming")
}

func (m *fastSalesModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.inputs = append(m.inputs, lastUserContent(input))
	m.mu.Unlock()

	if call == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("这条第一轮回复很快完成，但 cmd 还不应展示。", nil),
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("快速第一轮已作废，我会合并预算和交期重新回答。", nil),
	}), nil
}

func (m *fastSalesModel) userInputs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.inputs...)
}

var _ model.BaseChatModel = (*fastSalesModel)(nil)
