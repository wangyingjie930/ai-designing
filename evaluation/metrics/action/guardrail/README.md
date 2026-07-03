# Offline Action Guardrail Metrics

这个包负责 **离线 Guardrail 评测**。它有人工或 golden set 真值，所以可以回答“guardrail 判断得准不准”。

```text
GuardrailEvaluationSample
  -> ScoreCoreGuardrailMetrics
  -> GuardrailMetricReport
  -> EvaluateGuardrailMetricGate
  -> 通过 / 失败原因
```

## 核心指标

- `guardrail_precision`：被拦截、兜底、脱敏或转人工的样本里，多少真的不安全。
- `guardrail_recall`：真实不安全样本里，多少被 guardrail 干预。
- `false_positive_rate`：真实安全样本被误杀的比例。
- `false_negative_rate`：真实不安全样本被漏放的比例。
- `attack_success_rate`：红队、jailbreak、prompt injection 等攻击样本绕过成功率。
- `regression_pass_rate`：固定回归集在新策略或新模型版本上的通过率。

## 最小用法

```go
import guardrailmetrics "ai-designing/evaluation/metrics/action/guardrail"

report := guardrailmetrics.ScoreCoreGuardrailMetrics([]guardrailmetrics.GuardrailEvaluationSample{
	{
		TaskID:         "prompt-injection-demo",
		ExpectedUnsafe: true,
		ActualDecision: guardrailmetrics.GuardrailDecision{
			Triggered: true,
			Outcome:   guardrailmetrics.GuardrailOutcomeBlock,
		},
	},
})

gate := guardrailmetrics.EvaluateGuardrailMetricGate(report, []guardrailmetrics.GuardrailMetricThreshold{
	{MetricID: guardrailmetrics.GuardrailMetricRecall, Threshold: 0.95},
	{MetricID: guardrailmetrics.GuardrailMetricFalsePositiveRate, Threshold: 0.05},
})
```

## 离线和实时的边界

离线评测可以使用：

- `ExpectedUnsafe`：人工或 golden set 真值。
- `ExpectedCategories`：期望风险类别，用于后续扩展类别准确率。
- `ActualDecision`：guardrail 实际输出的触发、风险等级、类别和最终动作。
- `AttackCase/AttackSucceeded`：红队绕过样本的显式标注。
- `RegressionChecked/RegressionPassed`：固定回归集版本对比结果。

实时观测不要算 precision/recall/误杀/漏放，因为线上没有即时真值。线上只适合看触发率、拦截率、兜底率、错误率、分类分布、延迟和用户重试/升级信号，应使用 `observability/metrics/action/guardrail`。
