package multimodalfusion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultMaxVisionBridgeImages    = 8
	defaultMaxLocalVisionImageBytes = 4 * 1024 * 1024
)

// imageAwareChatModel 在最终模型调用前，把工具返回的 image_ref 转成真实多模态图片输入。
type imageAwareChatModel struct {
	base               model.BaseChatModel
	maxImages          int
	maxLocalImageBytes int
}

// wrapImageAwareChatModel 保持 tool message 纯文本，同时让最终分析轮真正收到图片。
func wrapImageAwareChatModel(base model.BaseChatModel) model.BaseChatModel {
	if base == nil {
		return nil
	}
	if _, ok := base.(*imageAwareChatModel); ok {
		return base
	}
	return &imageAwareChatModel{
		base:               base,
		maxImages:          defaultMaxVisionBridgeImages,
		maxLocalImageBytes: defaultMaxLocalVisionImageBytes,
	}
}

// Generate 注入图片桥接消息后再调用底层模型。
func (m *imageAwareChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.base.Generate(ctx, m.withVisionBridgeMessages(input), opts...)
}

// Stream 注入图片桥接消息后再调用底层流式模型。
func (m *imageAwareChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.base.Stream(ctx, m.withVisionBridgeMessages(input), opts...)
}

// withVisionBridgeMessages 只追加临时消息，不修改 ADK 持有的历史消息。
func (m *imageAwareChatModel) withVisionBridgeMessages(input []*schema.Message) []*schema.Message {
	bridge := buildVisionBridgeMessage(input, m.maxImages, m.maxLocalImageBytes)
	if bridge == nil {
		return input
	}
	out := append([]*schema.Message{}, input...)
	out = append(out, bridge)
	return out
}

// buildVisionBridgeMessage 从 prepare_report_context 的 JSON 里提取图片引用。
func buildVisionBridgeMessage(messages []*schema.Message, maxImages int, maxLocalImageBytes int) *schema.Message {
	if maxImages <= 0 {
		maxImages = defaultMaxVisionBridgeImages
	}
	var blocks []FusionBlock
	for _, message := range messages {
		blocks = append(blocks, imageRefBlocksFromToolMessage(message)...)
		if len(blocks) >= maxImages {
			blocks = blocks[:maxImages]
			break
		}
	}
	if len(blocks) == 0 {
		return nil
	}

	parts := []schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText,
		Text: "以下图片来自 prepare_report_context 的 image_ref。请结合工具返回的 JSON source/hint 做视觉分析；精确数字仍以表格或正文为准。",
	}}
	imageCount := 0
	for idx, block := range blocks {
		part, err := imageBlockToInputPart(block, maxLocalImageBytes)
		if err != nil {
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: fmt.Sprintf("[image_ref %d skipped] source=%s uri=%s reason=%s", idx+1, block.Source, block.URI, err.Error()),
			})
			continue
		}
		parts = append(parts,
			schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: imageBridgeLabel(block, idx)},
			part,
		)
		imageCount++
	}
	if imageCount == 0 {
		return nil
	}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
}

// imageRefBlocksFromToolMessage 只消费报告上下文工具的纯文本 JSON 输出。
func imageRefBlocksFromToolMessage(message *schema.Message) []FusionBlock {
	if message == nil || message.Role != schema.Tool || strings.TrimSpace(message.Content) == "" {
		return nil
	}
	if message.ToolName != "" && message.ToolName != ReportContextToolName {
		return nil
	}
	var prepared PreparedReportContext
	if err := json.Unmarshal([]byte(message.Content), &prepared); err != nil {
		return nil
	}
	blocks := make([]FusionBlock, 0, len(prepared.Content))
	for _, block := range prepared.Content {
		if block.Type != "image_ref" || strings.TrimSpace(block.URI) == "" {
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// imageBlockToInputPart 把远程 URL 直传给模型，把本地图片转成 base64。
func imageBlockToInputPart(block FusionBlock, maxLocalImageBytes int) (schema.MessageInputPart, error) {
	uri := strings.TrimSpace(block.URI)
	if uri == "" {
		return schema.MessageInputPart{}, fmt.Errorf("empty image uri")
	}
	image := &schema.MessageInputImage{
		MessagePartCommon: schema.MessagePartCommon{MIMEType: firstNonEmpty(block.MIMEType, imageMIMEType(uri))},
		Detail:            schema.ImageURLDetailAuto,
	}
	if isHTTPURL(uri) || isDataURL(uri) {
		image.URL = stringPtr(uri)
		return schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: image}, nil
	}

	data, err := os.ReadFile(uri)
	if err != nil {
		return schema.MessageInputPart{}, err
	}
	if maxLocalImageBytes > 0 && len(data) > maxLocalImageBytes {
		return schema.MessageInputPart{}, fmt.Errorf("local image is %d bytes, max allowed is %d", len(data), maxLocalImageBytes)
	}
	mimeType, err := localImageMIMEType(uri, data, block.MIMEType)
	if err != nil {
		return schema.MessageInputPart{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	image.MIMEType = mimeType
	image.Base64Data = &encoded
	return schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: image}, nil
}

// localImageMIMEType 优先使用显式 MIME，其次从路径和文件头推断。
func localImageMIMEType(uri string, data []byte, explicit string) (string, error) {
	if strings.HasPrefix(explicit, "image/") {
		return explicit, nil
	}
	if mimeType := imageMIMEType(uri); mimeType != "" {
		return mimeType, nil
	}
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		return detected, nil
	}
	return "", fmt.Errorf("local file is not recognized as an image: %s", detected)
}

// imageBridgeLabel 给每张图片补充稳定引用，方便模型把视觉判断写回 source/hint。
func imageBridgeLabel(block FusionBlock, index int) string {
	parts := []string{fmt.Sprintf("[image_ref %d]", index+1)}
	if block.Source != "" {
		parts = append(parts, "source="+block.Source)
	}
	if block.Hint != "" {
		parts = append(parts, "hint="+block.Hint)
	}
	if block.URI != "" {
		parts = append(parts, "uri="+block.URI)
	}
	return strings.Join(parts, " ")
}

// stringPtr 避免在构造 schema.MessageInputImage 时重复写临时变量。
func stringPtr(value string) *string {
	return &value
}
