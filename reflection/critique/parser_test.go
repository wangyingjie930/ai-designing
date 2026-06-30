package critique

import (
	"strings"
	"testing"
)

// TestParseCritiqueResultAcceptsFencedJSON 验证 critic 输出被代码块包裹时仍能解析成稳定结构。
func TestParseCritiqueResultAcceptsFencedJSON(t *testing.T) {
	result, err := ParseCritiqueResult("```json\n" + `{
  "approved": false,
  "issues": ["缺少明确 CTA", "风险兜底不够"],
  "suggestions": ["补充报名入口", "加入雨天备用方案"],
  "score": 1.2
}` + "\n```")
	if err != nil {
		t.Fatalf("ParseCritiqueResult() error = %v", err)
	}
	if result.Approved {
		t.Fatalf("approved = true, want false")
	}
	if result.Score != 1 {
		t.Fatalf("score = %.2f, want 1.00", result.Score)
	}
	feedback := result.FeedbackText()
	for _, want := range []string{"Issues found:", "缺少明确 CTA", "Suggestions:", "加入雨天备用方案"} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("feedback missing %q:\n%s", want, feedback)
		}
	}
}

// TestParseCritiqueResultRejectsMalformedJSON 验证 critic 没有返回 JSON 时不会被误判为通过。
func TestParseCritiqueResultRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseCritiqueResult("这个方案还可以，但我不输出 JSON"); err == nil {
		t.Fatal("ParseCritiqueResult() error = nil, want error")
	}
}
