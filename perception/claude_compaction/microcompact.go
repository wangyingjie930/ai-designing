package claudecompaction

// ClearedToolResultContent 是 microcompact 清理旧工具结果后的占位文本。
const ClearedToolResultContent = "[Old tool result content cleared]"

var compactableTools = map[string]bool{
	"Read":      true,
	"Bash":      true,
	"Grep":      true,
	"Glob":      true,
	"WebSearch": true,
	"WebFetch":  true,
	"Edit":      true,
	"Write":     true,
}

// MicrocompactConfig 控制只清理工具结果的轻量压缩。
type MicrocompactConfig struct {
	KeepRecentToolResults int
}

// MicrocompactResult 记录 microcompact 后的消息和节省估算。
type MicrocompactResult struct {
	Messages          []Message
	TokensSaved       int
	ClearedToolUseIDs []string
}

// Microcompact 清理旧工具结果，不触发语义摘要模型。
func Microcompact(messages []Message, config MicrocompactConfig) MicrocompactResult {
	keepRecent := config.KeepRecentToolResults
	if keepRecent <= 0 {
		keepRecent = 1
	}
	toolNames := collectToolUseNames(messages)
	compactableIDs := collectCompactableToolResultIDs(messages, toolNames)
	if len(compactableIDs) <= keepRecent {
		return MicrocompactResult{Messages: cloneMessages(messages)}
	}

	keep := make(map[string]bool, keepRecent)
	for _, id := range compactableIDs[len(compactableIDs)-keepRecent:] {
		keep[id] = true
	}
	clear := make(map[string]bool, len(compactableIDs)-keepRecent)
	for _, id := range compactableIDs[:len(compactableIDs)-keepRecent] {
		clear[id] = true
	}

	result := cloneMessages(messages)
	var saved int
	var cleared []string
	for i, msg := range result {
		if msg.Subtype != SubtypeToolResult || !clear[msg.ToolUseID] {
			continue
		}
		if msg.Content == ClearedToolResultContent {
			continue
		}
		saved += msg.TokenCount() - EstimateTokens(ClearedToolResultContent)
		result[i].Content = ClearedToolResultContent
		result[i].Tokens = EstimateTokens(ClearedToolResultContent)
		cleared = append(cleared, msg.ToolUseID)
	}
	if saved < 0 {
		saved = 0
	}
	return MicrocompactResult{
		Messages:          result,
		TokensSaved:       saved,
		ClearedToolUseIDs: cleared,
	}
}

// collectToolUseNames 建立 tool_use_id 到工具名称的映射。
func collectToolUseNames(messages []Message) map[string]string {
	names := make(map[string]string)
	for _, msg := range messages {
		if msg.Subtype == SubtypeToolUse && msg.ToolUseID != "" {
			names[msg.ToolUseID] = msg.ToolName
		}
	}
	return names
}

// collectCompactableToolResultIDs 按出现顺序收集可轻量压缩的工具结果。
func collectCompactableToolResultIDs(messages []Message, toolNames map[string]string) []string {
	var ids []string
	for _, msg := range messages {
		if msg.Subtype != SubtypeToolResult {
			continue
		}
		if compactableTools[toolNames[msg.ToolUseID]] {
			ids = append(ids, msg.ToolUseID)
		}
	}
	return ids
}
