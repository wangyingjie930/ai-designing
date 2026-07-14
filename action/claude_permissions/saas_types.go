package claudepermissions

const (
	InspectTenantToolName    = "inspect_tenant"
	DraftChangePlanToolName  = "draft_change_plan"
	ApplyFeatureFlagToolName = "apply_feature_flag"
	SendChangeNoticeToolName = "send_change_notice"
	DeleteTenantToolName     = "delete_tenant"
)

// SaaSRequest 是生产变更助理的自然语言输入。
type SaaSRequest struct {
	Message string `json:"message"`
}

// SaaSResponse 是生产变更助理的最终回复。
type SaaSResponse struct {
	Message string `json:"message"`
}

// InspectTenantRequest 表示读取租户当前配置的工具参数。
type InspectTenantRequest struct {
	TenantID string `json:"tenant_id" jsonschema:"description=需要查询的租户编号"`
}

// TenantState 是模型可读取的租户配置快照。
type TenantState struct {
	TenantID     string          `json:"tenant_id"`
	FeatureFlags map[string]bool `json:"feature_flags"`
}

// DraftChangePlanRequest 表示只生成计划、不改变生产状态的工具参数。
type DraftChangePlanRequest struct {
	TenantID string `json:"tenant_id" jsonschema:"description=目标租户编号"`
	Goal     string `json:"goal" jsonschema:"description=期望完成的变更目标"`
}

// ChangePlan 是只读计划工具生成的操作建议。
type ChangePlan struct {
	TenantID string   `json:"tenant_id"`
	Goal     string   `json:"goal"`
	Steps    []string `json:"steps"`
}

// ApplyFeatureFlagRequest 表示可逆的生产功能开关变更。
type ApplyFeatureFlagRequest struct {
	TenantID string `json:"tenant_id" jsonschema:"description=目标租户编号"`
	Flag     string `json:"flag" jsonschema:"description=功能开关名称"`
	Enabled  bool   `json:"enabled" jsonschema:"description=目标开关状态"`
}

// FeatureFlagChange 是内存执行器返回的变更结果。
type FeatureFlagChange struct {
	Changed  bool   `json:"changed"`
	TenantID string `json:"tenant_id"`
	Flag     string `json:"flag"`
	Before   bool   `json:"before"`
	After    bool   `json:"after"`
}

// SendChangeNoticeRequest 表示向外部收件人发送变更通知。
type SendChangeNoticeRequest struct {
	TenantID string `json:"tenant_id" jsonschema:"description=相关租户编号"`
	Channel  string `json:"channel" jsonschema:"description=通知渠道，如 email 或 wecom"`
	Message  string `json:"message" jsonschema:"description=将要发送的通知正文"`
}

// ChangeNoticeResult 是模拟外发动作的执行结果。
type ChangeNoticeResult struct {
	Sent     bool   `json:"sent"`
	TenantID string `json:"tenant_id"`
	Channel  string `json:"channel"`
}

// DeleteTenantRequest 表示不可逆删除租户的危险参数。
type DeleteTenantRequest struct {
	TenantID string `json:"tenant_id" jsonschema:"description=要删除的租户编号"`
}

// DeleteTenantResult 是危险工具的返回结构，正常情况下会在执行前被权限内核拦截。
type DeleteTenantResult struct {
	Deleted  bool   `json:"deleted"`
	TenantID string `json:"tenant_id"`
}

// ExecutorSnapshot 是内存执行器供测试和演示检查的只读副本。
type ExecutorSnapshot struct {
	FeatureFlags map[string]map[string]bool `json:"feature_flags"`
	Notices      []SendChangeNoticeRequest  `json:"notices"`
	Deleted      []string                   `json:"deleted"`
}
