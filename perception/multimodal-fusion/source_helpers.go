package multimodalfusion

import (
	"path/filepath"
	"strings"

	"net/url"
)

// fileReference 统一读取调用方传入的本地路径或远程 URL。
func fileReference(file FileInput) string {
	if strings.TrimSpace(file.URL) != "" {
		return strings.TrimSpace(file.URL)
	}
	return strings.TrimSpace(file.Path)
}

// sourceExt 从本地路径或 URL path 中提取扩展名，避免 query 参数污染图片识别。
func sourceExt(source string) string {
	source = strings.TrimSpace(source)
	if source == "" || isDataURL(source) {
		return ""
	}
	if parsed, err := url.Parse(source); err == nil && parsed.Scheme != "" {
		return strings.ToLower(filepath.Ext(parsed.Path))
	}
	return strings.ToLower(filepath.Ext(source))
}

// isHTTPURL 判断来源是否为模型可直接访问的远程 URL。
func isHTTPURL(source string) bool {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

// isDataURL 判断来源是否已经是 RFC-2397 data URL。
func isDataURL(source string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "data:")
}

// isImageDataURL 识别内联图片 data URL，便于没有文件后缀时仍走图片链路。
func isImageDataURL(source string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "data:image/")
}

// imageSourceMetadata 给 trace 标注图片来源类型，方便排查 URL 是否真的进入 vision 链路。
func imageSourceMetadata(source string) map[string]string {
	switch {
	case isHTTPURL(source):
		return map[string]string{"source_type": "url"}
	case isDataURL(source):
		return map[string]string{"source_type": "data_url"}
	case strings.TrimSpace(source) != "":
		return map[string]string{"source_type": "local_file"}
	default:
		return nil
	}
}
