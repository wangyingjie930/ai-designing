package claudeautomemory

import (
	"context"
	"errors"
	"sync"
)

// Extractor 维护已处理消息游标，并把模型候选逐条交给安全存储层。
type Extractor struct {
	store  *Store
	model  MemoryExtractor
	cursor int
	mu     sync.Mutex
}

// NewExtractor 创建回答后提取器，拒绝缺失的存储或模型依赖。
func NewExtractor(store *Store, model MemoryExtractor) (*Extractor, error) {
	if store == nil {
		return nil, errors.New("memory store is required")
	}
	if model == nil {
		return nil, errors.New("memory extractor model is required")
	}
	return &Extractor{store: store, model: model}, nil
}

// ExtractNew 只处理游标之后的新消息；模型调用失败时保留游标以便重试。
func (e *Extractor) ExtractNew(ctx context.Context, history []ConversationMessage) ExtractionResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cursor > len(history) {
		return ExtractionResult{Warnings: []error{errors.New("conversation history is shorter than extraction cursor")}}
	}
	if e.cursor == len(history) {
		return ExtractionResult{}
	}
	batch := append([]ConversationMessage(nil), history[e.cursor:]...)
	candidates, err := e.model.Extract(ctx, batch)
	if err != nil {
		return ExtractionResult{Warnings: []error{err}}
	}
	result := ExtractionResult{ProcessedMessages: len(batch)}
	for _, candidate := range candidates {
		record, writeErr := e.store.Upsert(ctx, candidate)
		if writeErr != nil {
			result.Warnings = append(result.Warnings, writeErr)
			continue
		}
		result.Written = append(result.Written, record)
	}
	// 整个模型批次已经完成后才推进，单条坏候选不会让旧消息被无限重复提取。
	e.cursor = len(history)
	return result
}

// Cursor 返回当前已处理消息数量，主要用于生命周期诊断和测试。
func (e *Extractor) Cursor() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursor
}
