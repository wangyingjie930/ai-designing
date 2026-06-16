package mem0

import (
	"encoding/json"
	"strings"
)

// extractionItem 是 LLM 从对话中抽出的单条可长期复用记忆。
type extractionItem struct {
	Text         string `json:"text"`
	AttributedTo string `json:"attributed_to,omitempty"`
}

// extractionPayload 对齐 mem0 源码里 {"memory": [...]} 的抽取返回协议。
type extractionPayload struct {
	Memory []extractionItem `json:"memory"`
}

// additiveExtractionSystemPrompt 约束模型只做长期记忆抽取，不直接回答用户。
func additiveExtractionSystemPrompt() string {
	return strings.Join([]string{
		"你是一个长期记忆抽取器，只负责从对话中提取未来仍有用的事实、偏好、决策或稳定约束。",
		"不要保存一次性的寒暄、临时推测、无证据结论或已经被新消息否定的内容。",
		"只输出 JSON 对象，不要输出 markdown，不要解释。",
		`输出格式必须是 {"memory":[{"text":"...","attributed_to":"user|assistant|system"}]}`,
		"如果没有值得保存的内容，输出 {\"memory\":[]}",
	}, "\n")
}

// buildAdditiveExtractionPrompt 复刻 mem0 add 的抽取输入：已有记忆、最近消息和本轮新消息。
func buildAdditiveExtractionPrompt(existing []map[string]string, lastMessages []Message, newMessages []Message, customInstructions string) string {
	payload := map[string]any{
		"existing_memories": existing,
		"last_k_messages":   lastMessages,
		"new_messages":      newMessages,
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	parts := []string{
		"请基于下面 JSON 执行长期记忆抽取。",
		"处理要求：",
		"1. 如果新消息和 existing_memories 重复，不要重复输出。",
		"2. 如果新消息修正了旧事实，只输出最新事实。",
		"3. 每条 text 必须是独立可读的中文事实句。",
		"4. attributed_to 表示这条记忆主要来自 user、assistant 或 system。",
	}
	if strings.TrimSpace(customInstructions) != "" {
		parts = append(parts, "额外业务约束：", strings.TrimSpace(customInstructions))
	}
	parts = append(parts, "", string(b))
	return strings.Join(parts, "\n")
}

// parseExtractionPayload 从模型输出中提取 JSON，并容忍偶发代码块包裹。
func parseExtractionPayload(text string) ([]extractionItem, error) {
	jsonText := extractJSONObject(text)
	if jsonText == "" {
		return nil, nil
	}
	var payload extractionPayload
	if err := json.Unmarshal([]byte(jsonText), &payload); err != nil {
		return nil, err
	}
	var items []extractionItem
	for _, item := range payload.Memory {
		item.Text = strings.TrimSpace(item.Text)
		item.AttributedTo = strings.TrimSpace(item.AttributedTo)
		if item.Text == "" {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// extractJSONObject 兼容模型把 JSON 放进 markdown fence 或额外文本中的情况。
func extractJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 3 {
			trimmed = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end < start {
		return ""
	}
	candidate := strings.TrimSpace(trimmed[start : end+1])
	if !json.Valid([]byte(candidate)) {
		return ""
	}
	return candidate
}
