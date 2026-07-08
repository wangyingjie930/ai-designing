package cot_v2

import (
	"context"
	"testing"
)

type fakeEvidenceProvider struct {
	binding EvidenceBinding
}

func (p fakeEvidenceProvider) ResolveEvidence(context.Context, EvidenceRequest) (EvidenceBinding, error) {
	return p.binding, nil
}

// TestBuildClaimTraceUsesEvidenceProvider 验证正式 ClaimTrace 的证据来自外部 provider，而不是关键词硬猜。
func TestBuildClaimTraceUsesEvidenceProvider(t *testing.T) {
	drafts := StepDraftList{Steps: []StepDraft{
		{
			Kind:                   StepKindVerify,
			ClaimText:              "季度奖金审批单存在，金额、审批人和生效月份均匹配。",
			SuggestedSubject:       "approval:BONUS-18472",
			SuggestedPredicate:     "approval_matches",
			SuggestedEvidenceQuery: "读取 BONUS-18472 审批单",
		},
	}}
	provider := fakeEvidenceProvider{binding: EvidenceBinding{
		EvidenceRefs: []EvidenceRef{{SourceID: "approval:BONUS-18472", SourceType: "approval", Version: "signed"}},
		Validator:    "approval_validator",
	}}

	trace, err := BuildClaimTrace(context.Background(), "trace-001", drafts, provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Steps) != 1 || trace.Steps[0].EvidenceRefs[0].SourceID != "approval:BONUS-18472" {
		t.Fatalf("trace = %+v", trace)
	}
	if trace.Steps[0].Validator != "approval_validator" {
		t.Fatalf("validator = %q", trace.Steps[0].Validator)
	}
}
