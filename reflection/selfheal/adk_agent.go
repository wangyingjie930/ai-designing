package selfheal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ADKAgent 把确定性自愈循环包装成 Eino ADK Agent，便于 Runner 调度和 callback 观测。
type ADKAgent struct {
	core *Loop
}

// NewADKAgent 创建自愈循环的 ADK 包装层。
func NewADKAgent(core *Loop) (*ADKAgent, error) {
	if core == nil {
		return nil, errors.New("self-heal loop is required")
	}
	return &ADKAgent{core: core}, nil
}

// NewRunner 创建自愈循环和 Eino ADK Runner，供 cmd 或业务入口复用。
func NewRunner(ctx context.Context, config Config) (*adk.Runner, *Loop, error) {
	core, err := NewLoop(config)
	if err != nil {
		return nil, nil, err
	}
	agent, err := NewADKAgent(core)
	if err != nil {
		return nil, nil, err
	}
	return adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}), core, nil
}

// Name 返回 ADK 框架展示和 callback 使用的 agent 名称。
func (a *ADKAgent) Name(context.Context) string {
	if a == nil || a.core == nil {
		return ""
	}
	return a.core.Name()
}

// Description 返回 ADK 框架展示用的 agent 描述。
func (a *ADKAgent) Description(context.Context) string {
	if a == nil || a.core == nil {
		return ""
	}
	return a.core.Description()
}

// Run 实现 Eino ADK Agent 接口，把输入消息解析为 FailureSignal 后交给核心状态机。
func (a *ADKAgent) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		if a == nil || a.core == nil {
			gen.Send(&adk.AgentEvent{Err: errors.New("self-heal loop is required")})
			return
		}
		var messages []*schema.Message
		if input != nil {
			messages = input.Messages
		}
		failure, err := failureFromMessages(messages)
		if err != nil {
			gen.Send(&adk.AgentEvent{AgentName: a.core.Name(), Err: err})
			return
		}
		response, err := a.core.Heal(ctx, failure)
		if err != nil {
			gen.Send(&adk.AgentEvent{AgentName: a.core.Name(), Err: err})
			return
		}
		event := adk.EventFromMessage(schema.AssistantMessage(response.UserMessage(), nil), nil, schema.Assistant, "")
		event.AgentName = a.core.Name()
		if event.Output != nil {
			event.Output.CustomizedOutput = response
		}
		gen.Send(event)
	}()
	return iter
}

// failureFromMessages 从 ADK 文本消息中解析 FailureSignal，兼容裸 JSON 和 initial_failure 包装。
func failureFromMessages(messages []*schema.Message) (FailureSignal, error) {
	text := strings.TrimSpace(messagesText(messages))
	if text == "" {
		return FailureSignal{}, errors.New("failure input is required")
	}
	var failure FailureSignal
	if err := json.Unmarshal([]byte(text), &failure); err == nil && failure.Kind != "" {
		return failure, nil
	}
	var wrapped struct {
		InitialFailure FailureSignal `json:"initial_failure"`
	}
	if err := json.Unmarshal([]byte(text), &wrapped); err == nil && wrapped.InitialFailure.Kind != "" {
		return wrapped.InitialFailure, nil
	}
	return FailureSignal{
		Kind:      "unstructured_failure",
		Severity:  1,
		ErrorText: text,
	}, nil
}

// messagesText 提取 ADK 普通文本消息，多模态消息只读取 text part。
func messagesText(messages []*schema.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		if strings.TrimSpace(message.Content) != "" {
			parts = append(parts, strings.TrimSpace(message.Content))
			continue
		}
		for _, part := range message.UserInputMultiContent {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
	}
	return strings.Join(parts, "\n")
}
