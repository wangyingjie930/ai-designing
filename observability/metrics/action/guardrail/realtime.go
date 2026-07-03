package guardrail

import (
	"sort"
	"time"

	runtimeguardrail "ai-designing/action/guardrail"
)

// RealtimeGuardrailMetricID 是实时可观测 guardrail 指标的稳定标识。
type RealtimeGuardrailMetricID string

const (
	RealtimeGuardrailMetricTriggerRate               RealtimeGuardrailMetricID = "guardrail_trigger_rate"
	RealtimeGuardrailMetricBlockRate                 RealtimeGuardrailMetricID = "guardrail_block_rate"
	RealtimeGuardrailMetricFallbackRate              RealtimeGuardrailMetricID = "guardrail_fallback_rate"
	RealtimeGuardrailMetricErrorRate                 RealtimeGuardrailMetricID = "guardrail_error_rate"
	RealtimeGuardrailMetricUserRetryOrEscalationRate RealtimeGuardrailMetricID = "user_retry_or_escalation_rate"
)

// RealtimeGuardrailMetricStatus 表示实时指标在当前事件窗口里是否可计算。
type RealtimeGuardrailMetricStatus string

const (
	RealtimeGuardrailMetricStatusScored        RealtimeGuardrailMetricStatus = "scored"
	RealtimeGuardrailMetricStatusNotApplicable RealtimeGuardrailMetricStatus = "not_applicable"
)

// RealtimeGuardrailMetricResult 保存实时指标定义、分子分母和分数，便于直接上报或打印。
type RealtimeGuardrailMetricResult struct {
	ID          RealtimeGuardrailMetricID
	Definition  RealtimeGuardrailMetricDefinition
	Numerator   int
	Denominator int
	Score       float64
	Status      RealtimeGuardrailMetricStatus
}

// RealtimeGuardrailMetricReport 是实时观测侧聚合结果，保留比例指标、类别分布、风险等级分布和延迟。
type RealtimeGuardrailMetricReport struct {
	Results             []RealtimeGuardrailMetricResult
	CategoryCountByName map[string]int
	RiskLevelCount      map[runtimeguardrail.RiskLevel]int
	ErrorCountByType    map[string]int
	LatencyP95          time.Duration
}

// Result 按实时指标 ID 返回聚合结果。
func (r RealtimeGuardrailMetricReport) Result(metricID RealtimeGuardrailMetricID) (RealtimeGuardrailMetricResult, bool) {
	for _, result := range r.Results {
		if result.ID == metricID {
			return result, true
		}
	}
	return RealtimeGuardrailMetricResult{}, false
}

// realtimeGuardrailMetricAccumulator 是实时指标内部计数器，只保存分子和分母。
type realtimeGuardrailMetricAccumulator struct {
	numerator   int
	denominator int
}

// ScoreRealtimeGuardrailMetrics 从线上 guardrail 判断事件中计算实时可观测指标。
func ScoreRealtimeGuardrailMetrics(events []GuardrailDecisionEvent) RealtimeGuardrailMetricReport {
	accumulators := newRealtimeGuardrailMetricAccumulators()
	categoryCountByName := make(map[string]int)
	riskLevelCount := make(map[runtimeguardrail.RiskLevel]int)
	errorCountByType := make(map[string]int)
	latencies := make([]time.Duration, 0, len(events))
	for _, event := range events {
		scoreRealtimeGuardrailDecisionMetrics(accumulators, errorCountByType, event)
		collectRealtimeGuardrailDimensions(categoryCountByName, riskLevelCount, event)
		if event.Latency > 0 {
			latencies = append(latencies, event.Latency)
		}
	}
	return RealtimeGuardrailMetricReport{
		Results:             buildRealtimeGuardrailMetricResults(accumulators),
		CategoryCountByName: categoryCountByName,
		RiskLevelCount:      riskLevelCount,
		ErrorCountByType:    errorCountByType,
		LatencyP95:          percentileNearestRank(latencies, 0.95),
	}
}

// newRealtimeGuardrailMetricAccumulators 初始化实时指标计数器，保证空事件也能返回稳定指标列表。
func newRealtimeGuardrailMetricAccumulators() map[RealtimeGuardrailMetricID]*realtimeGuardrailMetricAccumulator {
	accumulators := make(map[RealtimeGuardrailMetricID]*realtimeGuardrailMetricAccumulator, len(realtimeGuardrailMetricDefinitions))
	for _, definition := range realtimeGuardrailMetricDefinitions {
		accumulators[definition.ID] = &realtimeGuardrailMetricAccumulator{}
	}
	return accumulators
}

// scoreRealtimeGuardrailDecisionMetrics 计算线上命中、拦截、兜底、错误和误杀代理指标。
func scoreRealtimeGuardrailDecisionMetrics(
	accumulators map[RealtimeGuardrailMetricID]*realtimeGuardrailMetricAccumulator,
	errorCountByType map[string]int,
	event GuardrailDecisionEvent,
) {
	for _, metricID := range []RealtimeGuardrailMetricID{
		RealtimeGuardrailMetricTriggerRate,
		RealtimeGuardrailMetricBlockRate,
		RealtimeGuardrailMetricFallbackRate,
		RealtimeGuardrailMetricErrorRate,
	} {
		incrementRealtimeGuardrailDenominator(accumulators, metricID)
	}

	if event.Triggered {
		incrementRealtimeGuardrailNumerator(accumulators, RealtimeGuardrailMetricTriggerRate)
	}
	switch event.Outcome {
	case GuardrailOutcomeBlock:
		incrementRealtimeGuardrailNumerator(accumulators, RealtimeGuardrailMetricBlockRate)
	case GuardrailOutcomeFallback:
		incrementRealtimeGuardrailNumerator(accumulators, RealtimeGuardrailMetricFallbackRate)
	case GuardrailOutcomeError:
		incrementRealtimeGuardrailNumerator(accumulators, RealtimeGuardrailMetricErrorRate)
		incrementRealtimeGuardrailErrorType(errorCountByType, event.ErrorType)
	}

	if realtimeGuardrailIntervened(event.Outcome) {
		incrementRealtimeGuardrailDenominator(accumulators, RealtimeGuardrailMetricUserRetryOrEscalationRate)
		if event.UserRetriedOrEscalated {
			incrementRealtimeGuardrailNumerator(accumulators, RealtimeGuardrailMetricUserRetryOrEscalationRate)
		}
	}
}

// collectRealtimeGuardrailDimensions 聚合风险类别和风险等级分布，供实时看板定位异常来源。
func collectRealtimeGuardrailDimensions(
	categoryCountByName map[string]int,
	riskLevelCount map[runtimeguardrail.RiskLevel]int,
	event GuardrailDecisionEvent,
) {
	for _, category := range event.Categories {
		if category != "" {
			categoryCountByName[category]++
		}
	}
	if event.RiskLevel != "" {
		riskLevelCount[event.RiskLevel]++
	}
}

// realtimeGuardrailIntervened 判断某个线上 outcome 是否属于会影响用户或工具链路的干预动作。
func realtimeGuardrailIntervened(outcome GuardrailOutcome) bool {
	switch outcome {
	case GuardrailOutcomeBlock, GuardrailOutcomeFallback, GuardrailOutcomeHumanReview, GuardrailOutcomeRedact:
		return true
	default:
		return false
	}
}

// buildRealtimeGuardrailMetricResults 把计数器转成稳定顺序输出，并显式处理无分母指标。
func buildRealtimeGuardrailMetricResults(accumulators map[RealtimeGuardrailMetricID]*realtimeGuardrailMetricAccumulator) []RealtimeGuardrailMetricResult {
	results := make([]RealtimeGuardrailMetricResult, 0, len(realtimeGuardrailMetricDefinitions))
	for _, definition := range realtimeGuardrailMetricDefinitions {
		accumulator := accumulators[definition.ID]
		result := RealtimeGuardrailMetricResult{
			ID:          definition.ID,
			Definition:  definition,
			Numerator:   accumulator.numerator,
			Denominator: accumulator.denominator,
			Status:      RealtimeGuardrailMetricStatusNotApplicable,
		}
		if accumulator.denominator > 0 {
			result.Score = float64(accumulator.numerator) / float64(accumulator.denominator)
			result.Status = RealtimeGuardrailMetricStatusScored
		}
		results = append(results, result)
	}
	return results
}

// incrementRealtimeGuardrailErrorType 记录 guardrail 自身错误类型，空值统一归为 unknown_error。
func incrementRealtimeGuardrailErrorType(errorCountByType map[string]int, errorType string) {
	if errorType == "" {
		errorType = "unknown_error"
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

// incrementRealtimeGuardrailNumerator 增加实时指标分子计数。
func incrementRealtimeGuardrailNumerator(accumulators map[RealtimeGuardrailMetricID]*realtimeGuardrailMetricAccumulator, metricID RealtimeGuardrailMetricID) {
	accumulators[metricID].numerator++
}

// incrementRealtimeGuardrailDenominator 增加实时指标分母计数。
func incrementRealtimeGuardrailDenominator(accumulators map[RealtimeGuardrailMetricID]*realtimeGuardrailMetricAccumulator, metricID RealtimeGuardrailMetricID) {
	accumulators[metricID].denominator++
}
