package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ai-designing/cmd/internal/e2etest"
)

// TestDefaultBusinessMessagesShape 验证 demo 输入是两轮自然语言业务消息，而不是手动工具参数。
func TestDefaultBusinessMessagesShape(t *testing.T) {
	messages := defaultBusinessMessages()
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}
	if !strings.Contains(messages[0], "修复动作") || !strings.Contains(messages[0], "复盘") {
		t.Fatalf("first message should be a resolved retrospective, got %q", messages[0])
	}
	if !strings.Contains(messages[1], "现场求助") || strings.Contains(messages[1], "修复动作") {
		t.Fatalf("second message should be a live request without a baked-in fix, got %q", messages[1])
	}
	joined := strings.Join(messages, "\n")
	for _, fabricatedKey := range []string{
		"assign_room_or_compensation",
		"pms_room_status",
		"housekeeping_room_status",
		"room_inventory",
		"compensation_approval",
		"mechanical_keys=",
		"tool=",
	} {
		if strings.Contains(joined, fabricatedKey) {
			t.Fatalf("default business messages should not seed fabricated recall key %q; messages=%s", fabricatedKey, joined)
		}
	}
}

// TestResolveDBPathFromEnv 验证外部需要持久 SQLite 时只需设置环境变量，不需要业务参数。
func TestResolveDBPathFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.sqlite")
	t.Setenv(dbEnvKey, path)
	got, cleanup, err := resolveDBPath()
	if err != nil {
		t.Fatalf("resolveDBPath() error = %v", err)
	}
	cleanup()
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

// TestFailureTrackingAgentEndToEnd 用真实 Eino ADK Agent 跑通 first record -> later search 的两轮业务链路。
func TestFailureTrackingAgentEndToEnd(t *testing.T) {
	if !e2etest.Enabled() {
		t.Skipf("跳过 cmd 端到端测试；设置 %s=1 后会使用 .env 真实调用模型", e2etest.EnvName)
	}
	t.Setenv(dbEnvKey, "")
	output, err := runAgent(context.Background())
	if err != nil {
		t.Fatalf("runAgent() error = %v", err)
	}
	if output.Rounds != 2 {
		t.Fatalf("rounds = %d, want 2", output.Rounds)
	}
	if output.Entries != 1 {
		t.Fatalf("entries = %d, want 1; first round should record, second round should search without duplicate record", output.Entries)
	}
}
