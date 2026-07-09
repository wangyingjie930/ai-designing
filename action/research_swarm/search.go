package researchswarm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// SearchRequest 是 web_search 工具和外部搜索供应商之间的统一请求。
type SearchRequest struct {
	Query    string `json:"query" jsonschema:"required" jsonschema_description:"调查报告需要搜索的问题或关键词"`
	TopK     int    `json:"top_k,omitempty" jsonschema:"minimum=1,maximum=10,default=3" jsonschema_description:"最多返回多少条搜索结果"`
	Language string `json:"language,omitempty" jsonschema_description:"结果语言偏好，例如 zh 或 en"`
}

// SearchResult 是外部搜索返回后进入资料卡前的候选来源。
type SearchResult struct {
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Snippet     string    `json:"snippet"`
	Source      string    `json:"source"`
	RetrievedAt time.Time `json:"retrieved_at,omitempty"`
}

// SearchResponse 包装一组候选搜索结果。
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchClient 隔离真实搜索供应商，leader/worker 只依赖这个稳定接口。
type SearchClient interface {
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}

// FakeSearchClient 提供稳定的离线搜索结果，保证 demo 和测试不依赖外网。
type FakeSearchClient struct{}

// NewFakeSearchClient 创建默认 fake 搜索客户端。
func NewFakeSearchClient() *FakeSearchClient {
	return &FakeSearchClient{}
}

// Search 返回固定但和 query 相关的调查资料。
func (c *FakeSearchClient) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 3
	}
	now := time.Now().UTC()
	results := []SearchResult{
		{
			Title:       "外部搜索进入 Agent 报告前需要证据卡片化",
			URL:         "https://example.com/research/source-cards",
			Snippet:     "将搜索结果先沉淀为 source cards，可以让分析员和撰稿员通过证据 ID 引用事实，减少上下文口口相传导致的漂移。",
			Source:      "fake_search",
			RetrievedAt: now,
		},
		{
			Title:       "多角色审查能降低单一 Agent 的检索误用风险",
			URL:         "https://example.com/research/multi-agent-review",
			Snippet:     "搜索员、分析员和撰稿员分离后，搜索、事实归类和表达生成由不同角色完成，冲突事实更容易被标记。",
			Source:      "fake_search",
			RetrievedAt: now,
		},
		{
			Title:       "调查报告型 Agent 需要保留来源和不确定性",
			URL:         "https://example.com/research/report-uncertainty",
			Snippet:     "报告输出应包含来源引用、证据强弱和待确认问题，而不是把搜索摘要直接写成确定结论。",
			Source:      "fake_search",
			RetrievedAt: now,
		},
	}
	if topK > len(results) {
		topK = len(results)
	}
	return &SearchResponse{Results: results[:topK]}, nil
}

// HTTPJSONSearchClient 通过统一 JSON POST 协议接入外部搜索网关。
type HTTPJSONSearchClient struct {
	url    string
	apiKey string
	client *http.Client
}

// NewHTTPJSONSearchClient 创建 HTTP JSON 搜索客户端。
func NewHTTPJSONSearchClient(url string, apiKey string) *HTTPJSONSearchClient {
	return &HTTPJSONSearchClient{
		url:    strings.TrimSpace(url),
		apiKey: strings.TrimSpace(apiKey),
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Search 调用外部搜索服务，兼容返回 {"results":[...]} 的常见结构。
func (c *HTTPJSONSearchClient) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if c == nil || c.url == "" {
		return nil, fmt.Errorf("search api url is required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search api status %d", resp.StatusCode)
	}
	var out SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range out.Results {
		if out.Results[i].Source == "" {
			out.Results[i].Source = string(SearchProviderHTTPJSON)
		}
		if out.Results[i].RetrievedAt.IsZero() {
			out.Results[i].RetrievedAt = now
		}
	}
	return &out, nil
}

// NewSearchClientFromEnv 根据环境变量选择 fake 或 HTTP JSON 搜索。
func NewSearchClientFromEnv() (SearchProvider, SearchClient, error) {
	provider := SearchProvider(strings.TrimSpace(os.Getenv("SEARCH_PROVIDER")))
	if provider == "" {
		provider = SearchProviderFake
	}
	switch provider {
	case SearchProviderFake:
		return provider, NewFakeSearchClient(), nil
	case SearchProviderHTTPJSON:
		url := strings.TrimSpace(os.Getenv("SEARCH_API_URL"))
		if url == "" {
			return "", nil, fmt.Errorf("SEARCH_API_URL is required when SEARCH_PROVIDER=http_json")
		}
		return provider, NewHTTPJSONSearchClient(url, os.Getenv("SEARCH_API_KEY")), nil
	default:
		return "", nil, fmt.Errorf("unsupported search provider %q", provider)
	}
}
