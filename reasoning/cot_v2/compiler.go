package cot_v2

import (
	"errors"
	"strings"
)

// CompileInput 是 StepDraft 进入正式 ClaimStep 前由程序补齐的工程上下文。
type CompileInput struct {
	Draft        StepDraft
	StepID       string
	EvidenceRefs []EvidenceRef
	Validator    string
	DependsOn    []string
	Action       string
}

// ClaimCompiler 把模型候选草稿编译成应用侧可审计的 ClaimStep。
type ClaimCompiler struct{}

// Compile 将 LLM 草稿和程序侧证据、依赖、验证器合并，形成正式 ClaimStep。
func (ClaimCompiler) Compile(input CompileInput) (ClaimStep, error) {
	stepID := strings.TrimSpace(input.StepID)
	if stepID == "" {
		return ClaimStep{}, errors.New("step_id is required")
	}
	if strings.TrimSpace(input.Draft.SuggestedSubject) == "" || strings.TrimSpace(input.Draft.SuggestedPredicate) == "" {
		return ClaimStep{}, errors.New("draft lacks structured claim fields")
	}
	return ClaimStep{
		StepID:       stepID,
		Kind:         input.Draft.Kind,
		ClaimText:    strings.TrimSpace(input.Draft.ClaimText),
		Subject:      strings.TrimSpace(input.Draft.SuggestedSubject),
		Predicate:    strings.TrimSpace(input.Draft.SuggestedPredicate),
		Object:       input.Draft.SuggestedObject,
		DependsOn:    compactStrings(input.DependsOn),
		EvidenceRefs: compactEvidenceRefs(input.EvidenceRefs),
		Action:       strings.TrimSpace(input.Action),
		Validator:    strings.TrimSpace(input.Validator),
		Status:       StepStatusDraft,
	}, nil
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func compactEvidenceRefs(values []EvidenceRef) []EvidenceRef {
	result := make([]EvidenceRef, 0, len(values))
	for _, value := range values {
		value.SourceID = strings.TrimSpace(value.SourceID)
		value.SourceType = strings.TrimSpace(value.SourceType)
		value.Version = strings.TrimSpace(value.Version)
		value.EffectiveAt = strings.TrimSpace(value.EffectiveAt)
		value.ContentHash = strings.TrimSpace(value.ContentHash)
		if value.SourceID != "" || value.SourceType != "" {
			result = append(result, value)
		}
	}
	return result
}
