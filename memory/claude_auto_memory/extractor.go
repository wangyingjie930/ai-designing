package claudeautomemory

import (
	"context"
	"errors"
	"sync"
)

// Extractor 维护已处理消息游标，并把模型候选逐条交给安全存储层。
type Extractor struct {
	store                  *Store
	model                  MemoryExtractor
	lastExtractedMessageID string
	mu                     sync.Mutex
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
	batch, lastMessageID, err := messagesAfterCursor(history, e.lastExtractedMessageID)
	if err != nil {
		return ExtractionResult{Warnings: []error{err}}
	}
	if len(batch) == 0 {
		return ExtractionResult{}
	}
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
	e.lastExtractedMessageID = lastMessageID
	return result
}

// Cursor 返回最后成功处理的真实消息 UUID，主要用于生命周期诊断和测试。
func (e *Extractor) Cursor() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastExtractedMessageID
}

// messagesAfterCursor 从完整 Transcript 中选择游标之后的真实消息，并排除 Compact 摘要。
func messagesAfterCursor(history []ConversationMessage, cursor string) ([]ConversationMessage, string, error) {
	start := 0
	if cursor != "" {
		start = -1
		for index, message := range history {
			if message.ID == cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, "", errors.New("extraction cursor is missing from transcript")
		}
	}
	batch := make([]ConversationMessage, 0, len(history)-start)
	lastMessageID := cursor
	for _, message := range history[start:] {
		// 空 Kind 只用于兼容旧测试和旧调用方；新建消息始终显式标记 normal。
		if message.Kind != "" && message.Kind != MessageKindNormal {
			continue
		}
		if message.ID == "" {
			return nil, "", errors.New("conversation message ID is required for extraction")
		}
		batch = append(batch, message)
		lastMessageID = message.ID
	}
	return batch, lastMessageID, nil
}
