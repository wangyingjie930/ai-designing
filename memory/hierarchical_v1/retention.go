package hierarchicalv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// HierarchicalMemory 持有五层记忆条目和每层策略，负责写入校验、scratchpad 提升和预算组装。
type HierarchicalMemory struct {
	entries  map[string]MemoryEntry
	policies map[MemoryLayer]LayerPolicy
	store    *sqliteStore
	now      func() time.Time
	mu       sync.Mutex
}

// NewHierarchicalMemory 初始化五层策略和内存态条目表。
func NewHierarchicalMemory(config Config) (*HierarchicalMemory, error) {
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	memory := &HierarchicalMemory{
		entries:  map[string]MemoryEntry{},
		policies: mergePolicies(config.Policies),
		now:      now,
	}
	if strings.TrimSpace(config.DBPath) != "" {
		store, err := openSQLiteStore(context.Background(), config.DBPath)
		if err != nil {
			return nil, err
		}
		memory.store = store
		entries, err := store.loadEntries(context.Background())
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		for _, entry := range entries {
			if entry.Layer == MemoryLayerScratchpad {
				continue
			}
			memory.entries[entry.Key] = entry
		}
	}
	return memory, nil
}

// WriteEntry 对应附件 write(entry)，按 layer policy 校验写权限和证据要求。
func (h *HierarchicalMemory) WriteEntry(entry MemoryEntry) (MemoryEntry, error) {
	if h == nil {
		return MemoryEntry{}, errors.New("hierarchical memory is nil")
	}
	normalized, err := h.normalizeEntry(entry)
	if err != nil {
		return MemoryEntry{}, err
	}
	policy, ok := h.policies[normalized.Layer]
	if !ok {
		return MemoryEntry{}, fmt.Errorf("policy for layer %s is not configured", normalized.Layer)
	}
	if !policy.AllowAgentWrite && normalized.Source == MemorySourceAgentInference {
		return MemoryEntry{}, fmt.Errorf("agent cannot write directly to %s", normalized.Layer)
	}
	if policy.RequireEvidence && !normalized.IsVerified() {
		return MemoryEntry{}, fmt.Errorf("%s memory requires evidence", normalized.Layer)
	}
	if h.shouldPersist(normalized) {
		if err := h.store.upsertEntry(context.Background(), normalized); err != nil {
			return MemoryEntry{}, err
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries[normalized.Key] = normalized
	return cloneEntry(normalized), nil
}

// Close 关闭 SQLite 连接；纯内存运行时调用也安全。
func (h *HierarchicalMemory) Close() error {
	if h == nil || h.store == nil {
		return nil
	}
	return h.store.Close()
}

// DBPath 返回当前 SQLite 文件路径，空字符串表示只使用内存态。
func (h *HierarchicalMemory) DBPath() string {
	if h == nil || h.store == nil {
		return ""
	}
	return h.store.Path()
}

// Write 是 ADK tool 使用的写入边界，把 tool 请求转换成 MemoryEntry。
func (h *HierarchicalMemory) Write(_ context.Context, req WriteRequest) (*WriteResponse, error) {
	entry := MemoryEntry{
		Key:           req.Key,
		Value:         cloneAny(req.Value),
		Layer:         req.Layer,
		Source:        req.Source,
		EvidenceRefs:  append([]string(nil), req.EvidenceRefs...),
		TokenEstimate: req.TokenEstimate,
	}
	if req.Confidence != nil {
		entry.Confidence = *req.Confidence
	}
	if strings.TrimSpace(req.ValidUntil) != "" {
		validUntil := strings.TrimSpace(req.ValidUntil)
		entry.ValidUntil = &validUntil
	}
	written, err := h.WriteEntry(entry)
	if err != nil {
		return nil, err
	}
	return &WriteResponse{Entry: written}, nil
}

// ProposeFromScratchpad 对应附件 propose_from_scratchpad，只生成候选条目，不直接写入目标层。
func (h *HierarchicalMemory) ProposeFromScratchpad(entry MemoryEntry, targetLayer MemoryLayer) (MemoryEntry, error) {
	if h == nil {
		return MemoryEntry{}, errors.New("hierarchical memory is nil")
	}
	if entry.Layer != MemoryLayerScratchpad {
		return MemoryEntry{}, errors.New("only scratchpad entries can be promoted")
	}
	if !targetLayer.Valid() {
		return MemoryEntry{}, fmt.Errorf("invalid target layer: %s", targetLayer)
	}
	now := formatMemoryTime(h.now())
	proposed := MemoryEntry{
		Key:           entry.Key,
		Value:         cloneAny(entry.Value),
		Layer:         targetLayer,
		Source:        MemorySourceVerifiedTrace,
		EvidenceRefs:  append([]string(nil), entry.EvidenceRefs...),
		Confidence:    entry.Confidence,
		TokenEstimate: entry.TokenEstimate,
		ValidFrom:     now,
		CreatedAt:     now,
	}
	return proposed, nil
}

// PromoteScratchpadByKey 供 ADK tool 使用：验证证据、工程路由目标层，并写回正式记忆。
func (h *HierarchicalMemory) PromoteScratchpadByKey(_ context.Context, req PromoteScratchpadRequest) (*PromoteScratchpadResponse, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return nil, errors.New("key is required")
	}
	entry, ok := h.EntryByKey(key)
	if !ok {
		return nil, fmt.Errorf("scratchpad entry %q not found", key)
	}
	if entry.Layer != MemoryLayerScratchpad {
		return nil, errors.New("only scratchpad entries can be promoted")
	}
	evidenceRefs := mergeEvidenceRefs(entry.EvidenceRefs, req.EvidenceRefs)
	if len(evidenceRefs) == 0 {
		return nil, errors.New("scratchpad promotion requires evidence")
	}
	targetLayer := routeScratchpadPromotion(entry)
	proposed, err := h.ProposeFromScratchpad(entry, targetLayer)
	if err != nil {
		return nil, err
	}
	proposed.EvidenceRefs = evidenceRefs
	written, err := h.WriteEntry(proposed)
	if err != nil {
		return nil, err
	}
	return &PromoteScratchpadResponse{Entry: written}, nil
}

// AssembleContext 对应附件 assemble_context，按固定层顺序、层预算和置信度选出可见记忆。
func (h *HierarchicalMemory) AssembleContext() []MemoryEntry {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	now := h.now()
	nowText := formatMemoryTime(now)
	selected := make([]MemoryEntry, 0, len(h.entries))
	for _, layer := range assembleOrder {
		policy := h.policies[layer]
		used := 0
		keys := h.sortedLayerKeysLocked(layer, now)
		for _, key := range keys {
			entry := h.entries[key]
			if used+entry.TokenEstimate > policy.TokenBudget {
				continue
			}
			entry.LastAccessedAt = &nowText
			h.entries[key] = entry
			selected = append(selected, cloneEntry(entry))
			used += entry.TokenEstimate
		}
	}
	return selected
}

// AssemblePromptContext 给 ADK tool 和 CLI 返回可读上下文文本。
func (h *HierarchicalMemory) AssemblePromptContext() (*AssembleContextResponse, error) {
	entries := h.AssembleContext()
	contextText, err := renderContext(entries)
	if err != nil {
		return nil, err
	}
	return &AssembleContextResponse{Entries: entries, Context: contextText}, nil
}

// AssembleContextTool 是 ADK tool 使用的上下文组装边界。
func (h *HierarchicalMemory) AssembleContextTool(context.Context, AssembleContextRequest) (*AssembleContextResponse, error) {
	return h.AssemblePromptContext()
}

// HealthReport 对应附件 health_report，输出每层策略和条目数量。
func (h *HierarchicalMemory) HealthReport() HealthReport {
	if h == nil {
		return HealthReport{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	report := HealthReport{Layers: map[string]LayerHealth{}}
	for _, layer := range assembleOrder {
		policy := h.policies[layer]
		report.Layers[layer.String()] = LayerHealth{
			Backend:         policy.Backend,
			TokenBudget:     policy.TokenBudget,
			TTLSeconds:      ttlSeconds(policy.TTL),
			AllowAgentWrite: policy.AllowAgentWrite,
			RequireEvidence: policy.RequireEvidence,
			EntryCount:      h.entryCountLocked(layer),
		}
	}
	return report
}

// shouldPersist 判断一条记忆是否需要落 SQLite；scratchpad 是明确的进程内临时层。
func (h *HierarchicalMemory) shouldPersist(entry MemoryEntry) bool {
	return h != nil && h.store != nil && entry.Layer != MemoryLayerScratchpad
}

// Health 是 ADK tool 使用的健康状态读取边界。
func (h *HierarchicalMemory) Health(context.Context, HealthReportRequest) (*HealthReport, error) {
	report := h.HealthReport()
	return &report, nil
}

// EntryByKey 返回指定 key 的条目副本，主要供 CLI、测试和 scratchpad promotion 使用。
func (h *HierarchicalMemory) EntryByKey(key string) (MemoryEntry, bool) {
	if h == nil {
		return MemoryEntry{}, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return MemoryEntry{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.entries[key]
	if !ok {
		return MemoryEntry{}, false
	}
	return cloneEntry(entry), true
}

// Entries 返回当前所有条目的副本，便于测试和命令行观察。
func (h *HierarchicalMemory) Entries() []MemoryEntry {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]MemoryEntry, 0, len(h.entries))
	for _, entry := range h.entries {
		out = append(out, cloneEntry(entry))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})
	return out
}

// DefaultPolicies 返回附件代码里的五层默认策略。
func DefaultPolicies() map[MemoryLayer]LayerPolicy {
	taskTTL := 7 * 24 * time.Hour
	scratchpadTTL := 2 * time.Hour
	return map[MemoryLayer]LayerPolicy{
		MemoryLayerPolicy: {
			Layer:           MemoryLayerPolicy,
			TokenBudget:     1200,
			TTL:             nil,
			AllowAgentWrite: false,
			RequireEvidence: true,
			Backend:         "managed_file",
		},
		MemoryLayerProject: {
			Layer:           MemoryLayerProject,
			TokenBudget:     3000,
			TTL:             nil,
			AllowAgentWrite: false,
			RequireEvidence: true,
			Backend:         "git_file",
		},
		MemoryLayerUser: {
			Layer:           MemoryLayerUser,
			TokenBudget:     1500,
			TTL:             nil,
			AllowAgentWrite: true,
			RequireEvidence: true,
			Backend:         "postgres",
		},
		MemoryLayerTask: {
			Layer:           MemoryLayerTask,
			TokenBudget:     5000,
			TTL:             &taskTTL,
			AllowAgentWrite: true,
			RequireEvidence: true,
			Backend:         "checkpointer",
		},
		MemoryLayerScratchpad: {
			Layer:           MemoryLayerScratchpad,
			TokenBudget:     2500,
			TTL:             &scratchpadTTL,
			AllowAgentWrite: true,
			RequireEvidence: false,
			Backend:         "runtime_state",
		},
	}
}

// AssembleOrder 返回上下文组装顺序副本：policy -> project -> user -> task -> scratchpad。
func AssembleOrder() []MemoryLayer {
	return append([]MemoryLayer(nil), assembleOrder...)
}

// normalizeEntry 填充默认字段并校验 layer/source/time 字段。
func (h *HierarchicalMemory) normalizeEntry(entry MemoryEntry) (MemoryEntry, error) {
	entry.Key = strings.TrimSpace(entry.Key)
	if entry.Key == "" {
		return MemoryEntry{}, errors.New("key is required")
	}
	if !entry.Layer.Valid() {
		return MemoryEntry{}, fmt.Errorf("invalid layer: %s", entry.Layer)
	}
	if !entry.Source.Valid() {
		return MemoryEntry{}, fmt.Errorf("invalid source: %s", entry.Source)
	}
	if entry.Confidence == 0 {
		entry.Confidence = 1.0
	}
	now := formatMemoryTime(h.now())
	if strings.TrimSpace(entry.ValidFrom) == "" {
		entry.ValidFrom = now
	}
	if strings.TrimSpace(entry.CreatedAt) == "" {
		entry.CreatedAt = now
	}
	if _, err := parseMemoryTime(entry.ValidFrom); err != nil {
		return MemoryEntry{}, fmt.Errorf("valid_from: %w", err)
	}
	if _, err := parseMemoryTime(entry.CreatedAt); err != nil {
		return MemoryEntry{}, fmt.Errorf("created_at: %w", err)
	}
	if entry.ValidUntil != nil {
		validUntil := strings.TrimSpace(*entry.ValidUntil)
		if validUntil == "" {
			entry.ValidUntil = nil
		} else {
			if _, err := parseMemoryTime(validUntil); err != nil {
				return MemoryEntry{}, fmt.Errorf("valid_until: %w", err)
			}
			entry.ValidUntil = &validUntil
		}
	}
	if entry.LastAccessedAt != nil {
		lastAccessed := strings.TrimSpace(*entry.LastAccessedAt)
		if lastAccessed == "" {
			entry.LastAccessedAt = nil
		} else {
			if _, err := parseMemoryTime(lastAccessed); err != nil {
				return MemoryEntry{}, fmt.Errorf("last_accessed_at: %w", err)
			}
			entry.LastAccessedAt = &lastAccessed
		}
	}
	entry.EvidenceRefs = normalizeEvidenceRefs(entry.EvidenceRefs)
	entry.Value = cloneAny(entry.Value)
	return entry, nil
}

// sortedLayerKeysLocked 在持锁状态下取出指定层的未过期条目，并按 confidence/access time 排序。
func (h *HierarchicalMemory) sortedLayerKeysLocked(layer MemoryLayer, now time.Time) []string {
	keys := make([]string, 0)
	for key, entry := range h.entries {
		if entry.Layer != layer || entry.IsExpiredAt(now) {
			continue
		}
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		left := h.entries[keys[i]]
		right := h.entries[keys[j]]
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		return accessSortTime(left) > accessSortTime(right)
	})
	return keys
}

// entryCountLocked 统计某层当前保存的条目数量，和附件 health_report 一样不排除过期条目。
func (h *HierarchicalMemory) entryCountLocked(layer MemoryLayer) int {
	count := 0
	for _, entry := range h.entries {
		if entry.Layer == layer {
			count++
		}
	}
	return count
}

// mergePolicies 合并默认策略和调用方覆盖项，保证五层策略始终齐全。
func mergePolicies(overrides map[MemoryLayer]LayerPolicy) map[MemoryLayer]LayerPolicy {
	policies := DefaultPolicies()
	for layer, override := range overrides {
		if !layer.Valid() {
			continue
		}
		current := policies[layer]
		current.Layer = layer
		if override.TokenBudget > 0 {
			current.TokenBudget = override.TokenBudget
		}
		if override.TTL != nil {
			duration := *override.TTL
			current.TTL = &duration
		}
		if strings.TrimSpace(override.Backend) != "" {
			current.Backend = strings.TrimSpace(override.Backend)
		}
		current.AllowAgentWrite = override.AllowAgentWrite
		current.RequireEvidence = override.RequireEvidence
		policies[layer] = current
	}
	return policies
}

// renderContext 把 assemble_context 的条目列表按层渲染成 prompt 可读文本。
func renderContext(entries []MemoryEntry) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	byLayer := map[MemoryLayer][]MemoryEntry{}
	for _, entry := range entries {
		byLayer[entry.Layer] = append(byLayer[entry.Layer], entry)
	}
	sections := make([]string, 0, len(assembleOrder))
	for _, layer := range assembleOrder {
		layerEntries := byLayer[layer]
		if len(layerEntries) == 0 {
			continue
		}
		body, err := json.MarshalIndent(layerEntries, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal %s context: %w", layer, err)
		}
		sections = append(sections, fmt.Sprintf("## %s MEMORY\n%s", strings.ToUpper(layer.String()), string(body)))
	}
	return strings.Join(sections, "\n\n"), nil
}

// accessSortTime 返回附件排序里的 last_accessed_at or created_at。
func accessSortTime(entry MemoryEntry) string {
	if entry.LastAccessedAt != nil && strings.TrimSpace(*entry.LastAccessedAt) != "" {
		return strings.TrimSpace(*entry.LastAccessedAt)
	}
	return strings.TrimSpace(entry.CreatedAt)
}

// normalizeEvidenceRefs 清理空证据引用，避免空字符串绕过 evidence 判断。
func normalizeEvidenceRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// mergeEvidenceRefs 合并 scratchpad 原有证据和本次提升证据，保持顺序并去重。
func mergeEvidenceRefs(existing []string, additions []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(additions))
	for _, ref := range append(normalizeEvidenceRefs(existing), normalizeEvidenceRefs(additions)...) {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// routeScratchpadPromotion 是模型之外的层级路由边界，避免 target_layer 泄漏给 LLM。
func routeScratchpadPromotion(entry MemoryEntry) MemoryLayer {
	return promotionLayerFromKey(entry.Key)
}

// promotionLayerFromKey 根据稳定 key 前缀选择正式层，无前缀时默认进入 task。
func promotionLayerFromKey(key string) MemoryLayer {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.HasPrefix(normalized, "policy:"):
		return MemoryLayerPolicy
	case strings.HasPrefix(normalized, "project:"):
		return MemoryLayerProject
	case strings.HasPrefix(normalized, "user:"):
		return MemoryLayerUser
	case strings.HasPrefix(normalized, "task:"):
		return MemoryLayerTask
	default:
		return MemoryLayerTask
	}
}

// ttlSeconds 把 Go Duration 转成 health_report 里的秒数。
func ttlSeconds(ttl *time.Duration) *float64 {
	if ttl == nil {
		return nil
	}
	seconds := ttl.Seconds()
	return &seconds
}

// cloneEntry 深拷贝单条记忆，保护内部 evidence/value。
func cloneEntry(entry MemoryEntry) MemoryEntry {
	entry.Value = cloneAny(entry.Value)
	entry.EvidenceRefs = append([]string(nil), entry.EvidenceRefs...)
	if entry.ValidUntil != nil {
		value := *entry.ValidUntil
		entry.ValidUntil = &value
	}
	if entry.LastAccessedAt != nil {
		value := *entry.LastAccessedAt
		entry.LastAccessedAt = &value
	}
	return entry
}

// cloneAny 尽量按 JSON 语义复制值，保持 tool 输入输出适合跨进程观察。
func cloneAny(input any) any {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return input
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return input
	}
	return out
}

// formatMemoryTime 输出 Go 可解析的 ISO 时间，承担附件里 datetime.utcnow().isoformat() 的角色。
func formatMemoryTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// parseMemoryTime 同时兼容 Go RFC3339 和 Python datetime.utcnow().isoformat() 的无时区格式。
func parseMemoryTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("time is empty")
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid ISO time %q", value)
}
