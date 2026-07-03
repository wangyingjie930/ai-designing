package guardrail

import runtimeguardrail "ai-designing/action/guardrail"

// GuardrailOutcome 表示 guardrail 对一次样本最终采取的动作。
type GuardrailOutcome string

const (
	GuardrailOutcomeAllow       GuardrailOutcome = "allow"
	GuardrailOutcomeBlock       GuardrailOutcome = "block"
	GuardrailOutcomeFallback    GuardrailOutcome = "fallback"
	GuardrailOutcomeHumanReview GuardrailOutcome = "human_review"
	GuardrailOutcomeRedact      GuardrailOutcome = "redact"
	GuardrailOutcomeError       GuardrailOutcome = "error"
)

// GuardrailDecision 记录一次离线回放中 guardrail 的实际判断结果。
type GuardrailDecision struct {
	// Triggered 表示 guardrail 是否命中过风险规则、分类器或策略条件。
	Triggered bool

	// Outcome 是最终动作；离线质量指标以最终动作判断误杀和漏放。
	Outcome GuardrailOutcome

	RiskLevel  runtimeguardrail.RiskLevel
	Categories []string
	Reason     string
}

// GuardrailEvaluationSample 是离线 guardrail 评测的最小样本单元。
type GuardrailEvaluationSample struct {
	TaskID    string
	UserInput string

	// ExpectedUnsafe 是人工或 golden set 给出的真值，决定 precision/recall 的离线口径。
	ExpectedUnsafe     bool
	ExpectedCategories []string

	ActualDecision GuardrailDecision

	// AttackCase 和 AttackSucceeded 专门服务红队/绕过评估，不和自然流量指标混在一起。
	AttackCase      bool
	AttackSucceeded bool

	// RegressionChecked 让固定回归集和普通探索样本可以共用同一个结构。
	RegressionChecked bool
	RegressionPassed  bool
}
