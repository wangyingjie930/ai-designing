package claudeautomemory

import (
	"context"
	"errors"
	"fmt"
)

// SessionCompactor 用最新 Session Summary 替换已摘要旧消息，但从不修改完整 Transcript。
type SessionCompactor struct {
	store     *SessionStore
	scheduler *SessionScheduler
	estimator TokenEstimator
	config    SessionMemoryConfig
}

// NewSessionCompactor 创建带等待上限、UUID 边界验证和最近消息保留策略的压缩器。
func NewSessionCompactor(store *SessionStore, scheduler *SessionScheduler, estimator TokenEstimator, config SessionMemoryConfig) (*SessionCompactor, error) {
	if store == nil {
		return nil, errors.New("session store is required")
	}
	if scheduler == nil {
		return nil, errors.New("session scheduler is required")
	}
	if estimator == nil {
		return nil, errors.New("token estimator is required")
	}
	if err := validateSessionMemoryConfig(config); err != nil {
		return nil, err
	}
	return &SessionCompactor{store: store, scheduler: scheduler, estimator: estimator, config: config}, nil
}

// MaybeCompact 在达到阈值或 Resume 时构造 Summary + 最近消息的 Context View。
func (c *SessionCompactor) MaybeCompact(ctx context.Context, messages []ConversationMessage, resume bool) CompactResult {
	original := append([]ConversationMessage(nil), messages...)
	result := CompactResult{Messages: original, Before: len(messages), After: len(messages)}
	if len(messages) == 0 {
		return result
	}
	if !resume && c.estimator.Estimate(messages) < c.config.CompactTokens {
		return result
	}
	// Compact 只在消费边界等待生产者，普通回答路径始终保持异步。
	waitCtx, cancel := context.WithTimeout(ctx, c.config.ExtractionWaitTimeout)
	_, waitErr := c.scheduler.Wait(waitCtx)
	cancel()
	if waitErr != nil {
		result.Warnings = append(result.Warnings, fmt.Errorf("wait for session memory update: %w", waitErr))
	}
	snapshot, err := c.store.Load(ctx)
	if err != nil {
		result.Warnings = append(result.Warnings, err)
		return result
	}
	if !snapshot.State.Initialized || c.store.IsEmptySummary(snapshot.Summary) {
		return result
	}
	normalMessages := filterNormalMessages(messages)
	if len(normalMessages) == 0 {
		return result
	}
	start, found := findMessageAfterBoundary(normalMessages, snapshot.State.LastSummarizedMessageID)
	hadCompactSummary := len(messages) > 0 && messages[0].Kind == MessageKindCompactSummary
	if !found {
		if !resume && !hadCompactSummary {
			result.Warnings = append(result.Warnings,
				fmt.Errorf("last summarized message %q is missing from context", snapshot.State.LastSummarizedMessageID))
			return result
		}
		// Resume 或二次 Compact 的 Context 可能已经不含旧边界，只保留安全尾部。
		start = recentMessagesStart(normalMessages, c.config.MinimumRecentMessages)
	}
	recentStart := recentMessagesStart(normalMessages, c.config.MinimumRecentMessages)
	if recentStart < start {
		start = recentStart
	}
	// 不从 assistant 开始截取一对自然问答，避免丢失它对应的用户问题。
	if start > 0 && start < len(normalMessages) && normalMessages[start].Role == RoleAssistant && normalMessages[start-1].Role == RoleUser {
		start--
	}
	summaryMessage := NewConversationMessage(RoleUser, "<session_memory>\n"+snapshot.Summary+"</session_memory>")
	summaryMessage.Kind = MessageKindCompactSummary
	compacted := make([]ConversationMessage, 0, 1+len(normalMessages)-start)
	compacted = append(compacted, summaryMessage)
	compacted = append(compacted, normalMessages[start:]...)
	result.Messages = compacted
	result.Compacted = true
	result.After = len(compacted)
	return result
}

// findMessageAfterBoundary 返回 UUID 边界后的起始下标。
func findMessageAfterBoundary(messages []ConversationMessage, boundary string) (int, bool) {
	if boundary == "" {
		return 0, false
	}
	for index, message := range messages {
		if message.ID == boundary {
			return index + 1, true
		}
	}
	return 0, false
}

// recentMessagesStart 计算至少保留多少条最近真实消息。
func recentMessagesStart(messages []ConversationMessage, minimum int) int {
	if len(messages) <= minimum {
		return 0
	}
	return len(messages) - minimum
}
