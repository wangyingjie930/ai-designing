package claudeautomemory

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingSessionSummarizer 阻塞第一次摘要，便于验证主链路和 latest-wins。
type blockingSessionSummarizer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

// Summarize 第一次等待测试释放，后续 trailing 调用立即完成。
func (s *blockingSessionSummarizer) Summarize(_ context.Context, current string, _ []ConversationMessage) (string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		s.once.Do(func() { close(s.started) })
		<-s.release
	}
	return strings.Replace(current, "# 当前状态\n", "# 当前状态\n后台摘要已更新。\n", 1), nil
}

// TestSessionSchedulerReturnsBeforeBlockedSummary 验证 Schedule 不把摘要模型延迟带入主回答。
func TestSessionSchedulerReturnsBeforeBlockedSummary(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	model := &blockingSessionSummarizer{started: make(chan struct{}), release: make(chan struct{})}
	updater, _ := NewSessionMemoryUpdater(store, model, perMessageTokenEstimator(2), lowSessionConfig())
	scheduler, _ := NewSessionScheduler(updater)
	messages := []ConversationMessage{NewConversationMessage(RoleUser, "任务"), NewConversationMessage(RoleAssistant, "回答")}
	returned := make(chan struct{})
	go func() {
		scheduler.Schedule(context.Background(), messages)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("Schedule blocked")
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("summary did not start")
	}
	close(model.release)
	if _, err := scheduler.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestSessionSchedulerCoalescesToLatestTranscript 验证繁忙时只处理最新完整 Transcript 快照。
func TestSessionSchedulerCoalescesToLatestTranscript(t *testing.T) {
	store, _ := NewSessionStore(t.TempDir(), "session-1")
	model := &blockingSessionSummarizer{started: make(chan struct{}), release: make(chan struct{})}
	updater, _ := NewSessionMemoryUpdater(store, model, perMessageTokenEstimator(2), lowSessionConfig())
	scheduler, _ := NewSessionScheduler(updater)
	transcript := []ConversationMessage{
		NewConversationMessage(RoleUser, "一"), NewConversationMessage(RoleAssistant, "二"),
		NewConversationMessage(RoleUser, "三"), NewConversationMessage(RoleAssistant, "四"),
		NewConversationMessage(RoleUser, "五"), NewConversationMessage(RoleAssistant, "六"),
	}
	scheduler.Schedule(context.Background(), transcript[:2])
	<-model.started
	scheduler.Schedule(context.Background(), transcript[:4])
	scheduler.Schedule(context.Background(), transcript)
	close(model.release)
	drained, err := scheduler.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if drained.Batches != 2 || drained.Updates != 2 || drained.ProcessedMessages != 6 {
		t.Fatalf("drained = %+v", drained)
	}
}
