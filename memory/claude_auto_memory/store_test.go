package claudeautomemory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreUpsertMaintainsPrivateAndTeamIndexes 验证两个作用域各自维护主题文件和 MEMORY.md。
func TestStoreUpsertMaintainsPrivateAndTeamIndexes(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	private, err := store.Upsert(context.Background(), MemoryCandidate{
		Type:        MemoryTypeUser,
		Scope:       ScopePrivate,
		Topic:       "中文注释偏好",
		Description: "用户希望新增代码使用中文用途注释",
		Content:     "新增函数和结构体前写中文用途注释。",
	})
	if err != nil {
		t.Fatal(err)
	}
	team, err := store.Upsert(context.Background(), MemoryCandidate{
		Type:        MemoryTypeProject,
		Scope:       ScopeTeam,
		Topic:       "tool-schema-convention",
		Description: "新工具参数需要描述",
		Content:     "所有新工具参数都要声明 jsonschema_description。",
	})
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := store.LoadManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Private) != 1 || len(manifest.Team) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if private.Ref.Scope != ScopePrivate || team.Ref.Scope != ScopeTeam {
		t.Fatalf("records = %+v %+v", private, team)
	}
	for _, path := range []string{
		filepath.Join(store.Root(), memoryIndexName),
		filepath.Join(store.Root(), teamDirectoryName, memoryIndexName),
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(content), "# Memory Index") {
			t.Fatalf("index %s = %s", path, content)
		}
	}
}

// TestStoreUpsertReplacesTopicWithoutDuplicatingIndex 验证同一主题更新正文时索引仍只有一项。
func TestStoreUpsertReplacesTopicWithoutDuplicatingIndex(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"旧内容", "新内容"} {
		_, err = store.Upsert(context.Background(), MemoryCandidate{
			Type: MemoryTypeFeedback, Scope: ScopePrivate, Topic: "answer-style",
			Description: "回答风格", Content: content,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := store.LoadManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Private) != 1 {
		t.Fatalf("private index = %+v", manifest.Private)
	}
	record, err := store.Read(context.Background(), manifest.Private[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if record.Content != "新内容" {
		t.Fatalf("content = %q", record.Content)
	}
}

// TestStoreRejectsUnsafeCandidates 验证工程边界拒绝非法分类、路径穿越和团队凭据。
func TestStoreRejectsUnsafeCandidates(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := []MemoryCandidate{
		{Type: MemoryTypeUser, Scope: ScopeTeam, Topic: "preference", Description: "x", Content: "x"},
		{Type: MemoryTypeProject, Scope: ScopePrivate, Topic: "../escape", Description: "x", Content: "x"},
		{Type: MemoryType("other"), Scope: ScopePrivate, Topic: "invalid-type", Description: "x", Content: "x"},
		{Type: MemoryTypeProject, Scope: ScopeTeam, Topic: "deployment", Description: "token", Content: "OPENAI_API_KEY=sk-secret-value"},
	}
	for _, candidate := range cases {
		if _, err := store.Upsert(context.Background(), candidate); err == nil {
			t.Fatalf("candidate should fail: %+v", candidate)
		}
	}
}

// TestStoreReadRejectsReferenceOutsideManifest 验证召回层不能用模型构造的任意路径读取文件。
func TestStoreReadRejectsReferenceOutsideManifest(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), MemoryRef{Scope: ScopePrivate, Topic: "not-indexed"}); err == nil {
		t.Fatal("expected unindexed reference rejection")
	}
}
