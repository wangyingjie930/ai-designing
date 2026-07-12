package claudeautomemory

import (
	"context"
	"testing"
)

// TestTranscriptStoreAppendAndLoad 验证完整会话按原顺序追加并可从磁盘恢复。
func TestTranscriptStoreAppendAndLoad(t *testing.T) {
	store, err := NewTranscriptStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	first := NewConversationMessage(RoleUser, "开始任务")
	second := NewConversationMessage(RoleAssistant, "收到")
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].ID != first.ID || loaded[1].ID != second.ID {
		t.Fatalf("loaded = %+v", loaded)
	}
}

// TestTranscriptStoreRejectsDuplicateAndCompactSummary 验证事实日志不会接受重复消息或派生摘要。
func TestTranscriptStoreRejectsDuplicateAndCompactSummary(t *testing.T) {
	store, err := NewTranscriptStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	message := NewConversationMessage(RoleUser, "开始任务")
	if err := store.Append(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), message); err == nil {
		t.Fatal("expected duplicate UUID error")
	}
	summary := NewConversationMessage(RoleUser, "会话摘要")
	summary.Kind = MessageKindCompactSummary
	if err := store.Append(context.Background(), summary); err == nil {
		t.Fatal("expected compact summary rejection")
	}
}

// TestTranscriptStoreRejectsUnsafeSessionID 验证 session ID 不能逃逸既定会话目录。
func TestTranscriptStoreRejectsUnsafeSessionID(t *testing.T) {
	if _, err := NewTranscriptStore(t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected unsafe session ID error")
	}
}
