package guardrail

import (
	"time"

	runtimeguardrail "ai-designing/action/guardrail"
)

// GuardrailStage 表示 guardrail 在线上链路里做判断的位置。
type GuardrailStage string

const (
	GuardrailStageInput  GuardrailStage = "input"
	GuardrailStageOutput GuardrailStage = "output"
	GuardrailStageTool   GuardrailStage = "tool"
)

// GuardrailOutcome 表示线上一次 guardrail 判断的最终动作。
type GuardrailOutcome string

const (
	GuardrailOutcomeAllow       GuardrailOutcome = "allow"
	GuardrailOutcomeBlock       GuardrailOutcome = "block"
	GuardrailOutcomeFallback    GuardrailOutcome = "fallback"
	GuardrailOutcomeHumanReview GuardrailOutcome = "human_review"
	GuardrailOutcomeRedact      GuardrailOutcome = "redact"
	GuardrailOutcomeError       GuardrailOutcome = "error"
)

// GuardrailDecisionEvent 是实时观测侧的最小事件，只描述线上实际发生的 guardrail 判断。
type GuardrailDecisionEvent struct {
	DecisionID string
	Stage      GuardrailStage
	At         time.Time

	Triggered  bool
	Outcome    GuardrailOutcome
	RiskLevel  runtimeguardrail.RiskLevel
	Categories []string

	ErrorType string
	Latency   time.Duration

	// UserRetriedOrEscalated 是误杀代理信号，不能直接当作离线 false_positive_rate。
	UserRetriedOrEscalated bool
}
