package progresstracking

import "time"

const (
	defaultPlanID = "default"
)

// TaskStatus 是 Python TaskStatus Enum 的 Go 版本，保持持久化字符串稳定。
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
)

// Valid 判断状态是否属于 progress tracker 支持的闭集。
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted, TaskStatusFailed:
		return true
	default:
		return false
	}
}

// TaskItem 是 Python dataclass TaskItem 的 Go 翻译，保存单个任务的恢复状态。
type TaskItem struct {
	Description   string     `json:"description"`
	Status        TaskStatus `json:"status"`
	Result        *string    `json:"result,omitempty"`
	FilesModified []string   `json:"files_modified"`
	Error         *string    `json:"error,omitempty"`
}

// Config 汇总进度跟踪器的 SQLite 路径、计划标识和时钟依赖。
type Config struct {
	DBPath string
	PlanID string
	Now    func() time.Time
}

// CreatePlanRequest 初始化或替换当前计划，items 由调用方或 Agent 动态生成。
type CreatePlanRequest struct {
	Items []string `json:"items"`
	Reset bool     `json:"reset,omitempty"`
}

// CreatePlanResponse 返回创建后的计划快照，便于 Agent 继续推理下一步。
type CreatePlanResponse struct {
	PlanID string     `json:"plan_id"`
	Items  []TaskItem `json:"items"`
}

// StartTaskRequest 把指定任务标记为 in_progress，index 沿用 Python 的 0-based 位置。
type StartTaskRequest struct {
	Index int `json:"index"`
}

// CompleteTaskRequest 对应 Python complete(index, result, files) 的输入。
type CompleteTaskRequest struct {
	Index  int      `json:"index"`
	Result string   `json:"result"`
	Files  []string `json:"files,omitempty"`
}

// FailTaskRequest 对应 Python fail(index, error) 的输入。
type FailTaskRequest struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

// TaskUpdateResponse 返回一次状态变更后的任务和完整恢复上下文。
type TaskUpdateResponse struct {
	PlanID            string   `json:"plan_id"`
	Item              TaskItem `json:"item"`
	ResumptionContext string   `json:"resumption_context"`
}

// ResumptionContextRequest 预留给 ADK tool 的空输入，避免工具无 schema。
type ResumptionContextRequest struct{}

// ResumptionContextResponse 返回 Python resumption_context() 的文本结果。
type ResumptionContextResponse struct {
	PlanID  string `json:"plan_id"`
	Context string `json:"context"`
}
