package claudetasklist

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestDefaultInstructionDefinesTaskProgressRules 验证固定中文 Prompt 覆盖创建、推进和恢复边界。
func TestDefaultInstructionDefinesTaskProgressRules(t *testing.T) {
	instruction := DefaultInstruction()
	if strings.Count(instruction, "[CLAUDE_TASK_AGENT]") != 1 {
		t.Fatalf("instruction marker count is not one: %q", instruction)
	}
	for _, want := range []string{
		"复杂、多步骤",
		"简单问答不创建",
		"先调用 TaskList",
		"TaskCreate",
		"in_progress",
		"completed",
		"未完成依赖",
		"不能让摘要或回答替代 Task JSON",
		"恢复",
		"复用已有任务",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction missing %q:\n%s", want, instruction)
		}
	}
}

// TestAgentRunsTaskToolLoop 验证真实 ADK ReAct 循环会依次执行任务工具并汇总最终回答。
func TestAgentRunsTaskToolLoop(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "agent-loop")
	if err != nil {
		t.Fatal(err)
	}
	fake := &taskAgentFakeModel{responses: []taskAgentFakeResponse{
		{message: taskAgentToolCall("list-1", TaskListToolName, `{}`)},
		{message: taskAgentToolCall("create-1", TaskCreateToolName, `{"subject":"实现任务循环","description":"使用真实 Eino ADK 执行四工具循环","activeForm":"正在实现任务循环"}`)},
		{message: taskAgentToolCall("update-1", TaskUpdateToolName, `{"taskId":"1","status":"in_progress"}`)},
		{message: schema.AssistantMessage("任务 1 已开始执行。", nil)},
	}}
	agent, err := NewAgent(ctx, AgentConfig{Model: fake, Store: store, MaxIterations: 8})
	if err != nil {
		t.Fatal(err)
	}

	result, err := agent.Run(ctx, []*schema.Message{schema.UserMessage("请跟踪并开始这个复杂任务")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "任务 1 已开始执行。" || result.ToolCallCount != 3 {
		t.Fatalf("result=%+v", result)
	}
	assertTaskAgentToolSchemas(t, fake.BoundTools())

	tasks, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "1" || tasks[0].Status != TaskStatusInProgress {
		t.Fatalf("tasks=%+v", tasks)
	}
}

// TestAgentSecondRunReusesExistingTask 验证同一 Agent 和 Store 的后续轮次只推进任务 1，不重复创建。
func TestAgentSecondRunReusesExistingTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "agent-resume")
	if err != nil {
		t.Fatal(err)
	}
	fake := &taskAgentFakeModel{responses: []taskAgentFakeResponse{
		{message: taskAgentToolCall("list-first", TaskListToolName, `{}`)},
		{message: taskAgentToolCall("create-first", TaskCreateToolName, `{"subject":"恢复任务","description":"验证跨轮复用同一任务"}`)},
		{message: taskAgentToolCall("start-first", TaskUpdateToolName, `{"taskId":"1","status":"in_progress"}`)},
		{message: schema.AssistantMessage("任务 1 已开始。", nil)},
		{message: taskAgentToolCall("list-second", TaskListToolName, `{}`)},
		{message: taskAgentToolCall("complete-second", TaskUpdateToolName, `{"taskId":"1","status":"completed"}`)},
		{message: schema.AssistantMessage("任务 1 已完成。", nil)},
	}}
	agent, err := NewAgent(ctx, AgentConfig{Model: fake, Store: store, MaxIterations: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Run(ctx, []*schema.Message{schema.UserMessage("开始任务")}); err != nil {
		t.Fatal(err)
	}

	result, err := agent.Run(ctx, []*schema.Message{
		schema.UserMessage("开始任务"),
		schema.AssistantMessage("任务 1 已开始。", nil),
		schema.UserMessage("继续并完成原任务"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "任务 1 已完成。" || result.ToolCallCount != 2 {
		t.Fatalf("second result=%+v", result)
	}
	tasks, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "1" || tasks[0].Status != TaskStatusCompleted {
		t.Fatalf("tasks=%+v", tasks)
	}
}

// TestAgentKeepsSuccessfulWritesWhenLaterEventFails 验证后续模型错误不会伪装成 Store 回滚。
func TestAgentKeepsSuccessfulWritesWhenLaterEventFails(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "agent-error")
	if err != nil {
		t.Fatal(err)
	}
	modelErr := errors.New("model unavailable")
	fake := &taskAgentFakeModel{responses: []taskAgentFakeResponse{
		{message: taskAgentToolCall("create-before-error", TaskCreateToolName, `{"subject":"保留写入","description":"后续错误不回滚已经成功的工具写入"}`)},
		{err: modelErr},
	}}
	agent, err := NewAgent(ctx, AgentConfig{Model: fake, Store: store, MaxIterations: 4})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := agent.Run(ctx, []*schema.Message{schema.UserMessage("创建任务后继续")}); !errors.Is(err, modelErr) {
		t.Fatalf("Run() error=%v want=%v", err, modelErr)
	}
	tasks, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "1" || tasks[0].Status != TaskStatusPending {
		t.Fatalf("successful write was not retained: %+v", tasks)
	}
}

// TestAgentReturnsExactErrorWithoutFinalAssistantText 验证只有工具轨迹而无最终正文时返回稳定错误。
func TestAgentReturnsExactErrorWithoutFinalAssistantText(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "agent-no-final")
	if err != nil {
		t.Fatal(err)
	}
	fake := &taskAgentFakeModel{responses: []taskAgentFakeResponse{
		{message: taskAgentToolCall("list-no-final", TaskListToolName, `{}`)},
		{message: schema.AssistantMessage("   ", nil)},
	}}
	agent, err := NewAgent(ctx, AgentConfig{Model: fake, Store: store, MaxIterations: 4})
	if err != nil {
		t.Fatal(err)
	}

	_, err = agent.Run(ctx, []*schema.Message{schema.UserMessage("查看任务")})
	if err == nil || err.Error() != "agent finished without assistant output" {
		t.Fatalf("Run() error=%v", err)
	}
}

// TestAgentRejectsEmptyMessages 验证调用方必须提供当前完整 Context View。
func TestAgentRejectsEmptyMessages(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "agent-empty")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(ctx, AgentConfig{Model: &taskAgentFakeModel{}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Run(ctx, nil); err == nil {
		t.Fatal("Run() should reject empty messages")
	}
}

// taskAgentFakeResponse 描述 fake 模型一次可控的消息或错误输出。
type taskAgentFakeResponse struct {
	message *schema.Message
	err     error
}

// taskAgentFakeModel 只替代外部模型生成边界，ADK Runner、工具调度和 Store 均使用真实实现。
type taskAgentFakeModel struct {
	mu        sync.Mutex
	responses []taskAgentFakeResponse
	tools     []*schema.ToolInfo
}

// Generate 记录模型实际收到的工具契约，并返回下一条预设 ReAct 消息。
func (m *taskAgentFakeModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	options := model.GetCommonOptions(nil, opts...)
	if len(options.Tools) > 0 {
		m.tools = append([]*schema.ToolInfo{}, options.Tools...)
	}
	if len(m.responses) == 0 {
		return schema.AssistantMessage("", nil), nil
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response.message, response.err
}

// Stream 返回单条预设输出以满足 Eino BaseChatModel；当前 Agent 使用非流式 Runner。
func (m *taskAgentFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", nil)}), nil
}

// BoundTools 返回模型调用时收到的工具 Schema 副本。
func (m *taskAgentFakeModel) BoundTools() []*schema.ToolInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*schema.ToolInfo{}, m.tools...)
}

// taskAgentToolCall 构造与真实模型响应结构一致的单个函数工具调用。
func taskAgentToolCall(id, name, arguments string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: id,
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}})
}

// assertTaskAgentToolSchemas 验证四个 Claude Task 工具全部真实传入模型。
func assertTaskAgentToolSchemas(t *testing.T, tools []*schema.ToolInfo) {
	t.Helper()
	if len(tools) != 4 {
		t.Fatalf("bound tools=%d want=4", len(tools))
	}
	wantNames := []string{TaskCreateToolName, TaskGetToolName, TaskUpdateToolName, TaskListToolName}
	for index, wantName := range wantNames {
		if tools[index] == nil || tools[index].Name != wantName || tools[index].ParamsOneOf == nil {
			t.Fatalf("tool[%d]=%+v want=%s with schema", index, tools[index], wantName)
		}
	}
}
