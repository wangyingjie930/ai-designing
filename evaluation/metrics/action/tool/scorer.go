package tool

// ToolMetricStatus 表示某个指标在当前样本集合上是否真的可计算。
type ToolMetricStatus string

const (
	ToolMetricStatusScored        ToolMetricStatus = "scored"
	ToolMetricStatusNotApplicable ToolMetricStatus = "not_applicable"
)

// ToolMetricResult 是单个指标的可用计算结果，保留分子分母方便复盘口径。
type ToolMetricResult struct {
	ID          ToolMetricID
	Definition  ToolMetricDefinition
	Numerator   int
	Denominator int
	Score       float64
	Status      ToolMetricStatus
}

// ToolMetricReport 是一次 Tool 质量评测的聚合报告，后续可以直接接门禁或展示层。
type ToolMetricReport struct {
	Results []ToolMetricResult
}

// Result 按指标 ID 返回报告中的单项结果。
func (r ToolMetricReport) Result(metricID ToolMetricID) (ToolMetricResult, bool) {
	for _, result := range r.Results {
		if result.ID == metricID {
			return result, true
		}
	}
	return ToolMetricResult{}, false
}

// toolMetricAccumulator 是内部计数器，只保存一个指标的分子和分母累计值。
type toolMetricAccumulator struct {
	numerator   int
	denominator int
}

// ScoreCoreToolMetrics 把多条 ToolEvaluationSample 聚合成 8 个核心 Tool 指标。
func ScoreCoreToolMetrics(samples []ToolEvaluationSample) ToolMetricReport {
	accumulators := newToolMetricAccumulators()
	for _, sample := range samples {
		scoreToolSelectionMetrics(accumulators, sample)
		scoreToolArgumentMetrics(accumulators, sample.ActualCalls)
		scoreToolExecutionAndResultMetrics(accumulators, sample.ActualCalls)
		scoreToolUsageMetric(accumulators, sample)
	}
	return buildToolMetricReport(accumulators)
}

// newToolMetricAccumulators 为所有核心指标预置计数器，保证报告稳定输出 8 个指标。
func newToolMetricAccumulators() map[ToolMetricID]*toolMetricAccumulator {
	accumulators := make(map[ToolMetricID]*toolMetricAccumulator, len(coreToolMetricDefinitions))
	for _, definition := range coreToolMetricDefinitions {
		accumulators[definition.ID] = &toolMetricAccumulator{}
	}
	return accumulators
}

// scoreToolSelectionMetrics 计算工具选择层指标：该不该调、漏没漏调、有没有选错工具。
func scoreToolSelectionMetrics(accumulators map[ToolMetricID]*toolMetricAccumulator, sample ToolEvaluationSample) {
	coveredExpectedCalls := make(map[string]bool, len(sample.ExpectedCalls))
	expectedCalls := expectedToolCallsByID(sample.ExpectedCalls)
	for _, call := range sample.ActualCalls {
		incrementDenominator(accumulators, ToolMetricCallPrecision)
		if call.Necessary {
			incrementNumerator(accumulators, ToolMetricCallPrecision)
		}

		incrementDenominator(accumulators, ToolMetricWrongToolRate)
		if actualCallUsesWrongTool(call, expectedCalls) {
			incrementNumerator(accumulators, ToolMetricWrongToolRate)
		}

		if call.CoveredExpectedCallID != "" {
			coveredExpectedCalls[call.CoveredExpectedCallID] = true
		}
	}

	for _, expected := range sample.ExpectedCalls {
		incrementDenominator(accumulators, ToolMetricCallRecall)
		if expected.ID != "" && coveredExpectedCalls[expected.ID] {
			incrementNumerator(accumulators, ToolMetricCallRecall)
		}
	}
}

// expectedToolCallsByID 将期望工具调用转成 ID 索引，供召回和错工具判断复用同一份期望口径。
func expectedToolCallsByID(expectedCalls []ExpectedToolCall) map[string]ExpectedToolCall {
	index := make(map[string]ExpectedToolCall, len(expectedCalls))
	for _, expected := range expectedCalls {
		if expected.ID == "" {
			continue
		}
		index[expected.ID] = expected
	}
	return index
}

// actualCallUsesWrongTool 判断一次实际调用是否选错工具；显式 WrongTool 标注优先，其次根据期望工具白名单推导。
func actualCallUsesWrongTool(call ActualToolCall, expectedCalls map[string]ExpectedToolCall) bool {
	if call.WrongTool {
		return true
	}
	if call.CoveredExpectedCallID == "" {
		return false
	}
	expected, ok := expectedCalls[call.CoveredExpectedCallID]
	if !ok {
		return true
	}
	return !toolNameAllowed(call.ToolName, expected)
}

// toolNameAllowed 判断实际工具名是否落在期望工具或等价工具白名单里。
func toolNameAllowed(toolName string, expected ExpectedToolCall) bool {
	if toolName == expected.ToolName {
		return true
	}
	for _, acceptable := range expected.AcceptableTools {
		if toolName == acceptable {
			return true
		}
	}
	return false
}

// scoreToolArgumentMetrics 计算参数层指标：schema 合法性和业务实体绑定准确性分开看。
func scoreToolArgumentMetrics(accumulators map[ToolMetricID]*toolMetricAccumulator, calls []ActualToolCall) {
	for _, call := range calls {
		if call.SchemaChecked {
			incrementDenominator(accumulators, ToolMetricSchemaValidRate)
			if call.SchemaValid {
				incrementNumerator(accumulators, ToolMetricSchemaValidRate)
			}
		}
		if call.EntityBindingRequired {
			incrementDenominator(accumulators, ToolMetricEntityBindingAccuracy)
			if call.EntityBindingCorrect {
				incrementNumerator(accumulators, ToolMetricEntityBindingAccuracy)
			}
		}
	}
}

// scoreToolExecutionAndResultMetrics 计算工具执行和工具结果质量，避免把“跑通”和“有用”混为一谈。
func scoreToolExecutionAndResultMetrics(accumulators map[ToolMetricID]*toolMetricAccumulator, calls []ActualToolCall) {
	for _, call := range calls {
		if call.Executed {
			incrementDenominator(accumulators, ToolMetricSuccessRate)
			if call.ExecutionSuccessful {
				incrementNumerator(accumulators, ToolMetricSuccessRate)
			}
		}
		if call.ExecutionSuccessful && call.ResultHasContent {
			incrementDenominator(accumulators, ToolMetricResultRelevance)
			if call.ResultRelevant {
				incrementNumerator(accumulators, ToolMetricResultRelevance)
			}
		}
	}
}

// scoreToolUsageMetric 计算最终回答是否用上工具结果；这个指标按任务算，不按单次工具调用算。
func scoreToolUsageMetric(accumulators map[ToolMetricID]*toolMetricAccumulator, sample ToolEvaluationSample) {
	if !sample.HasAvailableToolResult {
		return
	}
	incrementDenominator(accumulators, ToolMetricResultUsedRate)
	if sample.FinalAnswerUsedToolResult {
		incrementNumerator(accumulators, ToolMetricResultUsedRate)
	}
}

// buildToolMetricReport 把计数器转成稳定顺序的报告，并显式处理分母为 0 的不适用状态。
func buildToolMetricReport(accumulators map[ToolMetricID]*toolMetricAccumulator) ToolMetricReport {
	results := make([]ToolMetricResult, 0, len(coreToolMetricDefinitions))
	for _, definition := range coreToolMetricDefinitions {
		accumulator := accumulators[definition.ID]
		result := ToolMetricResult{
			ID:          definition.ID,
			Definition:  definition,
			Numerator:   accumulator.numerator,
			Denominator: accumulator.denominator,
			Status:      ToolMetricStatusNotApplicable,
		}
		if accumulator.denominator > 0 {
			result.Score = float64(accumulator.numerator) / float64(accumulator.denominator)
			result.Status = ToolMetricStatusScored
		}
		results = append(results, result)
	}
	return ToolMetricReport{Results: results}
}

// incrementNumerator 增加指标分子计数，集中封装避免各处直接操作 map。
func incrementNumerator(accumulators map[ToolMetricID]*toolMetricAccumulator, metricID ToolMetricID) {
	accumulators[metricID].numerator++
}

// incrementDenominator 增加指标分母计数，集中封装避免各处直接操作 map。
func incrementDenominator(accumulators map[ToolMetricID]*toolMetricAccumulator, metricID ToolMetricID) {
	accumulators[metricID].denominator++
}
