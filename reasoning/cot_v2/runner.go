package cot_v2

import "fmt"

// TraceRunner 执行 ClaimTrace 的结构校验、依赖检查和验证器闸门。
type TraceRunner struct {
	registry *ValidatorRegistry
}

// NewTraceRunner 创建追踪运行器；nil registry 会被视为空注册表。
func NewTraceRunner(registry *ValidatorRegistry) *TraceRunner {
	if registry == nil {
		registry = NewValidatorRegistry()
	}
	return &TraceRunner{registry: registry}
}

// Run 按顺序执行 ClaimTrace，遇到结构错误、依赖断裂或验证失败立即停止。
func (r *TraceRunner) Run(trace ClaimTrace) ClaimTrace {
	result := trace
	result.Steps = append([]ClaimStep(nil), trace.Steps...)
	for index := range result.Steps {
		if result.Steps[index].Status == "" {
			result.Steps[index].Status = StepStatusDraft
		}
	}

	for index := range result.Steps {
		step := &result.Steps[index]
		if shapeErrors := ValidateStepShape(*step); len(shapeErrors) > 0 {
			step.Status = StepStatusNeedsReview
			step.Observation = map[string]any{
				"reason": "invalid_step_shape",
				"errors": shapeErrors,
			}
			result.StopReason = "invalid_step_shape"
			return result
		}
		if !result.dependenciesPassed(*step) {
			step.Status = StepStatusNeedsReview
			step.Observation = map[string]any{
				"reason":     "dependencies_not_passed",
				"depends_on": step.DependsOn,
			}
			result.StopReason = "dependency_not_passed"
			return result
		}
		if step.Kind == StepKindVerify || step.Kind == StepKindDecide {
			validation := r.registry.Run(*step)
			step.Status = validation.Status
			step.Observation = validation.Observation
			if validation.Message != "" {
				step.Observation = mergeObservationMessage(step.Observation, validation.Message)
			}
			if validation.Status != StepStatusPassed {
				result.StopReason = fmt.Sprintf("step_failed:%s", step.StepID)
				return result
			}
			continue
		}

		// OBSERVE / DECOMPOSE / DERIVE 在这个最小实现里由上游证据读取或工具执行负责填充，这里只做形状放行。
		step.Status = StepStatusPassed
	}

	if decision := lastPassedDecision(result.Steps); decision != nil {
		result.FinalDecision = fmt.Sprint(decision.Object)
		result.StopReason = "all_required_claims_verified"
	} else {
		result.StopReason = "no_decision_step"
	}
	return result
}

func (trace ClaimTrace) index() map[string]ClaimStep {
	byID := make(map[string]ClaimStep, len(trace.Steps))
	for _, step := range trace.Steps {
		byID[step.StepID] = step
	}
	return byID
}

func (trace ClaimTrace) dependenciesPassed(step ClaimStep) bool {
	byID := trace.index()
	for _, depID := range step.DependsOn {
		dep, ok := byID[depID]
		if !ok || dep.Status != StepStatusPassed {
			return false
		}
	}
	return true
}

func lastPassedDecision(steps []ClaimStep) *ClaimStep {
	for index := len(steps) - 1; index >= 0; index-- {
		if steps[index].Kind == StepKindDecide && steps[index].Status == StepStatusPassed {
			return &steps[index]
		}
	}
	return nil
}

func mergeObservationMessage(observation any, message string) any {
	if values, ok := observation.(map[string]any); ok {
		values["message"] = message
		return values
	}
	if observation == nil {
		return map[string]any{"message": message}
	}
	return map[string]any{
		"value":   observation,
		"message": message,
	}
}
