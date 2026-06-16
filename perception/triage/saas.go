package triage

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const defaultSaaSBudget = 24_000

// RuntimeContext 是多租户 SaaS Agent 的强制运行时上下文。
type RuntimeContext struct {
	TenantID    string `json:"tenant_id" jsonschema:"description=Tenant id; every resource must belong to this tenant"`
	UserID      string `json:"user_id" jsonschema:"description=Current end user id"`
	ProjectID   string `json:"project_id,omitempty" jsonschema:"description=Optional project/workspace id inside the tenant"`
	SessionID   string `json:"session_id" jsonschema:"description=Current support session id"`
	TicketID    string `json:"ticket_id,omitempty" jsonschema:"description=Current support ticket id"`
	ProductLine string `json:"product_line,omitempty" jsonschema:"description=SaaS product line or module family"`
	Plan        string `json:"plan,omitempty" jsonschema:"description=Tenant subscription plan"`
}

// Validate 确认 P0 身份字段完整，缺失时不允许继续分诊。
func (c RuntimeContext) Validate() error {
	if strings.TrimSpace(c.TenantID) == "" {
		return fmt.Errorf("runtime tenant_id is required")
	}
	if strings.TrimSpace(c.UserID) == "" {
		return fmt.Errorf("runtime user_id is required")
	}
	if strings.TrimSpace(c.SessionID) == "" {
		return fmt.Errorf("runtime session_id is required")
	}
	return nil
}

// TenantProfile 保存当前租户可进入 P1/P2/P3 分诊池的资料。
type TenantProfile struct {
	TenantID               string             `json:"tenant_id,omitempty"`
	TenantName             string             `json:"tenant_name,omitempty"`
	ProductConfig          map[string]string  `json:"product_config,omitempty"`
	CurrentTicket          TicketContext      `json:"current_ticket,omitempty"`
	RecentTurns            []ConversationTurn `json:"recent_turns,omitempty"`
	ManualIndexes          []KnowledgeIndex   `json:"manual_indexes,omitempty"`
	FAQSummaries           []KnowledgeIndex   `json:"faq_summaries,omitempty"`
	SimilarTicketSummaries []KnowledgeIndex   `json:"similar_ticket_summaries,omitempty"`
	DeferredResources      []ResourceRef      `json:"deferred_resources,omitempty"`
	SafetyPolicies         []string           `json:"safety_policies,omitempty"`
}

// TicketContext 是当前工单页或客服会话里的 P1 证据。
type TicketContext struct {
	TicketID string   `json:"ticket_id,omitempty"`
	Status   string   `json:"status,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Signals  []string `json:"signals,omitempty"`
}

// ConversationTurn 表示连续 session 里最近 5-10 轮上下文。
type ConversationTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// KnowledgeIndex 是 P2 摘要或目录索引，帮助 Agent 知道下一步该取哪个 handle。
type KnowledgeIndex struct {
	ID      string `json:"id,omitempty"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Handle  string `json:"handle,omitempty"`
}

// ResourceRef 是 P3 冷资料入口，只暴露 handle，不预加载正文。
type ResourceRef struct {
	Kind     string `json:"kind,omitempty"`
	ID       string `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
	Handle   string `json:"handle,omitempty"`
	TenantID string `json:"tenant_id,omitempty"`
}

// TriageRequest 是 ADK 工具和命令行 demo 共享的分诊入参。
type TriageRequest struct {
	Runtime      RuntimeContext `json:"runtime" jsonschema:"description=Required multi-tenant runtime identity"`
	Tenant       TenantProfile  `json:"tenant,omitempty" jsonschema:"description=Tenant scoped context candidates"`
	Message      string         `json:"message" jsonschema:"description=Current customer message"`
	ExtraItems   []ContextItem  `json:"extra_items,omitempty" jsonschema:"description=Optional pre-classified context items"`
	Budget       int            `json:"budget,omitempty" jsonschema:"description=Context token budget for this triage call"`
	IncludeTrace bool           `json:"include_trace,omitempty" jsonschema:"description=Whether caller wants detailed triage trace in the final answer"`
}

// ContextBlock 是准备进入 prompt 的稳定上下文块。
type ContextBlock struct {
	Name          string            `json:"name"`
	Priority      Priority          `json:"priority"`
	PriorityLabel string            `json:"priority_label"`
	Content       string            `json:"content,omitempty"`
	Handle        string            `json:"handle,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	TokenEstimate int               `json:"token_estimate"`
	TenantID      string            `json:"tenant_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// PreparedTriageContext 是 deterministic triage 之后交给 ADK Agent 的资料包。
type PreparedTriageContext struct {
	Runtime        RuntimeContext `json:"runtime"`
	Message        string         `json:"message"`
	Prompt         string         `json:"prompt"`
	Selected       []ContextBlock `json:"selected"`
	Deferred       []ContextBlock `json:"deferred"`
	Dropped        []ContextBlock `json:"dropped"`
	Decision       TriageDecision `json:"decision"`
	Health         TriageHealth   `json:"health"`
	OutputContract []string       `json:"output_contract"`
}

// SaaSTriagePlanner 把 SaaS 客服候选资料转成 P0/P1/P2/P3 并执行分诊。
type SaaSTriagePlanner struct {
	config TriageConfig
}

// NewSaaSTriagePlanner 创建多租户 SaaS 客服分诊 planner。
func NewSaaSTriagePlanner(config TriageConfig) *SaaSTriagePlanner {
	return &SaaSTriagePlanner{config: config}
}

// PrepareTriageContext 先做租户隔离校验，再构造可送入 Eino ADK 的分诊结果。
func (p *SaaSTriagePlanner) PrepareTriageContext(_ context.Context, request TriageRequest) (*PreparedTriageContext, error) {
	if err := request.Runtime.Validate(); err != nil {
		return nil, err
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		return nil, fmt.Errorf("customer message is required")
	}
	profile, err := normalizeTenantProfile(request.Runtime, request.Tenant)
	if err != nil {
		return nil, err
	}
	items, err := buildSaaSContextItems(request.Runtime, profile, request.Message, request.ExtraItems)
	if err != nil {
		return nil, err
	}
	budget := request.Budget
	if budget <= 0 {
		if p.config.Budget > 0 {
			budget = p.config.Budget
		} else {
			budget = defaultSaaSBudget
		}
	}
	config := p.config
	config.Budget = budget
	if config.ErrorDetector == nil {
		config.ErrorDetector = DefaultSupportErrorDetector
	}
	result := NewContextTriage(config).Triage(items)
	prepared := &PreparedTriageContext{
		Runtime:        request.Runtime,
		Message:        request.Message,
		Selected:       toContextBlocks(result.Selected),
		Deferred:       toContextBlocks(result.Deferred),
		Dropped:        toContextBlocks(result.Dropped),
		Decision:       result.Decision,
		Health:         result.Health,
		OutputContract: defaultOutputContract(),
	}
	prepared.Prompt = BuildPrompt(prepared)
	return prepared, nil
}

// DefaultSupportErrorDetector 识别客服故障里必须保留的错误证据。
func DefaultSupportErrorDetector(item ContextItem) bool {
	content := strings.ToLower(item.Content + " " + item.Name + " " + item.Kind)
	keywords := []string{"traceback", "exception", "panic", "stack", "error", "timeout", "502", "503", "failed", "失败", "报错", "超时", "告警"}
	for _, keyword := range keywords {
		if strings.Contains(content, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// BuildPrompt 将 selected context 和 P3 handles 组装成模型可读的工作区。
func BuildPrompt(prepared *PreparedTriageContext) string {
	if prepared == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("你正在处理一个多租户 SaaS 客服会话。必须遵守 tenant_id 隔离，不得读取或泄露其他租户资料。\n\n")
	b.WriteString("## Runtime P0\n")
	b.WriteString(fmt.Sprintf("- tenant_id: %s\n- user_id: %s\n- session_id: %s\n", prepared.Runtime.TenantID, prepared.Runtime.UserID, prepared.Runtime.SessionID))
	if prepared.Runtime.ProjectID != "" {
		b.WriteString("- project_id: " + prepared.Runtime.ProjectID + "\n")
	}
	if prepared.Runtime.TicketID != "" {
		b.WriteString("- ticket_id: " + prepared.Runtime.TicketID + "\n")
	}
	b.WriteString("\n## Selected Context\n")
	for _, block := range prepared.Selected {
		b.WriteString(fmt.Sprintf("### %s %s\n", block.PriorityLabel, block.Name))
		if block.Handle != "" {
			b.WriteString("handle: " + block.Handle + "\n")
		}
		if block.Content != "" {
			b.WriteString(block.Content + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Deferred P3 Handles\n")
	if len(prepared.Deferred) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, block := range prepared.Deferred {
			b.WriteString(fmt.Sprintf("- %s: %s (%s)\n", block.Name, block.Handle, block.Kind))
		}
	}
	b.WriteString("\n## Output Contract\n")
	for _, item := range prepared.OutputContract {
		b.WriteString("- " + item + "\n")
	}
	return b.String()
}

// normalizeTenantProfile 补齐 demo 默认值并拒绝跨租户 profile。
func normalizeTenantProfile(runtime RuntimeContext, profile TenantProfile) (TenantProfile, error) {
	if strings.TrimSpace(profile.TenantID) == "" {
		profile.TenantID = runtime.TenantID
	}
	if profile.TenantID != runtime.TenantID {
		return TenantProfile{}, fmt.Errorf("tenant profile belongs to %q, want %q", profile.TenantID, runtime.TenantID)
	}
	if profile.TenantName == "" {
		profile.TenantName = profile.TenantID
	}
	if len(profile.SafetyPolicies) == 0 {
		profile.SafetyPolicies = []string{
			"绝不泄露、推断或读取其他 tenant_id 的客户资料。",
			"涉及退款、合同、账单权限变更时先确认当前用户权限和工单证据。",
		}
	}
	return profile, nil
}

// buildSaaSContextItems 将客服资料按文档规则映射到 P0/P1/P2/P3。
func buildSaaSContextItems(runtime RuntimeContext, profile TenantProfile, message string, extraItems []ContextItem) ([]ContextItem, error) {
	items := []ContextItem{
		{
			Name:     "platform_safety_policy",
			Content:  strings.Join(profile.SafetyPolicies, "\n"),
			Priority: PriorityCritical,
			TenantID: runtime.TenantID,
			Kind:     "safety_policy",
		},
		{
			Name:     "runtime_identity",
			Content:  runtimeIdentityText(runtime, profile),
			Priority: PriorityCritical,
			TenantID: runtime.TenantID,
			Kind:     "runtime_schema",
		},
		{
			Name:     "current_customer_message",
			Content:  message,
			Priority: PriorityCritical,
			TenantID: runtime.TenantID,
			Kind:     "customer_message",
		},
	}

	if content := productConfigText(runtime, profile.ProductConfig); content != "" {
		items = append(items, ContextItem{Name: "tenant_product_config_snapshot", Content: content, Priority: PriorityImportant, TenantID: runtime.TenantID, Kind: "product_config"})
	}
	if content := ticketContextText(profile.CurrentTicket); content != "" {
		items = append(items, ContextItem{Name: "current_ticket_context", Content: content, Priority: PriorityImportant, TenantID: runtime.TenantID, Kind: "ticket_context"})
	}
	if content := recentTurnsText(profile.RecentTurns, 10); content != "" {
		items = append(items, ContextItem{Name: "recent_session_turns", Content: content, Priority: PriorityImportant, TenantID: runtime.TenantID, Kind: "conversation_history"})
	}
	if content := knowledgeIndexesText("产品手册目录索引", profile.ManualIndexes); content != "" {
		items = append(items, ContextItem{Name: "tenant_manual_index_summary", Content: content, Priority: PrioritySupporting, TenantID: runtime.TenantID, Kind: "manual_index", Handle: TenantHandle("manual_index", runtime.TenantID, "root")})
	}
	if content := knowledgeIndexesText("FAQ 摘要", profile.FAQSummaries); content != "" {
		items = append(items, ContextItem{Name: "tenant_faq_summary", Content: content, Priority: PrioritySupporting, TenantID: runtime.TenantID, Kind: "faq_summary", Handle: TenantHandle("faq_index", runtime.TenantID, "root")})
	}
	if content := knowledgeIndexesText("相似历史工单摘要", profile.SimilarTicketSummaries); content != "" {
		items = append(items, ContextItem{Name: "similar_ticket_cluster_summary", Content: content, Priority: PrioritySupporting, TenantID: runtime.TenantID, Kind: "similar_ticket_summary", Handle: TenantHandle("ticket_cluster", runtime.TenantID, "recent")})
	}
	for _, resource := range profile.DeferredResources {
		item, err := resourceToContextItem(runtime.TenantID, resource)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	items = append(items, extraItems...)
	return normalizeSaaSItems(runtime, items)
}

// normalizeSaaSItems 在进入模型前统一做租户归属和 P3 handle 校验。
func normalizeSaaSItems(runtime RuntimeContext, items []ContextItem) ([]ContextItem, error) {
	out := make([]ContextItem, 0, len(items))
	for _, item := range items {
		item.TenantID = strings.TrimSpace(item.TenantID)
		if item.TenantID == "" {
			item.TenantID = runtime.TenantID
		}
		if item.TenantID != runtime.TenantID {
			return nil, fmt.Errorf("context item %q belongs to tenant %q, want %q", item.Name, item.TenantID, runtime.TenantID)
		}
		if item.Handle != "" {
			if err := ValidateTenantHandle(runtime.TenantID, item.Handle); err != nil {
				return nil, fmt.Errorf("context item %q: %w", item.Name, err)
			}
		}
		if item.Priority == PriorityDeferrable && item.Handle == "" {
			return nil, fmt.Errorf("P3 context item %q must have tenant-scoped handle", item.Name)
		}
		out = append(out, item.Normalize())
	}
	return out, nil
}

// resourceToContextItem 将 P3 资料引用转换成只挂 handle 的上下文项。
func resourceToContextItem(defaultTenantID string, resource ResourceRef) (ContextItem, error) {
	tenantID := strings.TrimSpace(resource.TenantID)
	if tenantID == "" {
		tenantID = defaultTenantID
	}
	kind := strings.TrimSpace(resource.Kind)
	if kind == "" {
		kind = "resource"
	}
	handle := strings.TrimSpace(resource.Handle)
	if handle == "" {
		handle = TenantHandle(kind, tenantID, resource.ID)
	}
	if err := ValidateTenantHandle(tenantID, handle); err != nil {
		return ContextItem{}, err
	}
	name := strings.TrimSpace(resource.Title)
	if name == "" {
		name = resource.ID
	}
	return ContextItem{
		Name:     name,
		Content:  "",
		Priority: PriorityDeferrable,
		TenantID: tenantID,
		Handle:   handle,
		Kind:     kind,
	}, nil
}

// toContextBlocks 把内部 ContextItem 转为稳定 JSON 输出结构。
func toContextBlocks(items []ContextItem) []ContextBlock {
	blocks := make([]ContextBlock, 0, len(items))
	for _, item := range items {
		blocks = append(blocks, ContextBlock{
			Name:          item.Name,
			Priority:      item.Priority,
			PriorityLabel: item.Priority.String(),
			Content:       item.Content,
			Handle:        item.Handle,
			Kind:          item.Kind,
			TokenEstimate: item.TokenEstimate,
			TenantID:      item.TenantID,
			Metadata:      cloneStringMap(item.Metadata),
		})
	}
	return blocks
}

// runtimeIdentityText 把 schema 字段显式写入 P0，避免让模型从自然语言猜身份。
func runtimeIdentityText(runtime RuntimeContext, profile TenantProfile) string {
	lines := []string{
		"tenant_id=" + runtime.TenantID,
		"user_id=" + runtime.UserID,
		"session_id=" + runtime.SessionID,
		"tenant_name=" + profile.TenantName,
	}
	if runtime.ProjectID != "" {
		lines = append(lines, "project_id="+runtime.ProjectID)
	}
	if runtime.TicketID != "" {
		lines = append(lines, "ticket_id="+runtime.TicketID)
	}
	if runtime.ProductLine != "" {
		lines = append(lines, "product_line="+runtime.ProductLine)
	}
	if runtime.Plan != "" {
		lines = append(lines, "plan="+runtime.Plan)
	}
	return strings.Join(lines, "\n")
}

// productConfigText 渲染当前租户产品配置 snapshot，作为客服回答的 P1 语境。
func productConfigText(runtime RuntimeContext, config map[string]string) string {
	lines := []string{}
	if runtime.ProductLine != "" {
		lines = append(lines, "product_line="+runtime.ProductLine)
	}
	if runtime.Plan != "" {
		lines = append(lines, "plan="+runtime.Plan)
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, key+"="+config[key])
	}
	return strings.Join(lines, "\n")
}

// ticketContextText 渲染当前工单状态，保留 SLA、等级和故障信号。
func ticketContextText(ticket TicketContext) string {
	var lines []string
	if ticket.TicketID != "" {
		lines = append(lines, "ticket_id="+ticket.TicketID)
	}
	if ticket.Status != "" {
		lines = append(lines, "status="+ticket.Status)
	}
	if ticket.Severity != "" {
		lines = append(lines, "severity="+ticket.Severity)
	}
	if ticket.Summary != "" {
		lines = append(lines, "summary="+ticket.Summary)
	}
	for _, signal := range ticket.Signals {
		if strings.TrimSpace(signal) != "" {
			lines = append(lines, "signal="+strings.TrimSpace(signal))
		}
	}
	return strings.Join(lines, "\n")
}

// recentTurnsText 保留最近几轮连续会话，帮助 Agent 承接当前 session。
func recentTurnsText(turns []ConversationTurn, maxTurns int) string {
	if maxTurns <= 0 {
		maxTurns = 10
	}
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	var lines []string
	for _, turn := range turns {
		content := strings.TrimSpace(turn.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(turn.Role)
		if role == "" {
			role = "unknown"
		}
		lines = append(lines, role+": "+content)
	}
	return strings.Join(lines, "\n")
}

// knowledgeIndexesText 把 P2 目录、FAQ、相似工单压成可进一步回取的索引。
func knowledgeIndexesText(title string, indexes []KnowledgeIndex) string {
	if len(indexes) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, title)
	for _, index := range indexes {
		parts := []string{}
		if index.Title != "" {
			parts = append(parts, "title="+index.Title)
		}
		if index.Summary != "" {
			parts = append(parts, "summary="+index.Summary)
		}
		if index.Handle != "" {
			parts = append(parts, "handle="+index.Handle)
		}
		if len(parts) > 0 {
			lines = append(lines, "- "+strings.Join(parts, "; "))
		}
	}
	return strings.Join(lines, "\n")
}

// defaultOutputContract 固定客服 Agent 的回答边界和升级规则。
func defaultOutputContract() []string {
	return []string{
		"先说明本轮判断使用了哪些 P0/P1/P2 证据。",
		"如需 P3 原文，只能通过 read_tenant_handle 工具读取同 tenant_id 的 handle。",
		"不得猜测其他租户资料；handle 租户不一致时必须拒绝。",
		"输出客服处置建议、是否升级人工/二线、以及下一步需要读取的 handle。",
		"如果分诊 health 出现 warning，需要解释风险。",
	}
}
