package hierarchical

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// testEmbedder 是单测用的可预测向量器，生产代码不会把它作为 fallback 注入。
type testEmbedder struct{}

// Embed 根据少量关键词生成稳定向量，让单测聚焦三层记忆语义和 SQLite 写入。
func (testEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	text = strings.ToLower(text)
	vector := []float64{0, 0, 0, 0}
	for _, token := range []string{"budget", "quiet", "boat", "museum"} {
		if strings.Contains(text, token) {
			switch token {
			case "budget":
				vector[0]++
			case "quiet":
				vector[1]++
			case "boat":
				vector[2]++
			case "museum":
				vector[3]++
			}
		}
	}
	if vector[0]+vector[1]+vector[2]+vector[3] == 0 {
		vector[0] = 0.1
	}
	return vector, nil
}

// TestHierarchicalMemoryConsolidateAndRetrieve 验证 important memory 能进入 SQLite 并被重新召回。
func TestHierarchicalMemoryConsolidateAndRetrieve(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.sqlite")
	memory, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        dbPath,
		Scope:         "trip",
		WorkingBudget: 20,
		Embedder:      testEmbedder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	importance := 0.9
	if _, err := memory.Add(ctx, AddRequest{
		Content:    "Prefer quiet hotels and avoid long boat rides.",
		Source:     "user",
		Importance: &importance,
	}); err != nil {
		t.Fatal(err)
	}
	consolidated, err := memory.Consolidate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if consolidated.Persisted != 1 {
		t.Fatalf("persisted=%d, want 1", consolidated.Persisted)
	}

	reloaded, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        dbPath,
		Scope:         "trip",
		WorkingBudget: 20,
		Embedder:      testEmbedder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	retrieved, err := reloaded.Retrieve(ctx, RetrieveRequest{Query: "quiet boat", K: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieved.Results) != 1 {
		t.Fatalf("results=%d, want 1", len(retrieved.Results))
	}
	if !strings.Contains(retrieved.Results[0].Text, "quiet hotels") {
		t.Fatalf("unexpected result: %+v", retrieved.Results[0])
	}
}

// TestHierarchicalMemoryBudgetEviction 验证 working 超预算时低优先级内容会进入 session tier。
func TestHierarchicalMemoryBudgetEviction(t *testing.T) {
	ctx := context.Background()
	memory, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        filepath.Join(t.TempDir(), "memory.sqlite"),
		Scope:         "budget",
		WorkingBudget: 4,
		Embedder:      testEmbedder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	low := 0.1
	high := 0.95
	if _, err := memory.Add(ctx, AddRequest{Content: "temporary note with many words", Source: "user", Importance: &low}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Add(ctx, AddRequest{Content: "important budget rule", Source: "user", Importance: &high}); err != nil {
		t.Fatal(err)
	}
	context := memory.Context()
	if len(context.Session) == 0 {
		t.Fatalf("expected at least one evicted session memory, state=%s", memory.DebugString())
	}
}
