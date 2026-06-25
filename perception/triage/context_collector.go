package triage

import "context"

// SaaSContextSnapshot 是生产采集层返回给分诊策略的租户上下文快照。
type SaaSContextSnapshot struct {
	Tenant     TenantProfile
	ExtraItems []ContextItem
}

// SaaSContextCollector 抽象生产数据采集边界，真实实现可以读取工单、CRM、知识库和观测系统。
type SaaSContextCollector interface {
	CollectSaaSContext(ctx context.Context, request TriageRequest) (SaaSContextSnapshot, error)
}

// RequestSaaSContextCollector 使用请求体里已经携带的资料，保持 demo 和单测的兼容路径。
type RequestSaaSContextCollector struct{}

// CollectSaaSContext 返回请求内联上下文，不做外部 IO。
func (RequestSaaSContextCollector) CollectSaaSContext(_ context.Context, request TriageRequest) (SaaSContextSnapshot, error) {
	return SaaSContextSnapshot{
		Tenant:     request.Tenant,
		ExtraItems: request.ExtraItems,
	}, nil
}
