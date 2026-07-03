package tool

import "time"

// ToolExecutionStatus 表示线上一次工具调用的执行状态，只来自实际运行结果。
type ToolExecutionStatus string

const (
	ToolExecutionStatusSuccess ToolExecutionStatus = "success"
	ToolExecutionStatusError   ToolExecutionStatus = "error"
	ToolExecutionStatusTimeout ToolExecutionStatus = "timeout"
)

// ToolCallEvent 是实时观测侧的最小事件，只描述线上实际发生的工具调用，不包含离线标准答案。
type ToolCallEvent struct {
	CallID   string
	ToolName string
	At       time.Time

	SchemaChecked bool
	SchemaValid   bool

	ExecutionStatus ToolExecutionStatus
	ErrorType       string
	Latency         time.Duration

	// FinalAnswerUsedFact 表示最终回答是否使用了这次成功工具调用的关键事实。
	FinalAnswerUsedFact bool
}
