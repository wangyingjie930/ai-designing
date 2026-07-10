package claudeautomemory

import (
	"context"
	"errors"
	"sync"
)

// scheduledExtraction 保存一次后台提取所需的不可变会话快照和上下文。
type scheduledExtraction struct {
	ctx     context.Context
	history []ConversationMessage
}

// ExtractionScheduler 串行执行后台提取，并在繁忙时只保留最新快照。
type ExtractionScheduler struct {
	extractor *Extractor

	mu        sync.Mutex
	running   bool
	pending   *scheduledExtraction
	idle      chan struct{}
	completed []ExtractionResult
}

// NewExtractionScheduler 创建初始为空闲态的后台提取调度器。
func NewExtractionScheduler(extractor *Extractor) (*ExtractionScheduler, error) {
	if extractor == nil {
		return nil, errors.New("memory extractor is required")
	}
	idle := make(chan struct{})
	close(idle)
	return &ExtractionScheduler{extractor: extractor, idle: idle}, nil
}

// Schedule 复制当前会话并立即返回；繁忙时用最新快照覆盖旧 pending。
func (s *ExtractionScheduler) Schedule(ctx context.Context, history []ConversationMessage) {
	if len(history) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request := scheduledExtraction{
		// 回答返回后调用方可能取消请求；后台记忆保留 trace/value，但不继承取消信号。
		ctx:     context.WithoutCancel(ctx),
		history: append([]ConversationMessage(nil), history...),
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

// Drain 等待当前任务和 trailing extraction，并原子取走已完成结果。
func (s *ExtractionScheduler) Drain(ctx context.Context) (DrainResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return DrainResult{}, ctx.Err()
	case <-idle:
	}

	s.mu.Lock()
	completed := append([]ExtractionResult(nil), s.completed...)
	s.completed = nil
	s.mu.Unlock()
	return mergeExtractionResults(completed), nil
}

// run 在单个 goroutine 中依次处理当前请求和至多一个最新 pending 快照。
func (s *ExtractionScheduler) run(request scheduledExtraction) {
	for {
		result := s.extractor.ExtractNew(request.ctx, request.history)
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

// mergeExtractionResults 把多个后台批次折叠成 CLI 和测试可消费的稳定摘要。
func mergeExtractionResults(results []ExtractionResult) DrainResult {
	merged := DrainResult{Batches: len(results)}
	for _, result := range results {
		merged.ProcessedMessages += result.ProcessedMessages
		merged.Written = append(merged.Written, result.Written...)
		merged.Warnings = append(merged.Warnings, result.Warnings...)
	}
	return merged
}
