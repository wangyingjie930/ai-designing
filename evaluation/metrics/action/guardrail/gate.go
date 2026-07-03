package guardrail

// GuardrailMetricThreshold 表示一个离线 guardrail 指标门禁阈值。
type GuardrailMetricThreshold struct {
	MetricID  GuardrailMetricID
	Threshold float64
}

// GuardrailMetricGateFailure 记录未通过门禁的指标和原因，便于写入 CI 日志。
type GuardrailMetricGateFailure struct {
	MetricID  GuardrailMetricID
	Score     float64
	Threshold float64
	Reason    string
}

// GuardrailMetricGateResult 表示一组 guardrail 指标阈值检查后的最终门禁结果。
type GuardrailMetricGateResult struct {
	Passed   bool
	Failures []GuardrailMetricGateFailure
}

// EvaluateGuardrailMetricGate 根据指标方向比较阈值；不适用指标配置阈值时视为失败。
func EvaluateGuardrailMetricGate(report GuardrailMetricReport, thresholds []GuardrailMetricThreshold) GuardrailMetricGateResult {
	result := GuardrailMetricGateResult{Passed: true}
	for _, threshold := range thresholds {
		metricResult, ok := report.Result(threshold.MetricID)
		if !ok {
			result.Passed = false
			result.Failures = append(result.Failures, GuardrailMetricGateFailure{
				MetricID:  threshold.MetricID,
				Threshold: threshold.Threshold,
				Reason:    "metric result is missing",
			})
			continue
		}
		if metricResult.Status != GuardrailMetricStatusScored {
			result.Passed = false
			result.Failures = append(result.Failures, GuardrailMetricGateFailure{
				MetricID:  threshold.MetricID,
				Threshold: threshold.Threshold,
				Reason:    "metric is not applicable",
			})
			continue
		}
		if !guardrailMetricThresholdPassed(metricResult, threshold.Threshold) {
			result.Passed = false
			result.Failures = append(result.Failures, GuardrailMetricGateFailure{
				MetricID:  threshold.MetricID,
				Score:     metricResult.Score,
				Threshold: threshold.Threshold,
				Reason:    "metric score does not satisfy threshold",
			})
		}
	}
	return result
}

// guardrailMetricThresholdPassed 按指标方向解释阈值，误杀率和漏放率这类指标使用小于等于。
func guardrailMetricThresholdPassed(result GuardrailMetricResult, threshold float64) bool {
	switch result.Definition.Direction {
	case GuardrailMetricDirectionLowerIsBetter:
		return result.Score <= threshold
	default:
		return result.Score >= threshold
	}
}
