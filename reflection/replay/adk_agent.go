package replay

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ADKAgent 把 ExperienceReplay 包装成 Eino ADK Agent，便于 Runner 调度。
type ADKAgent struct {
	core *ExperienceReplay
}

// NewADKAgent 创建 ADK 包装层，核心经验状态仍由 ExperienceReplay 持有。
func NewADKAgent(core *ExperienceReplay) (*ADKAgent, error) {
	if core == nil {
		return nil, errors.New("experience replay is required")
	}
	return &ADKAgent{core: core}, nil
}

// NewRunner 直接创建 ExperienceReplay 和 Eino ADK Runner，适合 cmd 或业务入口复用。
func NewRunner(ctx context.Context, config Config) (*adk.Runner, *ExperienceReplay, error) {
	core, err := NewExperienceReplay(ctx, config)
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

// Run 实现 Eino ADK Agent 接口，把输入消息交给 replay advisor 并发出最终 assistant message。
func (a *ADKAgent) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		if a == nil || a.core == nil {
			gen.Send(&adk.AgentEvent{Err: errors.New("experience replay is required")})
			return
		}
		var messages []*schema.Message
		if input != nil {
			messages = input.Messages
		}
		response, err := a.core.Advise(ctx, Request{Messages: messages})
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
