package tool

// ToolMetricID 是工具质量指标的稳定标识，后续报告、门禁和样本标注都应该引用它。
type ToolMetricID string

const (
	ToolMetricCallPrecision         ToolMetricID = "tool_call_precision"
	ToolMetricCallRecall            ToolMetricID = "tool_call_recall"
	ToolMetricWrongToolRate         ToolMetricID = "wrong_tool_rate"
	ToolMetricSchemaValidRate       ToolMetricID = "schema_valid_rate"
	ToolMetricEntityBindingAccuracy ToolMetricID = "entity_binding_accuracy"
	ToolMetricSuccessRate           ToolMetricID = "tool_success_rate"
	ToolMetricResultRelevance       ToolMetricID = "result_relevance"
	ToolMetricResultUsedRate        ToolMetricID = "tool_result_used_rate"
)

// ToolMetricLayer 表示指标评估的是工具链路中的哪一段，避免把选择、参数、执行和使用混在一起看。
type ToolMetricLayer string

const (
	ToolMetricLayerSelection ToolMetricLayer = "selection"
	ToolMetricLayerArguments ToolMetricLayer = "arguments"
	ToolMetricLayerExecution ToolMetricLayer = "execution"
	ToolMetricLayerResult    ToolMetricLayer = "result"
	ToolMetricLayerUsage     ToolMetricLayer = "usage"
)

// ToolMetricDirection 表示指标变好时数值应该上升还是下降，方便后续做回归门禁。
type ToolMetricDirection string

const (
	ToolMetricDirectionHigherIsBetter ToolMetricDirection = "higher_is_better"
	ToolMetricDirectionLowerIsBetter  ToolMetricDirection = "lower_is_better"
)

// ToolMetricDefinition 描述一个工具质量指标的业务口径和计算前需要准备的样本字段。
type ToolMetricDefinition struct {
	ID ToolMetricID

	// Layer 先标明指标所在链路层，避免把“选错工具”和“工具执行失败”算成同一种问题。
	Layer ToolMetricLayer

	DisplayName string

	// Meaning 用一句话说明这个指标到底回答什么问题，优先给评审和面试场景看。
	Meaning string

	// Numerator 是计算比例时的分子，必须写成可标注、可统计的事件口径。
	Numerator string

	// Denominator 是计算比例时的分母，决定这个指标适用在哪些样本上。
	Denominator string

	// Formula 把指标公式显式写出来；分母为 0 时应标记 not_applicable，避免把“不适用”误算成 0 分。
	Formula string

	// RequiredFields 记录样本或 trace 至少要包含哪些字段，避免后续 scorer 凭空判断。
	RequiredFields []string

	Direction ToolMetricDirection

	// Notes 专门写容易混淆的边界，比如 schema 合法不代表实体绑定正确。
	Notes []string
}

var coreToolMetricDefinitions = []ToolMetricDefinition{
	{
		ID:          ToolMetricCallPrecision,
		Layer:       ToolMetricLayerSelection,
		DisplayName: "工具调用精确率",
		Meaning:     "衡量 Agent 已经调用的工具里，有多少调用是当前任务真正需要的。",
		Numerator:   "被标注为必要的实际工具调用次数",
		Denominator: "所有实际工具调用次数",
		Formula:     "necessary_actual_tool_calls / actual_tool_calls",
		RequiredFields: []string{
			"actual_tool_calls[].call_id",
			"actual_tool_calls[].tool_name",
			"labels.necessary_call_ids",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"它看的是“已调用工具是否该调”，不是“该调的工具有没有都调到”。后者属于 tool_call_recall。",
			"一个工具返回成功但本来不该调用，precision 仍然要扣分。",
		},
	},
	{
		ID:          ToolMetricCallRecall,
		Layer:       ToolMetricLayerSelection,
		DisplayName: "工具调用召回率",
		Meaning:     "衡量任务需要工具时，Agent 有没有把应调用的工具覆盖到。",
		Numerator:   "被实际调用覆盖的期望工具调用次数",
		Denominator: "标注集中所有期望工具调用次数",
		Formula:     "covered_expected_tool_calls / expected_tool_calls",
		RequiredFields: []string{
			"expected_tool_calls[].tool_name",
			"expected_tool_calls[].intent",
			"actual_tool_calls[].tool_name",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"它看的是“该调有没有调”，不惩罚多调的无关工具；多调问题由 tool_call_precision 看。",
			"如果一个期望能力允许多个等价工具，需要在 expected_tool_calls 里标明 acceptable_tools。",
		},
	},
	{
		ID:          ToolMetricWrongToolRate,
		Layer:       ToolMetricLayerSelection,
		DisplayName: "工具选错率",
		Meaning:     "衡量 Agent 是否把任务交给了不匹配的工具或错误能力面。",
		Numerator:   "工具名或工具能力与期望能力不匹配的实际调用次数",
		Denominator: "所有实际工具调用次数",
		Formula:     "wrong_tool_calls / actual_tool_calls",
		RequiredFields: []string{
			"actual_tool_calls[].tool_name",
			"actual_tool_calls[].intent",
			"expected_tool_calls[].acceptable_tools",
		},
		Direction: ToolMetricDirectionLowerIsBetter,
		Notes: []string{
			"wrong_tool_rate 是错工具比例；tool_call_precision 是无效/不必要调用比例，两者可能重叠但口径不同。",
			"如果工具选对但参数错，应计入参数类指标，不直接计入 wrong_tool_rate。",
		},
	},
	{
		ID:          ToolMetricSchemaValidRate,
		Layer:       ToolMetricLayerArguments,
		DisplayName: "参数 Schema 合法率",
		Meaning:     "衡量工具入参是否满足工具声明的字段类型、必填项和枚举约束。",
		Numerator:   "参数通过 schema 校验的工具调用次数",
		Denominator: "所有需要参数校验的工具调用次数",
		Formula:     "schema_valid_tool_calls / schema_checked_tool_calls",
		RequiredFields: []string{
			"tool_schema",
			"actual_tool_calls[].arguments_json",
			"schema_validation.verdict",
			"schema_validation.error",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"schema 合法只说明字段形状对，不说明实体、租户、时间范围一定对。",
			"枚举值传错、必填字段缺失、JSON 类型不匹配，都属于 schema_valid_rate 的扣分范围。",
		},
	},
	{
		ID:          ToolMetricEntityBindingAccuracy,
		Layer:       ToolMetricLayerArguments,
		DisplayName: "实体绑定准确率",
		Meaning:     "衡量工具参数里的业务实体是否绑定到了用户真正指向的对象。",
		Numerator:   "实体、租户、时间范围等关键绑定完全正确的工具调用次数",
		Denominator: "所有需要业务实体绑定的工具调用次数",
		Formula:     "fully_correct_entity_binding_tool_calls / entity_binding_required_tool_calls",
		RequiredFields: []string{
			"expected_entities",
			"actual_tool_calls[].arguments_json",
			"entity_binding.verdict",
			"entity_binding.mismatches",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"这个指标专门看“值有没有绑对”，不是看参数字段是否合法。",
			"例如 customer_id 字段类型正确但绑定到了另一个客户，schema_valid_rate 可通过，entity_binding_accuracy 要扣分。",
		},
	},
	{
		ID:          ToolMetricSuccessRate,
		Layer:       ToolMetricLayerExecution,
		DisplayName: "工具执行成功率",
		Meaning:     "衡量工具在被调用后是否稳定返回可用的业务成功结果。",
		Numerator:   "工具执行结果为业务成功的调用次数",
		Denominator: "所有实际发起执行的工具调用次数",
		Formula:     "successful_tool_executions / actual_executed_tool_calls",
		RequiredFields: []string{
			"actual_tool_calls[].call_id",
			"tool_results[].call_id",
			"tool_results[].status",
			"tool_results[].error_type",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"它看的是工具运行是否成功，不代表 Agent 选择工具、传参或最终回答一定正确。",
			"权限失败、超时、上游错误、业务失败要保留 error_type，后续才能定位失败类型。",
		},
	},
	{
		ID:          ToolMetricResultRelevance,
		Layer:       ToolMetricLayerResult,
		DisplayName: "工具结果相关性",
		Meaning:     "衡量工具返回内容是否真的能支持当前任务或用户问题。",
		Numerator:   "返回结果被标注为相关且可支持当前任务的工具调用次数",
		Denominator: "所有执行成功且有结果内容的工具调用次数",
		Formula:     "relevant_successful_tool_results / successful_tool_results_with_content",
		RequiredFields: []string{
			"user_task",
			"tool_results[].content_summary",
			"result_judgement.relevance",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"工具执行成功不等于结果相关，例如查到了数据但不是当前用户要的那批数据。",
			"如果结果为空但业务上就是应该为空，需要在 result_judgement 里标明 expected_empty，避免误扣分。",
		},
	},
	{
		ID:          ToolMetricResultUsedRate,
		Layer:       ToolMetricLayerUsage,
		DisplayName: "工具结果使用率",
		Meaning:     "衡量最终回答是否真正吸收并使用了工具返回的关键信息。",
		Numerator:   "最终回答正确使用工具关键结果的任务次数",
		Denominator: "所有存在可用工具结果的任务次数",
		Formula:     "tasks_using_key_tool_results_in_final_answer / tasks_with_available_tool_results",
		RequiredFields: []string{
			"tool_results[].key_facts",
			"final_answer",
			"answer_grounding.used_result_call_ids",
		},
		Direction: ToolMetricDirectionHigherIsBetter,
		Notes: []string{
			"这个指标按任务算，不按工具调用算，因为最终回答通常综合多个工具结果。",
			"工具结果被调用出来但最终回答没有使用，或者回答与工具结果冲突，都应该扣分。",
		},
	},
}

// CoreToolMetricDefinitions 返回 Tool 质量评估第一阶段采用的 8 个核心指标定义。
func CoreToolMetricDefinitions() []ToolMetricDefinition {
	definitions := make([]ToolMetricDefinition, len(coreToolMetricDefinitions))
	copy(definitions, coreToolMetricDefinitions)
	return definitions
}
