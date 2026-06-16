package compaction

import "testing"

// TestParseAnchorFromJSON 验证 Anchor 只从结构化 JSON 中提取。
func TestParseAnchorFromJSON(t *testing.T) {
	base := Anchor{
		Intent:             "旧意图",
		ChangesMade:        []string{"已查询订单"},
		DecisionsTaken:     []string{"排除客户端网络"},
		ExcludedApproaches: []string{"不要只让用户重试"},
		NextSteps:          []string{"继续排查"},
	}

	got := ParseAnchor(`{
		"intent": "恢复支付网关",
		"changes_made": ["已查询订单", "已查询支付网关日志"],
		"decisions_taken": ["502 来自上游 timeout"],
		"excluded_approaches": ["不要只让用户重试", "不要重复走 retry path A"],
		"next_steps": ["升级二线", "检查 queue_depth=347"]
	}`, base)

	if got.Intent != "恢复支付网关" {
		t.Fatalf("unexpected intent: %q", got.Intent)
	}
	assertContainsString(t, got.ChangesMade, "已查询订单")
	assertContainsString(t, got.ChangesMade, "已查询支付网关日志")
	assertContainsString(t, got.DecisionsTaken, "排除客户端网络")
	assertContainsString(t, got.DecisionsTaken, "502 来自上游 timeout")
	assertContainsString(t, got.ExcludedApproaches, "不要只让用户重试")
	assertContainsString(t, got.ExcludedApproaches, "不要重复走 retry path A")
	if len(got.NextSteps) != 2 || got.NextSteps[0] != "升级二线" {
		t.Fatalf("unexpected next steps: %#v", got.NextSteps)
	}
}

// TestParseAnchorFromMarkdownFencedJSON 兼容模型偶发的 json fence。
func TestParseAnchorFromMarkdownFencedJSON(t *testing.T) {
	got := ParseAnchor("```json\n{\"intent\":\"处理 P1 工单\",\"next_steps\":[\"准备交接\"]}\n```", Anchor{})
	if got.Intent != "处理 P1 工单" {
		t.Fatalf("unexpected intent: %q", got.Intent)
	}
	assertContainsString(t, got.NextSteps, "准备交接")
}

// TestParseAnchorInvalidJSONKeepsBase 确保自由文本不会被误解析。
func TestParseAnchorInvalidJSONKeepsBase(t *testing.T) {
	base := Anchor{
		Intent:             "保留原始 Anchor",
		ExcludedApproaches: []string{"不要重复刷新"},
	}
	got := ParseAnchor("INTENT: 这不是 JSON\nNEXT: 重试", base)
	if got.Intent != base.Intent {
		t.Fatalf("expected base intent, got %q", got.Intent)
	}
	if len(got.ExcludedApproaches) != 1 || got.ExcludedApproaches[0] != "不要重复刷新" {
		t.Fatalf("expected base exclusions, got %#v", got.ExcludedApproaches)
	}
}

// assertContainsString 断言字符串切片包含目标值。
func assertContainsString(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("expected %q in %#v", want, values)
}
