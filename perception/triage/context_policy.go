package triage

import (
	"fmt"
	"strings"
)

const (
	defaultContextPolicySource = "default_saas_context_policy"
	unnamedContextPolicySource = "unnamed_saas_context_policy"
	unnamedContextRule         = "unnamed_context_rule"
	metadataContextPolicy      = "context_policy"
	metadataContextRule        = "context_rule"
	metadataPriorityReason     = "priority_reason"
)

// SaaSContextBuildInput 是上下文优先级策略的稳定输入，避免规则直接依赖外部 IO。
type SaaSContextBuildInput struct {
	Runtime    RuntimeContext
	Tenant     TenantProfile
	Message    string
	ExtraItems []ContextItem
}

// SaaSContextRule 表示一条可审查的上下文映射规则。
type SaaSContextRule struct {
	Name  string
	Build func(SaaSContextBuildInput) ([]ContextItem, error)
}

// SaaSContextPolicy 把采集到的事实映射成 P0/P1/P2/P3 上下文候选项。
type SaaSContextPolicy struct {
	Name  string
	Rules []SaaSContextRule
}

// DefaultSaaSContextPolicy 返回默认 SaaS 客服上下文策略，生产可按业务线替换。
func DefaultSaaSContextPolicy() *SaaSContextPolicy {
	return &SaaSContextPolicy{
		Name: defaultContextPolicySource,
		Rules: []SaaSContextRule{
			{Name: "p0_runtime_guardrails", Build: buildRuntimeGuardrailItems},
			{Name: "p1_current_task_evidence", Build: buildCurrentTaskEvidenceItems},
			{Name: "p2_supporting_indexes", Build: buildSupportingIndexItems},
			{Name: "p3_deferred_resources", Build: buildDeferredResourceItems},
		},
	}
}

// Build 执行策略规则，并在最后合并调用方显式传入的预分类上下文。
func (p *SaaSContextPolicy) Build(input SaaSContextBuildInput) ([]ContextItem, error) {
	policy := p
	if policy == nil || len(policy.Rules) == 0 {
		policy = DefaultSaaSContextPolicy()
	}
	policyName := strings.TrimSpace(policy.Name)
	if policyName == "" {
		policyName = unnamedContextPolicySource
	}
	items := make([]ContextItem, 0)
	for _, rule := range policy.Rules {
		if rule.Build == nil {
			continue
		}
		ruleName := strings.TrimSpace(rule.Name)
		if ruleName == "" {
			ruleName = unnamedContextRule
		}
		built, err := rule.Build(input)
		if err != nil {
			return nil, fmt.Errorf("context policy rule %q: %w", ruleName, err)
		}
		for _, item := range built {
			items = append(items, annotatePolicyItem(item, policyName, ruleName))
		}
	}
	for _, item := range input.ExtraItems {
		items = append(items, annotatePolicyItem(item, policyName, "request_extra_item"))
	}
	return normalizeSaaSItems(input.Runtime, items)
}

// buildRuntimeGuardrailItems 生成不能丢的运行时身份、当前消息和安全边界。
func buildRuntimeGuardrailItems(input SaaSContextBuildInput) ([]ContextItem, error) {
	return []ContextItem{
		{
			Name:     "platform_safety_policy",
			Content:  strings.Join(input.Tenant.SafetyPolicies, "\n"),
			Priority: PriorityCritical,
			TenantID: input.Runtime.TenantID,
			Kind:     "safety_policy",
			Metadata: contextPolicyMetadata("P0 safety policies constrain every answer"),
		},
		{
			Name:     "runtime_identity",
			Content:  runtimeIdentityText(input.Runtime, input.Tenant),
			Priority: PriorityCritical,
			TenantID: input.Runtime.TenantID,
			Kind:     "runtime_schema",
			Metadata: contextPolicyMetadata("P0 runtime identity prevents cross-tenant guessing"),
		},
		{
			Name:     "current_customer_message",
			Content:  input.Message,
			Priority: PriorityCritical,
			TenantID: input.Runtime.TenantID,
			Kind:     "customer_message",
			Metadata: contextPolicyMetadata("P0 current user request is the task anchor"),
		},
	}, nil
}

// buildCurrentTaskEvidenceItems 生成当前工单、租户配置和最近会话这些直接证据。
func buildCurrentTaskEvidenceItems(input SaaSContextBuildInput) ([]ContextItem, error) {
	items := make([]ContextItem, 0, 3)
	if content := productConfigText(input.Runtime, input.Tenant.ProductConfig); content != "" {
		items = append(items, ContextItem{
			Name:     "tenant_product_config_snapshot",
			Content:  content,
			Priority: PriorityImportant,
			TenantID: input.Runtime.TenantID,
			Kind:     "product_config",
			Metadata: contextPolicyMetadata("P1 product configuration directly changes support advice"),
		})
	}
	if content := ticketContextText(input.Tenant.CurrentTicket); content != "" {
		items = append(items, ContextItem{
			Name:     "current_ticket_context",
			Content:  content,
			Priority: PriorityImportant,
			TenantID: input.Runtime.TenantID,
			Kind:     "ticket_context",
			Metadata: contextPolicyMetadata("P1 current ticket status and signals are direct evidence"),
		})
	}
	if content := recentTurnsText(input.Tenant.RecentTurns, 10); content != "" {
		items = append(items, ContextItem{
			Name:     "recent_session_turns",
			Content:  content,
			Priority: PriorityImportant,
			TenantID: input.Runtime.TenantID,
			Kind:     "conversation_history",
			Metadata: contextPolicyMetadata("P1 recent conversation keeps the current session coherent"),
		})
	}
	return items, nil
}

// buildSupportingIndexItems 生成摘要和索引，帮助模型知道可以进一步读取哪些资料。
func buildSupportingIndexItems(input SaaSContextBuildInput) ([]ContextItem, error) {
	items := make([]ContextItem, 0, 3)
	if content := knowledgeIndexesText("产品手册目录索引", input.Tenant.ManualIndexes); content != "" {
		items = append(items, ContextItem{
			Name:     "tenant_manual_index_summary",
			Content:  content,
			Priority: PrioritySupporting,
			TenantID: input.Runtime.TenantID,
			Kind:     "manual_index",
			Handle:   TenantHandle("manual_index", input.Runtime.TenantID, "root"),
			Metadata: contextPolicyMetadata("P2 manual index supports deeper lookup without loading full docs"),
		})
	}
	if content := knowledgeIndexesText("FAQ 摘要", input.Tenant.FAQSummaries); content != "" {
		items = append(items, ContextItem{
			Name:     "tenant_faq_summary",
			Content:  content,
			Priority: PrioritySupporting,
			TenantID: input.Runtime.TenantID,
			Kind:     "faq_summary",
			Handle:   TenantHandle("faq_index", input.Runtime.TenantID, "root"),
			Metadata: contextPolicyMetadata("P2 FAQ summary gives reusable support background"),
		})
	}
	if content := knowledgeIndexesText("相似历史工单摘要", input.Tenant.SimilarTicketSummaries); content != "" {
		items = append(items, ContextItem{
			Name:     "similar_ticket_cluster_summary",
			Content:  content,
			Priority: PrioritySupporting,
			TenantID: input.Runtime.TenantID,
			Kind:     "similar_ticket_summary",
			Handle:   TenantHandle("ticket_cluster", input.Runtime.TenantID, "recent"),
			Metadata: contextPolicyMetadata("P2 similar tickets provide historical reference, not hard evidence"),
		})
	}
	return items, nil
}

// buildDeferredResourceItems 生成 P3 handle 页表，避免默认把长原文塞进 prompt。
func buildDeferredResourceItems(input SaaSContextBuildInput) ([]ContextItem, error) {
	items := make([]ContextItem, 0, len(input.Tenant.DeferredResources))
	for _, resource := range input.Tenant.DeferredResources {
		item, err := resourceToContextItem(input.Runtime.TenantID, resource)
		if err != nil {
			return nil, err
		}
		item.Metadata = contextPolicyMetadata("P3 raw resource must be read by tenant-scoped handle on demand")
		items = append(items, item)
	}
	return items, nil
}

// contextPolicyMetadata 记录默认策略为什么给出当前上下文优先级，便于生产 trace 审计。
func contextPolicyMetadata(reason string) map[string]string {
	return map[string]string{
		metadataPriorityReason: reason,
	}
}

// annotatePolicyItem 给规则产物补齐策略来源，不覆盖调用方已经写入的业务说明。
func annotatePolicyItem(item ContextItem, policyName string, ruleName string) ContextItem {
	item.Metadata = cloneStringMap(item.Metadata)
	if item.Metadata == nil {
		item.Metadata = map[string]string{}
	}
	if item.Metadata[metadataContextPolicy] == "" {
		item.Metadata[metadataContextPolicy] = policyName
	}
	if item.Metadata[metadataContextRule] == "" {
		item.Metadata[metadataContextRule] = ruleName
	}
	return item
}
