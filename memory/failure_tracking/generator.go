package failuretracking

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ModelLessonGenerator 使用 Eino ChatModel 生成可迁移经验和检索标签。
type ModelLessonGenerator struct {
	Model model.BaseChatModel
}

// NewModelLessonGenerator 把 Eino model 适配为 failure journal 的生成器。
func NewModelLessonGenerator(chatModel model.BaseChatModel) ModelLessonGenerator {
	return ModelLessonGenerator{Model: chatModel}
}

// GenerateHeuristic 让模型从 context/error/fix 中抽取一到两句可迁移经验。
func (g ModelLessonGenerator) GenerateHeuristic(ctx context.Context, failureContext string, errorMessage string, fix string) (string, error) {
	if g.Model == nil {
		return LocalLessonGenerator{}.GenerateHeuristic(ctx, failureContext, errorMessage, fix)
	}
	resp, err := g.Model.Generate(ctx, []*schema.Message{
		schema.SystemMessage("你负责把失败案例沉淀为可迁移经验。只输出一到两句中文 heuristic，不要输出标题或列表。"),
		schema.UserMessage(strings.Join([]string{
			"Agent 遇到了下面的失败：",
			"Context: " + failureContext,
			"Error: " + errorMessage,
			"Fix: " + fix,
			"",
			"请抽取一条对相似情况可复用的通用经验。",
		}, "\n")),
	})
	if err != nil {
		return "", fmt.Errorf("generate heuristic: %w", err)
	}
	heuristic := strings.TrimSpace(resp.Content)
	if heuristic == "" {
		return LocalLessonGenerator{}.GenerateHeuristic(ctx, failureContext, errorMessage, fix)
	}
	return heuristic, nil
}

// GenerateTags 让模型生成 3 到 5 个短标签，用于后续语义召回。
func (g ModelLessonGenerator) GenerateTags(ctx context.Context, failureContext string, errorMessage string) ([]string, error) {
	if g.Model == nil {
		return LocalLessonGenerator{}.GenerateTags(ctx, failureContext, errorMessage)
	}
	resp, err := g.Model.Generate(ctx, []*schema.Message{
		schema.SystemMessage("你负责为失败案例生成检索标签。只输出 3-5 个短标签，用英文逗号或中文逗号分隔。"),
		schema.UserMessage(strings.Join([]string{
			"Context: " + failureContext,
			"Error: " + errorMessage,
			"",
			"请输出标签。",
		}, "\n")),
	})
	if err != nil {
		return nil, fmt.Errorf("generate tags: %w", err)
	}
	tags := parseTags(resp.Content)
	if len(tags) == 0 {
		return LocalLessonGenerator{}.GenerateTags(ctx, failureContext, errorMessage)
	}
	return tags, nil
}

// LocalLessonGenerator 在没有模型时提供确定性降级，保证本地单测可运行。
type LocalLessonGenerator struct{}

// GenerateHeuristic 用规则生成一条保守经验，避免本地验证依赖真实模型。
func (LocalLessonGenerator) GenerateHeuristic(_ context.Context, failureContext string, errorMessage string, fix string) (string, error) {
	contextSummary := compactSentence(failureContext)
	errorSummary := compactSentence(errorMessage)
	fixSummary := compactSentence(fix)
	if contextSummary == "" {
		contextSummary = "类似失败"
	}
	if errorSummary == "" {
		errorSummary = "异常信号"
	}
	if fixSummary == "" {
		fixSummary = "先隔离影响面，再选择已验证的恢复动作"
	}
	return fmt.Sprintf("遇到%s并出现%s时，不要直接重复原路径；应先复核关键状态和边界条件，再采用已验证的恢复动作：%s。", contextSummary, errorSummary, fixSummary), nil
}

// GenerateTags 用规则从上下文和错误文本里抽取短标签。
func (LocalLessonGenerator) GenerateTags(_ context.Context, failureContext string, errorMessage string) ([]string, error) {
	joined := strings.ToLower(failureContext + " " + errorMessage)
	candidates := []struct {
		keyword string
		tag     string
	}{
		{"酒店", "酒店"},
		{"客人", "客诉"},
		{"房态", "房态"},
		{"pms", "PMS"},
		{"清洁", "清洁"},
		{"发票", "账单"},
		{"租户", "租户"},
		{"timeout", "超时"},
		{"notfound", "缺失"},
		{"timeout", "Timeout"},
		{"error", "错误"},
	}
	var tags []string
	for _, item := range candidates {
		if strings.Contains(joined, item.keyword) {
			tags = append(tags, item.tag)
		}
	}
	for _, token := range tokenize(failureContext + " " + errorMessage) {
		if len(tags) >= 5 {
			break
		}
		if len([]rune(token)) < 2 && !strings.EqualFold(token, "PMS") {
			continue
		}
		tags = appendUnique(tags, token)
	}
	if len(tags) == 0 {
		tags = []string{"失败经验", "恢复动作", "复盘"}
	}
	if len(tags) > 5 {
		tags = tags[:5]
	}
	return tags, nil
}

// parseTags 解析模型返回的逗号分隔标签，并做去重和空白清理。
func parseTags(raw string) []string {
	raw = strings.ReplaceAll(raw, "，", ",")
	raw = strings.ReplaceAll(raw, "\n", ",")
	parts := strings.Split(raw, ",")
	var tags []string
	for _, part := range parts {
		tag := strings.Trim(strings.TrimSpace(part), "[]`\"' ")
		if tag == "" {
			continue
		}
		tags = appendUnique(tags, tag)
	}
	if len(tags) > 5 {
		return tags[:5]
	}
	return tags
}

// appendUnique 追加不重复标签，保持模型或规则输出的原始顺序。
func appendUnique(values []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return values
	}
	for _, value := range values {
		if strings.EqualFold(value, next) {
			return values
		}
	}
	return append(values, next)
}

// compactSentence 截取一段短文本，避免本地 heuristic 变成冗长原文复述。
func compactSentence(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len([]rune(text)) <= 48 {
		return text
	}
	runes := []rune(text)
	return string(runes[:48]) + "..."
}
