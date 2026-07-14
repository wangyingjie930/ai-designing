package claudepermissions

import (
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

// DefaultSaaSToolPolicies 返回生产变更场景的工具权限入口，每个工具都有自己的 CheckPermissions。
func DefaultSaaSToolPolicies() []ToolPolicy {
	return []ToolPolicy{
		{Name: InspectTenantToolName, Checker: ToolPermissionCheckerFunc(checkInspectTenantPermission)},
		{Name: DraftChangePlanToolName, Checker: ToolPermissionCheckerFunc(checkDraftChangePlanPermission)},
		{Name: ApplyFeatureFlagToolName, Checker: ToolPermissionCheckerFunc(checkApplyFeatureFlagPermission)},
		{Name: SendChangeNoticeToolName, Checker: ToolPermissionCheckerFunc(checkSendChangeNoticePermission)},
		{Name: DeleteTenantToolName, Checker: ToolPermissionCheckerFunc(checkDeleteTenantPermission)},
	}
}

// DefaultSaaSPermissionRules 返回默认显式规则，危险删除工具对模型完全不可见。
func DefaultSaaSPermissionRules() []PermissionRule {
	return []PermissionRule{{
		Behavior: PermissionDeny,
		ToolName: DeleteTenantToolName,
		Reason:   "租户删除不允许由 Agent 执行",
	}}
}

// NewSaaSTools 把 SaaSExecutor 的五个动作转换成 Eino 工具。
func NewSaaSTools(executor SaaSExecutor) ([]tool.BaseTool, error) {
	if executor == nil {
		return nil, fmt.Errorf("saas executor is required")
	}
	inspect, err := toolutils.InferTool[InspectTenantRequest, *TenantState](
		InspectTenantToolName, "读取租户当前功能开关，不修改生产状态。", executor.InspectTenant,
	)
	if err != nil {
		return nil, err
	}
	plan, err := toolutils.InferTool[DraftChangePlanRequest, *ChangePlan](
		DraftChangePlanToolName, "生成生产变更计划，不执行变更。", executor.DraftChangePlan,
	)
	if err != nil {
		return nil, err
	}
	apply, err := toolutils.InferTool[ApplyFeatureFlagRequest, *FeatureFlagChange](
		ApplyFeatureFlagToolName, "修改租户功能开关；这是可逆写操作，需要权限确认。", executor.ApplyFeatureFlag,
	)
	if err != nil {
		return nil, err
	}
	notice, err := toolutils.InferTool[SendChangeNoticeRequest, *ChangeNoticeResult](
		SendChangeNoticeToolName, "模拟向外部发送变更通知；外部副作用需要权限确认。", executor.SendChangeNotice,
	)
	if err != nil {
		return nil, err
	}
	deleteTenant, err := toolutils.InferTool[DeleteTenantRequest, *DeleteTenantResult](
		DeleteTenantToolName, "删除租户；危险操作，默认权限策略会隐藏并拒绝。", executor.DeleteTenant,
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{inspect, plan, apply, notice, deleteTenant}, nil
}
