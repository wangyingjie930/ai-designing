package claudeautomemory

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// RunnerSessionConfig 装配一个可持久化、可 Compact 和可 Resume 的 Session 运行时。
type RunnerSessionConfig struct {
	SessionID       string
	TranscriptStore *TranscriptStore
	Scheduler       *SessionScheduler
	Compactor       *SessionCompactor
	Resume          bool
}

// Runner 固定 Recall -> Main Agent -> 双后台调度顺序，并分离完整 Transcript 与模型 Context。
type Runner struct {
	recaller  *Recaller
	chat      ChatAgent
	scheduler *ExtractionScheduler

	transcript       []ConversationMessage
	contextMessages  []ConversationMessage
	transcriptStore  *TranscriptStore
	sessionID        string
	sessionScheduler *SessionScheduler
	compactor        *SessionCompactor
	startupWarnings  []error

	mu sync.Mutex
}

// NewRunner 装配仅启用 Auto Memory 的兼容闭环。
func NewRunner(recaller *Recaller, chat ChatAgent, extractor *Extractor) (*Runner, error) {
	return newRunnerBase(recaller, chat, extractor)
}

// NewRunnerWithSession 装配完整双记忆闭环，并在 Resume 时恢复 Transcript 和摘要 Context。
func NewRunnerWithSession(ctx context.Context, recaller *Recaller, chat ChatAgent, extractor *Extractor, config RunnerSessionConfig) (*Runner, error) {
	runner, err := newRunnerBase(recaller, chat, extractor)
	if err != nil {
		return nil, err
	}
	if err := validateSessionID(config.SessionID); err != nil {
		return nil, err
	}
	if config.TranscriptStore == nil || config.Scheduler == nil || config.Compactor == nil {
		return nil, errors.New("transcript store, session scheduler and compactor are required")
	}
	transcript, err := config.TranscriptStore.Load(ctx)
	if err != nil {
		return nil, err
	}
	if config.Resume && len(transcript) == 0 {
		return nil, errors.New("cannot resume an empty session")
	}
	if !config.Resume && len(transcript) > 0 {
		return nil, errors.New("session already exists; use resume mode")
	}
	runner.transcriptStore = config.TranscriptStore
	runner.sessionID = config.SessionID
	runner.sessionScheduler = config.Scheduler
	runner.compactor = config.Compactor
	runner.transcript = append([]ConversationMessage(nil), transcript...)
	runner.contextMessages = append([]ConversationMessage(nil), transcript...)
	if config.Resume {
		// Auto Memory 的运行时游标从已持久化 Transcript 尾部继续，不重新抽取历史消息。
		extractor.ResumeAfter(transcript[len(transcript)-1].ID)
		compacted := config.Compactor.MaybeCompact(ctx, transcript, true)
		runner.contextMessages = compacted.Messages
		runner.startupWarnings = append(runner.startupWarnings, compacted.Warnings...)
	}
	return runner, nil
}

// newRunnerBase 只建立 Auto Memory 主链路，供兼容模式和 Session 模式共同复用。
func newRunnerBase(recaller *Recaller, chat ChatAgent, extractor *Extractor) (*Runner, error) {
	if recaller == nil {
		return nil, errors.New("memory recaller is required")
	}
	if chat == nil {
		return nil, errors.New("main chat agent is required")
	}
	if extractor == nil {
		return nil, errors.New("memory extractor is required")
	}
	scheduler, err := NewExtractionScheduler(extractor)
	if err != nil {
		return nil, err
	}
	return &Runner{recaller: recaller, chat: chat, scheduler: scheduler}, nil
}

// RunTurn 根据是否装配 Session Runtime 选择兼容路径或完整双通道路径。
func (r *Runner) RunTurn(ctx context.Context, userInput string) (TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return TurnResult{}, errors.New("user input is required")
	}
	if r.transcriptStore == nil {
		return r.runAutoMemoryTurn(ctx, userInput)
	}
	return r.runSessionTurn(ctx, userInput)
}

// runAutoMemoryTurn 保留原有行为：主回答成功后才提交完整历史给长期记忆。
func (r *Runner) runAutoMemoryTurn(ctx context.Context, userInput string) (TurnResult, error) {
	recall := r.recaller.Recall(ctx, userInput)
	pending := append([]ConversationMessage(nil), r.transcript...)
	pending = append(pending, NewConversationMessage(RoleUser, userInput))
	answer, err := r.chat.Generate(ctx, pending, recall.Context)
	if err != nil {
		return TurnResult{}, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return TurnResult{}, errors.New("main chat agent returned an empty answer")
	}
	r.transcript = append(pending, NewConversationMessage(RoleAssistant, answer))
	r.contextMessages = append([]ConversationMessage(nil), r.transcript...)
	r.scheduler.Schedule(ctx, r.transcript)
	return TurnResult{Answer: answer, Recalled: recall.Records, Warnings: append([]error(nil), recall.Warnings...)}, nil
}

// runSessionTurn 先持久化真实输入，再 Compact Context，成功回答后并行调度两套记忆。
func (r *Runner) runSessionTurn(ctx context.Context, userInput string) (TurnResult, error) {
	userMessage := NewConversationMessage(RoleUser, userInput)
	if err := r.transcriptStore.Append(ctx, userMessage); err != nil {
		return TurnResult{}, err
	}
	r.transcript = append(r.transcript, userMessage)
	pending := append([]ConversationMessage(nil), r.contextMessages...)
	pending = append(pending, userMessage)
	compactResult := r.compactor.MaybeCompact(ctx, pending, false)
	r.contextMessages = compactResult.Messages
	recall := r.recaller.Recall(ctx, userInput)
	answer, err := r.chat.Generate(ctx, r.contextMessages, recall.Context)
	if err != nil {
		return TurnResult{}, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return TurnResult{}, errors.New("main chat agent returned an empty answer")
	}
	assistantMessage := NewConversationMessage(RoleAssistant, answer)
	if err := r.transcriptStore.Append(ctx, assistantMessage); err != nil {
		return TurnResult{}, err
	}
	r.transcript = append(r.transcript, assistantMessage)
	r.contextMessages = append(r.contextMessages, assistantMessage)
	// Session Memory 维护当前任务状态；Auto Memory 独立提取跨会话稳定知识。
	r.sessionScheduler.Schedule(ctx, r.transcript)
	r.scheduler.Schedule(ctx, r.transcript)
	warnings := append([]error(nil), r.startupWarnings...)
	r.startupWarnings = nil
	warnings = append(warnings, compactResult.Warnings...)
	warnings = append(warnings, recall.Warnings...)
	return TurnResult{
		Answer: answer, Recalled: recall.Records, Compacted: compactResult.Compacted, Warnings: warnings,
	}, nil
}

// Drain 等待 Auto Memory 当前及 trailing 提取，保持原有生命周期契约。
func (r *Runner) Drain(ctx context.Context) (DrainResult, error) {
	return r.scheduler.Drain(ctx)
}

// WaitSession 只等待 Session Memory 更新，避免与长期记忆结果混成一个抽象。
func (r *Runner) WaitSession(ctx context.Context) (SessionDrainResult, error) {
	if r.sessionScheduler == nil {
		return SessionDrainResult{}, nil
	}
	return r.sessionScheduler.Wait(ctx)
}

// SessionID 返回当前持久化会话标识；Auto Memory-only Runner 返回空字符串。
func (r *Runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

// History 返回完整 Transcript 副本，保留旧调用方语义。
func (r *Runner) History() []ConversationMessage {
	return r.Transcript()
}

// Transcript 返回只追加事实日志的内存副本，不包含 Compact Summary。
func (r *Runner) Transcript() []ConversationMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConversationMessage(nil), r.transcript...)
}

// ContextMessages 返回当前发给主模型的可压缩消息视图。
func (r *Runner) ContextMessages() []ConversationMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConversationMessage(nil), r.contextMessages...)
}
