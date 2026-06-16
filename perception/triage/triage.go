package triage

import (
	"sort"
	"strings"
)

const (
	defaultTriageBudget       = 180_000
	defaultSupportingMaxToken = 800
)

// ErrorDetector 允许业务侧把失败测试、错误栈和告警日志标为不可丢证据。
type ErrorDetector func(ContextItem) bool

// SupportingCompressor 把 P2 背景材料压缩成结论、证据、索引三件套。
type SupportingCompressor func(ContextItem, int) ContextItem

// TriageConfig 控制上下文窗口预算、P2 压缩和错误识别策略。
type TriageConfig struct {
	Budget              int
	SupportingMaxTokens int
	ErrorDetector       ErrorDetector
	Compressor          SupportingCompressor
}

// ContextTriage 实现参考 pattern.py 中“排序、装箱、留 trace”的分诊核心。
type ContextTriage struct {
	budget              int
	supportingMaxTokens int
	errorDetector       ErrorDetector
	compressor          SupportingCompressor
	decisions           []TriageDecision
}

// NewContextTriage 创建一个可复用的上下文分诊器。
func NewContextTriage(config TriageConfig) *ContextTriage {
	budget := config.Budget
	if budget <= 0 {
		budget = defaultTriageBudget
	}
	supportingMaxTokens := config.SupportingMaxTokens
	if supportingMaxTokens <= 0 {
		supportingMaxTokens = defaultSupportingMaxToken
	}
	compressor := config.Compressor
	if compressor == nil {
		compressor = DefaultSupportingCompressor
	}
	return &ContextTriage{
		budget:              budget,
		supportingMaxTokens: supportingMaxTokens,
		errorDetector:       config.ErrorDetector,
		compressor:          compressor,
	}
}

// Triage 按 P0/P1/P2/P3 排序装入上下文，P3 延后，错误证据强制保留。
func (t *ContextTriage) Triage(items []ContextItem) TriageResult {
	sortedItems := make([]ContextItem, 0, len(items))
	for _, item := range items {
		item = item.Normalize()
		if t.isError(item) {
			item.IsError = true
		}
		sortedItems = append(sortedItems, item)
	}

	sort.SliceStable(sortedItems, func(i, j int) bool {
		left := sortedItems[i]
		right := sortedItems[j]
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		if left.IsError != right.IsError {
			return left.IsError
		}
		return len(left.Content) > len(right.Content)
	})

	var selected []ContextItem
	var deferred []ContextItem
	var dropped []ContextItem
	decision := TriageDecision{
		Timestamp: nowISO(),
		Budget:    t.budget,
		Selected:  []string{},
		Deferred:  []string{},
		Dropped:   []string{},
		Records:   []DecisionRecord{},
	}
	tokensUsed := 0

	for _, item := range sortedItems {
		if item.Priority == PriorityDeferrable {
			deferredItem := item
			// P3 只作为页表项进入模型，原文留在工具后面按需读取。
			deferredItem.Content = ""
			deferredItem.TokenEstimate = EstimateTokens(deferredItem.Handle)
			deferred = append(deferred, deferredItem)
			decision.Deferred = append(decision.Deferred, item.Name)
			decision.Records = append(decision.Records, decisionRecord(item, DecisionDeferred, "P3 handle only; not preloaded", tokensUsed, t.budget))
			continue
		}

		action := DecisionSelected
		reason := "fits budget"
		candidate := item
		if candidate.Priority == PrioritySupporting && candidate.TokenEstimate > t.supportingMaxTokens {
			candidate = t.compressor(candidate, t.supportingMaxTokens).Normalize()
			action = DecisionCompressed
			reason = "P2 compressed to conclusion/evidence/index summary"
		}

		if tokensUsed+candidate.TokenEstimate <= t.budget || candidate.IsError {
			if candidate.IsError && tokensUsed+candidate.TokenEstimate > t.budget {
				reason = "protected error evidence; forced into context"
			}
			selected = append(selected, candidate)
			tokensUsed += candidate.TokenEstimate
			decision.Selected = append(decision.Selected, candidate.Name)
			decision.Records = append(decision.Records, decisionRecord(candidate, action, reason, tokensUsed, t.budget))
			continue
		}

		dropped = append(dropped, candidate)
		decision.Dropped = append(decision.Dropped, candidate.Name)
		decision.Records = append(decision.Records, decisionRecord(candidate, DecisionDropped, "over budget after higher priority context", tokensUsed, t.budget))
	}

	decision.TokensUsed = tokensUsed
	if t.budget > 0 {
		decision.BudgetUsage = float64(tokensUsed) / float64(t.budget)
	}
	health := buildHealth(decision, selected, deferred, dropped)
	result := TriageResult{
		Selected: selected,
		Deferred: deferred,
		Dropped:  dropped,
		Decision: decision,
		Health:   health,
	}
	t.decisions = append(t.decisions, decision)
	return result
}

// Decisions 返回历史分诊记录副本，便于测试和外部观测系统采集。
func (t *ContextTriage) Decisions() []TriageDecision {
	out := make([]TriageDecision, len(t.decisions))
	copy(out, t.decisions)
	return out
}

// DefaultSupportingCompressor 将 P2 长背景压成结论、证据和索引，保留 handle 方便回取。
func DefaultSupportingCompressor(item ContextItem, maxTokens int) ContextItem {
	content := strings.TrimSpace(item.Content)
	if content == "" {
		return item
	}
	maxChars := maxTokens * 3
	if maxChars <= 0 {
		maxChars = defaultSupportingMaxToken * 3
	}
	evidence := selectEvidenceLines(content, 8)
	if evidence == "" {
		evidence = truncateString(content, maxChars/2)
	}
	summary := []string{
		"结论: 该背景材料可能影响当前客服判断，已按 P2 摘要进入上下文。",
		"证据: " + evidence,
	}
	if strings.TrimSpace(item.Handle) != "" {
		summary = append(summary, "索引: "+item.Handle)
	}
	joined := strings.Join(summary, "\n")
	if len(joined) > maxChars {
		joined = truncateString(joined, maxChars)
	}
	item.Content = joined
	item.TokenEstimate = EstimateTokens(joined)
	item.Metadata = cloneStringMap(item.Metadata)
	if item.Metadata == nil {
		item.Metadata = map[string]string{}
	}
	item.Metadata["triage_form"] = "summary"
	return item
}

// decisionRecord 构造单条 trace 记录，保持每个分支的观测字段一致。
func decisionRecord(item ContextItem, action DecisionAction, reason string, tokensUsed int, budget int) DecisionRecord {
	return DecisionRecord{
		ItemName:      item.Name,
		Priority:      item.Priority,
		PriorityLabel: item.Priority.String(),
		Action:        action,
		Reason:        reason,
		TokenEstimate: item.TokenEstimate,
		TokensUsed:    tokensUsed,
		Budget:        budget,
		TenantID:      item.TenantID,
		Handle:        item.Handle,
	}
}

// buildHealth 汇总生产里最需要报警的分诊指标。
func buildHealth(decision TriageDecision, selected []ContextItem, deferred []ContextItem, dropped []ContextItem) TriageHealth {
	health := TriageHealth{
		Status:        "ok",
		BudgetUsage:   decision.BudgetUsage,
		DroppedCount:  len(dropped),
		DeferredCount: len(deferred),
	}
	for _, item := range dropped {
		switch item.Priority {
		case PriorityCritical:
			health.CriticalDroppedCount++
		case PriorityImportant:
			health.ImportantDroppedCount++
		}
		if item.IsError {
			health.ProtectedErrorMissingCount++
		}
	}
	for _, item := range deferred {
		if strings.TrimSpace(item.Handle) != "" {
			health.DeferredHandleCount++
			if _, ok := TenantIDFromHandle(item.Handle); ok {
				health.DeferredHandleWithTenantRate++
			}
		}
	}
	if health.DeferredHandleCount > 0 {
		health.DeferredHandleWithTenantRate = health.DeferredHandleWithTenantRate / float64(health.DeferredHandleCount)
	}
	if health.BudgetUsage > 0.9 {
		health.Warnings = append(health.Warnings, "context budget usage is above 90%")
	}
	if health.CriticalDroppedCount > 0 {
		health.Warnings = append(health.Warnings, "P0 context was dropped; check triage budget or priority rules")
	}
	if health.ImportantDroppedCount > 0 {
		health.Warnings = append(health.Warnings, "P1 context was dropped; important current-task evidence may be missing")
	}
	if health.ProtectedErrorMissingCount > 0 {
		health.Warnings = append(health.Warnings, "protected error evidence was dropped")
	}
	if len(health.Warnings) > 0 {
		health.Status = "warning"
	}
	_ = selected
	return health
}

// isError 应用调用方传入的错误识别器，并尊重 item 自身标记。
func (t *ContextTriage) isError(item ContextItem) bool {
	if item.IsError {
		return true
	}
	if t.errorDetector != nil {
		return t.errorDetector(item)
	}
	return false
}

// selectEvidenceLines 优先保留能解释结论、证据、错误和 handle 的行。
func selectEvidenceLines(content string, maxLines int) string {
	lines := strings.Split(content, "\n")
	var picked []string
	keywords := []string{"结论", "证据", "已确认", "已排除", "关键", "handle", "error", "exception", "timeout", "trace", "失败", "告警"}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, keyword := range keywords {
			if strings.Contains(lower, strings.ToLower(keyword)) {
				picked = append(picked, trimmed)
				break
			}
		}
		if len(picked) >= maxLines {
			break
		}
	}
	if len(picked) == 0 {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			picked = append(picked, trimmed)
			if len(picked) >= maxLines {
				break
			}
		}
	}
	return truncateString(strings.Join(picked, " | "), 1200)
}

// truncateString 按字节裁剪即可满足 trace 可读性，保留后缀提示被截断。
func truncateString(value string, maxChars int) string {
	if maxChars <= 0 || len(value) <= maxChars {
		return value
	}
	const suffix = "...[truncated]"
	if maxChars <= len(suffix) {
		return value[:maxChars]
	}
	return value[:maxChars-len(suffix)] + suffix
}

// cloneStringMap 复制 map，避免压缩器修改调用方持有的 metadata。
func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
