package tool

// RealtimeToolMetricDirection 表示实时指标变好时数值应该上升还是下降。
type RealtimeToolMetricDirection string

const (
	RealtimeToolMetricDirectionHigherIsBetter RealtimeToolMetricDirection = "higher_is_better"
	RealtimeToolMetricDirectionLowerIsBetter  RealtimeToolMetricDirection = "lower_is_better"
)

// RealtimeToolMetricDefinition 描述一个实时 Tool 指标的公式、字段依赖和观测边界。
type RealtimeToolMetricDefinition struct {
	ID RealtimeToolMetricID

	DisplayName string

	// Meaning 说明这个实时指标回答什么线上问题。
	Meaning string

	// Numerator 是实时窗口里的分子事件口径。
	Numerator string

	// Denominator 是实时窗口里的分母事件口径。
	Denominator string

	// Formula 显式记录公式；分母为 0 时报告为 not_applicable。
	Formula string

	// RequiredFields 记录 ToolCallEvent 至少要采集哪些字段才能计算该指标。
	RequiredFields []string

	Direction RealtimeToolMetricDirection

	// Notes 记录实时观测边界，尤其说明哪些结论不能从线上事件直接推出。
	Notes []string
}

var realtimeToolMetricDefinitions = []RealtimeToolMetricDefinition{
	{
		ID:          RealtimeToolMetricSchemaValidRate,
		DisplayName: "实时参数 Schema 合法率",
		Meaning:     "衡量线上工具调用入参是否通过 schema 校验。",
		Numerator:   "schema 校验通过的工具调用事件数",
		Denominator: "明确执行过 schema 校验的工具调用事件数",
		Formula:     "schema_valid_tool_events / schema_checked_tool_events",
		RequiredFields: []string{
			"tool_call_events[].schema_checked",
			"tool_call_events[].schema_valid",
		},
		Direction: RealtimeToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"它只说明线上参数形状合法，不说明业务实体一定绑定正确。",
			"未采集 schema_checked 的事件不进入分母，避免把未观测误当失败。",
		},
	},
	{
		ID:          RealtimeToolMetricSuccessRate,
		DisplayName: "实时工具执行成功率",
		Meaning:     "衡量线上工具调用是否返回业务成功结果。",
		Numerator:   "execution_status 为 success 的工具调用事件数",
		Denominator: "所有线上工具调用事件数",
		Formula:     "successful_tool_events / tool_call_events",
		RequiredFields: []string{
			"tool_call_events[].execution_status",
		},
		Direction: RealtimeToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"它不判断 Agent 是否应该调用该工具，只判断实际调用是否跑通。",
			"选错工具、漏调工具这类问题需要离线 ExpectedToolCall 才能判断。",
		},
	},
	{
		ID:          RealtimeToolMetricTimeoutRate,
		DisplayName: "实时工具超时率",
		Meaning:     "衡量线上工具调用里有多少因为超时失败。",
		Numerator:   "execution_status 为 timeout 的工具调用事件数",
		Denominator: "所有线上工具调用事件数",
		Formula:     "timeout_tool_events / tool_call_events",
		RequiredFields: []string{
			"tool_call_events[].execution_status",
		},
		Direction: RealtimeToolMetricDirectionLowerIsBetter,
		Notes: []string{
			"timeout_rate 是错误率的一个子集，不要和 error_rate 相加当总失败率。",
			"排查时应结合 LatencyP95 和 ErrorCountByType 看。",
		},
	},
	{
		ID:          RealtimeToolMetricErrorRate,
		DisplayName: "实时工具错误率",
		Meaning:     "衡量线上工具调用里有多少没有成功完成。",
		Numerator:   "execution_status 为 error 或 timeout 的工具调用事件数",
		Denominator: "所有线上工具调用事件数",
		Formula:     "failed_tool_events / tool_call_events",
		RequiredFields: []string{
			"tool_call_events[].execution_status",
			"tool_call_events[].error_type",
		},
		Direction: RealtimeToolMetricDirectionLowerIsBetter,
		Notes: []string{
			"错误类型分布放在 ErrorCountByType，比例指标只保留总错误率。",
			"业务返回空结果但 execution_status 为 success 时，不计入 error_rate。",
		},
	},
	{
		ID:          RealtimeToolMetricResultUsedRate,
		DisplayName: "实时工具结果使用率",
		Meaning:     "衡量成功工具调用返回的关键事实是否进入最终回答。",
		Numerator:   "final_answer_used_fact 为 true 的成功工具调用事件数",
		Denominator: "execution_status 为 success 的工具调用事件数",
		Formula:     "successful_tool_events_used_in_final_answer / successful_tool_events",
		RequiredFields: []string{
			"tool_call_events[].execution_status",
			"tool_call_events[].final_answer_used_fact",
		},
		Direction: RealtimeToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"这个实时指标只能观察“成功结果有没有被使用”，不能判断结果本身是否相关。",
			"result_relevance 需要任务标准答案或人工判断，应放在离线 evaluation。",
		},
	},
}

// RealtimeToolMetricDefinitions 返回实时 Tool 指标定义，供报告、文档和上报层复用同一套口径。
func RealtimeToolMetricDefinitions() []RealtimeToolMetricDefinition {
	definitions := make([]RealtimeToolMetricDefinition, len(realtimeToolMetricDefinitions))
	copy(definitions, realtimeToolMetricDefinitions)
	return definitions
}
