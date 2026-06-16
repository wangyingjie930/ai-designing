package multimodalfusion

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const defaultMaxLocalVisionImageBytes = 4 * 1024 * 1024

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

// imageBlockToToolOutputPart 复用图片输入构造逻辑，保证工具返回和 OCR 链路的图片格式一致。
func imageBlockToToolOutputPart(block FusionBlock, index int) (schema.ToolOutputPart, error) {
	inputPart, err := imageBlockToInputPart(block, defaultMaxLocalVisionImageBytes)
	if err != nil {
		return schema.ToolOutputPart{}, err
	}
	if inputPart.Image == nil {
		return schema.ToolOutputPart{}, fmt.Errorf("image input part is empty")
	}
	return schema.ToolOutputPart{
		Type: schema.ToolPartTypeImage,
		Image: &schema.ToolOutputImage{
			MessagePartCommon: inputPart.Image.MessagePartCommon,
		},
		Extra: imageToolResultExtra(block, index),
	}, nil
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

// stringPtr 避免在构造 schema 图片结构时重复写临时变量。
func stringPtr(value string) *string {
	return &value
}
