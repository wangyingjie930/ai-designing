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
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted, TaskStatusFailed, TaskStatusBlocked, TaskStatusNeedsReview, TaskStatusNeedsRework:
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
	Items []string `json:"items" jsonschema:"required" jsonschema_description:"要初始化的可执行任务列表；每一项都应是可以被开始、完成或失败记录的具体动作"`
	Reset bool     `json:"reset,omitempty" jsonschema_description:"只有用户明确要求重建已有计划时才传 true，避免覆盖已经写入 SQLite 的 checkpoint"`
}

// CreatePlanResponse 返回创建后的计划快照，便于 Agent 继续推理下一步。
type CreatePlanResponse struct {
	PlanID string     `json:"plan_id"`
	Items  []TaskItem `json:"items"`
}

// StartTaskRequest 把指定任务标记为 in_progress，index 沿用 Python 的 0-based 位置。
type StartTaskRequest struct {
	Index int `json:"index" jsonschema:"required,minimum=0" jsonschema_description:"要标记为 in_progress 的任务下标，使用 0-based index"`
}

// CompleteTaskRequest 对应 Python complete(index, result, files) 的输入。
type CompleteTaskRequest struct {
	Index  int      `json:"index" jsonschema:"required,minimum=0" jsonschema_description:"要标记完成的任务下标，使用 0-based index"`
	Result string   `json:"result" jsonschema:"required" jsonschema_description:"任务完成后的真实结果或证据摘要；不能留空，也不要把下一步计划当成结果"`
	Files  []string `json:"files,omitempty" jsonschema_description:"本次任务产出的文件、材料或凭证引用，例如 venue-contract.pdf"`
}

// FailTaskRequest 对应 Python fail(index, error) 的输入。
type FailTaskRequest struct {
	Index int    `json:"index" jsonschema:"required,minimum=0" jsonschema_description:"要标记失败的任务下标，使用 0-based index"`
	Error string `json:"error" jsonschema:"required" jsonschema_description:"任务失败或阻塞的具体原因；用于恢复后优先处理问题"`
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
