package claudeautomemory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

// RoughTokenEstimator 用稳定 rune 估算替代供应商专属 tokenizer，便于本地演示和测试。
type RoughTokenEstimator struct{}

// Estimate 估算消息正文和角色包装成本，阈值只依赖当前 Context 大小而非累计用量。
func (RoughTokenEstimator) Estimate(messages []ConversationMessage) int {
	total := 0
	for _, message := range messages {
		total += (utf8.RuneCountInString(message.Content)+3)/4 + 4
	}
	return total
}

// SessionMemoryUpdater 按 Claude Code 风格阈值增量维护当前 Session 的结构化摘要。
type SessionMemoryUpdater struct {
	store      *SessionStore
	summarizer SessionSummarizer
	estimator  TokenEstimator
	config     SessionMemoryConfig
	mu         sync.Mutex
}

// NewSessionMemoryUpdater 创建失败不推进边界的 Session 摘要生产者。
func NewSessionMemoryUpdater(store *SessionStore, summarizer SessionSummarizer, estimator TokenEstimator, config SessionMemoryConfig) (*SessionMemoryUpdater, error) {
	if store == nil {
		return nil, errors.New("session store is required")
	}
	if summarizer == nil {
		return nil, errors.New("session summarizer is required")
	}
	if estimator == nil {
		return nil, errors.New("token estimator is required")
	}
	if err := validateSessionMemoryConfig(config); err != nil {
		return nil, err
	}
	return &SessionMemoryUpdater{store: store, summarizer: summarizer, estimator: estimator, config: config}, nil
}

// Update 在完整 Transcript 上判断阈值，只把 UUID 边界之后的真实消息交给摘要模型。
func (u *SessionMemoryUpdater) Update(ctx context.Context, transcript []ConversationMessage) SessionUpdateResult {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SessionUpdateResult{Warnings: []error{err}}
	}
	snapshot, err := u.store.Load(ctx)
	if err != nil {
		return SessionUpdateResult{Warnings: []error{err}}
	}
	normalMessages := filterNormalMessages(transcript)
	if len(normalMessages) == 0 {
		return SessionUpdateResult{}
	}
	newMessages, err := sessionMessagesAfterBoundary(normalMessages, snapshot.State.LastSummarizedMessageID)
	if err != nil {
		return SessionUpdateResult{Warnings: []error{err}}
	}
	if len(newMessages) == 0 {
		return SessionUpdateResult{}
	}
	currentTokens := u.estimator.Estimate(normalMessages)
	if !u.shouldUpdate(snapshot.State, currentTokens, newMessages) {
		return SessionUpdateResult{}
	}
	updatedSummary, err := u.summarizer.Summarize(ctx, snapshot.Summary, newMessages)
	if err != nil {
		return SessionUpdateResult{Warnings: []error{err}}
	}
	lastMessageID := normalMessages[len(normalMessages)-1].ID
	state := SessionState{
		LastSummarizedMessageID: lastMessageID,
		TokensAtLastUpdate:      currentTokens,
		Initialized:             true,
	}
	if err := u.store.Commit(ctx, updatedSummary, state); err != nil {
		return SessionUpdateResult{Warnings: []error{err}}
	}
	return SessionUpdateResult{
		Updated: true, SummarizedThrough: lastMessageID, ProcessedMessages: len(newMessages),
	}
}

// shouldUpdate 强制 token 增长门槛，并在工具批次完成或自然对话停顿时触发。
func (u *SessionMemoryUpdater) shouldUpdate(state SessionState, currentTokens int, newMessages []ConversationMessage) bool {
	if !state.Initialized {
		if currentTokens < u.config.MinimumTokensToInit {
			return false
		}
	} else if currentTokens-state.TokensAtLastUpdate < u.config.MinimumTokensBetweenUpdates {
		return false
	}
	toolCalls := 0
	for _, message := range newMessages {
		toolCalls += message.ToolCallCount
	}
	return toolCalls >= u.config.ToolCallsBetweenUpdates || isNaturalSessionBoundary(newMessages)
}

// filterNormalMessages 复制真实对话，确保 Compact Summary 永远不会进入摘要增量或长期记忆。
func filterNormalMessages(messages []ConversationMessage) []ConversationMessage {
	filtered := make([]ConversationMessage, 0, len(messages))
	for _, message := range messages {
		if message.Kind != "" && message.Kind != MessageKindNormal {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

// sessionMessagesAfterBoundary 返回已摘要 UUID 之后的真实消息，边界丢失时拒绝猜测。
func sessionMessagesAfterBoundary(messages []ConversationMessage, boundary string) ([]ConversationMessage, error) {
	if boundary == "" {
		return append([]ConversationMessage(nil), messages...), nil
	}
	for index, message := range messages {
		if message.ID == boundary {
			return append([]ConversationMessage(nil), messages[index+1:]...), nil
		}
	}
	return nil, fmt.Errorf("last summarized message %q is missing from transcript", boundary)
}

// isNaturalSessionBoundary 只在 assistant 完成且没有未结束工具调用时把当前状态固化为摘要。
func isNaturalSessionBoundary(messages []ConversationMessage) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	return last.Role == RoleAssistant && last.ToolCallCount == 0
}

// validateSessionMemoryConfig 拒绝会导致忙循环或无法保留上下文的阈值。
func validateSessionMemoryConfig(config SessionMemoryConfig) error {
	if config.MinimumTokensToInit <= 0 || config.MinimumTokensBetweenUpdates <= 0 || config.ToolCallsBetweenUpdates <= 0 {
		return errors.New("session update thresholds must be positive")
	}
	if config.CompactTokens <= 0 || config.MinimumRecentMessages <= 0 || config.ExtractionWaitTimeout <= 0 {
		return errors.New("session compact configuration must be positive")
	}
	return nil
}
