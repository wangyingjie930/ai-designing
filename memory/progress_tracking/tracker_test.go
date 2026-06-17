package progresstracking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTrackerCreateCompleteFailAndReload 验证任务状态写入 SQLite 后，新实例能恢复完整上下文。
func TestTrackerCreateCompleteFailAndReload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "progress.sqlite")
	tracker, err := NewProgressTracker(ctx, Config{
		DBPath: path,
		PlanID: "event-ops",
		Now: func() time.Time {
			return time.Unix(1710000000, 0).UTC()
		},
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	if err := tracker.CreatePlan(ctx, []string{
		"确认场地合同和付款节点",
		"锁定主讲嘉宾档期",
		"准备签到物料",
	}); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if err := tracker.Complete(ctx, 0, "场地合同已确认，付款节点写入共享清单。", []string{"venue-contract.pdf"}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := tracker.Fail(ctx, 1, "主讲嘉宾还没有确认返程时间"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	contextText := tracker.ResumptionContext()
	for _, want := range []string{
		"Progress: 1/3",
		"✓ 确认场地合同和付款节点",
		"Files: venue-contract.pdf",
		"✗ 锁定主讲嘉宾档期",
		"Last error: 主讲嘉宾还没有确认返程时间",
	} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("resumption context missing %q:\n%s", want, contextText)
		}
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reloaded, err := NewProgressTracker(ctx, Config{DBPath: path, PlanID: "event-ops"})
	if err != nil {
		t.Fatalf("reload NewProgressTracker() error = %v", err)
	}
	defer reloaded.Close()
	items := reloaded.Items()
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}
	if items[0].Status != TaskStatusCompleted || items[1].Status != TaskStatusFailed || items[2].Status != TaskStatusPending {
		t.Fatalf("unexpected statuses: %+v", items)
	}
	if !strings.Contains(reloaded.ResumptionContext(), "Progress: 1/3") {
		t.Fatalf("reload context = %s", reloaded.ResumptionContext())
	}
}

// TestTrackerStartAndValidation 验证 in_progress checkpoint 和越界保护。
func TestTrackerStartAndValidation(t *testing.T) {
	ctx := context.Background()
	tracker, err := NewProgressTracker(ctx, Config{
		DBPath: filepath.Join(t.TempDir(), "progress.sqlite"),
		PlanID: "event-ops",
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	defer tracker.Close()
	if err := tracker.CreatePlan(ctx, []string{"确认预算", "发布报名页"}); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if err := tracker.Start(ctx, 1); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := tracker.Items()[1].Status; got != TaskStatusInProgress {
		t.Fatalf("status = %s, want %s", got, TaskStatusInProgress)
	}
	if err := tracker.Start(ctx, 9); err == nil {
		t.Fatal("Start() expected out-of-range error")
	}
	if err := tracker.Complete(ctx, 1, "", nil); err == nil {
		t.Fatal("Complete() expected empty result error")
	}
}
