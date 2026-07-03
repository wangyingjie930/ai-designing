package tool

// ToolMetricThreshold 表示一个指标门禁阈值，比较方向由指标定义里的 Direction 决定。
type ToolMetricThreshold struct {
	MetricID  ToolMetricID
	Threshold float64
}

// ToolMetricGateFailure 记录未通过门禁的指标和原因，便于把失败项回写到报告或 CI 日志。
type ToolMetricGateFailure struct {
	MetricID  ToolMetricID
	Score     float64
	Threshold float64
	Reason    string
}

// ToolMetricGateResult 表示一组 Tool 指标阈值检查后的最终门禁结果。
type ToolMetricGateResult struct {
	Passed   bool
	Failures []ToolMetricGateFailure
}

// EvaluateToolMetricGate 根据指标方向比较阈值；有阈值但指标不适用时视为未通过，因为没有证据证明达标。
func EvaluateToolMetricGate(report ToolMetricReport, thresholds []ToolMetricThreshold) ToolMetricGateResult {
	result := ToolMetricGateResult{Passed: true}
	for _, threshold := range thresholds {
		metricResult, ok := report.Result(threshold.MetricID)
		if !ok {
			result.Passed = false
			result.Failures = append(result.Failures, ToolMetricGateFailure{
				MetricID:  threshold.MetricID,
				Threshold: threshold.Threshold,
				Reason:    "metric result is missing",
			})
			continue
		}
		if metricResult.Status != ToolMetricStatusScored {
			result.Passed = false
			result.Failures = append(result.Failures, ToolMetricGateFailure{
				MetricID:  threshold.MetricID,
				Threshold: threshold.Threshold,
				Reason:    "metric is not applicable",
			})
			continue
		}
		if !toolMetricThresholdPassed(metricResult, threshold.Threshold) {
			result.Passed = false
			result.Failures = append(result.Failures, ToolMetricGateFailure{
				MetricID:  threshold.MetricID,
				Score:     metricResult.Score,
				Threshold: threshold.Threshold,
				Reason:    "metric score does not satisfy threshold",
			})
		}
	}
	return result
}

// toolMetricThresholdPassed 按指标方向解释阈值，错误率这类 lower-is-better 指标使用小于等于。
func toolMetricThresholdPassed(result ToolMetricResult, threshold float64) bool {
	switch result.Definition.Direction {
	case ToolMetricDirectionLowerIsBetter:
		return result.Score <= threshold
	default:
		return result.Score >= threshold
	}
}
