package claudepermissions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// checkInspectTenantPermission 允许合法的只读租户查询，不再依赖统一 read 标签直接放行。
func checkInspectTenantPermission(_ context.Context, input ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	arguments, err := decodeToolArguments[InspectTenantRequest](input.ArgumentsJSON)
	if err != nil {
		return ToolPermissionCheckResult{}, err
	}
	if strings.TrimSpace(arguments.TenantID) == "" {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "查询租户前必须提供 tenant_id"}, nil
	}
	return ToolPermissionCheckResult{Behavior: PermissionAllow, Reason: "租户查询是无副作用的只读操作"}, nil
}

// checkDraftChangePlanPermission 允许合法的计划生成，因为该工具不会改变任何业务状态。
func checkDraftChangePlanPermission(_ context.Context, input ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	arguments, err := decodeToolArguments[DraftChangePlanRequest](input.ArgumentsJSON)
	if err != nil {
		return ToolPermissionCheckResult{}, err
	}
	if strings.TrimSpace(arguments.TenantID) == "" || strings.TrimSpace(arguments.Goal) == "" {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "生成变更计划必须提供 tenant_id 和 goal"}, nil
	}
	return ToolPermissionCheckResult{Behavior: PermissionAllow, Reason: "生成计划不会修改生产状态"}, nil
}

// checkApplyFeatureFlagPermission 根据租户、开关名称和当前模式判断可逆配置变更的风险。
func checkApplyFeatureFlagPermission(_ context.Context, input ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	arguments, err := decodeToolArguments[ApplyFeatureFlagRequest](input.ArgumentsJSON)
	if err != nil {
		return ToolPermissionCheckResult{}, err
	}
	if strings.TrimSpace(arguments.TenantID) == "" || strings.TrimSpace(arguments.Flag) == "" {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "功能开关变更必须提供 tenant_id 和 flag"}, nil
	}
	if strings.EqualFold(arguments.TenantID, "TENANT-LOCKED") {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "受保护租户禁止由 Agent 修改功能开关"}, nil
	}
	if input.Mode == PermissionModePlan {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "规划模式下不执行功能开关变更"}, nil
	}
	if strings.EqualFold(arguments.Flag, "security_enforcement") {
		return ToolPermissionCheckResult{
			Behavior: PermissionAsk, Reason: "安全策略开关必须由人工确认", BypassImmune: true,
		}, nil
	}
	if input.Mode == PermissionModeAcceptEdits {
		return ToolPermissionCheckResult{Behavior: PermissionAllow, Reason: "acceptEdits 允许普通可逆开关变更"}, nil
	}
	return ToolPermissionCheckResult{Behavior: PermissionAsk, Reason: "功能开关变更需要确认"}, nil
}

// checkSendChangeNoticePermission 拒绝疑似敏感内容，并要求所有真实外发动作必须人工确认。
func checkSendChangeNoticePermission(_ context.Context, input ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	arguments, err := decodeToolArguments[SendChangeNoticeRequest](input.ArgumentsJSON)
	if err != nil {
		return ToolPermissionCheckResult{}, err
	}
	if strings.TrimSpace(arguments.TenantID) == "" || strings.TrimSpace(arguments.Channel) == "" || strings.TrimSpace(arguments.Message) == "" {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "发送通知必须提供 tenant_id、channel 和 message"}, nil
	}
	if input.Mode == PermissionModePlan {
		return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "规划模式下不发送外部通知"}, nil
	}
	normalizedMessage := strings.ToLower(arguments.Message)
	for _, sensitive := range []string{"password", "token", "密钥", "密码"} {
		if strings.Contains(normalizedMessage, sensitive) {
			return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "外发通知疑似包含敏感信息"}, nil
		}
	}
	return ToolPermissionCheckResult{
		Behavior: PermissionAsk, Reason: "外发通知会影响系统外部，必须人工确认", BypassImmune: true,
	}, nil
}

// checkDeleteTenantPermission 永久拒绝不可逆租户删除，任何权限模式都不能覆盖。
func checkDeleteTenantPermission(_ context.Context, _ ToolPermissionCheckInput) (ToolPermissionCheckResult, error) {
	return ToolPermissionCheckResult{Behavior: PermissionDeny, Reason: "租户删除是不可逆操作，禁止由 Agent 执行"}, nil
}

// decodeToolArguments 把工具 JSON 参数解码为各工具自己的类型，避免权限检查依赖脆弱的字符串匹配。
func decodeToolArguments[T any](argumentsJSON string) (T, error) {
	var arguments T
	if err := json.Unmarshal([]byte(argumentsJSON), &arguments); err != nil {
		return arguments, fmt.Errorf("decode tool arguments: %w", err)
	}
	return arguments, nil
}
