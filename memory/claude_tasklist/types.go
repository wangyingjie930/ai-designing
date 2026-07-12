package claudetasklist

// TaskStatus 是任务文件中可长期保存的稳定状态。
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
)

// TaskUpdateStatus 是 TaskUpdate 接受的状态，额外包含删除动作。
type TaskUpdateStatus string

const (
	TaskUpdateStatusPending    TaskUpdateStatus = "pending"
	TaskUpdateStatusInProgress TaskUpdateStatus = "in_progress"
	TaskUpdateStatusCompleted  TaskUpdateStatus = "completed"
	TaskUpdateStatusDeleted    TaskUpdateStatus = "deleted"
)

// Task 是单个 JSON 文件保存的机械任务真相。
type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Status      TaskStatus     `json:"status"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskSummary 是 TaskList 返回的低成本任务视图。
type TaskSummary struct {
	ID        string     `json:"id"`
	Subject   string     `json:"subject"`
	Status    TaskStatus `json:"status"`
	Owner     string     `json:"owner,omitempty"`
	BlockedBy []string   `json:"blockedBy"`
}

// TaskCounts 汇总一个 Task List 中三种稳定状态的数量。
type TaskCounts struct {
	Pending    int `json:"pending"`
	InProgress int `json:"inProgress"`
	Completed  int `json:"completed"`
}

// TaskCreateRequest 描述新任务的模型可写字段。
type TaskCreateRequest struct {
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskUpdateRequest 描述一次需完整校验后再落盘的任务变更。
type TaskUpdateRequest struct {
	TaskID       string            `json:"taskId"`
	Subject      string            `json:"subject,omitempty"`
	Description  string            `json:"description,omitempty"`
	ActiveForm   string            `json:"activeForm,omitempty"`
	Status       *TaskUpdateStatus `json:"status,omitempty"`
	Owner        *string           `json:"owner,omitempty"`
	AddBlocks    []string          `json:"addBlocks,omitempty"`
	AddBlockedBy []string          `json:"addBlockedBy,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

// TaskUpdateResult 返回更新后的完整任务，或确认任务已经删除。
type TaskUpdateResult struct {
	Task    *Task `json:"task,omitempty"`
	Deleted bool  `json:"deleted,omitempty"`
}

// valid 判断持久化状态是否属于稳定三态。
func (status TaskStatus) valid() bool {
	switch status {
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted:
		return true
	default:
		return false
	}
}

// valid 判断更新状态是否属于稳定三态或显式删除动作。
func (status TaskUpdateStatus) valid() bool {
	switch status {
	case TaskUpdateStatusPending, TaskUpdateStatusInProgress, TaskUpdateStatusCompleted, TaskUpdateStatusDeleted:
		return true
	default:
		return false
	}
}
