package claudeautomemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const maxRecalledMemories = 5

// Recaller 通过索引让模型做语义筛选，再由存储层执行受控读取。
type Recaller struct {
	store    *Store
	selector MemorySelector
}

// NewRecaller 创建候选召回器，拒绝缺失的存储和选择模型。
func NewRecaller(store *Store, selector MemorySelector) (*Recaller, error) {
	if store == nil {
		return nil, errors.New("memory store is required")
	}
	if selector == nil {
		return nil, errors.New("memory selector is required")
	}
	return &Recaller{store: store, selector: selector}, nil
}

// Recall 读取最多五个唯一合法主题；失败被收集为警告而不是阻断主回答。
func (r *Recaller) Recall(ctx context.Context, query string) RecallResult {
	manifest, err := r.store.LoadManifest(ctx)
	if err != nil {
		return RecallResult{Warnings: []error{err}}
	}
	refs, err := r.selector.Select(ctx, query, manifest)
	if err != nil {
		return RecallResult{Warnings: []error{err}}
	}
	result := RecallResult{}
	seen := make(map[MemoryRef]struct{}, len(refs))
	for _, ref := range refs {
		if len(result.Records) == maxRecalledMemories {
			break
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		record, readErr := r.store.Read(ctx, ref)
		if readErr != nil {
			result.Warnings = append(result.Warnings, readErr)
			continue
		}
		result.Records = append(result.Records, record)
	}
	result.Context = renderMemoryContext(result.Records)
	return result
}

// renderMemoryContext 为每条正文附带来源和作用域，便于模型引用与运行时排查。
func renderMemoryContext(records []MemoryRecord) string {
	if len(records) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, record := range records {
		fmt.Fprintf(&builder, "<memory scope=%q source=%q type=%q>\n%s\n</memory>\n",
			record.Ref.Scope, record.Ref.Topic+".md", record.Type, strings.TrimSpace(record.Content))
	}
	return strings.TrimSpace(builder.String())
}
