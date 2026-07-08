package main

import (
	"context"
	"fmt"
	"strings"

	cotv2 "ai-designing/reasoning/cot_v2"
)

type evidenceSourceKind string

const (
	evidenceSourceRetrieval evidenceSourceKind = "retrieval"
	evidenceSourceTool      evidenceSourceKind = "tool"
	evidenceSourceLog       evidenceSourceKind = "log"
)

type evidenceCallback func(context.Context, evidenceCallbackRequest) (evidenceCallbackResponse, error)

type evidenceCollector struct {
	callback evidenceCallback
}

var newEvidenceCallback = func(question string) evidenceCallback {
	return newPromptEvidenceCallback(question)
}

// evidenceCallbackRequest 是命令层发给工程侧回调的统一请求。
type evidenceCallbackRequest struct {
	Source            evidenceSourceKind    `json:"source"`
	Step              cotv2.EvidenceRequest `json:"step"`
	PriorSteps        []cotv2.ClaimStep     `json:"prior_steps,omitempty"`
	PriorEvidenceRefs []cotv2.EvidenceRef   `json:"prior_evidence_refs,omitempty"`
}

type evidenceCallbackResponse struct {
	EvidenceRefs []cotv2.EvidenceRef `json:"evidence_refs,omitempty"`
	Validator    string              `json:"validator,omitempty"`
	Action       string              `json:"action,omitempty"`
}

type evidenceContext struct {
	PriorSteps []cotv2.ClaimStep
}

func newEvidenceCollector(question string) *evidenceCollector {
	return &evidenceCollector{callback: newEvidenceCallback(question)}
}

// ResolveEvidence 按步骤类型调进程内回调，不根据业务 predicate 硬编码证据内容。
func (c *evidenceCollector) ResolveEvidence(ctx context.Context, req cotv2.EvidenceRequest, evidenceCtx evidenceContext) (cotv2.EvidenceBinding, error) {
	if req.Kind == cotv2.StepKindDecide {
		return cotv2.EvidenceBinding{Validator: "decision_dependencies_gate"}, nil
	}
	if req.Kind == cotv2.StepKindDecompose {
		return cotv2.EvidenceBinding{}, nil
	}
	var binding cotv2.EvidenceBinding
	if c == nil || c.callback == nil {
		return cotv2.EvidenceBinding{}, fmt.Errorf("evidence callback is not configured for %s step predicate %q", req.Kind, req.Predicate)
	}
	for _, source := range sourceKindsForStep(req.Kind) {
		response, err := c.callback(ctx, evidenceCallbackRequest{
			Source:            source,
			Step:              req,
			PriorSteps:        evidenceCtx.PriorSteps,
			PriorEvidenceRefs: collectEvidenceRefs(evidenceCtx.PriorSteps),
		})
		if err != nil {
			return cotv2.EvidenceBinding{}, err
		}
		binding.EvidenceRefs = append(binding.EvidenceRefs, response.EvidenceRefs...)
		if binding.Validator == "" && strings.TrimSpace(response.Validator) != "" {
			binding.Validator = strings.TrimSpace(response.Validator)
		}
		if binding.Action == "" && strings.TrimSpace(response.Action) != "" {
			binding.Action = strings.TrimSpace(response.Action)
		}
	}
	binding.EvidenceRefs = dedupeEvidenceRefs(binding.EvidenceRefs)
	applyDefaultBinding(req.Kind, &binding)
	if err := validateBinding(req, binding); err != nil {
		return cotv2.EvidenceBinding{}, err
	}
	return binding, nil
}

func sourceKindsForStep(kind cotv2.StepKind) []evidenceSourceKind {
	switch kind {
	case cotv2.StepKindObserve:
		return []evidenceSourceKind{evidenceSourceRetrieval, evidenceSourceLog}
	case cotv2.StepKindDerive:
		return []evidenceSourceKind{evidenceSourceTool}
	case cotv2.StepKindVerify:
		return []evidenceSourceKind{evidenceSourceRetrieval, evidenceSourceLog}
	default:
		return nil
	}
}

func collectEvidenceRefs(steps []cotv2.ClaimStep) []cotv2.EvidenceRef {
	refs := make([]cotv2.EvidenceRef, 0)
	for _, step := range steps {
		refs = append(refs, step.EvidenceRefs...)
	}
	return refs
}

func applyDefaultBinding(kind cotv2.StepKind, binding *cotv2.EvidenceBinding) {
	switch kind {
	case cotv2.StepKindVerify:
		if strings.TrimSpace(binding.Validator) == "" {
			binding.Validator = "evidence_exists"
		}
	case cotv2.StepKindDecide:
		if strings.TrimSpace(binding.Validator) == "" {
			binding.Validator = "decision_dependencies_gate"
		}
	}
}

func validateBinding(req cotv2.EvidenceRequest, binding cotv2.EvidenceBinding) error {
	switch req.Kind {
	case cotv2.StepKindObserve, cotv2.StepKindVerify:
		if len(binding.EvidenceRefs) == 0 {
			return fmt.Errorf("missing evidence refs for %s step predicate %q", req.Kind, req.Predicate)
		}
	case cotv2.StepKindDerive:
		if strings.TrimSpace(binding.Action) == "" {
			return fmt.Errorf("missing tool action for derive step predicate %q", req.Predicate)
		}
		if len(binding.EvidenceRefs) == 0 {
			return fmt.Errorf("missing tool evidence refs for derive step predicate %q", req.Predicate)
		}
	case cotv2.StepKindDecide:
		if strings.TrimSpace(binding.Validator) == "" {
			return fmt.Errorf("missing decision validator for predicate %q", req.Predicate)
		}
	}
	return nil
}

func dedupeEvidenceRefs(values []cotv2.EvidenceRef) []cotv2.EvidenceRef {
	result := make([]cotv2.EvidenceRef, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.Join([]string{
			strings.TrimSpace(value.SourceType),
			strings.TrimSpace(value.SourceID),
			strings.TrimSpace(value.Version),
			strings.TrimSpace(value.EffectiveAt),
		}, "\x00")
		if key == "\x00\x00\x00" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
