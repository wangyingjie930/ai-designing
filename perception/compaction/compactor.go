package compaction

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// AnchorUpdater 将旧历史增量合并进 Anchor。
type AnchorUpdater interface {
	UpdateAnchor(ctx context.Context, current Anchor, turns []Turn, support SupportContext) (Anchor, error)
}

// AnchorUpdaterFunc 方便测试或业务侧注入自定义 Anchor 生成逻辑。
type AnchorUpdaterFunc func(ctx context.Context, current Anchor, turns []Turn, support SupportContext) (Anchor, error)

// UpdateAnchor 执行函数式 AnchorUpdater。
func (f AnchorUpdaterFunc) UpdateAnchor(ctx context.Context, current Anchor, turns []Turn, support SupportContext) (Anchor, error) {
	return f(ctx, current, turns, support)
}

// ModelAnchorUpdater 使用 Eino ChatModel 生成五槽位 Anchor。
type ModelAnchorUpdater struct {
	Model model.BaseChatModel
}

// UpdateAnchor 调用模型把新增旧历史折叠进现有 Anchor。
func (u ModelAnchorUpdater) UpdateAnchor(ctx context.Context, current Anchor, turns []Turn, support SupportContext) (Anchor, error) {
	if u.Model == nil || len(turns) == 0 {
		return current, nil
	}
	msg, err := u.Model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(anchorSystemPrompt()),
		schema.UserMessage(buildAnchorPrompt(current, turns, support)),
	})
	if err != nil {
		return current, fmt.Errorf("update anchor with model: %w", err)
	}
	return ParseAnchor(msg.Content, current), nil
}

// HeuristicAnchorUpdater 在没有模型时保守生成一个可用 Anchor。
type HeuristicAnchorUpdater struct{}

// UpdateAnchor 从显式 turn kind 中提取最小工作记忆。
func (HeuristicAnchorUpdater) UpdateAnchor(_ context.Context, current Anchor, turns []Turn, _ SupportContext) (Anchor, error) {
	next := Anchor{}
	for _, turn := range turns {
		switch turn.Kind {
		case TurnKindMessage:
			if turn.Role == RoleUser && current.Intent == "" && next.Intent == "" {
				next.Intent = trimForPrompt(turn.Content, 160)
			}
		case TurnKindAction:
			next.ChangesMade = append(next.ChangesMade, trimForPrompt(turn.Content, 160))
		case TurnKindDecision:
			next.DecisionsTaken = append(next.DecisionsTaken, trimForPrompt(turn.Content, 160))
		case TurnKindError:
			next.ExcludedApproaches = append(next.ExcludedApproaches, "不要重复触发已产生错误的相同排查路径: "+trimForPrompt(turn.Content, 120))
		}
	}
	if len(next.NextSteps) == 0 {
		next.NextSteps = []string{"继续围绕工单状态排查，并在风险升高时导出 handoff summary"}
	}
	return current.Merge(next), nil
}

// Config 控制语义压缩的窗口、阈值和业务策略。
type Config struct {
	ContextBudget                 int
	TargetTokens                  int
	PreserveRecent                int
	LongObservationTokenThreshold int
	MaxRecentErrors               int
	BaseTriggerRatio              float64
	MediumRiskTriggerRatio        float64
	HighRiskTriggerRatio          float64
	ErrorPatterns                 []string
	AnchorUpdater                 AnchorUpdater
	Now                           func() time.Time
}

// DefaultConfig 返回适合客服长会话的默认压缩配置。
func DefaultConfig() Config {
	return Config{
		ContextBudget:                 200_000,
		TargetTokens:                  90_000,
		PreserveRecent:                8,
		LongObservationTokenThreshold: 500,
		MaxRecentErrors:               3,
		BaseTriggerRatio:              0.65,
		MediumRiskTriggerRatio:        0.58,
		HighRiskTriggerRatio:          0.52,
		ErrorPatterns:                 defaultSupportErrorPatterns(),
		AnchorUpdater:                 HeuristicAnchorUpdater{},
		Now:                           func() time.Time { return time.Now().UTC() },
	}
}

// SupportCompactor 执行客服场景的三层语义压缩。
type SupportCompactor struct {
	config Config
	anchor Anchor
	events []CompactionEvent
}

// NewSupportCompactor 创建一个面向单个 session 的 compactor。
func NewSupportCompactor(config Config) *SupportCompactor {
	normalized := normalizeConfig(config)
	return &SupportCompactor{config: normalized}
}

// Anchor 返回当前 session 的工作记忆快照。
func (c *SupportCompactor) Anchor() Anchor {
	return c.anchor
}

// Events 返回压缩事件副本，供指标系统上报。
func (c *SupportCompactor) Events() []CompactionEvent {
	out := make([]CompactionEvent, len(c.events))
	copy(out, c.events)
	return out
}

// TokenPressure 先分类并估算历史 token，用于中间件决定是否真正进入压缩。
func (c *SupportCompactor) TokenPressure(support SupportContext, turns []Turn) TokenPressure {
	classified := c.classifyTurns(turns)
	return c.tokenPressureForTotal(support, totalTokens(classified))
}

// ShouldCompact 判断当前 token 压力是否达到业务阈值。
func (c *SupportCompactor) ShouldCompact(support SupportContext, totalTokens int) bool {
	return c.tokenPressureForTotal(support, totalTokens).ShouldCompact
}

// BuildPromptView 返回送入客服 Agent 前的压缩后上下文视图。
func (c *SupportCompactor) BuildPromptView(ctx context.Context, support SupportContext, turns []Turn) (PromptView, *CompactionEvent, error) {
	middleware := TokenLimitCompactionMiddleware{compactor: c}
	return middleware.BuildPromptView(ctx, support, turns)
}

// Compact 依次执行 ObservationMasking、Anchor 合并和极限交接压缩。
func (c *SupportCompactor) Compact(ctx context.Context, support SupportContext, turns []Turn) ([]Turn, *CompactionEvent, error) {
	classified := c.classifyTurns(turns)
	pressure := c.tokenPressureForTotal(support, totalTokens(classified))
	return c.compactClassified(ctx, support, classified, pressure)
}

// compactClassified 对已分类历史执行真实压缩；未超阈值时只返回历史副本。
func (c *SupportCompactor) compactClassified(ctx context.Context, support SupportContext, classified []Turn, pressure TokenPressure) ([]Turn, *CompactionEvent, error) {
	if !pressure.ShouldCompact {
		return cloneTurns(classified), nil, nil
	}
	boundary := len(classified) - c.config.PreserveRecent
	if boundary < 0 {
		boundary = 0
	}
	old := classified[:boundary]
	recent := classified[boundary:]
	errors := filterTurns(old, func(t Turn) bool { return t.IsError })
	errorsIn := countErrors(classified)

	level1 := append(c.clearObservations(old), cloneTurns(recent)...)
	if totalTokens(level1) <= c.config.TargetTokens {
		event := c.recordEvent(1, support, classified, level1, errorsIn, countErrors(level1), false)
		return level1, &event, nil
	}

	nonErrorOld := filterTurns(old, func(t Turn) bool { return !t.IsError })
	if len(nonErrorOld) > 0 {
		next, err := c.config.AnchorUpdater.UpdateAnchor(ctx, c.anchor, nonErrorOld, support)
		if err != nil {
			return nil, nil, err
		}
		c.anchor = next
	}
	anchorTurn := c.anchorTurn()
	level2 := append([]Turn{anchorTurn}, append(cloneTurns(errors), cloneTurns(recent)...)...)
	if totalTokens(level2) <= c.config.TargetTokens {
		event := c.recordEvent(2, support, classified, level2, errorsIn, countErrors(level2), false)
		return level2, &event, nil
	}

	recentErrors := tailTurns(errors, c.config.MaxRecentErrors)
	oldErrors := errors[:len(errors)-len(recentErrors)]
	errorSummary, represented := c.summarizeOldErrors(oldErrors)
	level3 := []Turn{anchorTurn}
	if errorSummary.Content != "" {
		level3 = append(level3, errorSummary)
	}
	level3 = append(level3, cloneTurns(recentErrors)...)
	level3 = append(level3, cloneTurns(recent)...)
	event := c.recordEvent(3, support, classified, level3, errorsIn, countErrors(level3), true)
	event.ErrorTracesRepresented = countErrors(recent) + len(recentErrors) + represented
	c.events[len(c.events)-1] = event
	return level3, &event, nil
}

// buildPromptView 统一组装压缩前后都会返回的模型可见上下文视图。
func (c *SupportCompactor) buildPromptView(support SupportContext, turns []Turn, event *CompactionEvent, pressure TokenPressure) PromptView {
	promptTurns := cloneTurns(turns)
	return PromptView{
		SupportContext:     support,
		Anchor:             c.anchor,
		Turns:              promptTurns,
		Event:              event,
		HandoffSummary:     NewHandoffSummary(support, c.anchor),
		EstimatedTokens:    totalTokens(promptTurns),
		HandoffRecommended: event != nil && event.HandoffRecommended,
		TokenPressure:      pressure,
	}
}

// HealthCheck 汇总 L3 触发率、压缩比和错误保留违规。
func (c *SupportCompactor) HealthCheck() HealthReport {
	if len(c.events) == 0 {
		return HealthReport{Status: "no compaction events yet"}
	}
	var level3, violations int
	var ratioSum float64
	for _, event := range c.events {
		if event.Level == 3 {
			level3++
		}
		if !event.AllErrorsRepresented() {
			violations++
		}
		ratioSum += event.CompressionRatio()
	}
	report := HealthReport{
		Status:                 "ok",
		Level3TriggerRate:      float64(level3) / float64(len(c.events)),
		AverageCompressionRate: ratioSum / float64(len(c.events)),
		ErrorLossViolations:    violations,
	}
	if report.Level3TriggerRate > 0.10 {
		report.Warnings = append(report.Warnings, "level_3_overuse")
	}
	if report.AverageCompressionRate < 0.20 {
		report.Warnings = append(report.Warnings, "over_compression")
	}
	if violations > 0 {
		report.Warnings = append(report.Warnings, "error_loss_violation")
	}
	if len(report.Warnings) > 0 {
		report.Status = "warning"
	}
	return report
}

// SystemPrompt 渲染客服 Agent 每轮实际可见的系统上下文。
func (v PromptView) SystemPrompt(baseInstruction string) string {
	var b strings.Builder
	if strings.TrimSpace(baseInstruction) != "" {
		b.WriteString(strings.TrimSpace(baseInstruction))
		b.WriteString("\n\n")
	}
	b.WriteString("你是一个长会话客服 Agent。你必须基于下方压缩后的工作记忆继续服务，不要重复已经排除的方案；遇到 P1、SLA 临近、Level 3 压缩时，要准备人工或二线交接。\n\n")
	b.WriteString("[Ticket]\n")
	b.WriteString(fmt.Sprintf("ticket_id: %s\ncustomer_id: %s\nproduct_line: %s\nseverity: %s\nsla_deadline: %s\n\n",
		v.SupportContext.TicketID,
		v.SupportContext.CustomerID,
		v.SupportContext.ProductLine,
		v.SupportContext.Severity,
		v.SupportContext.SLADeadlineISO,
	))
	if !v.Anchor.IsZero() {
		b.WriteString("[Working Memory Anchor]\n")
		b.WriteString(v.Anchor.ToSummary())
		b.WriteString("\n\n")
	}
	if v.HandoffRecommended {
		b.WriteString("[Handoff Recommended]\n")
		b.WriteString(v.HandoffSummary.JSON())
		b.WriteString("\n\n")
	}
	b.WriteString("[Compacted Session History]\n")
	if len(v.Turns) == 0 {
		b.WriteString("(no previous turns)\n")
		return b.String()
	}
	for _, turn := range v.Turns {
		if turn.Kind == TurnKindAnchor {
			continue
		}
		b.WriteString(formatTurnForPrompt(turn))
		b.WriteString("\n")
	}
	return b.String()
}

// triggerRatio 根据工单风险动态下调压缩触发阈值。
func (c *SupportCompactor) triggerRatio(support SupportContext) float64 {
	now := c.config.Now()
	switch support.SeverityUpper() {
	case "P0", "P1", "SEV1", "CRITICAL":
		return c.config.HighRiskTriggerRatio
	case "P2", "SEV2", "HIGH":
		return c.config.MediumRiskTriggerRatio
	}
	if deadline, ok := support.Deadline(); ok && deadline.Sub(now) <= 30*time.Minute {
		return c.config.HighRiskTriggerRatio
	}
	return c.config.BaseTriggerRatio
}

// tokenPressureForTotal 把动态阈值换算成可观测的 token 触发决策。
func (c *SupportCompactor) tokenPressureForTotal(support SupportContext, totalTokens int) TokenPressure {
	triggerRatio := c.triggerRatio(support)
	triggerTokens := 0
	if c.config.ContextBudget > 0 {
		triggerTokens = int(math.Ceil(float64(c.config.ContextBudget) * triggerRatio))
	}
	exceedsTrigger := totalTokens > 0 && triggerTokens > 0 && totalTokens >= triggerTokens
	return TokenPressure{
		TotalTokens:    totalTokens,
		ContextBudget:  c.config.ContextBudget,
		TriggerTokens:  triggerTokens,
		TriggerRatio:   triggerRatio,
		TargetTokens:   c.config.TargetTokens,
		ExceedsTrigger: exceedsTrigger,
		ShouldCompact:  exceedsTrigger && totalTokens > c.config.TargetTokens,
	}
}

// classifyTurns 按客服错误模式补全 IsError 标记。
func (c *SupportCompactor) classifyTurns(turns []Turn) []Turn {
	out := cloneTurns(turns)
	for i := range out {
		if out[i].IsError || out[i].Kind == TurnKindError {
			out[i].IsError = true
			out[i].Kind = TurnKindError
			continue
		}
		if isSupportError(out[i].Content, c.config.ErrorPatterns) {
			out[i].IsError = true
			out[i].Kind = TurnKindError
		}
		if out[i].Tokens == 0 {
			out[i].Tokens = EstimateTokens(out[i].Content)
		}
	}
	return out
}

// clearObservations 对非错误的长 Observation 做遮蔽，保留动作路径。
func (c *SupportCompactor) clearObservations(turns []Turn) []Turn {
	out := cloneTurns(turns)
	for i := range out {
		turn := out[i]
		if turn.IsError {
			continue
		}
		if turn.Kind != TurnKindObservation && turn.Role != RoleToolResult {
			continue
		}
		if turn.TokenCount() <= c.config.LongObservationTokenThreshold {
			continue
		}
		handle := turn.Handle
		if handle == "" {
			handle = "not-provided"
		}
		out[i].Content = fmt.Sprintf("[Observation masked: %d tokens. handle=%s. Re-run the tool or reload the handle if exact payload is needed.]", turn.TokenCount(), handle)
		out[i].Tokens = EstimateTokens(out[i].Content)
	}
	return out
}

// anchorTurn 将当前 Anchor 作为 system Turn 注入压缩结果。
func (c *SupportCompactor) anchorTurn() Turn {
	summary := c.anchor.ToSummary()
	if summary == "" {
		summary = "INTENT: continue support diagnosis\nNEXT: ask for missing information and avoid retrying failed paths"
	}
	return NewTurn(RoleSystem, TurnKindAnchor, "[Anchor State]\n"+summary)
}

// summarizeOldErrors 把较早错误折叠为不可重试清单，并返回代表的错误数。
func (c *SupportCompactor) summarizeOldErrors(errors []Turn) (Turn, int) {
	if len(errors) == 0 {
		return Turn{}, 0
	}
	var b strings.Builder
	b.WriteString("[Old errors represented; do not retry blindly]\n")
	for _, errTurn := range errors {
		line := "- " + trimForPrompt(errTurn.Content, 220)
		if errTurn.Handle != "" {
			line += " (handle=" + errTurn.Handle + ")"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return NewTurn(RoleSystem, TurnKindError, b.String(), WithError(true)), len(errors)
}

// recordEvent 记录一次压缩事件并返回副本。
func (c *SupportCompactor) recordEvent(level int, support SupportContext, before []Turn, after []Turn, errorsIn int, errorsOut int, handoff bool) CompactionEvent {
	event := CompactionEvent{
		Level:                  level,
		TurnsBefore:            len(before),
		TurnsAfter:             len(after),
		TokensBefore:           totalTokens(before),
		TokensAfter:            totalTokens(after),
		ErrorTracesIn:          errorsIn,
		ErrorTracesOut:         errorsOut,
		ErrorTracesRepresented: errorsOut,
		TriggerRatio:           c.triggerRatio(support),
		TargetTokens:           c.config.TargetTokens,
		HandoffRecommended:     handoff,
		Timestamp:              c.config.Now(),
	}
	c.events = append(c.events, event)
	return event
}

// normalizeConfig 合并默认值和调用方配置。
func normalizeConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.ContextBudget == 0 {
		config.ContextBudget = defaults.ContextBudget
	}
	if config.TargetTokens == 0 {
		config.TargetTokens = defaults.TargetTokens
	}
	if config.PreserveRecent == 0 {
		config.PreserveRecent = defaults.PreserveRecent
	}
	if config.LongObservationTokenThreshold == 0 {
		config.LongObservationTokenThreshold = defaults.LongObservationTokenThreshold
	}
	if config.MaxRecentErrors == 0 {
		config.MaxRecentErrors = defaults.MaxRecentErrors
	}
	if config.BaseTriggerRatio == 0 {
		config.BaseTriggerRatio = defaults.BaseTriggerRatio
	}
	if config.MediumRiskTriggerRatio == 0 {
		config.MediumRiskTriggerRatio = defaults.MediumRiskTriggerRatio
	}
	if config.HighRiskTriggerRatio == 0 {
		config.HighRiskTriggerRatio = defaults.HighRiskTriggerRatio
	}
	if len(config.ErrorPatterns) == 0 {
		config.ErrorPatterns = defaults.ErrorPatterns
	}
	if config.AnchorUpdater == nil {
		config.AnchorUpdater = defaults.AnchorUpdater
	}
	if config.Now == nil {
		config.Now = defaults.Now
	}
	return config
}

// anchorSystemPrompt 约束模型只输出 Anchor 五槽位。
func anchorSystemPrompt() string {
	return strings.TrimSpace(`You update a long-session support anchor.
Return ONLY one valid JSON object. Do not wrap it in markdown. Do not add explanations.
The JSON schema is:
{
  "intent": "string",
  "changes_made": ["string"],
  "decisions_taken": ["string"],
  "excluded_approaches": ["string"],
  "next_steps": ["string"]
}
Hard rules:
- Preserve all existing excluded_approaches unless the new chunk explicitly proves they are no longer excluded.
- Append new changes_made and decisions_taken; do not erase still-relevant prior items.
- next_steps should be the current actionable support plan.`)
}

// buildAnchorPrompt 组装 Anchor 更新请求。
func buildAnchorPrompt(current Anchor, turns []Turn, support SupportContext) string {
	var b strings.Builder
	b.WriteString("[Support Context]\n")
	b.WriteString(fmt.Sprintf("ticket_id=%s customer_id=%s product_line=%s severity=%s sla_deadline=%s\n\n",
		support.TicketID, support.CustomerID, support.ProductLine, support.Severity, support.SLADeadlineISO))
	b.WriteString("[Existing Anchor]\n")
	if current.IsZero() {
		b.WriteString(anchorPayload{}.JSON())
		b.WriteString("\n\n")
	} else {
		b.WriteString(current.JSON())
		b.WriteString("\n\n")
	}
	b.WriteString("[New Conversation Chunk]\n")
	for _, turn := range turns {
		b.WriteString(formatTurnForPrompt(turn))
		b.WriteString("\n")
	}
	b.WriteString("\nReturn only valid JSON matching the schema from the system message. Keep support handoff readiness in next_steps.\n")
	return b.String()
}

// formatTurnForPrompt 将 Turn 渲染为短标签历史行。
func formatTurnForPrompt(turn Turn) string {
	flags := []string{turn.Role, string(turn.Kind)}
	if turn.IsError {
		flags = append(flags, "protected_error")
	}
	if turn.Handle != "" {
		flags = append(flags, "handle="+turn.Handle)
	}
	return fmt.Sprintf("[%s] %s", strings.Join(flags, "/"), trimForPrompt(turn.Content, 1200))
}

// trimForPrompt 控制单条历史进入 prompt 的最大长度。
func trimForPrompt(content string, limit int) string {
	trimmed := strings.TrimSpace(content)
	if limit <= 0 || len([]rune(trimmed)) <= limit {
		return trimmed
	}
	runes := []rune(trimmed)
	return string(runes[:limit]) + "...[truncated]"
}

// defaultSupportErrorPatterns 返回客服场景默认保护的错误信号。
func defaultSupportErrorPatterns() []string {
	return []string{
		"timeout",
		"traceback",
		"failed",
		"failure",
		"panic",
		"exception",
		"connectionpoolexhausted",
		"queue_depth",
		"fatal",
		"http 5",
		"status=5",
		" 5xx",
	}
}

// isSupportError 判断文本是否命中客服错误模式。
func isSupportError(content string, patterns []string) bool {
	lower := strings.ToLower(content)
	for _, pattern := range patterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// totalTokens 汇总 turns 的 token 估算。
func totalTokens(turns []Turn) int {
	var total int
	for _, turn := range turns {
		total += turn.TokenCount()
	}
	return total
}

// countErrors 统计受保护错误 Turn 数。
func countErrors(turns []Turn) int {
	var count int
	for _, turn := range turns {
		if turn.IsError {
			count++
		}
	}
	return count
}

// filterTurns 保留符合谓词的 Turn。
func filterTurns(turns []Turn, keep func(Turn) bool) []Turn {
	var out []Turn
	for _, turn := range turns {
		if keep(turn) {
			out = append(out, turn)
		}
	}
	return out
}

// cloneTurns 复制 Turn 切片，避免压缩过程修改调用方历史。
func cloneTurns(turns []Turn) []Turn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]Turn, len(turns))
	copy(out, turns)
	for i := range out {
		out[i].Metadata = cloneStringMap(out[i].Metadata)
	}
	return out
}

// tailTurns 返回最后 limit 条 Turn。
func tailTurns(turns []Turn, limit int) []Turn {
	if limit <= 0 {
		return nil
	}
	if len(turns) <= limit {
		return cloneTurns(turns)
	}
	return cloneTurns(turns[len(turns)-limit:])
}
