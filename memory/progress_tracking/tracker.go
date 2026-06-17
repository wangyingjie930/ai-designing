package progresstracking

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ProgressTracker 是 Python ProgressTracker 的 SQLite 版本，负责计划状态和每次变更后的 checkpoint。
type ProgressTracker struct {
	planID string
	store  *SQLiteStore
	items  []TaskItem
	now    func() time.Time
	mu     sync.Mutex
}

// NewProgressTracker 初始化 SQLite 存储并加载已有计划，进程重启后可直接继续。
func NewProgressTracker(ctx context.Context, config Config) (*ProgressTracker, error) {
	dbPath := strings.TrimSpace(config.DBPath)
	if dbPath == "" {
		return nil, errors.New("db path is required")
	}
	planID := strings.TrimSpace(config.PlanID)
	if planID == "" {
		planID = defaultPlanID
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	store, err := openSQLiteStore(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	items, err := store.loadItems(ctx, planID)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &ProgressTracker{
		planID: planID,
		store:  store,
		items:  items,
		now:    now,
	}, nil
}

// CreatePlan 对应 Python create_plan(items)，把自然语言任务列表初始化为 pending。
func (t *ProgressTracker) CreatePlan(ctx context.Context, items []string) error {
	if t == nil {
		return errors.New("progress tracker is nil")
	}
	normalized, err := buildTaskItems(items)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.store.replacePlan(ctx, t.planID, normalized, t.now()); err != nil {
		return err
	}
	t.items = cloneTaskItems(normalized)
	return nil
}

// Start 把任务标记为 in_progress，给长任务恢复时留下“正在处理”的 checkpoint。
func (t *ProgressTracker) Start(ctx context.Context, index int) error {
	return t.updateTask(ctx, index, func(item *TaskItem) error {
		if item.Status == TaskStatusCompleted {
			return fmt.Errorf("task index %d is already completed", index)
		}
		item.Status = TaskStatusInProgress
		item.Error = nil
		return nil
	})
}

// Complete 对应 Python complete(index, result, files)，完成后立即写入 SQLite。
func (t *ProgressTracker) Complete(ctx context.Context, index int, result string, files []string) error {
	result = strings.TrimSpace(result)
	if result == "" {
		return errors.New("result is required")
	}
	return t.updateTask(ctx, index, func(item *TaskItem) error {
		item.Status = TaskStatusCompleted
		item.Result = stringPtr(result)
		item.FilesModified = normalizeStringList(files)
		item.Error = nil
		return nil
	})
}

// Fail 对应 Python fail(index, error)，记录失败原因后立即写入 SQLite。
func (t *ProgressTracker) Fail(ctx context.Context, index int, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if taskError == "" {
		return errors.New("error is required")
	}
	return t.updateTask(ctx, index, func(item *TaskItem) error {
		item.Status = TaskStatusFailed
		item.Error = stringPtr(taskError)
		return nil
	})
}

// Items 返回当前计划快照的拷贝，防止外部直接修改内部状态。
func (t *ProgressTracker) Items() []TaskItem {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneTaskItems(t.items)
}

// ResumptionContext 对应 Python resumption_context()，生成重启后交给 Agent 的恢复文本。
func (t *ProgressTracker) ResumptionContext() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return buildResumptionContext(t.items)
}

// PlanID 返回当前业务计划标识，便于日志、CLI 和工具结果串联。
func (t *ProgressTracker) PlanID() string {
	if t == nil {
		return ""
	}
	return t.planID
}

// Path 返回底层 SQLite 路径，方便排查 checkpoint 写到哪里。
func (t *ProgressTracker) Path() string {
	if t == nil || t.store == nil {
		return ""
	}
	return t.store.Path()
}

// Close 释放底层数据库连接。
func (t *ProgressTracker) Close() error {
	if t == nil || t.store == nil {
		return nil
	}
	return t.store.Close()
}

// updateTask 统一执行单任务状态变更，保证内存和 SQLite checkpoint 同步。
func (t *ProgressTracker) updateTask(ctx context.Context, index int, mutate func(*TaskItem) error) error {
	if t == nil {
		return errors.New("progress tracker is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if index < 0 || index >= len(t.items) {
		return fmt.Errorf("task index %d out of range", index)
	}
	next := cloneTaskItems(t.items)
	if err := mutate(&next[index]); err != nil {
		return err
	}
	if !next[index].Status.Valid() {
		return fmt.Errorf("invalid task status %q", next[index].Status)
	}
	if err := t.store.updateItem(ctx, t.planID, index, next[index], t.now()); err != nil {
		return err
	}
	t.items = next
	return nil
}

// buildTaskItems 做输入归一化，避免空白任务被持久化后干扰恢复上下文。
func buildTaskItems(items []string) ([]TaskItem, error) {
	if len(items) == 0 {
		return nil, errors.New("items are required")
	}
	out := make([]TaskItem, 0, len(items))
	for _, description := range items {
		description = strings.TrimSpace(description)
		if description == "" {
			continue
		}
		out = append(out, TaskItem{
			Description:   description,
			Status:        TaskStatusPending,
			FilesModified: []string{},
		})
	}
	if len(out) == 0 {
		return nil, errors.New("items are empty after normalization")
	}
	return out, nil
}

// buildResumptionContext 保持 Python 示例的文本结构，同时展示 failed 的上次错误。
func buildResumptionContext(items []TaskItem) string {
	var completed []TaskItem
	var pending []TaskItem
	for _, item := range items {
		if item.Status == TaskStatusCompleted {
			completed = append(completed, item)
			continue
		}
		pending = append(pending, item)
	}

	lines := []string{
		fmt.Sprintf("Progress: %d/%d", len(completed), len(items)),
		"",
		"Completed:",
	}
	for _, item := range completed {
		lines = append(lines, fmt.Sprintf("  ✓ %s", item.Description))
		if len(item.FilesModified) > 0 {
			lines = append(lines, fmt.Sprintf("    Files: %s", strings.Join(item.FilesModified, ", ")))
		}
		if item.Result != nil && strings.TrimSpace(*item.Result) != "" {
			lines = append(lines, fmt.Sprintf("    Result: %s", *item.Result))
		}
	}
	lines = append(lines, "\nRemaining:")
	for _, item := range pending {
		prefix := "☐"
		if item.Status == TaskStatusFailed {
			prefix = "✗"
		}
		lines = append(lines, fmt.Sprintf("  %s %s", prefix, item.Description))
		if item.Error != nil && strings.TrimSpace(*item.Error) != "" {
			lines = append(lines, fmt.Sprintf("    Last error: %s", *item.Error))
		}
	}
	return strings.Join(lines, "\n")
}

// cloneTaskItems 深拷贝可变 slice 字段，避免工具响应被外部误改后污染内存状态。
func cloneTaskItems(items []TaskItem) []TaskItem {
	out := make([]TaskItem, len(items))
	for idx, item := range items {
		out[idx] = item
		out[idx].FilesModified = append([]string{}, item.FilesModified...)
		if item.Result != nil {
			out[idx].Result = stringPtr(*item.Result)
		}
		if item.Error != nil {
			out[idx].Error = stringPtr(*item.Error)
		}
	}
	return out
}

// normalizeStringList 清理文件或材料引用，保留用户提供的顺序并去重。
func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// stringPtr 返回字符串指针，减少状态字段赋值时的临时变量噪音。
func stringPtr(value string) *string {
	return &value
}
