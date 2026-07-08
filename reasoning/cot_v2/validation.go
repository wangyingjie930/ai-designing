package cot_v2

import "strings"

var ambiguousConnectors = []string{
	"并且", "同时", "以及", "因此", "所以", "从而",
	"说明", "证明", "可以判断", "综合来看",
}

// LooksTooComposite 用粗粒度语言信号拦截明显承载多个判断的命题。
func LooksTooComposite(claimText string) bool {
	hits := 0
	for _, word := range ambiguousConnectors {
		if strings.Contains(claimText, word) {
			hits++
		}
	}
	return hits >= 2
}

// ValidateStepShape 执行 Step Schema 和类型准入检查，失败的步骤不能进入正式放行链。
func ValidateStepShape(step ClaimStep) []string {
	errors := make([]string, 0)
	if strings.TrimSpace(step.StepID) == "" {
		errors = append(errors, "step_id is required")
	}
	if strings.TrimSpace(step.ClaimText) == "" {
		errors = append(errors, "claim_text is required")
	}
	if strings.TrimSpace(step.Subject) == "" || strings.TrimSpace(step.Predicate) == "" {
		errors = append(errors, "structured claim requires subject and predicate")
	}
	if LooksTooComposite(step.ClaimText) {
		errors = append(errors, "claim_text seems to contain multiple claims")
	}

	switch step.Kind {
	case StepKindObserve:
		if len(step.EvidenceRefs) == 0 {
			errors = append(errors, "OBSERVE step requires evidence_refs")
		}
		if len(step.DependsOn) > 0 {
			errors = append(errors, "OBSERVE step should not depend on derived steps")
		}
	case StepKindDecompose:
		if len(step.DependsOn) == 0 {
			errors = append(errors, "DECOMPOSE step requires an observed problem")
		}
		if len(step.EvidenceRefs) > 0 {
			errors = append(errors, "DECOMPOSE should output questions, not claim external evidence")
		}
	case StepKindDerive:
		if len(step.DependsOn) == 0 {
			errors = append(errors, "DERIVE step requires upstream dependencies")
		}
		if strings.TrimSpace(step.Action) == "" {
			errors = append(errors, "DERIVE step requires a tool or computation action")
		}
	case StepKindVerify:
		if len(step.EvidenceRefs) == 0 {
			errors = append(errors, "VERIFY step requires evidence_refs")
		}
		if strings.TrimSpace(step.Validator) == "" {
			errors = append(errors, "VERIFY step requires validator")
		}
	case StepKindDecide:
		if len(step.DependsOn) == 0 {
			errors = append(errors, "DECIDE step requires dependencies")
		}
		if strings.TrimSpace(step.Validator) == "" {
			errors = append(errors, "DECIDE step requires decision gate validator")
		}
	default:
		errors = append(errors, "kind is invalid")
	}
	return errors
}
