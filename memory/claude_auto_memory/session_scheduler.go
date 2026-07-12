package claudeautomemory

import (
	"context"
	"errors"
	"sync"
)

// scheduledSessionUpdate 保存一次后台摘要所需的不可变 Transcript 快照。
type scheduledSessionUpdate struct {
	ctx        context.Context
	transcript []ConversationMessage
}

// SessionScheduler 串行维护 Session Summary，繁忙时只保留最新 Transcript。
type SessionScheduler struct {
	updater *SessionMemoryUpdater

	mu        sync.Mutex
	running   bool
	pending   *scheduledSessionUpdate
	idle      chan struct{}
	completed []SessionUpdateResult
}

// NewSessionScheduler 创建与 Auto Memory 调度器互不共享状态的后台队列。
func NewSessionScheduler(updater *SessionMemoryUpdater) (*SessionScheduler, error) {
	if updater == nil {
		return nil, errors.New("session memory updater is required")
	}
	idle := make(chan struct{})
	close(idle)
	return &SessionScheduler{updater: updater, idle: idle}, nil
}

// Schedule 复制完整 Transcript 并立即返回；请求取消不会中断已经提交的摘要维护。
func (s *SessionScheduler) Schedule(ctx context.Context, transcript []ConversationMessage) {
	if len(transcript) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request := scheduledSessionUpdate{
		ctx: context.WithoutCancel(ctx), transcript: append([]ConversationMessage(nil), transcript...),
	}
	s.mu.Lock()
	if s.running {
		s.pending = &request
		s.mu.Unlock()
		return
	}
	s.running = true
	s.idle = make(chan struct{})
	s.mu.Unlock()
	go s.run(request)
}

// Wait 等待当前任务和 trailing update，并原子取走后台结果。
func (s *SessionScheduler) Wait(ctx context.Context) (SessionDrainResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return SessionDrainResult{}, ctx.Err()
	case <-idle:
	}
	s.mu.Lock()
	completed := append([]SessionUpdateResult(nil), s.completed...)
	s.completed = nil
	s.mu.Unlock()
	return mergeSessionUpdateResults(completed), nil
}

// run 在单个 goroutine 中依次处理当前请求和最新 trailing Transcript。
func (s *SessionScheduler) run(request scheduledSessionUpdate) {
	for {
		result := s.updater.Update(request.ctx, request.transcript)
		s.mu.Lock()
		s.completed = append(s.completed, result)
		if s.pending != nil {
			request = *s.pending
			s.pending = nil
			s.mu.Unlock()
			continue
		}
		s.running = false
		close(s.idle)
		s.mu.Unlock()
		return
	}
}

// mergeSessionUpdateResults 折叠多批摘要结果，保留最后成功的 UUID 边界。
func mergeSessionUpdateResults(results []SessionUpdateResult) SessionDrainResult {
	merged := SessionDrainResult{Batches: len(results)}
	for _, result := range results {
		if result.Updated {
			merged.Updates++
			merged.SummarizedThrough = result.SummarizedThrough
		}
		merged.ProcessedMessages += result.ProcessedMessages
		merged.Warnings = append(merged.Warnings, result.Warnings...)
	}
	return merged
}
