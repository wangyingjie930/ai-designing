package compaction

import (
	"encoding/json"
	"strings"
)

// Anchor 是长会话持续演化的工作记忆。
type Anchor struct {
	Intent             string
	ChangesMade        []string
	DecisionsTaken     []string
	ExcludedApproaches []string
	NextSteps          []string
}

// ToSummary 将 Anchor 渲染成稳定、可继续推理的五槽位文本。
func (a Anchor) ToSummary() string {
	var parts []string
	if a.Intent != "" {
		parts = append(parts, "INTENT: "+a.Intent)
	}
	if len(a.ChangesMade) > 0 {
		parts = append(parts, "CHANGES: "+strings.Join(tail(a.ChangesMade, 10), " | "))
	}
	if len(a.DecisionsTaken) > 0 {
		parts = append(parts, "DECIDED: "+strings.Join(tail(a.DecisionsTaken, 10), " | "))
	}
	if len(a.ExcludedApproaches) > 0 {
		parts = append(parts, "EXCLUDED (do not retry): "+strings.Join(a.ExcludedApproaches, " | "))
	}
	if len(a.NextSteps) > 0 {
		parts = append(parts, "NEXT: "+strings.Join(tail(a.NextSteps, 5), " | "))
	}
	return strings.Join(parts, "\n")
}

// IsZero 判断 Anchor 是否还没有沉淀有效工作记忆。
func (a Anchor) IsZero() bool {
	return a.Intent == "" &&
		len(a.ChangesMade) == 0 &&
		len(a.DecisionsTaken) == 0 &&
		len(a.ExcludedApproaches) == 0 &&
		len(a.NextSteps) == 0
}

// Merge 合并新 Anchor，保留已排除方案以阻断重复试错。
func (a Anchor) Merge(next Anchor) Anchor {
	if strings.TrimSpace(next.Intent) != "" {
		a.Intent = strings.TrimSpace(next.Intent)
	}
	a.ChangesMade = appendUniqueLimited(a.ChangesMade, next.ChangesMade, 30)
	a.DecisionsTaken = appendUniqueLimited(a.DecisionsTaken, next.DecisionsTaken, 30)
	a.ExcludedApproaches = appendUniqueLimited(a.ExcludedApproaches, next.ExcludedApproaches, 40)
	if len(next.NextSteps) > 0 {
		a.NextSteps = appendUniqueLimited(nil, next.NextSteps, 10)
	}
	return a
}

// ParseAnchor 从 LLM 输出中解析五槽位 Anchor，并合并到 base。
func ParseAnchor(text string, base Anchor) Anchor {
	payload, ok := extractJSONObject(text)
	if !ok {
		return base
	}

	var next anchorPayload
	if err := json.Unmarshal([]byte(payload), &next); err != nil {
		return base
	}
	return base.Merge(next.toAnchor())
}

// JSON 将 Anchor 转成模型输入使用的结构化 JSON。
func (a Anchor) JSON() string {
	return anchorPayloadFromAnchor(a).JSON()
}

type anchorPayload struct {
	Intent             string   `json:"intent"`
	ChangesMade        []string `json:"changes_made"`
	DecisionsTaken     []string `json:"decisions_taken"`
	ExcludedApproaches []string `json:"excluded_approaches"`
	NextSteps          []string `json:"next_steps"`
}

// JSON 序列化 Anchor payload，供 prompt 和调试输出复用。
func (p anchorPayload) JSON() string {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// anchorPayloadFromAnchor 保持 prompt schema 和内部 Anchor 字段一致。
func anchorPayloadFromAnchor(anchor Anchor) anchorPayload {
	return anchorPayload{
		Intent:             anchor.Intent,
		ChangesMade:        cloneStringSlice(anchor.ChangesMade),
		DecisionsTaken:     cloneStringSlice(anchor.DecisionsTaken),
		ExcludedApproaches: cloneStringSlice(anchor.ExcludedApproaches),
		NextSteps:          cloneStringSlice(anchor.NextSteps),
	}
}

// toAnchor 将 JSON payload 转回内部 Anchor。
func (p anchorPayload) toAnchor() Anchor {
	return Anchor{
		Intent:             p.Intent,
		ChangesMade:        cloneStringSlice(p.ChangesMade),
		DecisionsTaken:     cloneStringSlice(p.DecisionsTaken),
		ExcludedApproaches: cloneStringSlice(p.ExcludedApproaches),
		NextSteps:          cloneStringSlice(p.NextSteps),
	}
}

// extractJSONObject 提取模型输出中的 JSON 对象，兼容偶发 markdown fence。
func extractJSONObject(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 3 {
			trimmed = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end < start {
		return "", false
	}
	candidate := strings.TrimSpace(trimmed[start : end+1])
	if !json.Valid([]byte(candidate)) {
		return "", false
	}
	return candidate, true
}

// HandoffSummary 是客服 Agent 进入人工/二线交接时的摘要载体。
type HandoffSummary struct {
	TicketID           string   `json:"ticket_id"`
	CustomerID         string   `json:"customer_id"`
	ProductLine        string   `json:"product_line"`
	Severity           string   `json:"severity"`
	SLADeadline        string   `json:"sla_deadline"`
	Intent             string   `json:"intent"`
	ChangesAttempted   []string `json:"changes_attempted"`
	Decisions          []string `json:"decisions"`
	ExcludedApproaches []string `json:"excluded_approaches"`
	NextSteps          []string `json:"next_steps"`
}

// NewHandoffSummary 将业务上下文和 Anchor 合成可交接摘要。
func NewHandoffSummary(ctx SupportContext, anchor Anchor) HandoffSummary {
	return HandoffSummary{
		TicketID:           ctx.TicketID,
		CustomerID:         ctx.CustomerID,
		ProductLine:        ctx.ProductLine,
		Severity:           ctx.Severity,
		SLADeadline:        ctx.SLADeadlineISO,
		Intent:             anchor.Intent,
		ChangesAttempted:   cloneStringSlice(anchor.ChangesMade),
		Decisions:          cloneStringSlice(anchor.DecisionsTaken),
		ExcludedApproaches: cloneStringSlice(anchor.ExcludedApproaches),
		NextSteps:          cloneStringSlice(anchor.NextSteps),
	}
}

// JSON 把交接摘要序列化为便于日志和工单系统存储的 JSON。
func (h HandoffSummary) JSON() string {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// appendUniqueLimited 追加去重列表，并保留最近的 limit 项。
func appendUniqueLimited(base []string, additions []string, limit int) []string {
	seen := make(map[string]bool, len(base)+len(additions))
	var merged []string
	for _, item := range append(cloneStringSlice(base), additions...) {
		normalized := strings.TrimSpace(item)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		merged = append(merged, normalized)
	}
	return tail(merged, limit)
}

// tail 返回切片最后 limit 项，用于控制 Anchor 膨胀。
func tail(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return cloneStringSlice(items)
	}
	return cloneStringSlice(items[len(items)-limit:])
}

// cloneStringSlice 复制字符串切片，避免外部修改内部状态。
func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
