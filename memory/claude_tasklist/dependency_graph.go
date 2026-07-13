package claudetasklist

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// dependencyVisitState 表示依赖图深度优先遍历中的节点状态。
type dependencyVisitState uint8

const (
	dependencyUnvisited dependencyVisitState = iota
	dependencyVisiting
	dependencyVisited
)

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

// validateStoredDependencies 确保文件快照中的引用存在、无重复、双向对称且不成环。
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
	return validateAcyclicDependencies(tasks)
}

// validateAcyclicDependencies 沿 blocks 方向检查依赖图，发现回边即拒绝整个快照。
func validateAcyclicDependencies(tasks map[string]*Task) error {
	states := make(map[string]dependencyVisitState, len(tasks))
	var visit func(string) error
	visit = func(taskID string) error {
		switch states[taskID] {
		case dependencyVisiting:
			return fmt.Errorf("task dependency cycle detected at task %s", taskID)
		case dependencyVisited:
			return nil
		}
		states[taskID] = dependencyVisiting
		for _, blockedID := range tasks[taskID].Blocks {
			if err := visit(blockedID); err != nil {
				return err
			}
		}
		states[taskID] = dependencyVisited
		return nil
	}
	for taskID := range tasks {
		if states[taskID] == dependencyUnvisited {
			if err := visit(taskID); err != nil {
				return err
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
