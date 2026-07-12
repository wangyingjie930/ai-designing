package claudetasklist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	tasksDirectoryName   = "tasks"
	highWatermarkName    = ".highwatermark"
	taskFileExtension    = ".json"
	taskFilePermission   = 0o600
	taskDirectoryMode    = 0o700
	temporaryFilePattern = ".task-*"
)

var taskListIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Store 管理一个 Task List 的 JSON 文件、单调 ID 水位和进程内读改写互斥。
type Store struct {
	root   string
	listID string
	dir    string
	mu     sync.Mutex
}

// NewStore 校验 Task List ID，建立安全目录并初始化单调 ID 水位。
func NewStore(root, taskListID string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("task store root is required")
	}
	if !validTaskListID(taskListID) {
		return nil, fmt.Errorf("invalid task list ID %q", taskListID)
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve task store root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, taskDirectoryMode); err != nil {
		return nil, fmt.Errorf("create task store root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve task store root symlinks: %w", err)
	}

	tasksDir := filepath.Join(resolvedRoot, tasksDirectoryName)
	if err := createDirectoryWithoutSymlink(tasksDir); err != nil {
		return nil, fmt.Errorf("create tasks directory: %w", err)
	}
	listDir := filepath.Join(tasksDir, taskListID)
	if err := createDirectoryWithoutSymlink(listDir); err != nil {
		return nil, fmt.Errorf("create task list directory: %w", err)
	}

	store := &Store{root: resolvedRoot, listID: taskListID, dir: listDir}
	watermarkPath := store.highWatermarkPath()
	info, err := os.Lstat(watermarkPath)
	switch {
	case os.IsNotExist(err):
		if err := atomicWrite(watermarkPath, []byte("0\n")); err != nil {
			return nil, fmt.Errorf("initialize task high watermark: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("inspect task high watermark: %w", err)
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return nil, errors.New("task high watermark must be a regular file")
	default:
		if _, err := store.readHighWatermarkLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Dir 返回当前 Task List 已解析的绝对存储目录。
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Create 分配永不回退的数字 ID，并原子写入一个 pending 任务。
func (s *Store) Create(ctx context.Context, request TaskCreateRequest) (*Task, error) {
	if s == nil {
		return nil, errors.New("task store is required")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	subject := strings.TrimSpace(request.Subject)
	description := strings.TrimSpace(request.Description)
	if subject == "" || description == "" {
		return nil, errors.New("task subject and description are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectoryLocked(); err != nil {
		return nil, err
	}
	maxTaskID, err := s.maxTaskIDLocked()
	if err != nil {
		return nil, err
	}
	highWatermark, err := s.readHighWatermarkLocked()
	if err != nil {
		return nil, err
	}
	if highWatermark > maxTaskID {
		maxTaskID = highWatermark
	}
	if maxTaskID == int(^uint(0)>>1) {
		return nil, errors.New("task ID space is exhausted")
	}
	nextID := maxTaskID + 1
	task := &Task{
		ID:          strconv.Itoa(nextID),
		Subject:     subject,
		Description: description,
		ActiveForm:  strings.TrimSpace(request.ActiveForm),
		Status:      TaskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
		Metadata:    cloneMetadata(request.Metadata),
	}

	// 先推进水位；即使后续任务文件写入失败，该 ID 也不会在恢复后被复用。
	if err := s.writeHighWatermarkLocked(nextID); err != nil {
		return nil, err
	}
	if err := s.writeTaskLocked(task); err != nil {
		return nil, err
	}
	return cloneTask(task), nil
}

// Get 按受限数字 ID 读取完整任务，拒绝路径构造和符号链接文件。
func (s *Store) Get(ctx context.Context, taskID string) (*Task, error) {
	if s == nil {
		return nil, errors.New("task store is required")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if _, err := parseTaskID(taskID); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectoryLocked(); err != nil {
		return nil, err
	}
	task, err := s.readTaskLocked(taskID)
	if err != nil {
		return nil, err
	}
	return cloneTask(task), nil
}

// List 按数字 ID 返回完整任务，保留已完成 blocker 供精确读取和恢复。
func (s *Store) List(ctx context.Context) ([]Task, error) {
	if s == nil {
		return nil, errors.New("task store is required")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectoryLocked(); err != nil {
		return nil, err
	}
	tasks, orderedIDs, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	result := make([]Task, 0, len(orderedIDs))
	for _, taskID := range orderedIDs {
		result = append(result, *cloneTask(tasks[taskID]))
	}
	return result, nil
}

// ListSummaries 按数字 ID 返回低成本视图，并隐藏已经 completed 的 blocker。
func (s *Store) ListSummaries(ctx context.Context) ([]TaskSummary, error) {
	if s == nil {
		return nil, errors.New("task store is required")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectoryLocked(); err != nil {
		return nil, err
	}
	tasks, orderedIDs, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	summaries := make([]TaskSummary, 0, len(orderedIDs))
	for _, taskID := range orderedIDs {
		task := tasks[taskID]
		activeBlockers := make([]string, 0, len(task.BlockedBy))
		for _, blockerID := range task.BlockedBy {
			if tasks[blockerID].Status != TaskStatusCompleted {
				activeBlockers = append(activeBlockers, blockerID)
			}
		}
		sortTaskIDs(activeBlockers)
		summaries = append(summaries, TaskSummary{
			ID: task.ID, Subject: task.Subject, Status: task.Status, Owner: task.Owner, BlockedBy: activeBlockers,
		})
	}
	return summaries, nil
}

// Counts 汇总 pending、in_progress、completed 三种机械状态的任务数量。
func (s *Store) Counts(ctx context.Context) (TaskCounts, error) {
	if s == nil {
		return TaskCounts{}, errors.New("task store is required")
	}
	if err := contextError(ctx); err != nil {
		return TaskCounts{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectoryLocked(); err != nil {
		return TaskCounts{}, err
	}
	tasks, _, err := s.loadTasksLocked()
	if err != nil {
		return TaskCounts{}, err
	}
	var counts TaskCounts
	for _, task := range tasks {
		switch task.Status {
		case TaskStatusPending:
			counts.Pending++
		case TaskStatusInProgress:
			counts.InProgress++
		case TaskStatusCompleted:
			counts.Completed++
		}
	}
	return counts, nil
}

// Update 先完整校验整批变更，再在互斥区维护任务字段和双向依赖。
func (s *Store) Update(ctx context.Context, request TaskUpdateRequest) (*TaskUpdateResult, error) {
	if s == nil {
		return nil, errors.New("task store is required")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if _, err := parseTaskID(request.TaskID); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectoryLocked(); err != nil {
		return nil, err
	}
	tasks, orderedIDs, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	if _, exists := tasks[request.TaskID]; !exists {
		return nil, fmt.Errorf("task %s does not exist", request.TaskID)
	}
	if err := validateUpdateRequest(request, tasks); err != nil {
		return nil, err
	}

	mutableTasks := cloneTasks(tasks)
	if request.Status != nil && *request.Status == TaskUpdateStatusDeleted {
		return s.deleteTaskLocked(request.TaskID, mutableTasks, orderedIDs)
	}

	changed := make(map[string]*Task)
	current := mutableTasks[request.TaskID]
	applyScalarUpdates(current, request)
	applyMetadataUpdate(current, request.Metadata)
	changed[current.ID] = current
	for _, blockedID := range request.AddBlocks {
		blocked := mutableTasks[blockedID]
		if appendUniqueTaskID(&current.Blocks, blockedID) {
			changed[current.ID] = current
		}
		if appendUniqueTaskID(&blocked.BlockedBy, current.ID) {
			changed[blocked.ID] = blocked
		}
	}
	for _, blockerID := range request.AddBlockedBy {
		blocker := mutableTasks[blockerID]
		if appendUniqueTaskID(&current.BlockedBy, blockerID) {
			changed[current.ID] = current
		}
		if appendUniqueTaskID(&blocker.Blocks, current.ID) {
			changed[blocker.ID] = blocker
		}
	}
	for _, task := range changed {
		sortTaskIDs(task.Blocks)
		sortTaskIDs(task.BlockedBy)
	}
	if err := s.writeTaskBatchLocked(changed); err != nil {
		return nil, err
	}
	return &TaskUpdateResult{Task: cloneTask(current)}, nil
}

// validTaskListID 把 Task List 物理目录限制为单个安全路径段。
func validTaskListID(taskListID string) bool {
	return taskListID != "" && taskListID == strings.TrimSpace(taskListID) && taskListIDPattern.MatchString(taskListID)
}

// createDirectoryWithoutSymlink 创建单层目录，并拒绝复用符号链接或普通文件。
func createDirectoryWithoutSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, taskDirectoryMode); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("task path contains symlink: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("task path is not a directory: %s", path)
	}
	return nil
}

// ensureDirectoryLocked 在每次 I/O 前确认 Task List 目录没有被替换成符号链接。
func (s *Store) ensureDirectoryLocked() error {
	info, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("inspect task list directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("task list path must remain a real directory")
	}
	return nil
}

// maxTaskIDLocked 扫描已有数字文件名，避免水位文件落后时复用任务 ID。
func (s *Store) maxTaskIDLocked() (int, error) {
	ids, err := s.taskIDsLocked()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	maximum, _ := parseTaskID(ids[len(ids)-1])
	return maximum, nil
}

// taskIDsLocked 校验目录条目并返回按数值升序排列的任务 ID。
func (s *Store) taskIDsLocked() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read task list directory: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == highWatermarkName || strings.HasPrefix(name, ".task-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("task list contains symlink: %s", name)
		}
		if entry.IsDir() || filepath.Ext(name) != taskFileExtension {
			return nil, fmt.Errorf("unexpected task list entry: %s", name)
		}
		taskID := strings.TrimSuffix(name, taskFileExtension)
		_, err := parseTaskID(taskID)
		if err != nil {
			return nil, fmt.Errorf("invalid task filename %q: %w", name, err)
		}
		ids = append(ids, taskID)
	}
	sortTaskIDs(ids)
	return ids, nil
}

// loadTasksLocked 读取完整快照，并校验同一列表内的双向依赖一致性。
func (s *Store) loadTasksLocked() (map[string]*Task, []string, error) {
	orderedIDs, err := s.taskIDsLocked()
	if err != nil {
		return nil, nil, err
	}
	tasks := make(map[string]*Task, len(orderedIDs))
	for _, taskID := range orderedIDs {
		task, err := s.readTaskLocked(taskID)
		if err != nil {
			return nil, nil, err
		}
		tasks[taskID] = task
	}
	if err := validateStoredDependencies(tasks); err != nil {
		return nil, nil, err
	}
	return tasks, orderedIDs, nil
}

// validateUpdateRequest 在任何对象变更前校验字段、状态和全部依赖引用。
func validateUpdateRequest(request TaskUpdateRequest, tasks map[string]*Task) error {
	hasChanges := request.Subject != "" || request.Description != "" || request.ActiveForm != "" ||
		request.Status != nil || request.Owner != nil || len(request.AddBlocks) > 0 ||
		len(request.AddBlockedBy) > 0 || request.Metadata != nil
	if !hasChanges {
		return errors.New("task update has no changes")
	}
	if request.Subject != "" && strings.TrimSpace(request.Subject) == "" {
		return errors.New("task subject cannot be empty")
	}
	if request.Description != "" && strings.TrimSpace(request.Description) == "" {
		return errors.New("task description cannot be empty")
	}
	if request.Status != nil && !request.Status.valid() {
		return fmt.Errorf("invalid task update status %q", *request.Status)
	}
	for _, dependencyID := range append(append([]string{}, request.AddBlocks...), request.AddBlockedBy...) {
		if _, err := parseTaskID(dependencyID); err != nil {
			return err
		}
		if dependencyID == request.TaskID {
			return fmt.Errorf("task %s cannot depend on itself", request.TaskID)
		}
		if _, exists := tasks[dependencyID]; !exists {
			return fmt.Errorf("dependency task %s does not exist", dependencyID)
		}
	}
	return nil
}

// validateStoredDependencies 确保文件快照中的引用存在、无重复且保持双向对称。
func validateStoredDependencies(tasks map[string]*Task) error {
	for taskID, task := range tasks {
		for _, dependencySet := range []struct {
			name       string
			ids        []string
			reciprocal func(*Task) []string
		}{
			{name: "blocks", ids: task.Blocks, reciprocal: func(other *Task) []string { return other.BlockedBy }},
			{name: "blockedBy", ids: task.BlockedBy, reciprocal: func(other *Task) []string { return other.Blocks }},
		} {
			seen := make(map[string]struct{}, len(dependencySet.ids))
			for _, dependencyID := range dependencySet.ids {
				if _, err := parseTaskID(dependencyID); err != nil {
					return fmt.Errorf("task %s has invalid %s reference: %w", taskID, dependencySet.name, err)
				}
				if dependencyID == taskID {
					return fmt.Errorf("task %s has a self dependency", taskID)
				}
				if _, duplicate := seen[dependencyID]; duplicate {
					return fmt.Errorf("task %s has duplicate %s reference %s", taskID, dependencySet.name, dependencyID)
				}
				seen[dependencyID] = struct{}{}
				other, exists := tasks[dependencyID]
				if !exists {
					return fmt.Errorf("task %s references missing task %s", taskID, dependencyID)
				}
				if !containsTaskID(dependencySet.reciprocal(other), taskID) {
					return fmt.Errorf("task %s and task %s have inconsistent dependencies", taskID, dependencyID)
				}
			}
		}
	}
	return nil
}

// cloneTasks 为整批校验后的更新建立独立快照，失败时不会污染已加载对象。
func cloneTasks(tasks map[string]*Task) map[string]*Task {
	cloned := make(map[string]*Task, len(tasks))
	for taskID, task := range tasks {
		cloned[taskID] = cloneTask(task)
	}
	return cloned
}

// applyScalarUpdates 把已经验证过的可选标量字段应用到当前任务。
func applyScalarUpdates(task *Task, request TaskUpdateRequest) {
	if request.Subject != "" {
		task.Subject = strings.TrimSpace(request.Subject)
	}
	if request.Description != "" {
		task.Description = strings.TrimSpace(request.Description)
	}
	if request.ActiveForm != "" {
		task.ActiveForm = strings.TrimSpace(request.ActiveForm)
	}
	if request.Owner != nil {
		task.Owner = strings.TrimSpace(*request.Owner)
	}
	if request.Status != nil {
		task.Status = TaskStatus(*request.Status)
	}
}

// applyMetadataUpdate 按键合并扩展字段，并把 null 解释为删除该键。
func applyMetadataUpdate(task *Task, update map[string]any) {
	if update == nil {
		return
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any, len(update))
	}
	for key, value := range update {
		if value == nil {
			delete(task.Metadata, key)
			continue
		}
		task.Metadata[key] = value
	}
	if len(task.Metadata) == 0 {
		task.Metadata = nil
	}
}

// deleteTaskLocked 固化当前水位、清理所有双向引用，最后移除目标任务文件。
func (s *Store) deleteTaskLocked(taskID string, tasks map[string]*Task, orderedIDs []string) (*TaskUpdateResult, error) {
	changed := make(map[string]*Task)
	for otherID, task := range tasks {
		if otherID == taskID {
			continue
		}
		var removed bool
		task.Blocks, removed = removeTaskID(task.Blocks, taskID)
		if removed {
			changed[otherID] = task
		}
		task.BlockedBy, removed = removeTaskID(task.BlockedBy, taskID)
		if removed {
			changed[otherID] = task
		}
	}
	// 先编码整批文件，确保 metadata 等内容不可序列化时磁盘仍保持原状。
	encoded, changedIDs, err := encodeTaskBatch(changed)
	if err != nil {
		return nil, err
	}
	highWatermark, err := s.readHighWatermarkLocked()
	if err != nil {
		return nil, err
	}
	if len(orderedIDs) > 0 {
		maximum, _ := parseTaskID(orderedIDs[len(orderedIDs)-1])
		if maximum > highWatermark {
			highWatermark = maximum
		}
	}
	if err := s.writeHighWatermarkLocked(highWatermark); err != nil {
		return nil, err
	}
	if err := s.writeEncodedTaskBatchLocked(encoded, changedIDs); err != nil {
		return nil, err
	}
	if err := os.Remove(s.taskPath(taskID)); err != nil {
		return nil, fmt.Errorf("delete task %s: %w", taskID, err)
	}
	return &TaskUpdateResult{Deleted: true}, nil
}

// appendUniqueTaskID 在依赖切片中只追加尚不存在的 ID。
func appendUniqueTaskID(ids *[]string, taskID string) bool {
	if containsTaskID(*ids, taskID) {
		return false
	}
	*ids = append(*ids, taskID)
	return true
}

// containsTaskID 判断依赖切片是否已经包含指定任务。
func containsTaskID(ids []string, taskID string) bool {
	for _, current := range ids {
		if current == taskID {
			return true
		}
	}
	return false
}

// removeTaskID 删除依赖切片中的全部指定 ID，并保持非 nil 空切片。
func removeTaskID(ids []string, taskID string) ([]string, bool) {
	filtered := make([]string, 0, len(ids))
	removed := false
	for _, current := range ids {
		if current == taskID {
			removed = true
			continue
		}
		filtered = append(filtered, current)
	}
	return filtered, removed
}

// sortTaskIDs 按十进制数值排序已经验证过的任务 ID。
func sortTaskIDs(ids []string) {
	sort.Slice(ids, func(left, right int) bool {
		leftValue, _ := strconv.Atoi(ids[left])
		rightValue, _ := strconv.Atoi(ids[right])
		return leftValue < rightValue
	})
}

// readHighWatermarkLocked 读取并验证非负十进制水位。
func (s *Store) readHighWatermarkLocked() (int, error) {
	path := s.highWatermarkPath()
	info, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("inspect task high watermark: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, errors.New("task high watermark must be a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read task high watermark: %w", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid task high watermark %q", strings.TrimSpace(string(content)))
	}
	return value, nil
}

// writeHighWatermarkLocked 原子替换水位文件，避免暴露半写数字。
func (s *Store) writeHighWatermarkLocked(value int) error {
	if value < 0 {
		return errors.New("task high watermark cannot be negative")
	}
	if err := atomicWrite(s.highWatermarkPath(), []byte(strconv.Itoa(value)+"\n")); err != nil {
		return fmt.Errorf("write task high watermark: %w", err)
	}
	return nil
}

// readTaskLocked 从数字文件名读取并校验任务内外 ID 与稳定状态。
func (s *Store) readTaskLocked(taskID string) (*Task, error) {
	path := s.taskPath(taskID)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("task %s does not exist", taskID)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect task %s: %w", taskID, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("task %s must be a regular file", taskID)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read task %s: %w", taskID, err)
	}
	var task Task
	if err := json.Unmarshal(content, &task); err != nil {
		return nil, fmt.Errorf("decode task %s: %w", taskID, err)
	}
	if task.ID != taskID {
		return nil, fmt.Errorf("task %s file contains ID %q", taskID, task.ID)
	}
	if strings.TrimSpace(task.Subject) == "" || strings.TrimSpace(task.Description) == "" {
		return nil, fmt.Errorf("task %s has empty subject or description", taskID)
	}
	if !task.Status.valid() {
		return nil, fmt.Errorf("task %s has invalid status %q", taskID, task.Status)
	}
	if task.Blocks == nil {
		task.Blocks = []string{}
	}
	if task.BlockedBy == nil {
		task.BlockedBy = []string{}
	}
	return &task, nil
}

// writeTaskLocked 把单个完整任务编码为同目录原子替换的 JSON 文件。
func (s *Store) writeTaskLocked(task *Task) error {
	content, err := encodeTask(task)
	if err != nil {
		return err
	}
	if err := atomicWrite(s.taskPath(task.ID), content); err != nil {
		return fmt.Errorf("write task %s: %w", task.ID, err)
	}
	return nil
}

// writeTaskBatchLocked 先编码整批变更，再按数字 ID 顺序逐文件原子替换。
func (s *Store) writeTaskBatchLocked(tasks map[string]*Task) error {
	encoded, orderedIDs, err := encodeTaskBatch(tasks)
	if err != nil {
		return err
	}
	return s.writeEncodedTaskBatchLocked(encoded, orderedIDs)
}

// writeEncodedTaskBatchLocked 写入已经完成序列化校验的任务批次。
func (s *Store) writeEncodedTaskBatchLocked(encoded map[string][]byte, orderedIDs []string) error {
	for _, taskID := range orderedIDs {
		if err := atomicWrite(s.taskPath(taskID), encoded[taskID]); err != nil {
			return fmt.Errorf("write task %s: %w", taskID, err)
		}
	}
	return nil
}

// encodeTaskBatch 在首次落盘前验证批次中所有任务都能编码。
func encodeTaskBatch(tasks map[string]*Task) (map[string][]byte, []string, error) {
	orderedIDs := make([]string, 0, len(tasks))
	for taskID := range tasks {
		orderedIDs = append(orderedIDs, taskID)
	}
	sortTaskIDs(orderedIDs)
	encoded := make(map[string][]byte, len(tasks))
	for _, taskID := range orderedIDs {
		content, err := encodeTask(tasks[taskID])
		if err != nil {
			return nil, nil, err
		}
		encoded[taskID] = content
	}
	return encoded, orderedIDs, nil
}

// encodeTask 生成带末尾换行的稳定 JSON 内容。
func encodeTask(task *Task) ([]byte, error) {
	content, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode task %s: %w", task.ID, err)
	}
	return append(content, '\n'), nil
}

// parseTaskID 只接受从 1 开始、无前导零的十进制文件 ID。
func parseTaskID(taskID string) (int, error) {
	if taskID == "" || taskID != strings.TrimSpace(taskID) {
		return 0, fmt.Errorf("invalid task ID %q", taskID)
	}
	value, err := strconv.Atoi(taskID)
	if err != nil || value <= 0 || strconv.Itoa(value) != taskID {
		return 0, fmt.Errorf("invalid task ID %q", taskID)
	}
	return value, nil
}

// cloneTask 隔离 Store 内部对象与调用方可变切片、map。
func cloneTask(task *Task) *Task {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.Blocks = append([]string{}, task.Blocks...)
	cloned.BlockedBy = append([]string{}, task.BlockedBy...)
	cloned.Metadata = cloneMetadata(task.Metadata)
	return &cloned
}

// cloneMetadata 复制 metadata 顶层键，后续键级合并不会修改调用方 map。
func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

// contextError 统一拒绝 nil 或已经取消的上下文。
func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

// atomicWrite 以 0600 同目录临时文件、Sync、Close、Rename 完成原子替换。
func atomicWrite(path string, content []byte) (err error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, temporaryFilePattern)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(taskFilePermission); err != nil {
		return err
	}
	if _, err = temporary.Write(content); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// highWatermarkPath 返回固定水位文件路径，不接收模型输入。
func (s *Store) highWatermarkPath() string {
	return filepath.Join(s.dir, highWatermarkName)
}

// taskPath 把已经校验的数字 ID 映射为当前 Task List 内的 JSON 文件。
func (s *Store) taskPath(taskID string) string {
	return filepath.Join(s.dir, taskID+taskFileExtension)
}
