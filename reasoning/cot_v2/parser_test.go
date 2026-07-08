package cot_v2

import "testing"

// TestParseStepDraftListAcceptsStrictJSONAndCodeFence 验证模型响应解析只关心 JSON 对象本身。
func TestParseStepDraftListAcceptsStrictJSONAndCodeFence(t *testing.T) {
	text := "```json\n" + `{
  "steps": [
    {"kind":"observe","claim_text":"P 的 6 月应发工资比 5 月高 18%。","suggested_subject":"employee:P","suggested_predicate":"pay_delta_percent"}
  ]
}` + "\n```"

	drafts, err := ParseStepDraftList(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts.Steps) != 1 || drafts.Steps[0].Kind != StepKindObserve {
		t.Fatalf("drafts = %+v", drafts)
	}
}

// TestParseStepDraftListRejectsEmptyClaim 验证空命题不会进入后续编译器。
func TestParseStepDraftListRejectsEmptyClaim(t *testing.T) {
	_, err := ParseStepDraftList(`{"steps":[{"kind":"observe","claim_text":"   "} ]}`)
	if err == nil {
		t.Fatal("ParseStepDraftList() should reject empty claim_text")
	}
}
