package claudecompaction

import "fmt"

// HistorySnipConfig 控制 HISTORY_SNIP 在 microcompact 前裁掉旧历史的投影视图。
type HistorySnipConfig struct {
	Enabled            bool
	MaxTokens          int
	KeepRecentMessages int
}

// HistorySnipResult 记录 snip 后的消息视图、边界消息和释放的 token。
type HistorySnipResult struct {
	Messages          []Message
	Changed           bool
	BoundaryMessage   *Message
	TokensFreed       int
	RemovedMessageIDs []string
}

// HistorySnip 在上下文过大时保留最近尾部，并用 snip boundary 取代较早历史。
func HistorySnip(messages []Message, config HistorySnipConfig) HistorySnipResult {
	result := HistorySnipResult{Messages: cloneMessages(messages)}
	if !config.Enabled || config.MaxTokens <= 0 {
		return result
	}

	total := totalTokens(messages)
	if total <= config.MaxTokens {
		return result
	}
	keepRecent := config.KeepRecentMessages
	if keepRecent <= 0 {
		keepRecent = 4
	}
	start := len(messages) - keepRecent
	if start <= 0 {
		return result
	}
	start = adjustSnipStartForToolPair(messages, start)
	if start <= 0 {
		return result
	}

	removed := messages[:start]
	kept := cloneMessages(messages[start:])
	removedTokens := totalTokens(removed)
	boundary := HistorySnipBoundary(len(removed), removedTokens)
	next := append([]Message{boundary}, kept...)
	tokensFreed := removedTokens - boundary.TokenCount()
	if tokensFreed < 0 {
		tokensFreed = 0
	}

	return HistorySnipResult{
		Messages:          next,
		Changed:           true,
		BoundaryMessage:   cloneMessagePtr(boundary),
		TokensFreed:       tokensFreed,
		RemovedMessageIDs: messageIDs(removed),
	}
}

// HistorySnipBoundary 构造 HISTORY_SNIP 边界；后续 resume 可从这里之后恢复。
func HistorySnipBoundary(removedMessages int, removedTokens int) Message {
	content := fmt.Sprintf("History snipped: removed %d older messages and approximately %d tokens.", removedMessages, removedTokens)
	msg := TextMessage(
		fmt.Sprintf("history-snip-boundary-%d-%d", removedMessages, removedTokens),
		RoleSystem,
		content,
		WithTokens(EstimateTokens(content)),
	)
	msg.Subtype = SubtypeHistorySnipBoundary
	msg.IsMeta = true
	return msg
}

// adjustSnipStartForToolPair 避免把最近保留区切在 tool_use 和 tool_result 中间。
func adjustSnipStartForToolPair(messages []Message, start int) int {
	if start <= 0 || start >= len(messages) {
		return start
	}
	current := messages[start]
	previous := messages[start-1]
	if current.Subtype == SubtypeToolResult && previous.Subtype == SubtypeToolUse && current.ToolUseID == previous.ToolUseID {
		return start - 1
	}
	return start
}

// cloneMessagePtr 复制单条消息并返回指针，避免结果被调用方修改。
func cloneMessagePtr(message Message) *Message {
	cloned := message.Clone()
	return &cloned
}

// messageIDs 提取消息 ID，便于测试和外部记录 snip 删除范围。
func messageIDs(messages []Message) []string {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.ID != "" {
			ids = append(ids, msg.ID)
		}
	}
	return ids
}
