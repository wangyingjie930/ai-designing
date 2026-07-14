package claudepermissions

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolPermissionCheckInput 是单个工具判断当前调用风险时可以使用的完整上下文。
type ToolPermissionCheckInput struct {
	ToolName      string
	ArgumentsJSON string
	Mode          PermissionMode
}

// ToolPermissionCheckResult 是工具自己的权限意见；Behavior 为空表示 passthrough，最终会按默认安全策略转成 ask。
type ToolPermissionCheckResult struct {
	// Behavior 表示工具根据具体参数给出的 allow、ask 或 deny；空值表示 passthrough。
	Behavior PermissionBehavior
	// UpdatedArgumentsJSON 允许工具规范化参数，后续规则判断和真实执行都会使用新参数。
	UpdatedArgumentsJSON string
	// Reason 解释工具为什么给出当前权限结果，审批界面和拒绝结果会展示该原因。
	Reason string
	// BypassImmune 表示 ask 属于不可被 bypassPermissions 或普通 allow 覆盖的安全检查。
	BypassImmune bool
}

// ToolPermissionChecker 由每个工具实现，负责根据自己的参数和业务语义判断风险。
type ToolPermissionChecker interface {
	CheckPermissions(context.Context, ToolPermissionCheckInput) (ToolPermissionCheckResult, error)
}

// ToolPermissionCheckerFunc 让普通函数可以直接注册为工具自己的权限检查器。
type ToolPermissionCheckerFunc func(context.Context, ToolPermissionCheckInput) (ToolPermissionCheckResult, error)

// CheckPermissions 调用函数形式的工具权限检查器。
func (f ToolPermissionCheckerFunc) CheckPermissions(ctx context.Context, input ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	return f(ctx, input)
}

// CheckToolPermissions 查找并运行指定工具自己的检查器；未配置时返回 passthrough。
func (e *PermissionEngine) CheckToolPermissions(ctx context.Context, input ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	if e == nil {
		return ToolPermissionCheckResult{}, fmt.Errorf("permission engine is required")
	}
	policy, ok := e.tools[normalizeToolName(input.ToolName)]
	if !ok {
		return ToolPermissionCheckResult{}, nil
	}
	if policy.Checker == nil {
		return ToolPermissionCheckResult{}, fmt.Errorf("tool %s does not implement CheckPermissions", input.ToolName)
	}
	input.ToolName = normalizeToolName(input.ToolName)
	result, err := policy.Checker.CheckPermissions(ctx, input)
	if err != nil {
		return ToolPermissionCheckResult{}, fmt.Errorf("check permissions for %s: %w", input.ToolName, err)
	}
	if result.Behavior != "" && result.Behavior != PermissionAllow && result.Behavior != PermissionAsk && result.Behavior != PermissionDeny {
		return ToolPermissionCheckResult{}, fmt.Errorf("tool %s returned unsupported permission behavior %q", input.ToolName, result.Behavior)
	}
	if result.UpdatedArgumentsJSON != "" && !json.Valid([]byte(result.UpdatedArgumentsJSON)) {
		return ToolPermissionCheckResult{}, fmt.Errorf("tool %s returned invalid updated arguments JSON", input.ToolName)
	}
	if result.BypassImmune && result.Behavior != PermissionAsk {
		return ToolPermissionCheckResult{}, fmt.Errorf("tool %s can only mark ask decisions as bypass immune", input.ToolName)
	}
	return result, nil
}
