package claudetasklist

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestToolsetCreatesReadsUpdatesAndListsTasks 验证四工具的确定性边界复用同一份任务真相。
func TestToolsetCreatesReadsUpdatesAndListsTasks(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "tools")
	if err != nil {
		t.Fatal(err)
	}
	toolset := Toolset{Store: store}

	created, err := toolset.Create(ctx, TaskCreateRequest{
		Subject: "实现工具", Description: "实现四个 Eino 工具", Metadata: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task == nil || created.Task.Status != TaskStatusPending {
		t.Fatalf("created=%+v", created)
	}

	inProgress := TaskUpdateStatusInProgress
	updated, err := toolset.Update(ctx, TaskUpdateRequest{TaskID: created.Task.ID, Status: &inProgress})
	if err != nil {
		t.Fatal(err)
	}
	got, err := toolset.Get(ctx, TaskGetRequest{TaskID: created.Task.ID})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := toolset.List(ctx, TaskListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Task == nil || updated.Task.Status != TaskStatusInProgress || got.Task == nil || got.Task.Subject != "实现工具" {
		t.Fatalf("updated=%+v got=%+v", updated, got)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != created.Task.ID || listed.Tasks[0].Status != TaskStatusInProgress {
		t.Fatalf("listed=%+v", listed)
	}

	// TaskList 只返回摘要；完整描述和 metadata 必须通过 TaskGet 获取。
	rawSummary, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawSummary), "description") || strings.Contains(string(rawSummary), "metadata") {
		t.Fatalf("TaskList leaked full task fields: %s", rawSummary)
	}
	if got.Task.Description != "实现四个 Eino 工具" || got.Task.Metadata["source"] != "test" {
		t.Fatalf("TaskGet should return full task: %+v", got.Task)
	}
}

// TestToolsetUpdateDeletesTask 验证 deleted 作为动作删除任务并返回明确确认。
func TestToolsetUpdateDeletesTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "tool-delete")
	if err != nil {
		t.Fatal(err)
	}
	toolset := Toolset{Store: store}
	created, err := toolset.Create(ctx, TaskCreateRequest{Subject: "临时任务", Description: "删除工具测试"})
	if err != nil {
		t.Fatal(err)
	}

	deleted := TaskUpdateStatusDeleted
	result, err := toolset.Update(ctx, TaskUpdateRequest{TaskID: created.Task.ID, Status: &deleted})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Deleted || result.Task != nil {
		t.Fatalf("result=%+v", result)
	}
	if _, err := toolset.Get(ctx, TaskGetRequest{TaskID: created.Task.ID}); err == nil {
		t.Fatal("deleted task should not be readable")
	}
	listed, err := toolset.List(ctx, TaskListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 0 {
		t.Fatalf("listed=%+v", listed)
	}
}

// TestToolsetUpdateRejectsEmptyChange 验证模型只传 taskId 时不会产生伪更新。
func TestToolsetUpdateRejectsEmptyChange(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "tool-empty-update")
	if err != nil {
		t.Fatal(err)
	}
	toolset := Toolset{Store: store}
	created, err := toolset.Create(ctx, TaskCreateRequest{Subject: "保留任务", Description: "空更新测试"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := toolset.Update(ctx, TaskUpdateRequest{TaskID: created.Task.ID}); err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("empty update error=%v", err)
	}
}
