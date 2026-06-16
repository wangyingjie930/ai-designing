package mem0

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestAddAndSearchInferredMemory 验证 infer=true 会经由模型抽取、SQLite 写入并可搜索。
func TestAddAndSearchInferredMemory(t *testing.T) {
	ctx := context.Background()
	memory, err := NewMemory(ctx, Config{
		DBPath:   ":memory:",
		Model:    &fakeChatModel{responses: []string{`{"memory":[{"text":"用户喜欢科幻电影，不喜欢惊悚片。","attributed_to":"user"}]}`}},
		Embedder: NewFakeEmbedder(32),
	})
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}
	defer memory.Close()

	resp, err := memory.Add(ctx, AddRequest{
		UserID: "alice",
		Messages: []Message{
			{Role: "user", Content: "我不太喜欢惊悚片，但我很喜欢科幻电影。"},
			{Role: "assistant", Content: "好的，我以后会优先推荐科幻电影。"},
		},
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID == "" {
		t.Fatalf("Add() results = %+v", resp.Results)
	}

	threshold := 0.0
	search, err := memory.Search(ctx, SearchRequest{
		Query:     "她喜欢什么电影？",
		Filters:   map[string]any{"user_id": "alice"},
		Threshold: &threshold,
		Explain:   true,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("Search() len = %d, want 1", len(search.Results))
	}
	if !strings.Contains(search.Results[0].Memory, "科幻") {
		t.Fatalf("Search() memory = %q", search.Results[0].Memory)
	}
	if search.Results[0].ScoreDetails == nil {
		t.Fatal("Search() should include score details")
	}
}

// TestAddRawMemoryKeepsRole 验证 infer=false 会按原始消息保存 role/actor 元数据。
func TestAddRawMemoryKeepsRole(t *testing.T) {
	ctx := context.Background()
	memory, err := NewMemory(ctx, Config{
		DBPath:   ":memory:",
		Embedder: NewFakeEmbedder(32),
	})
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}
	defer memory.Close()

	infer := false
	resp, err := memory.Add(ctx, AddRequest{
		UserID: "bob",
		Infer:  &infer,
		Messages: []Message{
			{Role: "user", Name: "bob", Content: "我对坚果过敏。"},
		},
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Role != "user" || resp.Results[0].ActorID != "bob" {
		t.Fatalf("raw add result = %+v", resp.Results)
	}
}

// fakeChatModel 为 mem0 单测提供可控的 LLM 抽取输出。
type fakeChatModel struct {
	mu        sync.Mutex
	responses []string
}

// Generate 返回预设内容，模拟真实模型的 JSON 抽取结果。
func (m *fakeChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	content := `{"memory":[]}`
	if len(m.responses) > 0 {
		content = m.responses[0]
		m.responses = m.responses[1:]
	}
	return schema.AssistantMessage(content, nil), nil
}

// Stream 返回单条流式消息，满足 Eino BaseChatModel 接口。
func (m *fakeChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(`{"memory":[]}`, nil)}), nil
}
