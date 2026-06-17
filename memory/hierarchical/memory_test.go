package hierarchical

import (
	"context"
	"encoding/json"
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
	if !entriesContain(reloaded.Context().Working, "quiet hotels") {
		t.Fatalf("long-term retrieval should re-enter working, state=%s", reloaded.DebugString())
	}
}

// TestLongTermRetrieveReentersWorking 验证长期记忆召回后会重新进入 working。
func TestLongTermRetrieveReentersWorking(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.sqlite")
	memory, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        dbPath,
		Scope:         "longterm-small-budget",
		WorkingBudget: 8,
		Embedder:      testEmbedder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	importance := 0.9
	if _, err := memory.Add(ctx, AddRequest{Content: "quiet boat", Source: "user", Importance: &importance}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Consolidate(ctx); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        dbPath,
		Scope:         "longterm-small-budget",
		WorkingBudget: 3,
		Embedder:      testEmbedder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if _, err := reloaded.Retrieve(ctx, RetrieveRequest{Query: "quiet boat", K: 1}); err != nil {
		t.Fatal(err)
	}
	working := reloaded.Context().Working
	if !entriesContain(working, "quiet boat") {
		t.Fatalf("retrieved long-term memory should stay in working, state=%s", reloaded.DebugString())
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

// TestConsolidateMigratesImportantSessionToLongTerm 验证 session 里的重要记忆会迁移到长期记忆并从 session 移除。
func TestConsolidateMigratesImportantSessionToLongTerm(t *testing.T) {
	ctx := context.Background()
	memory, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        filepath.Join(t.TempDir(), "memory.sqlite"),
		Scope:         "session-migration",
		WorkingBudget: 1,
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
	if len(memory.Context().Session) == 0 {
		t.Fatalf("test setup expected important memory in session, state=%s", memory.DebugString())
	}
	consolidated, err := memory.Consolidate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if consolidated.Persisted != 1 {
		t.Fatalf("persisted=%d, want 1", consolidated.Persisted)
	}
	if len(memory.Context().Session) != 0 {
		t.Fatalf("session should be empty after migration, state=%s", memory.DebugString())
	}
	longTerm, err := memory.LongTerm(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(longTerm) != 1 || !strings.Contains(longTerm[0].Content, "quiet hotels") {
		t.Fatalf("unexpected long-term entries: %+v", longTerm)
	}
}

// TestRetrieveMovesSessionBackToWorking 验证 session 命中召回后会重新进入 working。
func TestRetrieveMovesSessionBackToWorking(t *testing.T) {
	ctx := context.Background()
	memory, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        filepath.Join(t.TempDir(), "memory.sqlite"),
		Scope:         "session-retrieve",
		WorkingBudget: 8,
		Embedder:      testEmbedder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	low := 0.1
	high := 0.95
	sessionText := "quiet boat memory with many words"
	if _, err := memory.Add(ctx, AddRequest{Content: sessionText, Source: "user", Importance: &low}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Add(ctx, AddRequest{Content: "budget rule", Source: "user", Importance: &high}); err != nil {
		t.Fatal(err)
	}
	if !entriesContain(memory.Context().Session, "quiet boat") {
		t.Fatalf("test setup expected quiet boat in session, state=%s", memory.DebugString())
	}
	if _, err := memory.Retrieve(ctx, RetrieveRequest{Query: "quiet boat", K: 1}); err != nil {
		t.Fatal(err)
	}
	context := memory.Context()
	if !entriesContain(context.Working, "quiet boat") {
		t.Fatalf("session retrieval should move memory back to working, state=%s", memory.DebugString())
	}
	if entriesContain(context.Session, "quiet boat") {
		t.Fatalf("retrieved session memory should be removed from session, state=%s", memory.DebugString())
	}
}

// TestContextToolDoesNotExposeSession 验证 ADK context 工具不会把内部 session 淘汰区暴露给 LLM。
func TestContextToolDoesNotExposeSession(t *testing.T) {
	ctx := context.Background()
	memory, err := NewHierarchicalMemory(ctx, Config{
		DBPath:        filepath.Join(t.TempDir(), "memory.sqlite"),
		Scope:         "tool-context",
		WorkingBudget: 6,
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
	if len(memory.Context().Session) == 0 {
		t.Fatalf("test setup expected session memory, state=%s", memory.DebugString())
	}
	response, err := Toolset{Memory: memory}.ContextMemory(ctx, ContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Working) == 0 {
		t.Fatal("expected visible working memory")
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "session") {
		t.Fatalf("tool response leaked session: %s", payload)
	}
}

// entriesContain 判断记忆列表里是否包含指定内容片段。
func entriesContain(entries []MemoryEntry, fragment string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Content, fragment) {
			return true
		}
	}
	return false
}
