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
	Subject     string         `json:"subject" jsonschema:"required" jsonschema_description:"任务标题，使用简洁、可验证的动作表述"`
	Description string         `json:"description" jsonschema:"required" jsonschema_description:"任务的完整目标、边界和完成标准"`
	ActiveForm  string         `json:"activeForm,omitempty" jsonschema_description:"任务进行中展示的现在进行式文案"`
	Metadata    map[string]any `json:"metadata,omitempty" jsonschema_description:"可选扩展信息；不能承载任务核心状态"`
}

// TaskGetRequest 指定要读取完整详情的任务 ID。
type TaskGetRequest struct {
	TaskID string `json:"taskId" jsonschema:"required" jsonschema_description:"要读取或更新的任务 ID"`
}

// TaskListRequest 是 TaskList 的无参数输入，保留空对象工具契约。
type TaskListRequest struct{}

// TaskUpdateRequest 描述一次需完整校验后再落盘的任务变更。
type TaskUpdateRequest struct {
	TaskID       string            `json:"taskId" jsonschema:"required" jsonschema_description:"要读取或更新的任务 ID"`
	Subject      string            `json:"subject,omitempty" jsonschema_description:"新的任务标题；不修改时省略"`
	Description  string            `json:"description,omitempty" jsonschema_description:"新的任务完整描述；不修改时省略"`
	ActiveForm   string            `json:"activeForm,omitempty" jsonschema_description:"新的进行中文案；不修改时省略"`
	Status       *TaskUpdateStatus `json:"status,omitempty" jsonschema:"enum=pending,enum=in_progress,enum=completed,enum=deleted" jsonschema_description:"新状态；deleted 表示删除任务"`
	Owner        *string           `json:"owner,omitempty" jsonschema_description:"负责人名称；空字符串表示取消分配"`
	AddBlocks    []string          `json:"addBlocks,omitempty" jsonschema_description:"要新增的后续任务 ID；当前任务会阻塞这些任务"`
	AddBlockedBy []string          `json:"addBlockedBy,omitempty" jsonschema_description:"要新增的前置任务 ID；这些任务未完成时会阻塞当前任务"`
	Metadata     map[string]any    `json:"metadata,omitempty" jsonschema_description:"按键合并扩展信息；值为 null 时删除对应键"`
}

// TaskUpdateResult 返回更新后的完整任务，或确认任务已经删除。
type TaskUpdateResult struct {
	Task    *Task `json:"task,omitempty"`
	Deleted bool  `json:"deleted,omitempty"`
}

// TaskCreateResponse 返回刚创建并已持久化的完整任务。
type TaskCreateResponse struct {
	Task *Task `json:"task"`
}

// TaskGetResponse 返回指定任务的完整详情。
type TaskGetResponse struct {
	Task *Task `json:"task"`
}

// TaskListResponse 返回适合模型低成本查看的任务摘要。
type TaskListResponse struct {
	Tasks []TaskSummary `json:"tasks"`
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
