package cot_v2

import (
	"context"
	"fmt"
)

// EvidenceRequest 是 ClaimCompiler 向外部证据系统发起的解析请求。
type EvidenceRequest struct {
	StepID        string   `json:"step_id"`
	Kind          StepKind `json:"kind"`
	ClaimText     string   `json:"claim_text"`
	Subject       string   `json:"subject"`
	Predicate     string   `json:"predicate"`
	EvidenceQuery string   `json:"evidence_query,omitempty"`
}

// EvidenceBinding 是外部证据系统返回的工程绑定，模型不能自己声明这些字段已经成立。
type EvidenceBinding struct {
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
	Validator    string        `json:"validator,omitempty"`
	Action       string        `json:"action,omitempty"`
}

// EvidenceProvider 从业务系统、RAG、DB、RPC 或人工确认结果中解析证据引用和验证器。
type EvidenceProvider interface {
	ResolveEvidence(context.Context, EvidenceRequest) (EvidenceBinding, error)
}

// BuildClaimTrace 把模型 StepDraftList 编译成正式 ClaimTrace，证据绑定必须来自 EvidenceProvider。
func BuildClaimTrace(ctx context.Context, traceID string, drafts StepDraftList, provider EvidenceProvider) (ClaimTrace, error) {
	if provider == nil {
		return ClaimTrace{}, fmt.Errorf("evidence provider is required")
	}
	compiler := ClaimCompiler{}
	steps := make([]ClaimStep, 0, len(drafts.Steps))
	for index, draft := range drafts.Steps {
		stepID := fmt.Sprintf("S%d", index+1)
		binding, err := provider.ResolveEvidence(ctx, EvidenceRequest{
			StepID:        stepID,
			Kind:          draft.Kind,
			ClaimText:     draft.ClaimText,
			Subject:       draft.SuggestedSubject,
			Predicate:     draft.SuggestedPredicate,
			EvidenceQuery: draft.SuggestedEvidenceQuery,
		})
		if err != nil {
			return ClaimTrace{}, fmt.Errorf("resolve evidence for %s: %w", stepID, err)
		}
		step, err := compiler.Compile(CompileInput{
			Draft:        draft,
			StepID:       stepID,
			DependsOn:    inferStepDependsOn(index, draft, steps),
			EvidenceRefs: binding.EvidenceRefs,
			Validator:    binding.Validator,
			Action:       binding.Action,
		})
		if err != nil {
			return ClaimTrace{}, fmt.Errorf("compile %s: %w", stepID, err)
		}
		steps = append(steps, step)
	}
	return ClaimTrace{
		TraceID: traceID,
		Steps:   steps,
	}, nil
}

func inferStepDependsOn(index int, draft StepDraft, existing []ClaimStep) []string {
	if index == 0 || draft.Kind == StepKindObserve {
		return nil
	}
	if draft.Kind == StepKindDecide {
		deps := make([]string, 0, len(existing))
		for _, step := range existing {
			deps = append(deps, step.StepID)
		}
		return deps
	}
	return []string{existing[len(existing)-1].StepID}
}
