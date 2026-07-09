package researchswarm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFakeSearchClientReturnsStableResults 验证默认搜索不依赖外网，也能产出可引用资料。
func TestFakeSearchClientReturnsStableResults(t *testing.T) {
	client := NewFakeSearchClient()
	resp, err := client.Search(context.Background(), SearchRequest{Query: "AI Agent search risk", TopK: 2, Language: "zh"})
	requireNoError(t, err)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].URL == "" || resp.Results[0].Title == "" || resp.Results[0].Snippet == "" {
		t.Fatalf("first result missing citation fields: %#v", resp.Results[0])
	}
}

// TestHTTPJSONSearchClientPostsRequest 验证真实搜索供应商只需要适配统一 HTTP JSON 边界。
func TestHTTPJSONSearchClientPostsRequest(t *testing.T) {
	var captured SearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		requireNoError(t, json.NewDecoder(r.Body).Decode(&captured))
		requireNoError(t, json.NewEncoder(w).Encode(SearchResponse{Results: []SearchResult{{
			Title:   "External Search",
			URL:     "https://search.example/doc",
			Snippet: "search snippet",
			Source:  "http_json",
		}}}))
	}))
	defer server.Close()

	client := NewHTTPJSONSearchClient(server.URL, "test-key")
	resp, err := client.Search(context.Background(), SearchRequest{Query: "external search", TopK: 3, Language: "zh"})
	requireNoError(t, err)
	if captured.Query != "external search" || captured.TopK != 3 {
		t.Fatalf("captured request = %#v", captured)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL == "" {
		t.Fatalf("response = %#v", resp)
	}
}
