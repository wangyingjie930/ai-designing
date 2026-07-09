package claudecompaction

import (
	"context"
	"sync"
)

// TaskBudget 记录长任务剩余上下文预算，模拟 Claude Code 在压缩后更新任务预算的动作。
type TaskBudget struct {
	Total     int
	Remaining int
}

// QueryPipelineConfig 描述 query 主链上压缩相关的可插拔策略。
type QueryPipelineConfig struct {
	Compactor         *Compactor
	CompactorConfig   Config
	SkillState        *SkillState
	ToolResultBudget  *ToolResultBudgetConfig
	HistorySnip       *HistorySnipConfig
	AutoCompactPolicy AutoCompactPolicy
	Microcompact      *MicrocompactConfig
}

// QueryPipelineRequest 是一次进入模型前的上下文准备请求。
type QueryPipelineRequest struct {
	Messages          []Message
	SummaryModel      SummaryModel
	AgentID           string
	Source            QuerySource
	Trigger           Trigger
	SuppressFollowUp  bool
	TranscriptPath    string
	MessagesToKeep    []Message
	RestoreReferences RestoreReferences
	SessionMemory     *SessionMemory
	TaskBudget        *TaskBudget
}

// QueryPipelineResult 暴露 query 循环需要继续向下传递和写回 session 的消息视图。
type QueryPipelineResult struct {
	MessagesForQuery  []Message
	YieldedMessages   []Message
	WritebackMessages []Message
	ToolResultBudget  ToolResultBudgetResult
	HistorySnip       HistorySnipResult
	Microcompact      MicrocompactResult
	AutoCompact       AutoCompactDecision
	Compaction        *CompactResult
	TaskBudget        *TaskBudget
}

// QueryPipeline 还原 Claude Code query 主链里“进入模型前”的压缩编排。
type QueryPipeline struct {
	compactor        *Compactor
	skillState       *SkillState
	policy           AutoCompactPolicy
	toolResultBudget *ToolResultBudgetConfig
	historySnip      *HistorySnipConfig
	microcompact     *MicrocompactConfig
}

// NewQueryPipeline 创建 query 压缩流水线；未传 compactor 时使用默认 Compactor。
func NewQueryPipeline(config QueryPipelineConfig) *QueryPipeline {
	compactor := config.Compactor
	if compactor == nil {
		compactor = NewCompactor(config.CompactorConfig)
	}
	return &QueryPipeline{
		compactor:        compactor,
		skillState:       config.SkillState,
		policy:           config.AutoCompactPolicy,
		toolResultBudget: config.ToolResultBudget,
		historySnip:      config.HistorySnip,
		microcompact:     config.Microcompact,
	}
}

// Run 按 Claude Code 的主链顺序准备本轮模型上下文，并在自动压缩后替换 messagesForQuery。
func (p *QueryPipeline) Run(ctx context.Context, req QueryPipelineRequest) (QueryPipelineResult, error) {
	if p == nil {
		p = NewQueryPipeline(QueryPipelineConfig{})
	}
	source := req.Source
	if source == "" {
		source = QuerySourceMain
	}

	messagesForQuery := MessagesAfterLastCompactBoundary(req.Messages)
	toolResultBudgetResult := ToolResultBudgetResult{Messages: cloneMessages(messagesForQuery)}
	if p.toolResultBudget != nil {
		toolResultBudgetResult = ApplyToolResultBudget(messagesForQuery, *p.toolResultBudget)
		messagesForQuery = toolResultBudgetResult.Messages
	}

	var yieldedMessages []Message
	var writebackMessages []Message
	historySnipResult := HistorySnipResult{Messages: cloneMessages(messagesForQuery)}
	if p.historySnip != nil {
		historySnipResult = HistorySnip(messagesForQuery, *p.historySnip)
		messagesForQuery = historySnipResult.Messages
		if historySnipResult.BoundaryMessage != nil {
			yieldedMessages = append(yieldedMessages, historySnipResult.BoundaryMessage.Clone())
			writebackMessages = append(writebackMessages, historySnipResult.BoundaryMessage.Clone())
		}
	}

	microcompactResult := MicrocompactResult{Messages: cloneMessages(messagesForQuery)}
	if p.microcompact != nil {
		microcompactResult = Microcompact(messagesForQuery, *p.microcompact)
		messagesForQuery = microcompactResult.Messages
	}

	decision := p.policy.Decide(messagesForQuery, source)
	result := QueryPipelineResult{
		MessagesForQuery:  cloneMessages(messagesForQuery),
		YieldedMessages:   cloneMessages(yieldedMessages),
		WritebackMessages: cloneMessages(writebackMessages),
		ToolResultBudget:  toolResultBudgetResult,
		HistorySnip:       historySnipResult,
		Microcompact:      microcompactResult,
		AutoCompact:       decision,
		TaskBudget:        cloneTaskBudget(req.TaskBudget),
	}
	if !decision.ShouldCompact {
		return result, nil
	}

	compactResult, err := p.compactor.Compact(ctx, CompactRequest{
		Messages:          messagesForQuery,
		SummaryModel:      req.SummaryModel,
		AgentID:           req.AgentID,
		Trigger:           defaultTrigger(req.Trigger),
		SuppressFollowUp:  req.SuppressFollowUp,
		TranscriptPath:    req.TranscriptPath,
		MessagesToKeep:    req.MessagesToKeep,
		RestoreReferences: req.RestoreReferences,
		SessionMemory:     req.SessionMemory,
		SkillState:        p.skillState,
	})
	if err != nil {
		return QueryPipelineResult{}, err
	}

	compaction := compactResult
	postCompactMessages := cloneMessages(compactResult.Messages)
	result.MessagesForQuery = cloneMessages(postCompactMessages)
	result.YieldedMessages = append(cloneMessages(yieldedMessages), cloneMessages(postCompactMessages)...)
	result.WritebackMessages = append(cloneMessages(writebackMessages), cloneMessages(postCompactMessages)...)
	result.Compaction = &compaction
	result.TaskBudget = consumeTaskBudget(req.TaskBudget, decision.TotalTokens)
	return result, nil
}

// QuerySession 保存一条可恢复的会话历史，用于演示 compact boundary 写回后的 resume 语义。
type QuerySession struct {
	mu         sync.Mutex
	messages   []Message
	skillState *SkillState
}

// NewQuerySession 创建内存 session，并复制初始历史以隔离外部修改。
func NewQuerySession(messages []Message) *QuerySession {
	return &QuerySession{messages: cloneMessages(messages)}
}

// NewQuerySessionWithSkillState 创建带 skill 状态恢复的内存 session，用于模拟 resume 后继续 compact。
func NewQuerySessionWithSkillState(messages []Message, skillState *SkillState) *QuerySession {
	session := &QuerySession{
		messages:   cloneMessages(messages),
		skillState: skillState,
	}
	if skillState != nil {
		skillState.RestoreFromMessages(messages, "")
	}
	return session
}

// Messages 返回当前 session 历史副本。
func (s *QuerySession) Messages() []Message {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMessages(s.messages)
}

// Append 追加普通 query 产生的新消息。
func (s *QuerySession) Append(messages ...Message) {
	if s == nil || len(messages) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, cloneMessages(messages)...)
}

// PrepareNextQuery 使用 session 历史运行 pipeline，并把 compact 产生的 boundary/summary 写回。
func (s *QuerySession) PrepareNextQuery(ctx context.Context, pipeline *QueryPipeline, req QueryPipelineRequest) (QueryPipelineResult, error) {
	if s == nil {
		if pipeline == nil {
			pipeline = NewQueryPipeline(QueryPipelineConfig{})
		}
		return pipeline.Run(ctx, req)
	}

	s.mu.Lock()
	req.Messages = cloneMessages(s.messages)
	s.mu.Unlock()

	if pipeline == nil {
		pipeline = NewQueryPipeline(QueryPipelineConfig{})
	}
	result, err := pipeline.Run(ctx, req)
	if err != nil {
		return QueryPipelineResult{}, err
	}
	if len(result.WritebackMessages) > 0 {
		s.Append(result.WritebackMessages...)
	}
	if pipeline.skillState != nil {
		pipeline.skillState.RestoreFromMessages(result.WritebackMessages, req.AgentID)
	}
	if s.skillState != nil && s.skillState != pipeline.skillState {
		s.skillState.RestoreFromMessages(result.WritebackMessages, req.AgentID)
	}
	return result, nil
}

// defaultTrigger 补齐 query pipeline 里自动压缩的默认触发来源。
func defaultTrigger(trigger Trigger) Trigger {
	if trigger == "" {
		return TriggerAuto
	}
	return trigger
}

// cloneTaskBudget 复制任务预算，避免调用方在结果外部被意外改写。
func cloneTaskBudget(in *TaskBudget) *TaskBudget {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// consumeTaskBudget 在发生自动压缩时扣除压缩前本轮上下文预算。
func consumeTaskBudget(in *TaskBudget, consumed int) *TaskBudget {
	if in == nil {
		return nil
	}
	out := *in
	if out.Remaining <= 0 {
		return &out
	}
	out.Remaining -= consumed
	if out.Remaining < 0 {
		out.Remaining = 0
	}
	return &out
}
