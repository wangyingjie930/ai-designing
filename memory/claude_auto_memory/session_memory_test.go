package claudeautomemory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// perMessageTokenEstimator 用固定单条成本制造可预测的摘要阈值。
type perMessageTokenEstimator int

// Estimate 返回与消息数量成正比的测试 token 数。
func (e perMessageTokenEstimator) Estimate(messages []ConversationMessage) int {
	return int(e) * len(messages)
}

// fakeSessionSummarizer 记录增量输入并返回结构合法的摘要。
type fakeSessionSummarizer struct {
	mu     sync.Mutex
	inputs [][]ConversationMessage
	err    error
}

// Summarize 模拟隔离的 Session 模型更新。
func (s *fakeSessionSummarizer) Summarize(_ context.Context, current string, messages []ConversationMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, append([]ConversationMessage(nil), messages...))
	if s.err != nil {
		return "", s.err
	}
	return strings.Replace(current, "# 当前状态\n", "# 当前状态\n已处理消息数："+string(rune('0'+len(messages)))+"。\n", 1), nil
}

// Calls 返回摘要模型调用次数。
func (s *fakeSessionSummarizer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inputs)
}

// lowSessionConfig 创建适合单元测试的低阈值配置。
func lowSessionConfig() SessionMemoryConfig {
	config := DefaultSessionMemoryConfig()
	config.MinimumTokensToInit = 2
	config.MinimumTokensBetweenUpdates = 2
	config.ToolCallsBetweenUpdates = 1
	return config
}

// TestSessionUpdaterSkipsBelowInitializationThreshold 验证短会话不会制造无意义摘要。
func TestSessionUpdaterSkipsBelowInitializationThreshold(t *testing.T) {
	store, err := NewSessionStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeSessionSummarizer{}
	updater, err := NewSessionMemoryUpdater(store, model, perMessageTokenEstimator(1), lowSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	result := updater.Update(context.Background(), []ConversationMessage{NewConversationMessage(RoleUser, "短消息")})
	if result.Updated || model.Calls() != 0 {
		t.Fatalf("result = %+v calls = %d", result, model.Calls())
	}
}

// TestSessionUpdaterAdvancesOnlyAfterSuccessfulCommit 验证模型失败不会吞掉尚未摘要的 Transcript。
func TestSessionUpdaterAdvancesOnlyAfterSuccessfulCommit(t *testing.T) {
	store, err := NewSessionStore(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeSessionSummarizer{err: errors.New("model unavailable")}
	updater, err := NewSessionMemoryUpdater(store, model, perMessageTokenEstimator(1), lowSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	messages := []ConversationMessage{
		NewConversationMessage(RoleUser, "当前任务"),
		NewConversationMessage(RoleAssistant, "开始处理"),
	}
	failed := updater.Update(context.Background(), messages)
	if failed.Updated || len(failed.Warnings) != 1 {
		t.Fatalf("failed = %+v", failed)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.LastSummarizedMessageID != "" || snapshot.State.Initialized {
		t.Fatalf("state advanced after failure: %+v", snapshot.State)
	}
	model.err = nil
	retry := updater.Update(context.Background(), messages)
	if !retry.Updated || retry.SummarizedThrough != messages[1].ID {
		t.Fatalf("retry = %+v", retry)
	}
	snapshot, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.LastSummarizedMessageID != messages[1].ID || !snapshot.State.Initialized {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

// TestSessionUpdaterProcessesOnlyMessagesAfterUUIDBoundary 验证后续更新只发送新增真实消息。
func TestSessionUpdaterProcessesOnlyMessagesAfterUUIDBoundary(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	model := &fakeSessionSummarizer{}
	updater, _ := NewSessionMemoryUpdater(store, model, perMessageTokenEstimator(2), lowSessionConfig())
	messages := []ConversationMessage{
		NewConversationMessage(RoleUser, "第一轮"),
		NewConversationMessage(RoleAssistant, "回答一"),
	}
	if result := updater.Update(context.Background(), messages); !result.Updated {
		t.Fatalf("first = %+v", result)
	}
	messages = append(messages,
		NewConversationMessage(RoleUser, "第二轮"),
		NewConversationMessage(RoleAssistant, "回答二"),
	)
	if result := updater.Update(context.Background(), messages); !result.Updated || result.ProcessedMessages != 2 {
		t.Fatalf("second = %+v", result)
	}
	if len(model.inputs) != 2 || len(model.inputs[1]) != 2 || model.inputs[1][0].ID != messages[2].ID {
		t.Fatalf("inputs = %+v", model.inputs)
	}
}

// TestSessionUpdaterInitializesAtToolCallBoundary 验证首次更新也支持 token 与工具调用双阈值。
func TestSessionUpdaterInitializesAtToolCallBoundary(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	model := &fakeSessionSummarizer{}
	config := lowSessionConfig()
	config.ToolCallsBetweenUpdates = 3
	updater, _ := NewSessionMemoryUpdater(store, model, perMessageTokenEstimator(1), config)
	user := NewConversationMessage(RoleUser, "执行工具任务")
	assistant := NewConversationMessage(RoleAssistant, "正在调用工具")
	assistant.ToolCallCount = 3
	result := updater.Update(context.Background(), []ConversationMessage{user, assistant})
	if !result.Updated || model.Calls() != 1 {
		t.Fatalf("result = %+v calls = %d", result, model.Calls())
	}
}

// TestRoughTokenEstimatorCountsCompactSummary 验证 Context 阈值包含实际占窗的 Session Summary。
func TestRoughTokenEstimatorCountsCompactSummary(t *testing.T) {
	estimator := RoughTokenEstimator{}
	normal := NewConversationMessage(RoleUser, "继续")
	summary := NewConversationMessage(RoleUser, strings.Repeat("会话摘要", 40))
	summary.Kind = MessageKindCompactSummary
	withoutSummary := estimator.Estimate([]ConversationMessage{normal})
	withSummary := estimator.Estimate([]ConversationMessage{summary, normal})
	if withSummary <= withoutSummary {
		t.Fatalf("with summary = %d without summary = %d", withSummary, withoutSummary)
	}
}
