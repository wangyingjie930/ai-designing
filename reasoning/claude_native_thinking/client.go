package claude_native_thinking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ResponsesClient 是最小 OpenAI Responses API 客户端，用来承载原生 reasoning 输出。
type ResponsesClient struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewResponsesClient 创建 Responses API 客户端；BaseURL 为空时使用 OpenAI 默认地址。
func NewResponsesClient(config ResponsesClientConfig) *ResponsesClient {
	baseURL := normalizeBaseURL(config.BaseURL)
	return &ResponsesClient{
		apiKey:     strings.TrimSpace(config.APIKey),
		model:      strings.TrimSpace(config.Model),
		baseURL:    baseURL,
		httpClient: http.DefaultClient,
	}
}

// GenerateStepDrafts 调用 Responses API，并把 OpenAI output 转成 Claude Code 风格的 content blocks。
func (c *ResponsesClient) GenerateStepDrafts(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if c == nil {
		return nil, errors.New("responses client is nil")
	}
	if c.apiKey == "" {
		return nil, errors.New("openai api key is required")
	}
	if c.model == "" {
		return nil, errors.New("openai model is required")
	}
	body, err := buildResponsesRequest(c.model, req)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal responses request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call responses api: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read responses api body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("responses api status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var apiResp responsesAPIResponse
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return nil, fmt.Errorf("decode responses api body: %w", err)
	}
	return apiResp.toResult(), nil
}

type responsesAPIResponse struct {
	ID     string               `json:"id"`
	Output []ResponseOutputItem `json:"output"`
	Usage  responsesUsage       `json:"usage"`
}

type responsesUsage struct {
	InputTokens          int                 `json:"input_tokens"`
	OutputTokens         int                 `json:"output_tokens"`
	OutputTokensDetails  outputTokensDetails `json:"output_tokens_details"`
	CompletionTokenUsage outputTokensDetails `json:"completion_tokens_details"`
}

type outputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

func (r responsesAPIResponse) toResult() *GenerateResult {
	blocks := make([]ContentBlock, 0, len(r.Output))
	var textParts []string
	for _, item := range r.Output {
		switch item.Type {
		case "reasoning":
			if summary := joinSummaryText(item.Summary); summary != "" {
				blocks = append(blocks, ContentBlock{Type: BlockThinking, Thinking: summary})
			}
			if strings.TrimSpace(item.EncryptedContent) != "" {
				blocks = append(blocks, ContentBlock{
					Type:             BlockRedactedThinking,
					Data:             item.EncryptedContent,
					EncryptedContent: item.EncryptedContent,
				})
			}
		case "message":
			for _, content := range item.Content {
				if content.Type != "output_text" || strings.TrimSpace(content.Text) == "" {
					continue
				}
				textParts = append(textParts, content.Text)
				blocks = append(blocks, ContentBlock{Type: BlockText, Text: content.Text})
			}
		}
	}
	reasoningTokens := r.Usage.OutputTokensDetails.ReasoningTokens
	if reasoningTokens == 0 {
		reasoningTokens = r.Usage.CompletionTokenUsage.ReasoningTokens
	}
	return &GenerateResult{
		ResponseID: r.ID,
		Text:       strings.TrimSpace(strings.Join(textParts, "\n")),
		Blocks:     blocks,
		Output:     r.Output,
		Usage: Usage{
			InputTokens:     r.Usage.InputTokens,
			OutputTokens:    r.Usage.OutputTokens,
			ReasoningTokens: reasoningTokens,
		},
	}
}

func joinSummaryText(summary []ReasoningSummary) string {
	parts := make([]string, 0, len(summary))
	for _, item := range summary {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return defaultOpenAIBaseURL
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}
