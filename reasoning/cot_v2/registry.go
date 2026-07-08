package cot_v2

import (
	"fmt"
	"strings"
)

// ValidatorResult 是确定性验证器返回的状态和观察结果。
type ValidatorResult struct {
	Status      StepStatus `json:"status"`
	Observation any        `json:"observation,omitempty"`
	Message     string     `json:"message,omitempty"`
}

// Reason 返回适合写入 stop_reason 或测试断言的简短原因。
func (r ValidatorResult) Reason() string {
	if strings.TrimSpace(r.Message) != "" {
		return strings.TrimSpace(r.Message)
	}
	if values, ok := r.Observation.(map[string]any); ok {
		if reason, ok := values["reason"].(string); ok {
			return strings.TrimSpace(reason)
		}
	}
	return ""
}

// Validator 是绑定到 ClaimStep.Validator 名称上的确定性检查函数。
type Validator func(ClaimStep) ValidatorResult

// ValidatorRegistry 把验证器名称绑定到程序侧检查逻辑。
type ValidatorRegistry struct {
	validators map[string]Validator
}

// NewValidatorRegistry 创建空验证器注册表。
func NewValidatorRegistry() *ValidatorRegistry {
	return &ValidatorRegistry{validators: make(map[string]Validator)}
}

// Register 注册一个验证器；空名称或 nil 函数会被忽略，避免运行期 panic。
func (r *ValidatorRegistry) Register(name string, validator Validator) {
	if r == nil || validator == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if r.validators == nil {
		r.validators = make(map[string]Validator)
	}
	r.validators[name] = validator
}

// Run 执行命名验证器；缺失或未知时进入 needs_review，而不是让模型继续硬编。
func (r *ValidatorRegistry) Run(step ClaimStep) ValidatorResult {
	validatorName := strings.TrimSpace(step.Validator)
	if validatorName == "" {
		return needsReviewResult("missing validator")
	}
	if r == nil || r.validators == nil {
		return needsReviewResult(fmt.Sprintf("unknown validator: %s", validatorName))
	}
	validator := r.validators[validatorName]
	if validator == nil {
		return needsReviewResult(fmt.Sprintf("unknown validator: %s", validatorName))
	}
	result := validator(step)
	if result.Status == "" {
		result.Status = StepStatusNeedsReview
	}
	return result
}

func needsReviewResult(reason string) ValidatorResult {
	return ValidatorResult{
		Status:      StepStatusNeedsReview,
		Observation: map[string]any{"reason": reason},
	}
}
