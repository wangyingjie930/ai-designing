package claudepermissions

import "context"

// PreToolUseInput 是工具执行前 Hook 可检查的调用信息。
type PreToolUseInput struct {
	ToolName      string
	ArgumentsJSON string
}

// PreToolUseResult 允许 Hook 拒绝、要求确认或更新工具参数。
type PreToolUseResult struct {
	Behavior             PermissionBehavior
	UpdatedArgumentsJSON string
	Reason               string
}

// PreToolUseHook 在权限内核裁决前运行，但放行结果不能覆盖显式 deny/ask 规则。
type PreToolUseHook interface {
	BeforeToolUse(context.Context, PreToolUseInput) (PreToolUseResult, error)
}

// PreToolUseHookFunc 让普通函数可以直接充当执行前 Hook。
type PreToolUseHookFunc func(context.Context, PreToolUseInput) (PreToolUseResult, error)

// BeforeToolUse 调用函数形式的执行前 Hook。
func (f PreToolUseHookFunc) BeforeToolUse(ctx context.Context, input PreToolUseInput) (PreToolUseResult, error) {
	return f(ctx, input)
}

// PostToolUseInput 是工具成功执行后交给 Hook 的结构化上下文。
type PostToolUseInput struct {
	ToolName      string
	ArgumentsJSON string
	Output        string
}

// PostToolUseHook 可审计并重写成功工具输出。
type PostToolUseHook interface {
	AfterToolUse(context.Context, PostToolUseInput) (string, error)
}

// PostToolUseHookFunc 让普通函数可以直接充当执行后 Hook。
type PostToolUseHookFunc func(context.Context, PostToolUseInput) (string, error)

// AfterToolUse 调用函数形式的执行后 Hook。
func (f PostToolUseHookFunc) AfterToolUse(ctx context.Context, input PostToolUseInput) (string, error) {
	return f(ctx, input)
}

// StopHook 在 Agent 准备结束时检查最终回复。
type StopHook interface {
	BeforeStop(context.Context, string) error
}

// StopHookFunc 让普通函数可以直接充当停止 Hook。
type StopHookFunc func(context.Context, string) error

// BeforeStop 调用函数形式的停止 Hook。
func (f StopHookFunc) BeforeStop(ctx context.Context, finalMessage string) error {
	return f(ctx, finalMessage)
}

// PermissionRequestHook 可以像 Claude Code Hook 一样从宿主侧直接响应审批请求。
type PermissionRequestHook interface {
	ResolvePermission(context.Context, PermissionRequest) (*PermissionResponse, error)
}

// PermissionRequestHookFunc 让普通函数可以直接充当审批 Hook。
type PermissionRequestHookFunc func(context.Context, PermissionRequest) (*PermissionResponse, error)

// ResolvePermission 调用函数形式的审批 Hook。
func (f PermissionRequestHookFunc) ResolvePermission(ctx context.Context, request PermissionRequest) (*PermissionResponse, error) {
	return f(ctx, request)
}
