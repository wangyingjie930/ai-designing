package multimodalfusion

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// buildPreparedReportToolResult 把融合上下文作为 JSON 文本返回，同时把 image_ref 升级为原生图片 part。
func buildPreparedReportToolResult(prepared *PreparedReportContext) (*schema.ToolResult, error) {
	payload, err := json.MarshalIndent(prepared, "", "  ")
	if err != nil {
		return nil, err
	}

	parts := []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: string(payload),
		Extra: map[string]any{
			"content_type": "application/json",
			"tool":         ReportContextToolName,
		},
	}}
	if prepared == nil {
		return &schema.ToolResult{Parts: parts}, nil
	}

	imageIndex := 0
	for _, block := range prepared.Content {
		if block.Type != "image_ref" || strings.TrimSpace(block.URI) == "" {
			continue
		}
		parts = append(parts, schema.ToolOutputPart{
			Type:  schema.ToolPartTypeText,
			Text:  imageToolResultLabel(block, imageIndex),
			Extra: imageToolResultExtra(block, imageIndex),
		})
		imagePart, err := imageBlockToToolOutputPart(block, imageIndex)
		if err != nil {
			parts = append(parts, schema.ToolOutputPart{
				Type:  schema.ToolPartTypeText,
				Text:  fmt.Sprintf("[image_ref %d skipped] source=%s uri=%s reason=%s", imageIndex+1, block.Source, block.URI, err.Error()),
				Extra: imageToolResultExtra(block, imageIndex),
			})
			imageIndex++
			continue
		}
		parts = append(parts, imagePart)
		imageIndex++
	}
	return &schema.ToolResult{Parts: parts}, nil
}

// imageToolResultLabel 给每张图片补充稳定引用，方便模型把视觉判断写回 source/hint。
func imageToolResultLabel(block FusionBlock, index int) string {
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

// imageToolResultExtra 把 source/page/chart 等元数据带到图片 part，便于模型输出引用。
func imageToolResultExtra(block FusionBlock, index int) map[string]any {
	extra := map[string]any{
		"image_ref_index": index + 1,
		"modality":        string(block.Modality),
	}
	if block.Source != "" {
		extra["source"] = block.Source
	}
	if block.Hint != "" {
		extra["hint"] = block.Hint
	}
	if block.URI != "" {
		extra["uri"] = block.URI
	}
	if block.MIMEType != "" {
		extra["mime_type"] = block.MIMEType
	}
	if len(block.Metadata) > 0 {
		extra["metadata"] = block.Metadata
	}
	return extra
}
