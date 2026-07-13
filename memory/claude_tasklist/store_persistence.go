package claudetasklist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

var (
	taskListIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	storeMutexGuard   sync.Mutex
	storeMutexes      = make(map[string]*sync.Mutex)
)

// sharedStoreMutex 返回规范化 Task List 目录在当前进程内共用的互斥锁。
func sharedStoreMutex(directory string) *sync.Mutex {
	storeMutexGuard.Lock()
	defer storeMutexGuard.Unlock()
	mutex, exists := storeMutexes[directory]
	if !exists {
		mutex = &sync.Mutex{}
		storeMutexes[directory] = mutex
	}
	return mutex
}

// validTaskListID 把 Task List 物理目录限制为单个安全路径段。
func validTaskListID(taskListID string) bool {
	return taskListID != "" && taskListID == strings.TrimSpace(taskListID) && taskListIDPattern.MatchString(taskListID)
}

// createDirectoryWithoutSymlink 创建单层目录，并拒绝复用符号链接或普通文件。
func createDirectoryWithoutSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if mkdirErr := os.Mkdir(path, taskDirectoryMode); mkdirErr != nil && !os.IsExist(mkdirErr) {
			return mkdirErr
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

// loadTasksLocked 读取完整快照，并校验同一列表内的双向依赖一致性与无环性。
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
