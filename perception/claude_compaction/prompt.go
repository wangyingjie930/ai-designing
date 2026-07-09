package claudecompaction

import (
	"regexp"
	"strings"
)

const noToolCompactPreamble = `关键约束：只输出文本，不要调用任何工具。

- 不要调用 Read、Bash、Grep、Glob、Edit、Write 或任何其他工具。
- 你已经拥有上方对话中足够的上下文。
- 工具调用会被拒绝，并会浪费本次压缩机会。
- 输出必须包含 <analysis> 草稿块和 <summary> 最终摘要块。

`

const baseCompactPrompt = `你的任务是为当前长会话生成一份可继续工作的详细摘要。摘要要特别关注用户的明确请求、你已经采取的动作、代码/文件路径、错误和修复、仍待完成的任务。

请在 <analysis> 中先按时间顺序检查对话，再在 <summary> 中输出这些章节：

1. 主要请求和意图：用户到底要解决什么问题。
2. 关键技术概念：涉及的模块、框架、设计模式、运行路径。
3. 文件和代码片段：读过、改过或需要继续关注的文件；必要时保留关键片段。
4. 错误和修复：遇到的错误、失败路线、用户纠正过的点。
5. 问题解决进展：已经确认的事实和仍在排查的点。
6. 用户消息：列出非工具结果的关键用户原话或意图变化。
7. 待办任务：用户明确要求但还没完成的事项。
8. 当前工作：压缩发生前最后正在做什么。
9. 下一步：如果任务未完成，给出紧贴最近请求的下一步。

请保证摘要精确、可恢复现场，避免泛泛而谈。`

var analysisBlockPattern = regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
var summaryBlockPattern = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)

// BuildCompactPrompt 构造中文压缩提示词，模拟 Claude Code 的“摘要专用模型调用”。
func BuildCompactPrompt(customInstructions string) string {
	prompt := noToolCompactPreamble + baseCompactPrompt
	if strings.TrimSpace(customInstructions) != "" {
		prompt += "\n\n额外压缩要求：\n" + strings.TrimSpace(customInstructions)
	}
	prompt += "\n\n提醒：不要调用工具，只输出文本。"
	return prompt
}

// FormatCompactSummary 去掉 analysis 草稿，并把 summary 标签转成普通上下文文本。
func FormatCompactSummary(summary string) string {
	formatted := analysisBlockPattern.ReplaceAllString(summary, "")
	if match := summaryBlockPattern.FindStringSubmatch(formatted); len(match) == 2 {
		formatted = summaryBlockPattern.ReplaceAllString(formatted, "Summary:\n"+strings.TrimSpace(match[1]))
	}
	formatted = strings.ReplaceAll(formatted, "\r\n", "\n")
	formatted = regexp.MustCompile(`\n{3,}`).ReplaceAllString(formatted, "\n\n")
	return strings.TrimSpace(formatted)
}

// CompactSummaryContent 包装模型摘要，形成压缩后继续会话的 synthetic user message。
func CompactSummaryContent(summary string, suppressFollowUp bool, transcriptPath string, recentPreserved bool) string {
	base := "本会话从一次上下文压缩后继续。下面的摘要覆盖压缩前较早部分的对话。\n\n" + FormatCompactSummary(summary)
	if strings.TrimSpace(transcriptPath) != "" {
		base += "\n\n如果需要压缩前的精确代码、错误输出或完整原文，请回查完整 transcript: " + transcriptPath
	}
	if recentPreserved {
		base += "\n\n最近未总结的消息已经按原文保留在摘要之后。"
	}
	if suppressFollowUp {
		base += "\n\n请直接从压缩前中断的位置继续，不要寒暄，不要复述摘要，也不要因为压缩而重新询问用户。"
	}
	return base
}

// PrepareMessagesForSummary 生成总结模型输入：切掉旧边界前消息，替换媒体，去掉会被代码补回的附件。
func PrepareMessagesForSummary(messages []Message) []Message {
	view := MessagesAfterLastCompactBoundary(messages)
	prepared := make([]Message, 0, len(view))
	for _, msg := range view {
		if msg.Subtype == SubtypeCompactBoundary {
			continue
		}
		if shouldStripBeforeSummary(msg) {
			continue
		}
		prepared = append(prepared, stripMediaForSummary(msg))
	}
	return prepared
}

// MessagesAfterLastCompactBoundary 返回最后一次上下文边界之后的模型可见历史。
func MessagesAfterLastCompactBoundary(messages []Message) []Message {
	lastBoundary := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if isReplayBoundary(messages[i]) {
			lastBoundary = i
			break
		}
	}
	if lastBoundary < 0 {
		return cloneMessages(messages)
	}
	return cloneMessages(messages[lastBoundary:])
}

// isReplayBoundary 表示后续 query 只应从这里之后恢复的上下文边界。
func isReplayBoundary(msg Message) bool {
	return msg.Subtype == SubtypeCompactBoundary || msg.Subtype == SubtypeHistorySnipBoundary
}

// shouldStripBeforeSummary 判断附件是否属于压缩后代码重建的上下文。
func shouldStripBeforeSummary(msg Message) bool {
	if msg.Attachment == nil {
		return false
	}
	switch msg.Attachment.Type {
	case AttachmentSkillListing,
		AttachmentSkillDiscovery,
		AttachmentDeferredToolDelta,
		AttachmentMCPInstructionDelta:
		return true
	default:
		return false
	}
}

// stripMediaForSummary 将图片和文档替换成占位符，避免摘要调用本身被大媒体撑爆。
func stripMediaForSummary(msg Message) Message {
	next := msg.Clone()
	for i, block := range next.Blocks {
		switch block.Type {
		case BlockImage:
			next.Blocks[i] = TextBlock("[image]")
		case BlockDocument:
			next.Blocks[i] = TextBlock("[document]")
		}
	}
	next.Tokens = EstimateTokens(next.FlattenContent())
	return next
}
