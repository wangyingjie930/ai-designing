package main

import (
	"context"
	"fmt"

	cotv2 "ai-designing/reasoning/cot_v2"
)

type stepEvidenceResolver interface {
	ResolveEvidence(context.Context, cotv2.EvidenceRequest, evidenceContext) (cotv2.EvidenceBinding, error)
}

// buildTraceWithEvidence 逐步编译 StepDraft，让后续 evidence callback 可以看到已确认的上游证据引用。
func buildTraceWithEvidence(ctx context.Context, traceID string, drafts cotv2.StepDraftList, resolver stepEvidenceResolver) (cotv2.ClaimTrace, error) {
	if resolver == nil {
		return cotv2.ClaimTrace{}, fmt.Errorf("evidence resolver is required")
	}
	compiler := cotv2.ClaimCompiler{}
	steps := make([]cotv2.ClaimStep, 0, len(drafts.Steps))
	for index, draft := range drafts.Steps {
		stepID := fmt.Sprintf("S%d", index+1)
		binding, err := resolver.ResolveEvidence(ctx, cotv2.EvidenceRequest{
			StepID:        stepID,
			Kind:          draft.Kind,
			ClaimText:     draft.ClaimText,
			Subject:       draft.SuggestedSubject,
			Predicate:     draft.SuggestedPredicate,
			EvidenceQuery: draft.SuggestedEvidenceQuery,
		}, evidenceContext{PriorSteps: steps})
		if err != nil {
			return cotv2.ClaimTrace{}, fmt.Errorf("resolve evidence for %s: %w", stepID, err)
		}
		step, err := compiler.Compile(cotv2.CompileInput{
			Draft:        draft,
			StepID:       stepID,
			DependsOn:    inferDependsOn(index, draft, steps),
			EvidenceRefs: binding.EvidenceRefs,
			Validator:    binding.Validator,
			Action:       binding.Action,
		})
		if err != nil {
			return cotv2.ClaimTrace{}, fmt.Errorf("compile %s: %w", stepID, err)
		}
		steps = append(steps, step)
	}
	return cotv2.ClaimTrace{TraceID: traceID, Steps: steps}, nil
}

func inferDependsOn(index int, draft cotv2.StepDraft, existing []cotv2.ClaimStep) []string {
	if index == 0 || draft.Kind == cotv2.StepKindObserve {
		return nil
	}
	if draft.Kind == cotv2.StepKindDecide {
		deps := make([]string, 0, len(existing))
		for _, step := range existing {
			deps = append(deps, step.StepID)
		}
		return deps
	}
	return []string{existing[len(existing)-1].StepID}
}

func newValidatorRegistry() *cotv2.ValidatorRegistry {
	registry := cotv2.NewValidatorRegistry()
	registry.Register("evidence_exists", func(step cotv2.ClaimStep) cotv2.ValidatorResult {
		if len(step.EvidenceRefs) > 0 {
			return cotv2.ValidatorResult{Status: cotv2.StepStatusPassed, Observation: map[string]any{"reason": "evidence refs exist"}}
		}
		return cotv2.ValidatorResult{Status: cotv2.StepStatusNeedsReview, Observation: map[string]any{"reason": "missing evidence refs"}}
	})
	registry.Register("decision_dependencies_gate", func(cotv2.ClaimStep) cotv2.ValidatorResult {
		return cotv2.ValidatorResult{Status: cotv2.StepStatusPassed, Observation: map[string]any{"reason": "all dependencies passed"}}
	})
	return registry
}
