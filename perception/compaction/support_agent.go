package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// SupportAgentConfig 配置长会话客服 Agent 的模型、工具和压缩器。
type SupportAgentConfig struct {
	Model           model.BaseChatModel
	ToolsConfig     adk.ToolsConfig
	CompactorConfig Config
	Store           *InMemorySessionStore
	Instruction     string
	MaxIterations   int
}

// SupportRequest 表示一次客服用户输入。
type SupportRequest struct {
	Context SupportContext
	Message string
}

// SupportResponse 返回 ADK 生成结果和本轮压缩观测信息。
type SupportResponse struct {
	Message         string
	PromptView      PromptView
	CompactionEvent *CompactionEvent
	HandoffSummary  HandoffSummary
}

// SupportAgent 用 Eino ADK 执行客服对话，用 compactor 管理长历史。
type SupportAgent struct {
	runner      *adk.Runner
	store       *InMemorySessionStore
	instruction string
}

// SupportAgentCallbackInput 是客服 Agent 根 callback 的轻量输入。
type SupportAgentCallbackInput struct {
	SupportContext SupportContext
	MessageTokens  int
}

// SupportAgentCallbackOutput 是客服 Agent 根 callback 的轻量输出。
type SupportAgentCallbackOutput struct {
	AnswerTokens        int
	CompactionTriggered bool
	CompactionLevel     int
	HandoffRecommended  bool
	TokenPressure       TokenPressure
}

// NewSupportAgent 创建基于 Eino ADK ChatModelAgent 的长会话客服 Agent。
func NewSupportAgent(ctx context.Context, config SupportAgentConfig) (*SupportAgent, error) {
	if config.Model == nil {
		return nil, errors.New("support agent model is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	compactorConfig := config.CompactorConfig
	if compactorConfig.AnchorUpdater == nil {
		compactorConfig.AnchorUpdater = ModelAnchorUpdater{Model: config.Model}
	}
	store := config.Store
	if store == nil {
		store = NewInMemorySessionStore(compactorConfig)
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = defaultSupportInstruction()
	}
	maxIterations := config.MaxIterations
	if maxIterations == 0 {
		maxIterations = 12
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "long_session_support_agent",
		Description:   "Long-session customer support agent with semantic compaction and handoff readiness.",
		Instruction:   instruction,
		Model:         config.Model,
		ToolsConfig:   config.ToolsConfig,
		MaxIterations: maxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model agent: %w", err)
	}
	return &SupportAgent{
		runner:      adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}),
		store:       store,
		instruction: instruction,
	}, nil
}

// Query 通过 prompt middleware 准备历史上下文、调用 ADK Runner，并把本轮轨迹写回 session。
func (a *SupportAgent) Query(ctx context.Context, req SupportRequest) (*SupportResponse, error) {
	if strings.TrimSpace(req.Message) == "" {
		return nil, errors.New("support request message is empty")
	}
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "support_agent_query",
		Type:      "SupportAgent",
		Component: components.Component("SupportAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, SupportAgentCallbackInput{
		SupportContext: req.Context,
		MessageTokens:  EstimateTokens(req.Message),
	})

	session := a.store.GetOrCreate(req.Context)
	view, event, err := session.BuildPromptView(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return nil, err
	}
	messages := []*schema.Message{
		schema.SystemMessage(view.SystemPrompt("")),
		schema.UserMessage(req.Message),
	}
	iter := a.runner.Run(callbackCtx, messages)
	answer, generatedTurns, err := collectAgentTurns(iter)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return nil, err
	}
	if strings.TrimSpace(answer) == "" {
		err := errors.New("support agent returned empty answer")
		callbacks.OnError(callbackCtx, err)
		return nil, err
	}

	session.AppendTurn(NewTurn(RoleUser, TurnKindMessage, req.Message))
	for _, turn := range generatedTurns {
		session.AppendTurn(turn)
	}
	session.AppendTurn(NewTurn(RoleAssistant, TurnKindMessage, answer))

	callbacks.OnEnd(callbackCtx, supportAgentCallbackOutput(answer, event, view))
	return &SupportResponse{
		Message:         answer,
		PromptView:      view,
		CompactionEvent: event,
		HandoffSummary:  view.HandoffSummary,
	}, nil
}

// supportAgentCallbackOutput 只上报根节点摘要，完整 prompt 继续由子节点负责。
func supportAgentCallbackOutput(answer string, event *CompactionEvent, view PromptView) SupportAgentCallbackOutput {
	output := SupportAgentCallbackOutput{
		AnswerTokens:        EstimateTokens(answer),
		CompactionTriggered: event != nil,
		HandoffRecommended:  view.HandoffRecommended,
		TokenPressure:       view.TokenPressure,
	}
	if event != nil {
		output.CompactionLevel = event.Level
	}
	return output
}

// AddTurn 手动写入外部工具观察、人工备注或历史回放记录。
func (a *SupportAgent) AddTurn(ctx SupportContext, turn Turn) {
	a.store.GetOrCreate(ctx).AppendTurn(turn)
}

// SessionTurns 返回某个客服 session 的历史副本。
func (a *SupportAgent) SessionTurns(ctx SupportContext) []Turn {
	return a.store.GetOrCreate(ctx).Turns()
}

// InMemorySessionStore 是示例实现使用的进程内 session 存储。
type InMemorySessionStore struct {
	mu              sync.Mutex
	compactorConfig Config
	sessions        map[string]*SupportSession
}

// NewInMemorySessionStore 创建进程内客服 session 存储。
func NewInMemorySessionStore(compactorConfig Config) *InMemorySessionStore {
	return &InMemorySessionStore{
		compactorConfig: compactorConfig,
		sessions:        make(map[string]*SupportSession),
	}
}

// GetOrCreate 获取或创建工单对应的 session。
func (s *InMemorySessionStore) GetOrCreate(ctx SupportContext) *SupportSession {
	key := ctx.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[key]; ok {
		return session
	}
	session := &SupportSession{
		context:          ctx,
		promptMiddleware: NewTokenLimitCompactionMiddleware(s.compactorConfig),
	}
	s.sessions[key] = session
	return session
}

// SupportSession 保存单个工单的历史和 prompt 中间件状态。
type SupportSession struct {
	mu               sync.Mutex
	context          SupportContext
	turns            []Turn
	promptMiddleware PromptViewMiddleware
}

// AppendTurn 追加一条 session 历史。
func (s *SupportSession) AppendTurn(turn Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, turn)
}

// Turns 返回 session 历史副本。
func (s *SupportSession) Turns() []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTurns(s.turns)
}

// BuildPromptView 生成当前 session 的模型 prompt 视图。
func (s *SupportSession) BuildPromptView(ctx context.Context) (PromptView, *CompactionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptMiddleware.BuildPromptView(ctx, s.context, s.turns)
}

// collectAgentTurns 从 ADK 事件流中提取最终回答和工具轨迹。
func collectAgentTurns(iter *adk.AsyncIterator[*adk.AgentEvent]) (string, []Turn, error) {
	var answer string
	var turns []Turn
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", nil, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return "", nil, err
		}
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.Assistant:
			if len(msg.ToolCalls) > 0 {
				turns = append(turns, toolCallActionTurn(msg))
				continue
			}
			if msg.Content != "" {
				answer = msg.Content
			}
		case schema.Tool:
			turns = append(turns, toolObservationTurn(msg))
		}
	}
	return answer, turns, nil
}

// toolCallActionTurn 把模型工具调用意图保存为 Action。
func toolCallActionTurn(msg *schema.Message) Turn {
	var parts []string
	for _, call := range msg.ToolCalls {
		parts = append(parts, fmt.Sprintf("%s(%s)", call.Function.Name, trimForPrompt(call.Function.Arguments, 240)))
	}
	return NewTurn(RoleAssistant, TurnKindAction, "called tools: "+strings.Join(parts, " | "))
}

// toolObservationTurn 把工具结果保存为 Observation，并自动保护错误输出。
func toolObservationTurn(msg *schema.Message) Turn {
	isError := isSupportError(msg.Content, defaultSupportErrorPatterns())
	return NewTurn(RoleToolResult, TurnKindObservation, msg.Content, WithHandle(msg.ToolName), WithError(isError))
}

// defaultSupportInstruction 返回客服 Agent 的基础行为约束。
func defaultSupportInstruction() string {
	return strings.TrimSpace(`
你负责处理长会话客服工单。
要求：
1. 先确认客户问题、影响范围、严重等级和 SLA 风险。
2. 使用工具结果时，要区分 Action、Observation、Error 和 Decision。
3. 不要重复已经排除的方案；如果需要重试，必须说明新证据是什么。
4. P1 或 SLA 临近时，输出可交接给二线/人工的下一步。
5. 回答用户时保持简洁、明确，并说明你下一步会做什么。`)
}
