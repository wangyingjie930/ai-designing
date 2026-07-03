package tool

// ExpectedToolCall 表示某个评测任务里“应该发生”的工具调用，用来计算召回率。
type ExpectedToolCall struct {
	ID       string
	ToolName string

	// AcceptableTools 允许一个期望能力由多个等价工具满足；为空时默认只接受 ToolName。
	AcceptableTools []string
}

// ActualToolCall 表示 Agent 实际发起的一次工具调用，以及人工或规则 scorer 对这次调用的判断。
type ActualToolCall struct {
	CallID   string
	ToolName string
	Intent   string

	// Necessary 表示这次调用本身是否必要，用于 tool_call_precision。
	Necessary bool

	// CoveredExpectedCallID 表示这次调用覆盖了哪个期望调用；为空表示没有覆盖期望能力。
	CoveredExpectedCallID string

	// WrongTool 专门标记“能力面选错”，不要用它承载参数错误或执行失败。
	WrongTool bool

	SchemaChecked bool
	SchemaValid   bool

	EntityBindingRequired bool
	EntityBindingCorrect  bool

	Executed            bool
	ExecutionSuccessful bool

	ResultHasContent bool
	ResultRelevant   bool
}

// ToolEvaluationSample 是闭环评估的最小输入单元：一个用户任务、期望工具、实际工具和最终回答判断。
type ToolEvaluationSample struct {
	TaskID   string
	UserTask string

	ExpectedCalls []ExpectedToolCall
	ActualCalls   []ActualToolCall

	// HasAvailableToolResult 和 FinalAnswerUsedToolResult 是任务级判断，用于最终回答是否真正用上工具结果。
	HasAvailableToolResult    bool
	FinalAnswerUsedToolResult bool
}
