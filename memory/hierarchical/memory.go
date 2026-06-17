package hierarchical

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// HierarchicalMemory 实现 working/session/long-term 三层记忆，以及预算驱动的提升和淘汰。
type HierarchicalMemory struct {
	store         *sqliteVectorStore
	scope         string
	workingBudget int
	working       []MemoryEntry
	session       []MemoryEntry
	now           func() time.Time
	mu            sync.Mutex
}

// NewHierarchicalMemory 初始化 SQLite long-term store，并创建当前进程内的 working/session tier。
func NewHierarchicalMemory(ctx context.Context, config Config) (*HierarchicalMemory, error) {
	scope := strings.TrimSpace(config.Scope)
	if scope == "" {
		scope = defaultScope
	}
	workingBudget := config.WorkingBudget
	if workingBudget <= 0 {
		workingBudget = defaultWorkingBudget
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	store, err := openSQLiteVectorStore(ctx, config.DBPath, scope, config.Embedder)
	if err != nil {
		return nil, err
	}
	return &HierarchicalMemory{
		store:         store,
		scope:         scope,
		workingBudget: workingBudget,
		now:           now,
	}, nil
}

// Add 对应 Python add()，新信息先进入 working tier，然后按预算触发 session 淘汰。
func (m *HierarchicalMemory) Add(_ context.Context, req AddRequest) (*AddResponse, error) {
	if m == nil {
		return nil, errors.New("hierarchical memory is nil")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, errors.New("content is required")
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		return nil, errors.New("source is required")
	}
	now := m.now()
	entry := MemoryEntry{
		Content:      content,
		Tier:         MemoryTierWorking,
		Source:       source,
		CreatedAt:    now,
		LastAccessed: now,
		Importance:   requestImportance(req.Importance),
		TokenCount:   estimateTokenCount(content),
		Metadata:     req.Metadata,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.working = append(m.working, entry)
	m.enforceBudgetLocked(now)
	return &AddResponse{
		Entry:             cloneEntry(entry),
		WorkingTokenCount: m.workingTokenCountLocked(),
	}, nil
}

// Retrieve 对应 Python retrieve()，先查 SQLite long-term，再把召回内容提升进 working tier。
func (m *HierarchicalMemory) Retrieve(ctx context.Context, req RetrieveRequest) (*RetrieveResponse, error) {
	if m == nil {
		return nil, errors.New("hierarchical memory is nil")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if req.K < 0 {
		return nil, errors.New("k must be non-negative")
	}
	k := req.K
	if k == 0 {
		k = defaultRetrieveTopK
	}
	now := m.now()
	results, err := m.store.search(ctx, query, k, now)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, result := range results {
		promoted := MemoryEntry{
			Content:      result.Text,
			Tier:         MemoryTierWorking,
			Source:       longTermRetrievalSource,
			CreatedAt:    now,
			LastAccessed: now,
			Importance:   clampImportance(result.Score),
			TokenCount:   estimateTokenCount(result.Text),
			Metadata: map[string]any{
				"longterm_id":     result.Entry.ID,
				"longterm_source": result.Entry.Source,
				"score":           result.Score,
			},
		}
		m.working = append(m.working, promoted)
	}
	m.enforceBudgetLocked(now)
	return &RetrieveResponse{
		Results:           cloneSearchResults(results),
		WorkingTokenCount: m.workingTokenCountLocked(),
	}, nil
}

// Context 返回当前 working/session 快照，给 Agent 作为回答前的短期上下文检查点。
func (m *HierarchicalMemory) Context() ContextResponse {
	if m == nil {
		return ContextResponse{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return ContextResponse{
		Scope:             m.scope,
		Working:           cloneEntries(m.working),
		Session:           cloneEntries(m.session),
		WorkingTokenCount: m.workingTokenCountLocked(),
		WorkingBudget:     m.workingBudget,
	}
}

// LongTerm 返回当前 scope 下已经持久化的长期记忆，供 CLI 展示记忆迁移结果。
func (m *HierarchicalMemory) LongTerm(ctx context.Context, limit int) ([]MemoryEntry, error) {
	if m == nil {
		return nil, errors.New("hierarchical memory is nil")
	}
	entries, err := m.store.listLongTerm(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, cloneEntry(entry.Entry))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Consolidate 对应 Python consolidate()，把重要或 reflection 来源的记忆写入 SQLite long-term。
func (m *HierarchicalMemory) Consolidate(ctx context.Context) (*ConsolidateResponse, error) {
	if m == nil {
		return nil, errors.New("hierarchical memory is nil")
	}
	candidates := m.consolidationCandidates()
	now := m.now()
	persisted := make([]MemoryEntry, 0, len(candidates))
	for _, candidate := range candidates {
		entry, err := m.store.upsertLongTerm(ctx, candidate, now)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, entry)
	}
	return &ConsolidateResponse{
		Persisted: len(persisted),
		Entries:   cloneEntries(persisted),
	}, nil
}

// Path 返回 SQLite 文件路径，方便确认长期记忆写到了哪里。
func (m *HierarchicalMemory) Path() string {
	if m == nil || m.store == nil {
		return ""
	}
	return m.store.storePath()
}

// Scope 返回当前隔离 scope，避免多个业务场景混在同一个长期记忆集合里。
func (m *HierarchicalMemory) Scope() string {
	if m == nil {
		return ""
	}
	return m.scope
}

// WorkingBudget 返回当前 working tier 的 token 预算。
func (m *HierarchicalMemory) WorkingBudget() int {
	if m == nil {
		return 0
	}
	return m.workingBudget
}

// Close 释放 SQLite 连接。
func (m *HierarchicalMemory) Close() error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.close()
}

// consolidationCandidates 在内存里筛选需要持久化的记忆，并按 scope/source/content 去重。
func (m *HierarchicalMemory) consolidationCandidates() []MemoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	candidates := make([]MemoryEntry, 0, len(m.session)+len(m.working))
	for _, entry := range append(cloneEntries(m.session), cloneEntries(m.working)...) {
		if entry.Source == longTermRetrievalSource {
			continue
		}
		if entry.Importance <= persistImportanceThreshold && entry.Source != "reflection" {
			continue
		}
		key := contentHash(m.scope, entry.Source, entry.Content)
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, entry)
	}
	return candidates
}

// enforceBudgetLocked 在持锁状态下执行 Python _enforce_budget() 的优先级淘汰逻辑。
func (m *HierarchicalMemory) enforceBudgetLocked(now time.Time) {
	total := m.workingTokenCountLocked()
	if total <= m.workingBudget {
		return
	}
	sort.SliceStable(m.working, func(i, j int) bool {
		return evictionScore(m.working[i], now) < evictionScore(m.working[j], now)
	})
	for total > m.workingBudget && len(m.working) > 0 {
		evicted := m.working[0]
		m.working = m.working[1:]
		evicted.Tier = MemoryTierSession
		m.session = append(m.session, evicted)
		total -= evicted.TokenCount
	}
}

// evictionScore 复刻 Python 示例的 importance/recency/access_count 综合分。
func evictionScore(entry MemoryEntry, now time.Time) float64 {
	recency := 1.0 / (1.0 + now.Sub(entry.LastAccessed).Hours())
	if recency < 0 {
		recency = 0
	}
	accessScore := float64(entry.AccessCount) / 10.0
	if accessScore > 1 {
		accessScore = 1
	}
	return entry.Importance*0.5 + recency*0.3 + accessScore*0.2
}

// workingTokenCountLocked 汇总 working tier 预算占用，调用方必须已经持锁。
func (m *HierarchicalMemory) workingTokenCountLocked() int {
	var total int
	for _, entry := range m.working {
		total += entry.TokenCount
	}
	return total
}

// cloneSearchResults 深拷贝召回结果，保护内部 entry metadata。
func cloneSearchResults(results []SearchResult) []SearchResult {
	out := make([]SearchResult, len(results))
	for idx, result := range results {
		out[idx] = result
		out[idx].Entry = cloneEntry(result.Entry)
	}
	return out
}

// DebugString 返回简短运行态摘要，便于测试失败时输出关键状态。
func (m *HierarchicalMemory) DebugString() string {
	if m == nil {
		return ""
	}
	context := m.Context()
	return fmt.Sprintf("scope=%s working=%d session=%d tokens=%d/%d",
		context.Scope,
		len(context.Working),
		len(context.Session),
		context.WorkingTokenCount,
		context.WorkingBudget,
	)
}
