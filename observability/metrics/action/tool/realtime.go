package tool

import (
	"sort"
	"time"
)

// RealtimeToolMetricID 是实时可观测 Tool 指标的稳定标识。
type RealtimeToolMetricID string

const (
	RealtimeToolMetricSchemaValidRate RealtimeToolMetricID = "schema_valid_rate"
	RealtimeToolMetricSuccessRate     RealtimeToolMetricID = "tool_success_rate"
	RealtimeToolMetricTimeoutRate     RealtimeToolMetricID = "timeout_rate"
	RealtimeToolMetricErrorRate       RealtimeToolMetricID = "error_rate"
	RealtimeToolMetricResultUsedRate  RealtimeToolMetricID = "tool_result_used_rate"
)

// RealtimeToolMetricStatus 表示实时指标在当前事件窗口里是否可计算。
type RealtimeToolMetricStatus string

const (
	RealtimeToolMetricStatusScored        RealtimeToolMetricStatus = "scored"
	RealtimeToolMetricStatusNotApplicable RealtimeToolMetricStatus = "not_applicable"
)

// RealtimeToolMetricResult 保存实时指标定义、分子分母和分数，便于直接上报或打印。
type RealtimeToolMetricResult struct {
	ID          RealtimeToolMetricID
	Definition  RealtimeToolMetricDefinition
	Numerator   int
	Denominator int
	Score       float64
	Status      RealtimeToolMetricStatus
}

// RealtimeToolMetricReport 是实时观测侧聚合结果，除了比例指标，还保留错误类型和延迟分位数。
type RealtimeToolMetricReport struct {
	Results          []RealtimeToolMetricResult
	ErrorCountByType map[string]int
	LatencyP95       time.Duration
}

// Result 按实时指标 ID 返回聚合结果。
func (r RealtimeToolMetricReport) Result(metricID RealtimeToolMetricID) (RealtimeToolMetricResult, bool) {
	for _, result := range r.Results {
		if result.ID == metricID {
			return result, true
		}
	}
	return RealtimeToolMetricResult{}, false
}

type realtimeToolMetricAccumulator struct {
	numerator   int
	denominator int
}

// ScoreRealtimeToolMetrics 从线上工具调用事件中计算实时可观测指标；这里不使用 ExpectedToolCall。
func ScoreRealtimeToolMetrics(events []ToolCallEvent) RealtimeToolMetricReport {
	accumulators := newRealtimeToolMetricAccumulators()
	errorCountByType := make(map[string]int)
	latencies := make([]time.Duration, 0, len(events))
	for _, event := range events {
		scoreRealtimeSchemaMetric(accumulators, event)
		scoreRealtimeExecutionMetrics(accumulators, errorCountByType, event)
		scoreRealtimeResultUsageMetric(accumulators, event)
		if event.Latency > 0 {
			latencies = append(latencies, event.Latency)
		}
	}
	return RealtimeToolMetricReport{
		Results:          buildRealtimeToolMetricResults(accumulators),
		ErrorCountByType: errorCountByType,
		LatencyP95:       percentileNearestRank(latencies, 0.95),
	}
}

// newRealtimeToolMetricAccumulators 初始化实时指标计数器，保证空事件也能返回稳定指标列表。
func newRealtimeToolMetricAccumulators() map[RealtimeToolMetricID]*realtimeToolMetricAccumulator {
	accumulators := make(map[RealtimeToolMetricID]*realtimeToolMetricAccumulator, len(realtimeToolMetricDefinitions))
	for _, definition := range realtimeToolMetricDefinitions {
		accumulators[definition.ID] = &realtimeToolMetricAccumulator{}
	}
	return accumulators
}

// scoreRealtimeSchemaMetric 只在事件明确做过 schema 校验时纳入分母，避免把未采集误当失败。
func scoreRealtimeSchemaMetric(accumulators map[RealtimeToolMetricID]*realtimeToolMetricAccumulator, event ToolCallEvent) {
	if !event.SchemaChecked {
		return
	}
	incrementRealtimeDenominator(accumulators, RealtimeToolMetricSchemaValidRate)
	if event.SchemaValid {
		incrementRealtimeNumerator(accumulators, RealtimeToolMetricSchemaValidRate)
	}
}

// scoreRealtimeExecutionMetrics 计算成功率、超时率和错误率，同时保留错误类型分布。
func scoreRealtimeExecutionMetrics(
	accumulators map[RealtimeToolMetricID]*realtimeToolMetricAccumulator,
	errorCountByType map[string]int,
	event ToolCallEvent,
) {
	incrementRealtimeDenominator(accumulators, RealtimeToolMetricSuccessRate)
	incrementRealtimeDenominator(accumulators, RealtimeToolMetricTimeoutRate)
	incrementRealtimeDenominator(accumulators, RealtimeToolMetricErrorRate)
	switch event.ExecutionStatus {
	case ToolExecutionStatusSuccess:
		incrementRealtimeNumerator(accumulators, RealtimeToolMetricSuccessRate)
	case ToolExecutionStatusTimeout:
		incrementRealtimeNumerator(accumulators, RealtimeToolMetricTimeoutRate)
		incrementRealtimeNumerator(accumulators, RealtimeToolMetricErrorRate)
		incrementErrorType(errorCountByType, event.ErrorType, "timeout")
	case ToolExecutionStatusError:
		incrementRealtimeNumerator(accumulators, RealtimeToolMetricErrorRate)
		incrementErrorType(errorCountByType, event.ErrorType, "unknown_error")
	}
}

// scoreRealtimeResultUsageMetric 只对执行成功的工具调用计算最终回答使用率。
func scoreRealtimeResultUsageMetric(accumulators map[RealtimeToolMetricID]*realtimeToolMetricAccumulator, event ToolCallEvent) {
	if event.ExecutionStatus != ToolExecutionStatusSuccess {
		return
	}
	incrementRealtimeDenominator(accumulators, RealtimeToolMetricResultUsedRate)
	if event.FinalAnswerUsedFact {
		incrementRealtimeNumerator(accumulators, RealtimeToolMetricResultUsedRate)
	}
}

// buildRealtimeToolMetricResults 把计数器转成稳定顺序输出，并显式处理无分母指标。
func buildRealtimeToolMetricResults(accumulators map[RealtimeToolMetricID]*realtimeToolMetricAccumulator) []RealtimeToolMetricResult {
	results := make([]RealtimeToolMetricResult, 0, len(realtimeToolMetricDefinitions))
	for _, definition := range realtimeToolMetricDefinitions {
		accumulator := accumulators[definition.ID]
		result := RealtimeToolMetricResult{
			ID:          definition.ID,
			Definition:  definition,
			Numerator:   accumulator.numerator,
			Denominator: accumulator.denominator,
			Status:      RealtimeToolMetricStatusNotApplicable,
		}
		if accumulator.denominator > 0 {
			result.Score = float64(accumulator.numerator) / float64(accumulator.denominator)
			result.Status = RealtimeToolMetricStatusScored
		}
		results = append(results, result)
	}
	return results
}

// incrementErrorType 记录错误类型；上游未给类型时使用稳定 fallback，避免空字符串难以检索。
func incrementErrorType(errorCountByType map[string]int, errorType string, fallback string) {
	if errorType == "" {
		errorType = fallback
	}
	errorCountByType[errorType]++
}

// percentileNearestRank 使用 nearest-rank 算法计算延迟分位数，适合小样本 demo 和离线回放。
func percentileNearestRank(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i int, j int) bool {
		return sorted[i] < sorted[j]
	})
	index := int(float64(len(sorted))*percentile + 0.999999)
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

// incrementRealtimeNumerator 增加实时指标分子计数。
func incrementRealtimeNumerator(accumulators map[RealtimeToolMetricID]*realtimeToolMetricAccumulator, metricID RealtimeToolMetricID) {
	accumulators[metricID].numerator++
}

// incrementRealtimeDenominator 增加实时指标分母计数。
func incrementRealtimeDenominator(accumulators map[RealtimeToolMetricID]*realtimeToolMetricAccumulator, metricID RealtimeToolMetricID) {
	accumulators[metricID].denominator++
}
