package hypothesis

import (
	"strings"
	"testing"
)

// TestBuildPlannerPromptDoesNotLeakPreviousCandidateText 验证重规划 prompt 不携带上一轮 candidate 原文。
func TestBuildPlannerPromptDoesNotLeakPreviousCandidateText(t *testing.T) {
	existing := []Hypothesis{
		{Description: "报名表新增身份证号造成用户流失", Status: StatusTesting, Prior: 0.4, Posterior: 0.5},
		{Description: "活动时间撞上学校运动会", Status: StatusFalsified, Prior: 0.3, Posterior: 0.1},
	}
	prompt := buildPlannerPrompt("社区活动报名下降", "社区活动诊断", existing, 2, 2)
	for _, h := range existing {
		if strings.Contains(prompt, h.Description) {
			t.Fatalf("planner prompt leaked candidate %q:\n%s", h.Description, prompt)
		}
	}
	if !strings.Contains(prompt, "已存在 2 个候选") || !strings.Contains(prompt, "testing=1") || !strings.Contains(prompt, "falsified=1") {
		t.Fatalf("planner prompt missing summary:\n%s", prompt)
	}
}

// TestFilterNovelProposalsDropsExistingCandidates 验证 planner 重复返回旧候选时会被代码层过滤。
func TestFilterNovelProposalsDropsExistingCandidates(t *testing.T) {
	existing := []Hypothesis{
		{Description: "报名表新增身份证号造成用户流失", Status: StatusTesting},
	}
	proposals := []Proposal{
		{Description: "报名表新增身份证号造成用户流失", Prior: 0.6},
		{Description: "海报标题降低亲子体验感", Prior: 0.4},
	}
	filtered := filterNovelProposals(proposals, existing)
	if len(filtered) != 1 || filtered[0].Description != "海报标题降低亲子体验感" {
		t.Fatalf("filtered = %+v", filtered)
	}
}
