package failuretracking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const defaultEmbeddingTimeout = 30 * time.Second

// HTTPEmbedderConfig 描述一个 OpenAI-compatible 或 Gemini embedContent 风格的 embedding 端点。
type HTTPEmbedderConfig struct {
	APIKey       string
	Model        string
	BaseURL      string
	EndpointPath string
	Dimension    int
	Timeout      time.Duration
}

// HTTPEmbedder 通过 HTTP 调用真实 embedding 服务。
type HTTPEmbedder struct {
	config HTTPEmbedderConfig
	client *http.Client
}

// NewHTTPEmbedder 创建真实 embedding 客户端；调用方负责从 .env 注入配置。
func NewHTTPEmbedder(config HTTPEmbedderConfig) (*HTTPEmbedder, error) {
	config.Model = strings.TrimSpace(config.Model)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.EndpointPath = strings.Trim(strings.TrimSpace(config.EndpointPath), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	if config.Model == "" {
		return nil, fmt.Errorf("embedding model is empty")
	}
	if config.BaseURL == "" {
		return nil, fmt.Errorf("embedding base url is empty")
	}
	if config.APIKey == "" {
		return nil, fmt.Errorf("embedding api key is empty")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultEmbeddingTimeout
	}
	return &HTTPEmbedder{
		config: config,
		client: &http.Client{Timeout: timeout},
	}, nil
}

// Embed 根据配置自动选择 Gemini embedContent 或 OpenAI-compatible embeddings 请求。
func (e *HTTPEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if e == nil || e.client == nil {
		return nil, fmt.Errorf("http embedder is not initialized")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("embedding text is empty")
	}
	if e.isGeminiEndpoint() {
		return e.embedWithGemini(ctx, text)
	}
	return e.embedWithOpenAICompatible(ctx, text)
}

// isGeminiEndpoint 判断当前配置是否是 Google Gemini embedContent 风格。
func (e *HTTPEmbedder) isGeminiEndpoint() bool {
	model := strings.ToLower(e.config.Model)
	endpointPath := strings.ToLower(e.config.EndpointPath)
	baseURL := strings.ToLower(e.config.BaseURL)
	return strings.HasPrefix(model, "google:") ||
		strings.Contains(endpointPath, ":embedcontent") ||
		strings.Contains(baseURL, "generativelanguage.googleapis.com")
}

// embedWithOpenAICompatible 调用 /embeddings 兼容接口。
func (e *HTTPEmbedder) embedWithOpenAICompatible(ctx context.Context, text string) ([]float64, error) {
	endpoint := e.endpointURL("embeddings")
	body := map[string]any{
		"model": e.cleanModel(),
		"input": text,
	}
	var parsed struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := e.postJSON(ctx, endpoint, body, true, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding response has no vector")
	}
	return parsed.Data[0].Embedding, nil
}

// embedWithGemini 调用 Google Gemini embedContent 风格接口。
func (e *HTTPEmbedder) embedWithGemini(ctx context.Context, text string) ([]float64, error) {
	endpoint := e.endpointURL("models/" + e.cleanModel() + ":embedContent")
	endpoint = e.withAPIKeyQuery(endpoint)
	body := map[string]any{
		"model": "models/" + e.cleanModel(),
		"content": map[string]any{
			"parts": []map[string]string{{"text": text}},
		},
	}
	if e.config.Dimension > 0 {
		body["outputDimensionality"] = e.config.Dimension
	}
	var parsed struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
	}
	if err := e.postJSON(ctx, endpoint, body, false, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Embedding.Values) == 0 {
		return nil, fmt.Errorf("embedding response has no vector")
	}
	return parsed.Embedding.Values, nil
}

// endpointURL 拼出最终请求 URL；显式 EMBEDDING_ENDPOINT_PATH 优先于默认路径。
func (e *HTTPEmbedder) endpointURL(defaultPath string) string {
	endpointPath := e.config.EndpointPath
	if endpointPath == "" {
		endpointPath = defaultPath
	}
	base, err := url.Parse(e.config.BaseURL)
	if err != nil {
		return e.config.BaseURL + "/" + endpointPath
	}
	base.Path = path.Join(base.Path, endpointPath)
	return base.String()
}

// withAPIKeyQuery 为 Google API 风格端点追加 key 查询参数。
func (e *HTTPEmbedder) withAPIKeyQuery(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	if query.Get("key") == "" {
		query.Set("key", e.config.APIKey)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// postJSON 发送 JSON 请求并解析响应；authorization 表示是否使用 Bearer 认证。
func (e *HTTPEmbedder) postJSON(ctx context.Context, endpoint string, body any, authorization bool, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization {
		req.Header.Set("Authorization", "Bearer "+e.config.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("embedding request failed: status=%d body=%s", resp.StatusCode, compactHTTPBody(respBody))
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode embedding response: %w", err)
	}
	return nil
}

// cleanModel 去掉 provider 前缀，并兼容 models/ 前缀。
func (e *HTTPEmbedder) cleanModel() string {
	model := strings.TrimSpace(e.config.Model)
	if before, after, ok := strings.Cut(model, ":"); ok && before != "models" {
		model = after
	}
	return strings.TrimPrefix(model, "models/")
}

// compactHTTPBody 避免错误里输出过长响应体。
func compactHTTPBody(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len([]rune(text)) <= 300 {
		return text
	}
	return string([]rune(text)[:300]) + "...(truncated " + strconv.Itoa(len(body)) + " bytes)"
}
