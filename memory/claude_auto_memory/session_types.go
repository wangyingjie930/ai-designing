package claudeautomemory

import (
	"context"
	"time"
)

// SessionMemoryConfig 控制后台摘要和 Context Compact 的生命周期阈值。
type SessionMemoryConfig struct {
	MinimumTokensToInit         int
	MinimumTokensBetweenUpdates int
	ToolCallsBetweenUpdates     int
	CompactTokens               int
	MinimumRecentMessages       int
	ExtractionWaitTimeout       time.Duration
}

// DefaultSessionMemoryConfig 返回接近 Claude Code 的生产默认阈值。
func DefaultSessionMemoryConfig() SessionMemoryConfig {
	return SessionMemoryConfig{
		MinimumTokensToInit:         10_000,
		MinimumTokensBetweenUpdates: 5_000,
		ToolCallsBetweenUpdates:     3,
		CompactTokens:               18_000,
		MinimumRecentMessages:       4,
		ExtractionWaitTimeout:       15 * time.Second,
	}
}

// SessionState 保存摘要覆盖边界和下一次阈值判断需要的稳定状态。
type SessionState struct {
	LastSummarizedMessageID string `json:"last_summarized_message_id"`
	TokensAtLastUpdate      int    `json:"tokens_at_last_update"`
	Initialized             bool   `json:"initialized"`
}

// SessionSnapshot 是 Summary 正文和边界状态的一致读取视图。
type SessionSnapshot struct {
	Summary string
	State   SessionState
}

// SessionSummarizer 把当前摘要和新增真实对话转换为完整的新摘要。
type SessionSummarizer interface {
	Summarize(ctx context.Context, currentSummary string, messages []ConversationMessage) (string, error)
}

// TokenEstimator 为阈值判断提供可替换、可测试的上下文 token 估算。
type TokenEstimator interface {
	Estimate(messages []ConversationMessage) int
}

// SessionUpdateResult 汇总一次后台摘要尝试，不把 Summary 正文暴露给 CLI trace。
type SessionUpdateResult struct {
	Updated           bool
	SummarizedThrough string
	ProcessedMessages int
	Warnings          []error
}

// SessionDrainResult 汇总 Wait 等待到的所有后台 Session 更新批次。
type SessionDrainResult struct {
	Batches           int
	Updates           int
	ProcessedMessages int
	SummarizedThrough string
	Warnings          []error
}
