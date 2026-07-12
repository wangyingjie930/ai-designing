package claudeautomemory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newCompactorForTest 创建空闲 Scheduler 和低 Compact 阈值。
func newCompactorForTest(t *testing.T, store *SessionStore) *SessionCompactor {
	t.Helper()
	updater, err := NewSessionMemoryUpdater(store, &fakeSessionSummarizer{}, perMessageTokenEstimator(10), lowSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewSessionScheduler(updater)
	if err != nil {
		t.Fatal(err)
	}
	config := lowSessionConfig()
	config.CompactTokens = 1
	config.MinimumRecentMessages = 2
	compactor, err := NewSessionCompactor(store, scheduler, perMessageTokenEstimator(10), config)
	if err != nil {
		t.Fatal(err)
	}
	return compactor
}

// validSessionSummary 为 Compact 测试提供非空且结构合法的 Session Memory。
func validSessionSummary() string {
	return strings.Replace(defaultSessionMemoryTemplate, "# 当前状态\n", "# 当前状态\n正在实现 Compact。\n", 1)
}

// TestSessionCompactorReplacesSummarizedPrefix 验证已摘要前缀被一个摘要消息替代，Transcript 输入保持不变。
func TestSessionCompactorReplacesSummarizedPrefix(t *testing.T) {
	store, err := NewSessionStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	messages := []ConversationMessage{
		NewConversationMessage(RoleUser, "一"), NewConversationMessage(RoleAssistant, "二"),
		NewConversationMessage(RoleUser, "三"), NewConversationMessage(RoleAssistant, "四"),
	}
	state := SessionState{LastSummarizedMessageID: messages[1].ID, TokensAtLastUpdate: 20, Initialized: true}
	if err := store.Commit(context.Background(), validSessionSummary(), state); err != nil {
		t.Fatal(err)
	}
	result := newCompactorForTest(t, store).MaybeCompact(context.Background(), messages, false)
	if !result.Compacted || result.Before != 4 || result.After != 3 {
		t.Fatalf("result = %+v", result)
	}
	if result.Messages[0].Kind != MessageKindCompactSummary || !strings.Contains(result.Messages[0].Content, "正在实现 Compact") {
		t.Fatalf("summary message = %+v", result.Messages[0])
	}
	if result.Messages[1].ID != messages[2].ID || result.Messages[2].ID != messages[3].ID || len(messages) != 4 {
		t.Fatalf("messages = %+v original = %+v", result.Messages, messages)
	}
}

// TestSessionCompactorMissingBoundaryFallsBackWithoutMutation 验证普通运行时不能猜测丢失的 UUID 边界。
func TestSessionCompactorMissingBoundaryFallsBackWithoutMutation(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	if err := store.Commit(context.Background(), validSessionSummary(), SessionState{
		LastSummarizedMessageID: "missing", TokensAtLastUpdate: 20, Initialized: true,
	}); err != nil {
		t.Fatal(err)
	}
	messages := []ConversationMessage{NewConversationMessage(RoleUser, "一"), NewConversationMessage(RoleAssistant, "二")}
	result := newCompactorForTest(t, store).MaybeCompact(context.Background(), messages, false)
	if result.Compacted || len(result.Warnings) != 1 || len(result.Messages) != len(messages) {
		t.Fatalf("result = %+v", result)
	}
}

// TestSessionCompactorResumeUsesSummaryAndTailWhenBoundaryMissing 验证恢复模式可以用摘要和 Transcript 尾部重建上下文。
func TestSessionCompactorResumeUsesSummaryAndTailWhenBoundaryMissing(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	if err := store.Commit(context.Background(), validSessionSummary(), SessionState{
		LastSummarizedMessageID: "missing", TokensAtLastUpdate: 20, Initialized: true,
	}); err != nil {
		t.Fatal(err)
	}
	transcript := []ConversationMessage{
		NewConversationMessage(RoleUser, "一"), NewConversationMessage(RoleAssistant, "二"),
		NewConversationMessage(RoleUser, "三"), NewConversationMessage(RoleAssistant, "四"),
	}
	result := newCompactorForTest(t, store).MaybeCompact(context.Background(), transcript, true)
	if !result.Compacted || result.After != 3 || result.Messages[0].Kind != MessageKindCompactSummary {
		t.Fatalf("result = %+v", result)
	}
	if result.Messages[1].ID != transcript[2].ID || result.Messages[2].ID != transcript[3].ID {
		t.Fatalf("messages = %+v", result.Messages)
	}
}

// TestSessionCompactorSkipsEmptyTemplate 验证尚未提取真实状态时不会用空模板替换对话。
func TestSessionCompactorSkipsEmptyTemplate(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	messages := []ConversationMessage{NewConversationMessage(RoleUser, "一"), NewConversationMessage(RoleAssistant, "二")}
	result := newCompactorForTest(t, store).MaybeCompact(context.Background(), messages, false)
	if result.Compacted || len(result.Messages) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

// TestSessionCompactorSurfacesBackgroundUpdateWarnings 验证 Compact 消费后台结果时不会吞掉摘要失败。
func TestSessionCompactorSurfacesBackgroundUpdateWarnings(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	messages := []ConversationMessage{
		NewConversationMessage(RoleUser, "旧问题"),
		NewConversationMessage(RoleAssistant, "旧回答"),
		NewConversationMessage(RoleUser, "新问题"),
		NewConversationMessage(RoleAssistant, "新回答"),
	}
	if err := store.Commit(context.Background(), validSessionSummary(), SessionState{
		LastSummarizedMessageID: messages[1].ID, TokensAtLastUpdate: 20, Initialized: true,
	}); err != nil {
		t.Fatal(err)
	}
	config := lowSessionConfig()
	config.CompactTokens = 1
	config.MinimumRecentMessages = 1
	updater, _ := NewSessionMemoryUpdater(store, &fakeSessionSummarizer{err: errors.New("summary unavailable")}, perMessageTokenEstimator(20), config)
	scheduler, _ := NewSessionScheduler(updater)
	scheduler.Schedule(context.Background(), messages)
	compactor, _ := NewSessionCompactor(store, scheduler, perMessageTokenEstimator(20), config)
	result := compactor.MaybeCompact(context.Background(), messages, false)
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0].Error(), "summary unavailable") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}
