package tot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/schema"
)

var (
	reflectionPattern       = regexp.MustCompile(`(?is)REFLECTION:\s*(.*?)(?:\*\*\s*Possible Options:\s*\*\*|Option\s+\d+:|$)`)
	optionHeaderPattern     = regexp.MustCompile(`(?is)Option\s+\d+:\s*`)
	numberPattern           = regexp.MustCompile(`[\d.]+`)
	batchOptionRatingRegexp = regexp.MustCompile(`(?is)(Option\s+\d+:.*?Rating:\s*[\d.]+)`)
	ratingRegexp            = regexp.MustCompile(`(?i)Rating:\s*([\d.]+)`)
)

// compactMessage 是 prompt rewriter 看到的消息精简视图，只保留角色和文本。
type compactMessage struct {
	Role    schema.RoleType `json:"role"`
	Content string          `json:"content"`
}

// parseReflection 从 thinker 回复中抽取 REFLECTION 段落。
func parseReflection(reply string) string {
	match := reflectionPattern.FindStringSubmatch(reply)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// parseOptions 从 thinker 回复中抽取 Option N 段落并去掉空选项。
func parseOptions(reply string) []string {
	indices := optionHeaderPattern.FindAllStringIndex(reply, -1)
	options := make([]string, 0, len(indices))
	for i, index := range indices {
		start := index[1]
		end := len(reply)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		option := strings.TrimSpace(reply[start:end])
		if option != "" {
			options = append(options, option)
		}
	}
	return options
}

// parseReward 从模型评分文本中取第一个数字，并归一化到 0 到 1。
func parseReward(text string, ratingScale int) float64 {
	if ratingScale <= 1 {
		return 0
	}
	raw := numberPattern.FindString(text)
	if raw == "" {
		return 0
	}
	score, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	reward := (score - 1.0) / float64(ratingScale-1)
	if reward < 0 {
		return 0
	}
	if reward > 1 {
		return 1
	}
	return reward
}

// parseBatchRewards 解析批量评分响应；格式不完整时返回 false，让调用方走默认低分。
func parseBatchRewards(text string, count int, ratingScale int) ([]float64, []string, bool) {
	blocks := batchOptionRatingRegexp.FindAllString(text, -1)
	if len(blocks) != count {
		return nil, nil, false
	}
	rewards := make([]float64, 0, len(blocks))
	details := make([]string, 0, len(blocks))
	for _, block := range blocks {
		match := ratingRegexp.FindStringSubmatch(block)
		if len(match) < 2 {
			return nil, nil, false
		}
		rewards = append(rewards, parseReward(match[1], ratingScale))
		details = append(details, strings.TrimSpace(block))
	}
	return rewards, details, true
}

// splitGroundTruth 从 prompt 中拆出 GROUND_TRUTH 段，用于 grader 更准确地打分。
func splitGroundTruth(content string) (string, string) {
	idx := strings.Index(content, "GROUND_TRUTH")
	if idx < 0 {
		return strings.TrimSpace(content), ""
	}
	return strings.TrimSpace(content[:idx]), strings.TrimSpace(content[idx:])
}

// messagesDebugText 把 schema.Message 列表转成稳定 JSON，交给 prompt rewriter 使用。
func messagesDebugText(messages []*schema.Message) string {
	compacted := make([]compactMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		compacted = append(compacted, compactMessage{
			Role:    message.Role,
			Content: messageText(message),
		})
	}
	data, err := json.MarshalIndent(compacted, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", compacted)
	}
	return string(data)
}

// messageText 读取文本消息；多模态消息只拼接其中的 text part，避免把图片二进制塞进推理 prompt。
func messageText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	var parts []string
	for _, part := range message.UserInputMultiContent {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
