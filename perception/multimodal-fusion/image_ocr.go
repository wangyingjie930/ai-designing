package multimodalfusion

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ModelImageTextExtractor 使用 vision 大模型执行 OCR，不依赖额外 OCR 服务。
type ModelImageTextExtractor struct {
	Model              model.BaseChatModel
	MaxLocalImageBytes int
}

// NewModelImageTextExtractor 创建基于 ChatModel 的图片 OCR 提取器。
func NewModelImageTextExtractor(chatModel model.BaseChatModel) *ModelImageTextExtractor {
	return &ModelImageTextExtractor{
		Model:              chatModel,
		MaxLocalImageBytes: defaultMaxLocalVisionImageBytes,
	}
}

// ExtractImageText 把图片作为多模态输入交给大模型，并要求它只返回 OCR 文本。
func (e *ModelImageTextExtractor) ExtractImageText(ctx context.Context, request ImageTextExtractionRequest) (string, error) {
	if e == nil || e.Model == nil {
		return "", fmt.Errorf("chat model for image OCR is not configured")
	}
	part, err := imageBlockToInputPart(FusionBlock{
		URI:      request.URI,
		MIMEType: request.MIMEType,
		Source:   request.Source,
		Hint:     request.Hint,
		Metadata: request.Metadata,
	}, e.MaxLocalImageBytes)
	if err != nil {
		return "", err
	}

	msg := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: buildImageOCRPrompt(request)},
			part,
		},
	}
	resp, err := e.Model.Generate(ctx, []*schema.Message{msg})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(assistantText(resp))
	if text == "" {
		return "", fmt.Errorf("model OCR returned empty text")
	}
	return text, nil
}

// buildImageOCRPrompt 约束大模型只做 OCR/结构提取，避免提前生成报告结论。
func buildImageOCRPrompt(request ImageTextExtractionRequest) string {
	var parts []string
	parts = append(parts,
		"请对这张图片做 OCR 和结构化信息抽取。",
		"只返回图片中可见的文字、关键数字、表格行列、图例、坐标轴和单位。",
		"不要写分析结论，不要补充图片里看不到的信息；看不清的内容标记为 [unclear]。",
	)
	if request.Source != "" {
		parts = append(parts, "source: "+request.Source)
	}
	if request.Hint != "" {
		parts = append(parts, "hint: "+request.Hint)
	}
	return strings.Join(parts, "\n")
}

// assistantText 兼容纯文本和多模态文本输出，作为 OCR 文本进入融合层。
func assistantText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	var parts []string
	for _, part := range message.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
