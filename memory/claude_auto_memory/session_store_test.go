package claudeautomemory

import (
	"context"
	"strings"
	"testing"
)

// TestSessionStoreCommitAndLoad 验证摘要正文和 UUID 边界可以作为同一 Session 快照恢复。
func TestSessionStoreCommitAndLoad(t *testing.T) {
	store, err := NewSessionStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	summary := strings.Replace(defaultSessionMemoryTemplate, "# 当前状态\n", "# 当前状态\n正在实现 Session Memory。\n", 1)
	state := SessionState{LastSummarizedMessageID: "message-1", TokensAtLastUpdate: 12000, Initialized: true}
	if err := store.Commit(context.Background(), summary, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != state || !strings.Contains(loaded.Summary, "正在实现 Session Memory") {
		t.Fatalf("loaded = %+v", loaded)
	}
}

// TestSessionStoreRejectsBrokenTemplate 验证模型不能删除固定栏目后覆盖可恢复摘要。
func TestSessionStoreRejectsBrokenTemplate(t *testing.T) {
	store, err := NewSessionStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(context.Background(), "# 当前状态\n内容", SessionState{}); err == nil {
		t.Fatal("expected template validation error")
	}
}

// TestSessionStoreInitializesEmptySnapshot 验证新会话立即拥有合法模板和空状态。
func TestSessionStoreInitializesEmptySnapshot(t *testing.T) {
	store, err := NewSessionStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary != defaultSessionMemoryTemplate || snapshot.State.Initialized || !store.IsEmptySummary(snapshot.Summary) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
