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
