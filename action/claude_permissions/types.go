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
	// PermissionModeDefault 表示标准交互模式：只读工具自动允许，写操作和外部副作用默认询问。
	PermissionModeDefault PermissionMode = "default"
	// PermissionModePlan 表示只规划不执行：只读工具允许，编辑、外部副作用和危险操作全部拒绝。
	PermissionModePlan PermissionMode = "plan"
	// PermissionModeAcceptEdits 表示自动接受可逆内部修改；外部副作用仍需询问，危险操作仍拒绝。
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	// PermissionModeDontAsk 表示禁止弹出人工确认；原本需要询问的调用直接拒绝，显式允许规则仍可放行。
	PermissionModeDontAsk PermissionMode = "dontAsk"
	// PermissionModeBypassPermissions 表示普通工具默认直接放行；安全硬限制和显式 deny/ask 规则仍然有效。
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// ToolKind 把非 coding 工具映射到 Claude Code 的读、编辑、外部副作用和危险操作边界。
type ToolKind string

const (
	// ToolKindRead 表示只读取信息、不改变业务状态的工具，例如查询租户配置；默认可自动放行。
	ToolKindRead ToolKind = "read"
	// ToolKindEdit 表示会修改内部状态但通常可撤销的工具，例如切换功能开关；默认需要确认。
	ToolKindEdit ToolKind = "edit"
	// ToolKindExternal 表示会影响系统外部的工具，例如发送短信或调用第三方接口；即使允许普通编辑也仍需确认。
	ToolKindExternal ToolKind = "external"
	// ToolKindDestructive 表示删除、清空等高危且难以恢复的工具；当前权限内核在所有模式下都会拒绝。
	ToolKindDestructive ToolKind = "destructive"
)

// ToolPolicy 描述一个工具的稳定安全属性，业务参数规则由 PermissionRule 另行承载。
type ToolPolicy struct {
	Name string
	Kind ToolKind
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
