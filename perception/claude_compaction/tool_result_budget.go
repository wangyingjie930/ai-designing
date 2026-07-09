package claudecompaction

// ToolResultBudgetReplacementContent 是工具结果超预算后的模型可见占位文本。
const ToolResultBudgetReplacementContent = "[Tool result content replaced by budget; original content is retained outside the prompt]"

// ToolResultBudgetConfig 控制进入 microcompact 前的大工具结果预算裁剪。
type ToolResultBudgetConfig struct {
	MaxResultTokens int
	Replacement     string
	SkipToolNames   []string
}

// ToolResultReplacementRecord 记录一次工具结果替换，模拟 Claude Code 写 transcript 的 replacement record。
type ToolResultReplacementRecord struct {
	MessageID         string
	ToolUseID         string
	ToolName          string
	OriginalTokens    int
	ReplacementTokens int
}

// ToolResultBudgetResult 记录工具结果预算裁剪后的消息视图和统计信息。
type ToolResultBudgetResult struct {
	Messages           []Message
	TokensSaved        int
	ReplacedToolUseIDs []string
	Records            []ToolResultReplacementRecord
}

// ApplyToolResultBudget 在 microcompact 前替换超预算工具结果，避免旧大输出继续撑爆上下文。
func ApplyToolResultBudget(messages []Message, config ToolResultBudgetConfig) ToolResultBudgetResult {
	result := ToolResultBudgetResult{Messages: cloneMessages(messages)}
	if config.MaxResultTokens <= 0 {
		return result
	}

	replacement := config.Replacement
	if replacement == "" {
		replacement = ToolResultBudgetReplacementContent
	}
	replacementTokens := EstimateTokens(replacement)
	toolNames := collectToolUseNames(messages)
	skipTools := toolNameSet(config.SkipToolNames)

	for i, msg := range result.Messages {
		if msg.Subtype != SubtypeToolResult {
			continue
		}
		toolName := toolNames[msg.ToolUseID]
		if skipTools[toolName] || msg.TokenCount() <= config.MaxResultTokens {
			continue
		}
		originalTokens := msg.TokenCount()
		result.Messages[i].Content = replacement
		result.Messages[i].Tokens = replacementTokens
		result.TokensSaved += originalTokens - replacementTokens
		result.ReplacedToolUseIDs = append(result.ReplacedToolUseIDs, msg.ToolUseID)
		result.Records = append(result.Records, ToolResultReplacementRecord{
			MessageID:         msg.ID,
			ToolUseID:         msg.ToolUseID,
			ToolName:          toolName,
			OriginalTokens:    originalTokens,
			ReplacementTokens: replacementTokens,
		})
	}
	if result.TokensSaved < 0 {
		result.TokensSaved = 0
	}
	return result
}

// toolNameSet 把工具名列表转成集合，便于跳过特定工具结果。
func toolNameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			out[name] = true
		}
	}
	return out
}
