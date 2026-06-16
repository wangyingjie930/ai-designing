package compaction

import (
	"strings"
	"time"
	"unicode/utf8"
)

// TurnKind 标记一条历史在压缩中的业务角色。
type TurnKind string

const (
	TurnKindMessage     TurnKind = "message"
	TurnKindAction      TurnKind = "action"
	TurnKindObservation TurnKind = "observation"
	TurnKindDecision    TurnKind = "decision"
	TurnKindError       TurnKind = "error"
	TurnKindAnchor      TurnKind = "anchor"
)

const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleSystem     = "system"
	RoleToolResult = "tool_result"
)

// Turn 表示一轮对话、一次动作、一次工具观察或一段错误证据。
type Turn struct {
	Role      string
	Kind      TurnKind
	Content   string
	Tokens    int
	IsError   bool
	Timestamp time.Time
	Handle    string
	Metadata  map[string]string
}

// TurnOption 调整 Turn 的可选字段。
type TurnOption func(*Turn)

// NewTurn 构造一条带 token 估算和 UTC 时间戳的历史记录。
func NewTurn(role string, kind TurnKind, content string, opts ...TurnOption) Turn {
	turn := Turn{
		Role:      role,
		Kind:      kind,
		Content:   content,
		Tokens:    EstimateTokens(content),
		Timestamp: time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(&turn)
	}
	if turn.Kind == TurnKindError {
		turn.IsError = true
	}
	if turn.Tokens == 0 && strings.TrimSpace(turn.Content) != "" {
		turn.Tokens = EstimateTokens(turn.Content)
	}
	return turn
}

// WithTokens 覆盖默认 token 估算，适合接入真实 tokenizer 后使用。
func WithTokens(tokens int) TurnOption {
	return func(t *Turn) {
		t.Tokens = tokens
	}
}

// WithError 显式声明该 Turn 是错误反馈回路的一部分。
func WithError(isError bool) TurnOption {
	return func(t *Turn) {
		t.IsError = isError
		if isError {
			t.Kind = TurnKindError
		}
	}
}

// WithHandle 记录可回查的原始日志、文件或查询结果句柄。
func WithHandle(handle string) TurnOption {
	return func(t *Turn) {
		t.Handle = handle
	}
}

// WithTimestamp 固定 Turn 时间，方便测试或回放。
func WithTimestamp(ts time.Time) TurnOption {
	return func(t *Turn) {
		t.Timestamp = ts.UTC()
	}
}

// WithMetadata 附加业务元数据，供外部观测系统关联。
func WithMetadata(metadata map[string]string) TurnOption {
	return func(t *Turn) {
		t.Metadata = cloneStringMap(metadata)
	}
}

// TokenCount 返回 Turn 的 token 数，缺失时退化到轻量估算。
func (t Turn) TokenCount() int {
	if t.Tokens > 0 {
		return t.Tokens
	}
	return EstimateTokens(t.Content)
}

// EstimateTokens 用轻量启发式估算 token，避免示例强绑定具体 tokenizer。
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

// SupportContext 保存客服工单中会影响压缩节奏的业务上下文。
type SupportContext struct {
	TicketID       string
	CustomerID     string
	ProductLine    string
	Severity       string
	SLADeadlineISO string
}

// Key 返回 session 存储使用的稳定主键。
func (c SupportContext) Key() string {
	if c.TicketID != "" {
		return c.TicketID
	}
	if c.CustomerID != "" {
		return c.CustomerID
	}
	return "default"
}

// SeverityUpper 归一化工单等级，避免 P1/p1 混用影响阈值。
func (c SupportContext) SeverityUpper() string {
	return strings.ToUpper(strings.TrimSpace(c.Severity))
}

// Deadline 解析 SLA 截止时间，解析失败时返回 false。
func (c SupportContext) Deadline() (time.Time, bool) {
	if c.SLADeadlineISO == "" {
		return time.Time{}, false
	}
	deadline, err := time.Parse(time.RFC3339, c.SLADeadlineISO)
	if err != nil {
		return time.Time{}, false
	}
	return deadline, true
}

// CompactionEvent 记录一次压缩的输入、输出和风险信号。
type CompactionEvent struct {
	Level                  int
	TurnsBefore            int
	TurnsAfter             int
	TokensBefore           int
	TokensAfter            int
	ErrorTracesIn          int
	ErrorTracesOut         int
	ErrorTracesRepresented int
	TriggerRatio           float64
	TargetTokens           int
	HandoffRecommended     bool
	Timestamp              time.Time
}

// TokenPressure 描述本轮历史进入 prompt 前的 token 压力和触发决策。
type TokenPressure struct {
	TotalTokens    int
	ContextBudget  int
	TriggerTokens  int
	TriggerRatio   float64
	TargetTokens   int
	ExceedsTrigger bool
	ShouldCompact  bool
}

// CompressionRatio 返回 after/before，用于过压缩和压不够告警。
func (e CompactionEvent) CompressionRatio() float64 {
	if e.TokensBefore <= 0 {
		return 0
	}
	return float64(e.TokensAfter) / float64(e.TokensBefore)
}

// AllErrorsRepresented 判断错误反馈是否至少被原文或摘要表示。
func (e CompactionEvent) AllErrorsRepresented() bool {
	return e.ErrorTracesRepresented >= e.ErrorTracesIn
}

// HealthReport 汇总 compactor 的核心可观测指标。
type HealthReport struct {
	Status                 string
	Level3TriggerRate      float64
	AverageCompressionRate float64
	ErrorLossViolations    int
	Warnings               []string
}

// PromptView 是送入 ADK 前的压缩后上下文视图。
type PromptView struct {
	SupportContext     SupportContext
	Anchor             Anchor
	Turns              []Turn
	Event              *CompactionEvent
	HandoffSummary     HandoffSummary
	EstimatedTokens    int
	HandoffRecommended bool
	TokenPressure      TokenPressure
}

// cloneStringMap 复制 map，避免外部修改污染 session 历史。
func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
