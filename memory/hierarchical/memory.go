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

// Retrieve 先查 SQLite long-term 和内部 session，再把召回内容提升回 working tier。
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
	longTermResults, err := m.store.search(ctx, query, k, now)
	if err != nil {
		return nil, err
	}
	sessionResults, err := m.searchSession(ctx, query, k)
	if err != nil {
		return nil, err
	}
	results := mergeSearchResults(longTermResults, sessionResults, k)

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, result := range results {
		promoted := m.promotedEntry(result, now)
		if result.Entry.Tier == MemoryTierSession {
			m.removeSessionEntryLocked(result.Entry)
		}
		m.upsertWorkingLocked(promoted)
	}
	m.enforceBudgetLocked(now)
	return &RetrieveResponse{
		Results:           cloneSearchResults(results),
		WorkingTokenCount: m.workingTokenCountLocked(),
	}, nil
}

// searchSession 在 session 缓冲中做轻量向量召回，命中的条目随后会迁回 working。
func (m *HierarchicalMemory) searchSession(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	session := m.sessionSnapshot()
	if len(session) == 0 {
		return nil, nil
	}
	queryVector, err := m.store.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(session))
	for _, entry := range session {
		vector, err := m.store.embedder.Embed(ctx, entry.Content)
		if err != nil {
			return nil, err
		}
		score := cosineSimilarity(queryVector, vector)
		entry.Tier = MemoryTierSession
		results = append(results, SearchResult{
			Entry: addTierName(entry),
			Text:  entry.Content,
			Score: score,
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}
	return results, nil
}

// sessionSnapshot 拷贝 session，避免 embedding 调用期间持有 memory 锁。
func (m *HierarchicalMemory) sessionSnapshot() []MemoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneEntries(m.session)
}

// promotedEntry 把召回结果转换成 working memory 条目。
func (m *HierarchicalMemory) promotedEntry(result SearchResult, now time.Time) MemoryEntry {
	if result.Entry.Tier == MemoryTierSession {
		entry := cloneEntry(result.Entry)
		entry.Tier = MemoryTierWorking
		entry.LastAccessed = now
		entry.AccessCount++
		if result.Score > entry.Importance {
			entry.Importance = clampImportance(result.Score)
		}
		if entry.Metadata == nil {
			entry.Metadata = map[string]any{}
		}
		entry.Metadata["retrieval_score"] = result.Score
		entry.Metadata["retrieval_source"] = "session"
		return entry
	}
	return MemoryEntry{
		Content:      result.Text,
		Tier:         MemoryTierWorking,
		Source:       longTermRetrievalSource,
		CreatedAt:    now,
		LastAccessed: now,
		AccessCount:  1,
		Importance:   clampImportance(result.Score),
		TokenCount:   estimateTokenCount(result.Text),
		Metadata: map[string]any{
			"longterm_id":     result.Entry.ID,
			"longterm_source": result.Entry.Source,
			"score":           result.Score,
		},
	}
}

// Context 返回当前 working/session 快照，供 CLI 和内部调试观察三层记忆状态。
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

// consolidationCandidate 标记一条待持久化记忆来自 working 还是 session。
type consolidationCandidate struct {
	Entry       MemoryEntry
	FromSession bool
	Key         string
}

// Consolidate 对应 Python consolidate()，把重要或 reflection 来源的记忆写入 SQLite long-term。
// session 中成功持久化的条目会被移出 session，形成 session -> long-term 的迁移闭环。
func (m *HierarchicalMemory) Consolidate(ctx context.Context) (*ConsolidateResponse, error) {
	if m == nil {
		return nil, errors.New("hierarchical memory is nil")
	}
	candidates := m.consolidationCandidates()
	now := m.now()
	persisted := make([]MemoryEntry, 0, len(candidates))
	persistedSessionKeys := make(map[string]bool)
	for _, candidate := range candidates {
		entry, err := m.store.upsertLongTerm(ctx, candidate.Entry, now)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, entry)
		if candidate.FromSession {
			persistedSessionKeys[candidate.Key] = true
		}
	}
	if len(persistedSessionKeys) > 0 {
		m.removeSessionEntries(persistedSessionKeys)
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
// session 先于 working 参与筛选，让被淘汰的重要记忆优先迁移到长期记忆。
func (m *HierarchicalMemory) consolidationCandidates() []consolidationCandidate {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	candidates := make([]consolidationCandidate, 0, len(m.session)+len(m.working))
	appendCandidate := func(entry MemoryEntry, fromSession bool) {
		if entry.Source == longTermRetrievalSource {
			return
		}
		if entry.Importance <= persistImportanceThreshold && entry.Source != "reflection" {
			return
		}
		key := contentHash(m.scope, entry.Source, entry.Content)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, consolidationCandidate{
			Entry:       entry,
			FromSession: fromSession,
			Key:         key,
		})
	}
	for _, entry := range cloneEntries(m.session) {
		appendCandidate(entry, true)
	}
	for _, entry := range cloneEntries(m.working) {
		appendCandidate(entry, false)
	}
	return candidates
}

// removeSessionEntries 删除已经成功持久化到 long-term 的 session 条目。
func (m *HierarchicalMemory) removeSessionEntries(keys map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.session[:0]
	for _, entry := range m.session {
		key := contentHash(m.scope, entry.Source, entry.Content)
		if keys[key] {
			continue
		}
		next = append(next, entry)
	}
	m.session = next
}

// removeSessionEntryLocked 在持锁状态下把命中的 session 记忆移出缓冲区。
func (m *HierarchicalMemory) removeSessionEntryLocked(target MemoryEntry) {
	targetKey := contentHash(m.scope, target.Source, target.Content)
	next := m.session[:0]
	for _, entry := range m.session {
		key := contentHash(m.scope, entry.Source, entry.Content)
		if key == targetKey {
			continue
		}
		next = append(next, entry)
	}
	m.session = next
}

// upsertWorkingLocked 在持锁状态下把召回记忆放入 working，已有同源同内容时只刷新访问态。
func (m *HierarchicalMemory) upsertWorkingLocked(entry MemoryEntry) {
	key := m.workingKey(entry)
	entry = cloneEntry(entry)
	for idx := range m.working {
		if m.workingKey(m.working[idx]) != key {
			continue
		}
		m.working[idx].LastAccessed = entry.LastAccessed
		m.working[idx].AccessCount += entry.AccessCount
		if entry.Importance > m.working[idx].Importance {
			m.working[idx].Importance = entry.Importance
		}
		if entry.Metadata != nil {
			m.working[idx].Metadata = entry.Metadata
		}
		return
	}
	m.working = append(m.working, entry)
}

// workingKey 生成 working 去重使用的稳定 key。
func (m *HierarchicalMemory) workingKey(entry MemoryEntry) string {
	return contentHash(m.scope, entry.Source, entry.Content)
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

// mergeSearchResults 合并长期记忆和 session 召回结果，并按分数截断到 topK。
func mergeSearchResults(longTerm []SearchResult, session []SearchResult, topK int) []SearchResult {
	combined := make([]SearchResult, 0, len(longTerm)+len(session))
	seen := map[string]bool{}
	appendResult := func(result SearchResult) {
		key := result.Entry.Source + "\x00" + result.Text
		if seen[key] {
			return
		}
		seen[key] = true
		combined = append(combined, result)
	}
	for _, result := range longTerm {
		appendResult(result)
	}
	for _, result := range session {
		appendResult(result)
	}
	sort.SliceStable(combined, func(i, j int) bool {
		return combined[i].Score > combined[j].Score
	})
	if topK > 0 && topK < len(combined) {
		combined = combined[:topK]
	}
	return combined
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
