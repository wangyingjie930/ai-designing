package guardrail

// RealtimeGuardrailMetricDirection 表示实时指标变好时数值应该上升、下降，或只做观测。
type RealtimeGuardrailMetricDirection string

const (
	RealtimeGuardrailMetricDirectionHigherIsBetter RealtimeGuardrailMetricDirection = "higher_is_better"
	RealtimeGuardrailMetricDirectionLowerIsBetter  RealtimeGuardrailMetricDirection = "lower_is_better"
	RealtimeGuardrailMetricDirectionWatchOnly      RealtimeGuardrailMetricDirection = "watch_only"
)

// RealtimeGuardrailMetricDefinition 描述一个实时 guardrail 指标的公式、字段依赖和观测边界。
type RealtimeGuardrailMetricDefinition struct {
	ID RealtimeGuardrailMetricID

	DisplayName string

	// Meaning 说明这个实时指标回答什么线上问题。
	Meaning string

	// Numerator 是实时窗口里的分子事件口径。
	Numerator string

	// Denominator 是实时窗口里的分母事件口径。
	Denominator string

	// Formula 显式记录公式；分母为 0 时报告为 not_applicable。
	Formula string

	// RequiredFields 记录 GuardrailDecisionEvent 至少要采集哪些字段才能计算该指标。
	RequiredFields []string

	Direction RealtimeGuardrailMetricDirection

	// Notes 记录实时观测边界，尤其说明哪些结论不能从线上事件直接推出。
	Notes []string
}

var realtimeGuardrailMetricDefinitions = []RealtimeGuardrailMetricDefinition{
	{
		ID:          RealtimeGuardrailMetricTriggerRate,
		DisplayName: "实时 Guardrail 命中率",
		Meaning:     "衡量线上请求或动作有多少命中了 guardrail 风险规则、分类器或策略条件。",
		Numerator:   "triggered 为 true 的 guardrail 判断事件数",
		Denominator: "所有 guardrail 判断事件数",
		Formula:     "triggered_guardrail_events / guardrail_events",
		RequiredFields: []string{
			"guardrail_events[].triggered",
		},
		Direction: RealtimeGuardrailMetricDirectionWatchOnly,
		Notes: []string{
			"命中率上升可能是攻击变多，也可能是策略过严，需要结合类别分布和用户重试/升级看。",
			"它没有人工真值，不能替代离线 guardrail_precision 或 guardrail_recall。",
		},
	},
	{
		ID:          RealtimeGuardrailMetricBlockRate,
		DisplayName: "实时拦截率",
		Meaning:     "衡量线上 guardrail 判断里有多少最终直接拦截。",
		Numerator:   "outcome 为 block 的 guardrail 判断事件数",
		Denominator: "所有 guardrail 判断事件数",
		Formula:     "blocked_guardrail_events / guardrail_events",
		RequiredFields: []string{
			"guardrail_events[].outcome",
		},
		Direction: RealtimeGuardrailMetricDirectionWatchOnly,
		Notes: []string{
			"拦截率突然升高通常需要排查策略变更、上游请求形态和攻击流量。",
			"block 只是最终动作之一，fallback、redact、human_review 也属于干预。",
		},
	},
	{
		ID:          RealtimeGuardrailMetricFallbackRate,
		DisplayName: "实时兜底率",
		Meaning:     "衡量线上 guardrail 判断里有多少走了安全回复、改写、脱敏或人工确认前的兜底路径。",
		Numerator:   "outcome 为 fallback 的 guardrail 判断事件数",
		Denominator: "所有 guardrail 判断事件数",
		Formula:     "fallback_guardrail_events / guardrail_events",
		RequiredFields: []string{
			"guardrail_events[].outcome",
		},
		Direction: RealtimeGuardrailMetricDirectionWatchOnly,
		Notes: []string{
			"兜底率适合和 block_rate 分开看，否则无法判断用户体验被哪类动作影响。",
			"如果业务把脱敏也视为兜底，可以在上报前统一 outcome 口径。",
		},
	},
	{
		ID:          RealtimeGuardrailMetricErrorRate,
		DisplayName: "实时 Guardrail 错误率",
		Meaning:     "衡量 guardrail 自己分类、策略执行或依赖调用失败的比例。",
		Numerator:   "outcome 为 error 的 guardrail 判断事件数",
		Denominator: "所有 guardrail 判断事件数",
		Formula:     "error_guardrail_events / guardrail_events",
		RequiredFields: []string{
			"guardrail_events[].outcome",
			"guardrail_events[].error_type",
		},
		Direction: RealtimeGuardrailMetricDirectionLowerIsBetter,
		Notes: []string{
			"错误率是 guardrail 稳定性指标，不代表模型内容一定安全或不安全。",
			"排查时应结合 ErrorCountByType 和 LatencyP95 看分类器超时、策略异常或依赖故障。",
		},
	},
	{
		ID:          RealtimeGuardrailMetricUserRetryOrEscalationRate,
		DisplayName: "用户重试/升级率",
		Meaning:     "衡量被 guardrail 干预后，用户立刻重试、投诉、转人工或升级的比例。",
		Numerator:   "被干预且 user_retried_or_escalated 为 true 的事件数",
		Denominator: "outcome 为 block、fallback、redact 或 human_review 的事件数",
		Formula:     "retried_or_escalated_interventions / guardrail_interventions",
		RequiredFields: []string{
			"guardrail_events[].outcome",
			"guardrail_events[].user_retried_or_escalated",
		},
		Direction: RealtimeGuardrailMetricDirectionLowerIsBetter,
		Notes: []string{
			"这是线上误杀代理指标，只能提示体验异常，不能替代离线 false_positive_rate。",
			"如果分母很小，应优先看绝对数量，避免小样本比例误导。",
		},
	},
}

// RealtimeGuardrailMetricDefinitions 返回实时 guardrail 指标定义，供报告、文档和上报层复用同一套口径。
func RealtimeGuardrailMetricDefinitions() []RealtimeGuardrailMetricDefinition {
	definitions := make([]RealtimeGuardrailMetricDefinition, len(realtimeGuardrailMetricDefinitions))
	copy(definitions, realtimeGuardrailMetricDefinitions)
	return definitions
}
