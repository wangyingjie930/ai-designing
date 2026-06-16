package triage

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Priority 表示上下文分诊的 P0/P1/P2/P3 四级优先级，数值越大越靠近模型。
type Priority int

const (
	PriorityDeferrable Priority = 1
	PrioritySupporting Priority = 2
	PriorityImportant  Priority = 3
	PriorityCritical   Priority = 4
)

// String 返回优先级的业务标签，便于 trace 和 prompt 审查。
func (p Priority) String() string {
	switch p {
	case PriorityCritical:
		return "P0_CRITICAL"
	case PriorityImportant:
		return "P1_IMPORTANT"
	case PrioritySupporting:
		return "P2_SUPPORTING"
	case PriorityDeferrable:
		return "P3_DEFERRABLE"
	default:
		return fmt.Sprintf("P?_UNKNOWN_%d", p)
	}
}

// Tier 返回文档中使用的 P0/P1/P2/P3 层级名。
func (p Priority) Tier() string {
	switch p {
	case PriorityCritical:
		return "P0"
	case PriorityImportant:
		return "P1"
	case PrioritySupporting:
		return "P2"
	case PriorityDeferrable:
		return "P3"
	default:
		return "P?"
	}
}

// ContextItem 是一次上下文分诊里竞争上下文窗口的候选信息。
type ContextItem struct {
	Name          string            `json:"name"`
	Content       string            `json:"content,omitempty"`
	Priority      Priority          `json:"priority"`
	PriorityLabel string            `json:"priority_label,omitempty"`
	TokenEstimate int               `json:"token_estimate,omitempty"`
	IsError       bool              `json:"is_error,omitempty"`
	TenantID      string            `json:"tenant_id,omitempty"`
	Handle        string            `json:"handle,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Normalize 补齐 token 估算和优先级标签，避免调用方漏填影响排序。
func (i ContextItem) Normalize() ContextItem {
	if i.Priority == 0 {
		i.Priority = PrioritySupporting
	}
	i.PriorityLabel = i.Priority.String()
	if i.TokenEstimate <= 0 && strings.TrimSpace(i.Content) != "" {
		i.TokenEstimate = EstimateTokens(i.Content)
	}
	if i.TokenEstimate <= 0 && strings.TrimSpace(i.Handle) != "" {
		i.TokenEstimate = EstimateTokens(i.Handle)
	}
	return i
}

// DecisionAction 表示一个 ContextItem 最终走到哪条处理路径。
type DecisionAction string

const (
	DecisionSelected   DecisionAction = "selected"
	DecisionCompressed DecisionAction = "compressed"
	DecisionDeferred   DecisionAction = "deferred"
	DecisionDropped    DecisionAction = "dropped"
)

// DecisionRecord 记录单条候选信息的分诊原因，生产排障时可直接落 trace。
type DecisionRecord struct {
	ItemName      string         `json:"item_name"`
	Priority      Priority       `json:"priority"`
	PriorityLabel string         `json:"priority_label"`
	Action        DecisionAction `json:"action"`
	Reason        string         `json:"reason"`
	TokenEstimate int            `json:"token_estimate"`
	TokensUsed    int            `json:"tokens_used"`
	Budget        int            `json:"budget"`
	TenantID      string         `json:"tenant_id,omitempty"`
	Handle        string         `json:"handle,omitempty"`
}

// TriageDecision 是每次分诊的结构化记录，和参考 pattern.py 的 trace 对齐。
type TriageDecision struct {
	Timestamp   string           `json:"timestamp"`
	Budget      int              `json:"budget"`
	Selected    []string         `json:"selected"`
	Deferred    []string         `json:"deferred"`
	Dropped     []string         `json:"dropped"`
	TokensUsed  int              `json:"tokens_used"`
	BudgetUsage float64          `json:"budget_usage"`
	Records     []DecisionRecord `json:"records"`
}

// TriageHealth 汇总分诊压力和关键层丢失风险。
type TriageHealth struct {
	Status                       string   `json:"status"`
	BudgetUsage                  float64  `json:"budget_usage"`
	DroppedCount                 int      `json:"dropped_count"`
	DeferredCount                int      `json:"deferred_count"`
	CriticalDroppedCount         int      `json:"critical_dropped_count"`
	ImportantDroppedCount        int      `json:"important_dropped_count"`
	ProtectedErrorMissingCount   int      `json:"protected_error_missing_count"`
	DeferredHandleCount          int      `json:"deferred_handle_count"`
	DeferredHandleWithTenantRate float64  `json:"deferred_handle_with_tenant_rate"`
	Warnings                     []string `json:"warnings,omitempty"`
}

// TriageResult 返回被选中、延后和丢弃的信息，以及可观测 trace。
type TriageResult struct {
	Selected []ContextItem  `json:"selected"`
	Deferred []ContextItem  `json:"deferred"`
	Dropped  []ContextItem  `json:"dropped"`
	Decision TriageDecision `json:"decision"`
	Health   TriageHealth   `json:"health"`
}

// EstimateTokens 用轻量启发式估算 token，生产接入真实 tokenizer 时可替换。
func EstimateTokens(content string) int {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0
	}
	runes := utf8.RuneCountInString(trimmed)
	words := len(strings.Fields(trimmed))
	estimate := runes / 3
	if words > estimate {
		estimate = words
	}
	if estimate < 1 {
		return 1
	}
	return estimate
}

// nowISO 统一 trace 时间格式，避免测试里到处处理 time.Time。
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
