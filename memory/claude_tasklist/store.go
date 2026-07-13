package claudetasklist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Store 管理一个 Task List 的 JSON 文件、单调 ID 水位和进程内读改写互斥。
type Store struct {
	root   string
	listID string
	dir    string
	mu     *sync.Mutex
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
	listDir := filepath.Join(tasksDir, taskListID)
	store := &Store{
		root: resolvedRoot, listID: taskListID, dir: listDir,
		mu: sharedStoreMutex(listDir),
	}

	// 同一规范化目录的初始化与后续读改写共享一把锁，避免并发初始化覆盖水位。
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := createDirectoryWithoutSymlink(tasksDir); err != nil {
		return nil, fmt.Errorf("create tasks directory: %w", err)
	}
	if err := createDirectoryWithoutSymlink(listDir); err != nil {
		return nil, fmt.Errorf("create task list directory: %w", err)
	}

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
	// 启动时立即验证完整快照，损坏或成环的依赖图不会被当作可用 Store 返回。
	if _, _, err := store.loadTasksLocked(); err != nil {
		return nil, err
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
	// 对更新后的完整克隆图做环检查，失败时尚未发生任何文件写入。
	if err := validateStoredDependencies(mutableTasks); err != nil {
		return nil, err
	}
	if err := s.writeTaskBatchLocked(changed); err != nil {
		return nil, err
	}
	return &TaskUpdateResult{Task: cloneTask(current)}, nil
}
