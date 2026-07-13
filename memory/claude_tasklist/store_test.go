package claudetasklist

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStorePersistsTasksAndNeverReusesDeletedIDs 验证任务重开后仍可读取，且删除不会回收数字 ID。
func TestStorePersistsTasksAndNeverReusesDeletedIDs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStore(root, "interview")
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(resolvedRoot, "tasks", "interview"); store.Dir() != want {
		t.Fatalf("store dir=%q want=%q", store.Dir(), want)
	}

	first, err := store.Create(ctx, TaskCreateRequest{
		Subject: "梳理源码", Description: "核对 Claude Code Task 工具链", ActiveForm: "正在梳理源码",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "1" || first.Status != TaskStatusPending {
		t.Fatalf("first=%+v", first)
	}
	for _, path := range []string{
		filepath.Join(store.Dir(), ".highwatermark"),
		filepath.Join(store.Dir(), "1.json"),
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode(%s)=%o", path, info.Mode().Perm())
		}
	}

	reopened, err := NewStore(root, "interview")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Get(ctx, "1")
	if err != nil || loaded.Subject != "梳理源码" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}

	deleted := TaskUpdateStatusDeleted
	if _, err := reopened.Update(ctx, TaskUpdateRequest{TaskID: "1", Status: &deleted}); err != nil {
		t.Fatal(err)
	}
	second, err := reopened.Create(ctx, TaskCreateRequest{Subject: "实现工具", Description: "实现四工具"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "2" {
		t.Fatalf("second id=%q", second.ID)
	}
}

// TestNewStoreRejectsEmptyAndUnsafeTaskListIDs 验证 Task List ID 不能为空或形成路径穿越。
func TestNewStoreRejectsEmptyAndUnsafeTaskListIDs(t *testing.T) {
	for _, taskListID := range []string{"", "../escape", "a/b"} {
		t.Run(taskListID, func(t *testing.T) {
			if _, err := NewStore(t.TempDir(), taskListID); err == nil {
				t.Fatalf("NewStore(%q) should fail", taskListID)
			}
		})
	}
}

// TestNewStoreRejectsSymlinkedTaskDirectory 验证 Store 不会沿符号链接把任务写到根目录之外。
func TestNewStoreRejectsSymlinkedTaskDirectory(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.Mkdir(tasksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(tasksDir, "linked")); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(root, "linked"); err == nil {
		t.Fatal("NewStore should reject a symlinked task directory")
	}
}

// TestStoreMaintainsDependenciesAndFiltersCompletedBlockers 验证依赖双向维护，摘要只暴露尚未完成的 blocker。
func TestStoreMaintainsDependenciesAndFiltersCompletedBlockers(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "dependencies")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(ctx, TaskCreateRequest{Subject: "设计", Description: "确定边界"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "编写代码"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(ctx, TaskUpdateRequest{TaskID: second.ID, AddBlockedBy: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.Blocks, []string{second.ID}) || !slices.Equal(b.BlockedBy, []string{first.ID}) {
		t.Fatalf("a=%+v b=%+v", a, b)
	}

	completed := TaskUpdateStatusCompleted
	if _, err := store.Update(ctx, TaskUpdateRequest{TaskID: first.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	fullSecond, err := store.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fullSecond.BlockedBy, []string{first.ID}) {
		t.Fatalf("completed dependency should remain in full task: %+v", fullSecond)
	}
	summaries, err := store.ListSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[1].ID != second.ID || len(summaries[1].BlockedBy) != 0 {
		t.Fatalf("summaries=%+v", summaries)
	}
}

// TestStoreRejectsInvalidDependenciesAtomically 验证缺失任务和自引用会让整次更新保持零写入。
func TestStoreRejectsInvalidDependenciesAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "invalid-dependencies")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(ctx, TaskCreateRequest{Subject: "设计", Description: "确定边界"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "编写代码"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Update(ctx, TaskUpdateRequest{
		TaskID: second.ID, Subject: "不应写入", AddBlockedBy: []string{first.ID, "999"},
	}); err == nil {
		t.Fatal("missing dependency should reject the whole update")
	}
	assertTaskUnchanged(t, store, first.ID, "设计", "", nil, nil)
	assertTaskUnchanged(t, store, second.ID, "实现", "", nil, nil)

	owner := "builder"
	if _, err := store.Update(ctx, TaskUpdateRequest{
		TaskID: second.ID, Owner: &owner, AddBlocks: []string{second.ID},
	}); err == nil {
		t.Fatal("self dependency should reject the whole update")
	}
	assertTaskUnchanged(t, store, second.ID, "实现", "", nil, nil)
}

// TestStoreRejectsDependencyCyclesAtomically 验证新增二元环或三元环时整批更新零落盘。
func TestStoreRejectsDependencyCyclesAtomically(t *testing.T) {
	testCases := []struct {
		name       string
		taskCount  int
		prepare    func(*testing.T, *Store, []*Task)
		cycleStart int
		cycleEnd   int
	}{
		{
			name:       "two tasks",
			taskCount:  2,
			cycleStart: 1,
			cycleEnd:   0,
			prepare: func(t *testing.T, store *Store, tasks []*Task) {
				t.Helper()
				if _, err := store.Update(context.Background(), TaskUpdateRequest{
					TaskID: tasks[0].ID, AddBlocks: []string{tasks[1].ID},
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "three tasks",
			taskCount:  3,
			cycleStart: 2,
			cycleEnd:   0,
			prepare: func(t *testing.T, store *Store, tasks []*Task) {
				t.Helper()
				for index := 0; index < 2; index++ {
					if _, err := store.Update(context.Background(), TaskUpdateRequest{
						TaskID: tasks[index].ID, AddBlocks: []string{tasks[index+1].ID},
					}); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewStore(t.TempDir(), "cycle-update")
			if err != nil {
				t.Fatal(err)
			}
			tasks := make([]*Task, 0, testCase.taskCount)
			for index := 0; index < testCase.taskCount; index++ {
				task, createErr := store.Create(ctx, TaskCreateRequest{
					Subject: fmt.Sprintf("任务 %d", index+1), Description: "验证有向无环依赖",
				})
				if createErr != nil {
					t.Fatal(createErr)
				}
				tasks = append(tasks, task)
			}
			testCase.prepare(t, store, tasks)
			before := snapshotTaskFiles(t, store.Dir())

			_, err = store.Update(ctx, TaskUpdateRequest{
				TaskID:    tasks[testCase.cycleStart].ID,
				Subject:   "不应写入",
				AddBlocks: []string{tasks[testCase.cycleEnd].ID},
			})
			if err == nil || !strings.Contains(err.Error(), "cycle") {
				t.Fatalf("cycle update error=%v", err)
			}
			assertTaskFilesEqual(t, before, snapshotTaskFiles(t, store.Dir()))
		})
	}
}

// TestNewStoreRejectsExistingDependencyCycle 验证重新加载已有 JSON 快照时会拒绝环且不改写文件。
func TestNewStoreRejectsExistingDependencyCycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStore(root, "snapshot-cycle")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(ctx, TaskCreateRequest{Subject: "设计", Description: "确定边界"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "编写代码"})
	if err != nil {
		t.Fatal(err)
	}

	first.Blocks = []string{second.ID}
	first.BlockedBy = []string{second.ID}
	second.Blocks = []string{first.ID}
	second.BlockedBy = []string{first.ID}
	writeTaskFixture(t, store, first)
	writeTaskFixture(t, store, second)
	before := snapshotTaskFiles(t, store.Dir())

	if _, err := NewStore(root, "snapshot-cycle"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("NewStore should reject the stored cycle, err=%v", err)
	}
	assertTaskFilesEqual(t, before, snapshotTaskFiles(t, store.Dir()))
}

// TestStoresForSameDirectorySerializeInitialization 验证同目录的 NewStore 初始化也进入共享互斥区。
func TestStoresForSameDirectorySerializeInitialization(t *testing.T) {
	root := t.TempDir()
	first, err := NewStore(root, "shared-init")
	if err != nil {
		t.Fatal(err)
	}

	first.mu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			first.mu.Unlock()
		}
	})
	result := make(chan error, 1)
	go func() {
		_, reopenErr := NewStore(root, "shared-init")
		result <- reopenErr
	}()
	select {
	case reopenErr := <-result:
		t.Fatalf("NewStore bypassed the shared list lock: %v", reopenErr)
	case <-time.After(100 * time.Millisecond):
	}
	first.mu.Unlock()
	locked = false
	if reopenErr := <-result; reopenErr != nil {
		t.Fatal(reopenErr)
	}
}

// TestStoresForSameDirectorySerializeConcurrentCreates 验证多 Store 并发创建不会覆盖 ID 或任务文件。
func TestStoresForSameDirectorySerializeConcurrentCreates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first, err := NewStore(root, "shared-create")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root, "shared-create")
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{first, second}

	const taskCount = 32
	start := make(chan struct{})
	results := make(chan *Task, taskCount)
	errors := make(chan error, taskCount)
	var wait sync.WaitGroup
	for index := 0; index < taskCount; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			task, createErr := stores[index%len(stores)].Create(ctx, TaskCreateRequest{
				Subject: fmt.Sprintf("并发任务 %d", index), Description: "验证共享 ID 水位",
			})
			if createErr != nil {
				errors <- createErr
				return
			}
			results <- task
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for createErr := range errors {
		t.Errorf("concurrent Create failed: %v", createErr)
	}
	if t.Failed() {
		return
	}

	seen := make(map[string]struct{}, taskCount)
	for task := range results {
		if _, duplicate := seen[task.ID]; duplicate {
			t.Fatalf("duplicate concurrent task ID %s", task.ID)
		}
		seen[task.ID] = struct{}{}
	}
	tasks, err := first.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != taskCount || len(tasks) != taskCount {
		t.Fatalf("created IDs=%d persisted tasks=%d want=%d", len(seen), len(tasks), taskCount)
	}
}

// TestStoresForSameDirectorySerializeConcurrentUpdates 验证多 Store 并发更新不会丢失依赖边。
func TestStoresForSameDirectorySerializeConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first, err := NewStore(root, "shared-update")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root, "shared-update")
	if err != nil {
		t.Fatal(err)
	}
	target, err := first.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "汇总并发依赖"})
	if err != nil {
		t.Fatal(err)
	}

	const blockerCount = 16
	blockers := make([]*Task, 0, blockerCount)
	for index := 0; index < blockerCount; index++ {
		blocker, createErr := first.Create(ctx, TaskCreateRequest{
			Subject: fmt.Sprintf("前置任务 %d", index), Description: "提供独立依赖",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		blockers = append(blockers, blocker)
	}

	stores := []*Store{first, second}
	start := make(chan struct{})
	errors := make(chan error, blockerCount)
	var wait sync.WaitGroup
	for index, blocker := range blockers {
		wait.Add(1)
		go func(index int, blockerID string) {
			defer wait.Done()
			<-start
			_, updateErr := stores[index%len(stores)].Update(ctx, TaskUpdateRequest{
				TaskID: target.ID, AddBlockedBy: []string{blockerID},
			})
			errors <- updateErr
		}(index, blocker.ID)
	}
	close(start)
	wait.Wait()
	close(errors)
	for updateErr := range errors {
		if updateErr != nil {
			t.Fatalf("concurrent Update failed: %v", updateErr)
		}
	}

	updated, err := first.Get(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.BlockedBy) != blockerCount {
		t.Fatalf("blockedBy=%v want %d dependencies", updated.BlockedBy, blockerCount)
	}
	if _, err := first.List(ctx); err != nil {
		t.Fatalf("concurrent updates left an inconsistent snapshot: %v", err)
	}
}

// TestStoreDeduplicatesDependencies 验证同一批和重复调用都不会制造重复依赖边。
func TestStoreDeduplicatesDependencies(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "deduplicate")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := store.Create(ctx, TaskCreateRequest{Subject: "设计", Description: "确定边界"})
	second, _ := store.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "编写代码"})
	third, _ := store.Create(ctx, TaskCreateRequest{Subject: "验证", Description: "运行测试"})
	request := TaskUpdateRequest{
		TaskID: second.ID, AddBlockedBy: []string{first.ID, first.ID}, AddBlocks: []string{third.ID, third.ID},
	}
	if _, err := store.Update(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, request); err != nil {
		t.Fatal(err)
	}

	first, _ = store.Get(ctx, first.ID)
	second, _ = store.Get(ctx, second.ID)
	third, _ = store.Get(ctx, third.ID)
	if !slices.Equal(first.Blocks, []string{second.ID}) ||
		!slices.Equal(second.BlockedBy, []string{first.ID}) ||
		!slices.Equal(second.Blocks, []string{third.ID}) ||
		!slices.Equal(third.BlockedBy, []string{second.ID}) {
		t.Fatalf("first=%+v second=%+v third=%+v", first, second, third)
	}
}

// TestStoreDeleteCleansReciprocalReferences 验证删除任务会清理其他文件中两个方向的引用。
func TestStoreDeleteCleansReciprocalReferences(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "delete-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := store.Create(ctx, TaskCreateRequest{Subject: "设计", Description: "确定边界"})
	second, _ := store.Create(ctx, TaskCreateRequest{Subject: "实现", Description: "编写代码"})
	third, _ := store.Create(ctx, TaskCreateRequest{Subject: "调研", Description: "收集证据"})
	if _, err := store.Update(ctx, TaskUpdateRequest{TaskID: first.ID, AddBlocks: []string{second.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, TaskUpdateRequest{TaskID: third.ID, AddBlocks: []string{first.ID}}); err != nil {
		t.Fatal(err)
	}

	deleted := TaskUpdateStatusDeleted
	result, err := store.Update(ctx, TaskUpdateRequest{TaskID: first.ID, Status: &deleted})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Deleted || result.Task != nil {
		t.Fatalf("result=%+v", result)
	}
	if _, err := store.Get(ctx, first.ID); err == nil {
		t.Fatal("deleted task should not remain readable")
	}
	second, _ = store.Get(ctx, second.ID)
	third, _ = store.Get(ctx, third.ID)
	if len(second.BlockedBy) != 0 || len(third.Blocks) != 0 {
		t.Fatalf("second=%+v third=%+v", second, third)
	}
}

// TestStoreUpdatesFieldsAndDeletesNullMetadata 验证标量更新、owner 清空和 metadata 键级合并语义。
func TestStoreUpdatesFieldsAndDeletesNullMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "metadata")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(ctx, TaskCreateRequest{
		Subject: " 实现 ", Description: " 编写代码 ", Metadata: map[string]any{"keep": "old", "remove": "old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Subject != "实现" || created.Description != "编写代码" {
		t.Fatalf("created=%+v", created)
	}

	owner := "coder"
	status := TaskUpdateStatusInProgress
	result, err := store.Update(ctx, TaskUpdateRequest{
		TaskID: created.ID, Subject: " 实现工具 ", Description: " 编写四个工具 ",
		ActiveForm: " 正在实现工具 ", Owner: &owner, Status: &status,
		Metadata: map[string]any{"keep": "new", "remove": nil, "added": "value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := result.Task
	if updated == nil || updated.Subject != "实现工具" || updated.Description != "编写四个工具" ||
		updated.ActiveForm != "正在实现工具" || updated.Owner != owner || updated.Status != TaskStatusInProgress {
		t.Fatalf("updated=%+v", updated)
	}
	if updated.Metadata["keep"] != "new" || updated.Metadata["added"] != "value" {
		t.Fatalf("metadata=%+v", updated.Metadata)
	}
	if _, exists := updated.Metadata["remove"]; exists {
		t.Fatalf("null metadata key should be deleted: %+v", updated.Metadata)
	}

	emptyOwner := ""
	result, err = store.Update(ctx, TaskUpdateRequest{TaskID: created.ID, Owner: &emptyOwner})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task == nil || result.Task.Owner != "" {
		t.Fatalf("owner was not cleared: %+v", result)
	}
}

// TestStoreListsTasksInNumericIDOrderAndCountsStatuses 验证 10 以上的 ID 仍按数字排序并正确计数。
func TestStoreListsTasksInNumericIDOrderAndCountsStatuses(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "numeric-sort")
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 11; index++ {
		if _, err := store.Create(ctx, TaskCreateRequest{
			Subject: fmt.Sprintf("任务 %d", index), Description: "验证数字排序",
		}); err != nil {
			t.Fatal(err)
		}
	}
	inProgress := TaskUpdateStatusInProgress
	completed := TaskUpdateStatusCompleted
	if _, err := store.Update(ctx, TaskUpdateRequest{TaskID: "2", Status: &inProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, TaskUpdateRequest{TaskID: "10", Status: &completed}); err != nil {
		t.Fatal(err)
	}

	tasks, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := store.ListSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 11 || len(summaries) != 11 {
		t.Fatalf("tasks=%d summaries=%d", len(tasks), len(summaries))
	}
	for index := range tasks {
		wantID := strconv.Itoa(index + 1)
		if tasks[index].ID != wantID || summaries[index].ID != wantID {
			t.Fatalf("index=%d task=%q summary=%q want=%q", index, tasks[index].ID, summaries[index].ID, wantID)
		}
	}
	counts, err := store.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts != (TaskCounts{Pending: 9, InProgress: 1, Completed: 1}) {
		t.Fatalf("counts=%+v", counts)
	}
}

// TestStoreRejectsEmptyAndInvalidScalarUpdates 验证空更新、空必填字段和非法状态不会改写任务。
func TestStoreRejectsEmptyAndInvalidScalarUpdates(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "invalid-updates")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, TaskCreateRequest{Subject: " ", Description: "描述"}); err == nil {
		t.Fatal("empty subject should fail")
	}
	task, err := store.Create(ctx, TaskCreateRequest{Subject: "任务", Description: "描述"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []TaskUpdateRequest{
		{TaskID: task.ID},
		{TaskID: task.ID, Subject: "   "},
		{TaskID: task.ID, Status: taskUpdateStatusPointer(TaskUpdateStatus("blocked"))},
	} {
		if _, err := store.Update(ctx, request); err == nil {
			t.Fatalf("request should fail: %+v", request)
		}
	}
	assertTaskUnchanged(t, store, task.ID, "任务", "", nil, nil)
}

// assertTaskUnchanged 读取任务并集中断言原子失败后的关键字段和依赖未变化。
func assertTaskUnchanged(t *testing.T, store *Store, taskID, subject, owner string, blocks, blockedBy []string) {
	t.Helper()
	task, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Subject != subject || task.Owner != owner || !slices.Equal(task.Blocks, blocks) || !slices.Equal(task.BlockedBy, blockedBy) {
		t.Fatalf("task changed unexpectedly: %+v", task)
	}
}

// taskUpdateStatusPointer 为表驱动测试构造可选状态指针。
func taskUpdateStatusPointer(status TaskUpdateStatus) *TaskUpdateStatus {
	return &status
}

// snapshotTaskFiles 读取当前任务 JSON 的原始字节，用于证明失败路径没有任何落盘变化。
func snapshotTaskFiles(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		snapshot[filepath.Base(path)] = content
	}
	return snapshot
}

// assertTaskFilesEqual 比较任务文件集合与内容，避免只检查内存返回值而遗漏部分写入。
func assertTaskFilesEqual(t *testing.T, before, after map[string][]byte) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("task file count changed: before=%d after=%d", len(before), len(after))
	}
	for name, beforeContent := range before {
		afterContent, exists := after[name]
		if !exists || !bytes.Equal(beforeContent, afterContent) {
			t.Fatalf("task file %s changed after rejected update", name)
		}
	}
}

// writeTaskFixture 直接写入测试快照，用来模拟进程启动前已经存在的损坏依赖图。
func writeTaskFixture(t *testing.T, store *Store, task *Task) {
	t.Helper()
	content, err := encodeTask(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.taskPath(task.ID), content, taskFilePermission); err != nil {
		t.Fatal(err)
	}
}
