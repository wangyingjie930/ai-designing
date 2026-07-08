package cot_v2

import (
	"strings"
	"testing"
)

// TestClaimCompilerAndShapeGateKeepStepsAtomic 验证草稿必须先被编译成带证据和结构化谓词的 ClaimStep。
func TestClaimCompilerAndShapeGateKeepStepsAtomic(t *testing.T) {
	compiler := ClaimCompiler{}
	step, err := compiler.Compile(CompileInput{
		Draft: StepDraft{
			Kind:               StepKindObserve,
			ClaimText:          "P 的 6 月应发工资比 5 月高 18%。",
			SuggestedSubject:   "employee:P",
			SuggestedPredicate: "pay_delta_percent",
			SuggestedObject:    18,
		},
		StepID: "S1",
		EvidenceRefs: []EvidenceRef{{
			SourceID:   "payroll_snapshot:2026-06:v3",
			SourceType: "payroll_snapshot",
			Version:    "v3",
		}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if step.Status != StepStatusDraft || step.StepID != "S1" || step.Predicate != "pay_delta_percent" {
		t.Fatalf("compiled step = %+v", step)
	}
	if errors := ValidateStepShape(step); len(errors) != 0 {
		t.Fatalf("ValidateStepShape() errors = %v", errors)
	}

	overloaded := step
	overloaded.ClaimText = "P 的工资上涨并且主要来自奖金，因此可以自动放行。"
	if errors := ValidateStepShape(overloaded); !containsError(errors, "multiple claims") {
		t.Fatalf("ValidateStepShape() should reject composite claim, errors=%v", errors)
	}
}

// TestValidateStepShapeEnforcesKindSpecificGates 验证不同 kind 有不同准入条件。
func TestValidateStepShapeEnforcesKindSpecificGates(t *testing.T) {
	tests := []struct {
		name string
		step ClaimStep
		want string
	}{
		{
			name: "observe requires evidence",
			step: ClaimStep{
				StepID:    "S1",
				Kind:      StepKindObserve,
				ClaimText: "P 的 6 月应发工资比 5 月高 18%。",
				Subject:   "employee:P",
				Predicate: "pay_delta_percent",
			},
			want: "OBSERVE step requires evidence_refs",
		},
		{
			name: "derive requires action",
			step: ClaimStep{
				StepID:    "S3",
				Kind:      StepKindDerive,
				ClaimText: "18% 的增量主要来自季度奖金。",
				Subject:   "employee:P",
				Predicate: "bonus_delta",
				DependsOn: []string{"S1"},
			},
			want: "DERIVE step requires a tool or computation action",
		},
		{
			name: "verify requires validator",
			step: ClaimStep{
				StepID:       "S4",
				Kind:         StepKindVerify,
				ClaimText:    "该租户规则要求应发涨幅超过 15% 时核查审批。",
				Subject:      "tenant:acme",
				Predicate:    "requires_approval_over_percent",
				EvidenceRefs: []EvidenceRef{{SourceID: "policy:payroll_anomaly:v7", SourceType: "policy"}},
				DependsOn:    []string{"S2"},
			},
			want: "VERIFY step requires validator",
		},
		{
			name: "decide requires dependency",
			step: ClaimStep{
				StepID:    "S6",
				Kind:      StepKindDecide,
				ClaimText: "本次异常可以自动放行。",
				Subject:   "payroll_exception:P:2026-06",
				Predicate: "release_decision",
				Validator: "payroll_release_gate",
			},
			want: "DECIDE step requires dependencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errors := ValidateStepShape(tt.step); !containsError(errors, tt.want) {
				t.Fatalf("ValidateStepShape() errors=%v, want %q", errors, tt.want)
			}
		})
	}
}

// TestTraceRunnerStopsWhenValidatorFails 验证确定性验证失败会停止，并阻断下游决策。
func TestTraceRunnerStopsWhenValidatorFails(t *testing.T) {
	registry := NewValidatorRegistry()
	registry.Register("policy_rule_check", func(ClaimStep) ValidatorResult {
		return ValidatorResult{
			Status:      StepStatusFailed,
			Observation: map[string]any{"reason": "policy version expired"},
		}
	})
	registry.Register("payroll_release_gate", func(ClaimStep) ValidatorResult {
		t.Fatal("decision gate should not run after upstream validator failed")
		return ValidatorResult{Status: StepStatusPassed}
	})

	trace := ClaimTrace{
		TraceID: "trace-payroll-001",
		Steps: []ClaimStep{
			{
				StepID:       "S1",
				Kind:         StepKindObserve,
				ClaimText:    "P 的 6 月应发工资比 5 月高 18%。",
				Subject:      "employee:P",
				Predicate:    "pay_delta_percent",
				EvidenceRefs: []EvidenceRef{{SourceID: "payroll_snapshot:2026-06:v3", SourceType: "payroll_snapshot"}},
			},
			{
				StepID:       "S2",
				Kind:         StepKindVerify,
				ClaimText:    "该租户规则要求应发涨幅超过 15% 时核查审批。",
				Subject:      "tenant:acme",
				Predicate:    "requires_approval_over_percent",
				DependsOn:    []string{"S1"},
				EvidenceRefs: []EvidenceRef{{SourceID: "policy:payroll_anomaly:v7", SourceType: "policy"}},
				Validator:    "policy_rule_check",
			},
			{
				StepID:    "S3",
				Kind:      StepKindDecide,
				ClaimText: "本次异常可以自动放行。",
				Subject:   "payroll_exception:P:2026-06",
				Predicate: "release_decision",
				DependsOn: []string{"S2"},
				Validator: "payroll_release_gate",
				Object:    "auto_release",
			},
		},
	}

	result := NewTraceRunner(registry).Run(trace)
	if result.StopReason != "step_failed:S2" {
		t.Fatalf("StopReason = %q", result.StopReason)
	}
	if result.Steps[0].Status != StepStatusPassed || result.Steps[1].Status != StepStatusFailed || result.Steps[2].Status != StepStatusDraft {
		t.Fatalf("step statuses = %s, %s, %s", result.Steps[0].Status, result.Steps[1].Status, result.Steps[2].Status)
	}
	if result.FinalDecision != "" {
		t.Fatalf("FinalDecision = %q, want empty", result.FinalDecision)
	}
}

// TestTraceRunnerWritesFinalDecisionWhenDecisionGatePasses 验证所有关键依赖通过后才生成最终决定。
func TestTraceRunnerWritesFinalDecisionWhenDecisionGatePasses(t *testing.T) {
	registry := NewValidatorRegistry()
	registry.Register("payroll_release_gate", func(ClaimStep) ValidatorResult {
		return ValidatorResult{
			Status:      StepStatusPassed,
			Observation: map[string]any{"reason": "all dependencies passed"},
		}
	})

	trace := ClaimTrace{
		TraceID: "trace-payroll-002",
		Steps: []ClaimStep{
			{
				StepID:       "S1",
				Kind:         StepKindObserve,
				ClaimText:    "P 的 6 月应发工资比 5 月高 18%。",
				Subject:      "employee:P",
				Predicate:    "pay_delta_percent",
				EvidenceRefs: []EvidenceRef{{SourceID: "payroll_snapshot:2026-06:v3", SourceType: "payroll_snapshot"}},
			},
			{
				StepID:    "S2",
				Kind:      StepKindDecide,
				ClaimText: "本次异常可以自动放行。",
				Subject:   "payroll_exception:P:2026-06",
				Predicate: "release_decision",
				DependsOn: []string{"S1"},
				Validator: "payroll_release_gate",
				Object:    "auto_release",
			},
		},
	}

	result := NewTraceRunner(registry).Run(trace)
	if result.StopReason != "all_required_claims_verified" {
		t.Fatalf("StopReason = %q", result.StopReason)
	}
	if result.FinalDecision != "auto_release" {
		t.Fatalf("FinalDecision = %q", result.FinalDecision)
	}
	if result.Steps[1].Status != StepStatusPassed {
		t.Fatalf("decision status = %s", result.Steps[1].Status)
	}
}

// TestValidatorRegistryReturnsNeedsReviewForMissingValidator 验证验证器不是可选散文，缺失时必须进入复核状态。
func TestValidatorRegistryReturnsNeedsReviewForMissingValidator(t *testing.T) {
	result := NewValidatorRegistry().Run(ClaimStep{
		StepID:    "S4",
		Kind:      StepKindVerify,
		ClaimText: "审批单金额和月份匹配。",
		Subject:   "approval:BONUS-18472",
		Predicate: "approval_matches",
	})
	if result.Status != StepStatusNeedsReview {
		t.Fatalf("Status = %s", result.Status)
	}
	if !strings.Contains(result.Reason(), "missing validator") {
		t.Fatalf("Reason() = %q", result.Reason())
	}
}

func containsError(errors []string, want string) bool {
	for _, err := range errors {
		if strings.Contains(err, want) {
			return true
		}
	}
	return false
}
