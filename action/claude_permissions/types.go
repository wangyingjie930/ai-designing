package claudepermissions

// PermissionBehavior 对齐 Claude Code 权限管线的三态裁决。
type PermissionBehavior string

const (
	// PermissionAllow 表示允许继续执行当前工具调用；安全硬限制和更高优先级显式规则仍可覆盖该结果。
	PermissionAllow PermissionBehavior = "allow"
	// PermissionAsk 表示暂停当前工具调用并发起审批；dontAsk 模式会把该结果转成拒绝。
	PermissionAsk PermissionBehavior = "ask"
	// PermissionDeny 表示禁止执行当前工具调用，并向模型返回可解释的结构化拒绝结果。
	PermissionDeny PermissionBehavior = "deny"
)

// PermissionMode 保留 Claude Code 对外暴露且适合本 demo 的权限模式名称。
type PermissionMode string

const (
	// PermissionModeDefault 表示标准交互模式：尊重每个工具的 CheckPermissions 结果，需要询问时进入人工审批。
	PermissionModeDefault PermissionMode = "default"
	// PermissionModePlan 表示规划模式；哪些调用可以执行由各工具结合自身语义判断，不由统一工具分类决定。
	PermissionModePlan PermissionMode = "plan"
	// PermissionModeAcceptEdits 表示工具可以自动接受自身认定为可逆编辑的调用，其他风险仍由工具判断。
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	// PermissionModeDontAsk 表示禁止弹出人工确认；原本需要询问的调用直接拒绝，显式允许规则仍可放行。
	PermissionModeDontAsk PermissionMode = "dontAsk"
	// PermissionModeBypassPermissions 表示普通工具默认直接放行；安全硬限制和显式 deny/ask 规则仍然有效。
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// ToolPolicy 描述一个工具的权限入口；每个工具必须提供自己的 Checker，不使用统一风险分类。
type ToolPolicy struct {
	Name    string
	Checker ToolPermissionChecker
}

// PermissionRule 表示用户或组织显式配置的工具权限规则。
type PermissionRule struct {
	Behavior         PermissionBehavior
	ToolName         string
	ArgumentContains string
	Reason           string
}

// EvaluationInput 是权限内核判断一次工具调用所需的完整输入。
type EvaluationInput struct {
	Mode          PermissionMode
	ToolName      string
	ArgumentsJSON string
	HookDecision  PermissionBehavior
	HookReason    string
	ToolCheck     ToolPermissionCheckResult
	Rules         []PermissionRule
}

// PermissionDecision 是权限内核的稳定输出，不负责实际等待人工确认。
type PermissionDecision struct {
	Behavior PermissionBehavior `json:"behavior"`
	Reason   string             `json:"reason,omitempty"`
	Source   string             `json:"source,omitempty"`
}

// PermissionRequest 是工具调用暂停后交给外层审批界面的结构化请求。
type PermissionRequest struct {
	ID            string `json:"id"`
	ToolName      string `json:"tool_name"`
	ArgumentsJSON string `json:"arguments_json"`
	Reason        string `json:"reason"`
}

// PermissionResponse 是外层对某个待审批工具调用的唯一决策。
type PermissionResponse struct {
	RequestID            string             `json:"request_id"`
	Behavior             PermissionBehavior `json:"behavior"`
	UpdatedArgumentsJSON string             `json:"updated_arguments_json,omitempty"`
	Reason               string             `json:"reason,omitempty"`
}
