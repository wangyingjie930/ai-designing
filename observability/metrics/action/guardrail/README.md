# Realtime Action Guardrail Metrics

这个包负责 **实时 Guardrail 观测**。它只记录线上实际发生的 `GuardrailDecisionEvent`，不包含人工真值。

```text
线上 GuardrailDecisionEvent
  -> ScoreRealtimeGuardrailMetrics
  -> RealtimeGuardrailMetricReport
  -> 命中率 / 拦截率 / 兜底率 / 错误率 / 用户重试或升级率 / 分类分布 / p95 延迟
```

## 文件职责

```text
definition.go  # 实时指标定义：ID、公式、分子分母、字段依赖、备注
event.go       # GuardrailDecisionEvent：线上实际 guardrail 判断事件
realtime.go    # ScoreRealtimeGuardrailMetrics：实时窗口聚合
```

## 实时核心指标

- `guardrail_trigger_rate = triggered_guardrail_events / guardrail_events`
- `guardrail_block_rate = blocked_guardrail_events / guardrail_events`
- `guardrail_fallback_rate = fallback_guardrail_events / guardrail_events`
- `guardrail_error_rate = error_guardrail_events / guardrail_events`
- `user_retry_or_escalation_rate = retried_or_escalated_interventions / guardrail_interventions`
- `CategoryCountByName`：按风险类别聚合，用来看 jailbreak、PII、tool injection 等异常来源。
- `RiskLevelCount`：按风险等级聚合，用来看 high/critical 是否突然升高。
- `LatencyP95`：用事件里的 `Latency` 算 nearest-rank p95。
- `ErrorCountByType`：按 `ErrorType` 聚合 guardrail 自身错误。

## 最小用法

```go
import (
	"time"

	runtimeguardrail "ai-designing/action/guardrail"
	realtimeguardrail "ai-designing/observability/metrics/action/guardrail"
)

report := realtimeguardrail.ScoreRealtimeGuardrailMetrics([]realtimeguardrail.GuardrailDecisionEvent{
	{
		DecisionID: "decision-1",
		Stage:      realtimeguardrail.GuardrailStageInput,
		Triggered:  true,
		Outcome:    realtimeguardrail.GuardrailOutcomeBlock,
		RiskLevel:  runtimeguardrail.RiskLevelHigh,
		Categories: []string{"jailbreak"},
		Latency:    120 * time.Millisecond,
	},
})
```

## 实时不要算什么

实时链路没有标准答案，所以不要在这里算：

- `guardrail_precision`
- `guardrail_recall`
- `false_positive_rate`
- `false_negative_rate`
- `attack_success_rate`
- `regression_pass_rate`

这些指标需要人工标注、红队集、历史事故样本或固定回归集，应放在 `evaluation/metrics/action/guardrail` 离线评测里。
