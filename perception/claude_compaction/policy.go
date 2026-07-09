package claudecompaction

const defaultSummaryOutputReserve = 20_000

// QuerySource 标记当前请求来源，避免压缩代理递归触发自动压缩。
type QuerySource string

const (
	QuerySourceMain          QuerySource = "main"
	QuerySourceCompact       QuerySource = "compact"
	QuerySourceSessionMemory QuerySource = "session_memory"
)

// AutoCompactPolicy 还原 Claude Code 的自动压缩阈值计算。
type AutoCompactPolicy struct {
	Enabled                bool
	ContextWindow          int
	MaxSummaryOutputTokens int
	BufferTokens           int
}

// AutoCompactDecision 描述一次自动压缩 gate 的判断结果。
type AutoCompactDecision struct {
	TotalTokens     int
	EffectiveWindow int
	Threshold       int
	ShouldCompact   bool
	Reason          string
}

// Decide 根据有效上下文窗口和递归保护判断是否自动压缩。
func (p AutoCompactPolicy) Decide(messages []Message, source QuerySource) AutoCompactDecision {
	total := totalTokens(messages)
	effective := p.EffectiveContextWindow()
	threshold := effective - p.Buffer()
	decision := AutoCompactDecision{
		TotalTokens:     total,
		EffectiveWindow: effective,
		Threshold:       threshold,
	}
	if !p.Enabled {
		decision.Reason = "disabled"
		return decision
	}
	if source == QuerySourceCompact || source == QuerySourceSessionMemory {
		decision.Reason = "recursion_guard"
		return decision
	}
	if total >= threshold {
		decision.ShouldCompact = true
		decision.Reason = "above_threshold"
		return decision
	}
	decision.Reason = "below_threshold"
	return decision
}

// EffectiveContextWindow 从模型窗口中扣除摘要输出预留。
func (p AutoCompactPolicy) EffectiveContextWindow() int {
	contextWindow := p.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 200_000
	}
	reserve := p.MaxSummaryOutputTokens
	if reserve <= 0 || reserve > defaultSummaryOutputReserve {
		reserve = defaultSummaryOutputReserve
	}
	return contextWindow - reserve
}

// Buffer 返回自动压缩前保留给继续生成和压缩调用的安全余量。
func (p AutoCompactPolicy) Buffer() int {
	if p.BufferTokens <= 0 {
		return 13_000
	}
	return p.BufferTokens
}
