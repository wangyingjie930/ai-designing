package hypothesis

import (
	"context"
	"strings"
	"testing"
)

// TestLoopConvergesAfterFalsifyingAlternatives 验证循环按“唯一确认幸存假设”收敛。
func TestLoopConvergesAfterFalsifyingAlternatives(t *testing.T) {
	planner := func(context.Context, string, []Hypothesis, int) ([]Proposal, error) {
		return []Proposal{
			{Description: "海报标题降低了亲子体验感", Prior: 0.35},
			{Description: "报名表新增字段造成流程摩擦", Prior: 0.45},
			{Description: "学校运动会撞期分流了家长", Prior: 0.20},
		}, nil
	}
	generator := func(_ context.Context, h Hypothesis) ([]EvidenceCandidate, error) {
		return []EvidenceCandidate{{Description: "已知报名表新增身份证号和紧急联系人必填字段", Source: "case_brief"}}, nil
	}
	evaluator := func(_ context.Context, h Hypothesis, evidence EvidenceCandidate) (Evaluation, error) {
		if strings.Contains(h.Description, "报名表") {
			return Evaluation{Effect: EffectSupports, PosteriorDelta: 0.55}, nil
		}
		return Evaluation{Effect: EffectRefutes, PosteriorDelta: -0.4}, nil
	}
	loop, err := NewIterativeHypothesisLoop(planner, generator, evaluator, 3)
	if err != nil {
		t.Fatal(err)
	}
	tree, outcome, err := loop.Run(context.Background(), "社区活动报名下滑")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Converged || outcome.NeedsHITL || outcome.IterationsUsed != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if tree.SurvivorCount() != 1 || len(tree.Confirmed()) != 1 {
		t.Fatalf("snapshot = %+v", tree.Snapshot())
	}
	confirmed := tree.ByID(outcome.ConfirmedID)
	if confirmed == nil || !strings.Contains(confirmed.Description, "报名表") {
		t.Fatalf("confirmed = %+v", confirmed)
	}
}

// TestLoopNeedsHITLWhenMultipleSurvivorsRemain 验证达到上限且多假设幸存时会要求人工介入。
func TestLoopNeedsHITLWhenMultipleSurvivorsRemain(t *testing.T) {
	planner := func(context.Context, string, []Hypothesis, int) ([]Proposal, error) {
		return []Proposal{
			{Description: "价格感知变差", Prior: 0.5},
			{Description: "活动主题不够清晰", Prior: 0.5},
		}, nil
	}
	generator := func(context.Context, Hypothesis) ([]EvidenceCandidate, error) {
		return []EvidenceCandidate{{Description: "现有信息无法排除该解释", Source: "missing_check"}}, nil
	}
	evaluator := func(context.Context, Hypothesis, EvidenceCandidate) (Evaluation, error) {
		return Evaluation{Effect: EffectNeutral, PosteriorDelta: 0}, nil
	}
	loop, err := NewIterativeHypothesisLoop(planner, generator, evaluator, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, outcome, err := loop.Run(context.Background(), "报名下降")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Converged || !outcome.NeedsHITL || outcome.IterationsUsed != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
}
