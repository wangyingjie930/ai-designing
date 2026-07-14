package claudepermissions

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// SaaSExecutor 定义生产变更工具真正产生业务效果的边界。
type SaaSExecutor interface {
	InspectTenant(context.Context, InspectTenantRequest) (*TenantState, error)
	DraftChangePlan(context.Context, DraftChangePlanRequest) (*ChangePlan, error)
	ApplyFeatureFlag(context.Context, ApplyFeatureFlagRequest) (*FeatureFlagChange, error)
	SendChangeNotice(context.Context, SendChangeNoticeRequest) (*ChangeNoticeResult, error)
	DeleteTenant(context.Context, DeleteTenantRequest) (*DeleteTenantResult, error)
}

// InMemorySaaSExecutor 用进程内状态模拟生产系统，避免 demo 修改任何真实数据。
type InMemorySaaSExecutor struct {
	mu           sync.Mutex
	featureFlags map[string]map[string]bool
	notices      []SendChangeNoticeRequest
	deleted      []string
}

// NewInMemorySaaSExecutor 创建带一个示例租户的确定性执行器。
func NewInMemorySaaSExecutor() *InMemorySaaSExecutor {
	return &InMemorySaaSExecutor{
		featureFlags: map[string]map[string]bool{
			"TENANT-42": {"invoice_v2": true, "smart_search": false},
		},
	}
}

// InspectTenant 返回租户当前配置的副本。
func (e *InMemorySaaSExecutor) InspectTenant(_ context.Context, request InspectTenantRequest) (*TenantState, error) {
	tenantID := strings.TrimSpace(request.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	flags := cloneFlags(e.featureFlags[tenantID])
	if flags == nil {
		flags = map[string]bool{}
	}
	return &TenantState{TenantID: tenantID, FeatureFlags: flags}, nil
}

// DraftChangePlan 生成只读变更计划，不修改执行器状态。
func (e *InMemorySaaSExecutor) DraftChangePlan(_ context.Context, request DraftChangePlanRequest) (*ChangePlan, error) {
	if strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.Goal) == "" {
		return nil, fmt.Errorf("tenant_id and goal are required")
	}
	return &ChangePlan{
		TenantID: request.TenantID,
		Goal:     request.Goal,
		Steps:    []string{"读取当前配置", "提交最小可逆变更", "验证结果并记录通知"},
	}, nil
}

// ApplyFeatureFlag 在内存中执行可逆开关变更。
func (e *InMemorySaaSExecutor) ApplyFeatureFlag(_ context.Context, request ApplyFeatureFlagRequest) (*FeatureFlagChange, error) {
	if strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.Flag) == "" {
		return nil, fmt.Errorf("tenant_id and flag are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.featureFlags[request.TenantID] == nil {
		e.featureFlags[request.TenantID] = make(map[string]bool)
	}
	before := e.featureFlags[request.TenantID][request.Flag]
	e.featureFlags[request.TenantID][request.Flag] = request.Enabled
	return &FeatureFlagChange{
		Changed: before != request.Enabled, TenantID: request.TenantID,
		Flag: request.Flag, Before: before, After: request.Enabled,
	}, nil
}

// SendChangeNotice 在内存中记录一次模拟外发，不连接真实消息系统。
func (e *InMemorySaaSExecutor) SendChangeNotice(_ context.Context, request SendChangeNoticeRequest) (*ChangeNoticeResult, error) {
	if strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.Channel) == "" || strings.TrimSpace(request.Message) == "" {
		return nil, fmt.Errorf("tenant_id, channel and message are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notices = append(e.notices, request)
	return &ChangeNoticeResult{Sent: true, TenantID: request.TenantID, Channel: request.Channel}, nil
}

// DeleteTenant 实现危险动作的内存版本；正常 Agent 路径会在抵达这里前永久拒绝。
func (e *InMemorySaaSExecutor) DeleteTenant(_ context.Context, request DeleteTenantRequest) (*DeleteTenantResult, error) {
	if strings.TrimSpace(request.TenantID) == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.featureFlags, request.TenantID)
	e.deleted = append(e.deleted, request.TenantID)
	return &DeleteTenantResult{Deleted: true, TenantID: request.TenantID}, nil
}

// Snapshot 返回深拷贝，避免测试并发读取内部 map。
func (e *InMemorySaaSExecutor) Snapshot() ExecutorSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	flags := make(map[string]map[string]bool, len(e.featureFlags))
	for tenantID, values := range e.featureFlags {
		flags[tenantID] = cloneFlags(values)
	}
	return ExecutorSnapshot{
		FeatureFlags: flags,
		Notices:      append([]SendChangeNoticeRequest(nil), e.notices...),
		Deleted:      append([]string(nil), e.deleted...),
	}
}

// cloneFlags 复制单个租户的功能开关集合。
func cloneFlags(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	result := make(map[string]bool, len(source))
	for name, enabled := range source {
		result[name] = enabled
	}
	return result
}
