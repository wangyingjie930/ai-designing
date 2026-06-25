package triage

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestContextTriageProtectsErrorsAndDefersP3 覆盖 pattern.py 的核心排序和错误保护逻辑。
func TestContextTriageProtectsErrorsAndDefersP3(t *testing.T) {
	engine := NewContextTriage(TriageConfig{Budget: 12, SupportingMaxTokens: 10})
	result := engine.Triage([]ContextItem{
		{Name: "P0 identity", Content: "tenant_id=acme user_id=u1", Priority: PriorityCritical},
		{Name: "P1 product", Content: strings.Repeat("product config ", 20), Priority: PriorityImportant},
		{Name: "P1 timeout stack", Content: "Traceback timeout in billing job", Priority: PriorityImportant, IsError: true},
		{Name: "P2 old tickets", Content: strings.Repeat("历史工单摘要，关键证据 handle=ticket://acme/T-1\n", 20), Priority: PrioritySupporting, Handle: TenantHandle("ticket_cluster", "acme", "recent")},
		{Name: "P3 full manual", Priority: PriorityDeferrable, Handle: TenantHandle("manual_section", "acme", "billing")},
	})

	if len(result.Deferred) != 1 {
		t.Fatalf("deferred len = %d, want 1", len(result.Deferred))
	}
	if result.Deferred[0].Content != "" {
		t.Fatalf("P3 content should not be preloaded: %q", result.Deferred[0].Content)
	}
	if !containsName(result.Selected, "P1 timeout stack") {
		t.Fatalf("protected error stack was not selected: %+v", result.Decision)
	}
	if result.Decision.TokensUsed <= result.Decision.Budget {
		t.Fatalf("expected protected error to allow budget overshoot, used=%d budget=%d", result.Decision.TokensUsed, result.Decision.Budget)
	}
	if len(engine.Decisions()) != 1 {
		t.Fatalf("decision history len = %d, want 1", len(engine.Decisions()))
	}
}

// TestPrepareTriageContextBuildsTenantScopedHandles 验证 SaaS planner 的 P0/P1/P2/P3 映射。
func TestPrepareTriageContextBuildsTenantScopedHandles(t *testing.T) {
	request, _ := DemoScenario(RuntimeContext{TenantID: "acme", UserID: "u1", SessionID: "s1"}, "发票和用量不一致怎么办？", 900)
	prepared, err := NewSaaSTriagePlanner(TriageConfig{}).PrepareTriageContext(context.Background(), request)
	if err != nil {
		t.Fatalf("PrepareTriageContext() error = %v", err)
	}
	if !strings.Contains(prepared.Prompt, "tenant_id: acme") {
		t.Fatalf("prompt missing tenant identity: %s", prepared.Prompt)
	}
	if !containsBlock(prepared.Selected, "current_customer_message") {
		t.Fatalf("current customer message must be selected: %+v", prepared.Selected)
	}
	runtimeBlock, ok := findBlock(prepared.Selected, "runtime_identity")
	if !ok {
		t.Fatalf("runtime identity must be selected: %+v", prepared.Selected)
	}
	if runtimeBlock.Metadata[metadataContextRule] != "p0_runtime_guardrails" {
		t.Fatalf("runtime identity missing policy trace: %+v", runtimeBlock.Metadata)
	}
	if len(prepared.Deferred) == 0 {
		t.Fatal("expected P3 handles to be deferred")
	}
	for _, block := range prepared.Deferred {
		if _, ok := TenantIDFromHandle(block.Handle); !ok {
			t.Fatalf("deferred handle is not tenant-scoped: %+v", block)
		}
		if block.Content != "" {
			t.Fatalf("deferred block should not include content: %+v", block)
		}
	}
}

// TestPrepareTriageContextUsesCollectorAndCustomPolicy 验证生产采集器和业务策略可以替换默认 demo 映射。
func TestPrepareTriageContextUsesCollectorAndCustomPolicy(t *testing.T) {
	collector := &fakeSaaSContextCollector{
		snapshot: SaaSContextSnapshot{
			Tenant: TenantProfile{
				TenantID:   "acme",
				TenantName: "Acme From Collector",
			},
			ExtraItems: []ContextItem{
				{
					Name:     "collector_alert",
					Content:  "payment timeout on checkout shard",
					Priority: PriorityImportant,
					TenantID: "acme",
					Kind:     "alert",
					Metadata: map[string]string{"source": "alert_store"},
				},
			},
		},
	}
	customPolicy := &SaaSContextPolicy{
		Name: "custom_policy",
		Rules: []SaaSContextRule{
			{
				Name: "custom_runtime_identity",
				Build: func(input SaaSContextBuildInput) ([]ContextItem, error) {
					return []ContextItem{
						{
							Name:     "custom_collected_identity",
							Content:  "tenant_id=" + input.Runtime.TenantID + "\ntenant_name=" + input.Tenant.TenantName,
							Priority: PriorityCritical,
							TenantID: input.Runtime.TenantID,
							Kind:     "custom_runtime",
							Metadata: map[string]string{metadataPriorityReason: "custom policy uses collector snapshot"},
						},
					}, nil
				},
			},
		},
	}
	planner := NewSaaSTriagePlannerWithConfig(SaaSTriagePlannerConfig{
		TriageConfig:  TriageConfig{Budget: 1000},
		Collector:     collector,
		ContextPolicy: customPolicy,
	})

	prepared, err := planner.PrepareTriageContext(context.Background(), TriageRequest{
		Runtime: RuntimeContext{TenantID: "acme", UserID: "u1", SessionID: "s1"},
		Tenant:  TenantProfile{TenantID: "globex", TenantName: "Ignored Request Tenant"},
		Message: "支付失败了，帮我看一下。",
	})
	if err != nil {
		t.Fatalf("PrepareTriageContext() error = %v", err)
	}
	if !collector.called {
		t.Fatal("collector was not called")
	}
	identity, ok := findBlock(prepared.Selected, "custom_collected_identity")
	if !ok {
		t.Fatalf("custom policy item missing: %+v", prepared.Selected)
	}
	if !strings.Contains(identity.Content, "Acme From Collector") {
		t.Fatalf("custom item did not use collector tenant: %+v", identity)
	}
	if identity.Metadata[metadataContextPolicy] != "custom_policy" {
		t.Fatalf("custom item missing policy metadata: %+v", identity.Metadata)
	}
	if !containsBlock(prepared.Selected, "collector_alert") {
		t.Fatalf("collector extra item missing: %+v", prepared.Selected)
	}
}

// TestPrepareTriageContextRejectsCrossTenantResource 确认租户隔离发生在模型调用之前。
func TestPrepareTriageContextRejectsCrossTenantResource(t *testing.T) {
	request, _ := DemoScenario(RuntimeContext{TenantID: "acme", UserID: "u1", SessionID: "s1"}, "需要查手册", 900)
	request.ExtraItems = append(request.ExtraItems, ContextItem{
		Name:     "evil manual",
		Priority: PriorityDeferrable,
		TenantID: "globex",
		Handle:   TenantHandle("manual_section", "globex", "billing"),
	})
	_, err := NewSaaSTriagePlanner(TriageConfig{}).PrepareTriageContext(context.Background(), request)
	if err == nil {
		t.Fatal("expected cross-tenant resource to be rejected")
	}
	if !strings.Contains(err.Error(), "globex") {
		t.Fatalf("error should mention wrong tenant: %v", err)
	}
}

// TestReadTenantHandleRejectsWrongTenant 验证 P3 回取工具同样执行租户校验。
func TestReadTenantHandleRejectsWrongTenant(t *testing.T) {
	handle := TenantHandle("manual_section", "acme", "billing")
	store := NewInMemoryResourceStore(ResourceDocument{
		TenantID: "acme",
		Handle:   handle,
		Content:  "billing doc",
	})
	if _, err := store.ReadTenantHandle(context.Background(), ReadTenantHandleRequest{TenantID: "globex", Handle: handle}); err == nil {
		t.Fatal("expected wrong tenant read to fail")
	}
	doc, err := store.ReadTenantHandle(context.Background(), ReadTenantHandleRequest{TenantID: "acme", Handle: handle})
	if err != nil {
		t.Fatalf("ReadTenantHandle() error = %v", err)
	}
	if doc.Content != "billing doc" {
		t.Fatalf("doc content = %q", doc.Content)
	}
}

// TestSaaSTriageAgentRunsWithFakeModel 确认 Eino ADK 封装和 runner 路径可执行。
func TestSaaSTriageAgentRunsWithFakeModel(t *testing.T) {
	request, docs := DemoScenario(RuntimeContext{TenantID: "acme", UserID: "u1", SessionID: "s1"}, "请帮我分诊", 900)
	agent, err := NewSaaSTriageAgent(context.Background(), &fakeChatModel{responses: []string{"已完成分诊：先核对账单快照，再必要时读取 P3 handle。"}}, AgentConfig{
		ResourceStore: NewInMemoryResourceStore(docs...),
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewSaaSTriageAgent() error = %v", err)
	}
	answer, err := RunSaaSTriageWithAgent(context.Background(), agent, request)
	if err != nil {
		t.Fatalf("RunSaaSTriageWithAgent() error = %v", err)
	}
	if !strings.Contains(answer, "分诊") {
		t.Fatalf("answer = %q", answer)
	}
}

type fakeChatModel struct {
	mu        sync.Mutex
	inputs    [][]*schema.Message
	responses []string
}

// fakeSaaSContextCollector 是单测用的生产采集器替身，用来证明 planner 不依赖请求内联资料。
type fakeSaaSContextCollector struct {
	snapshot SaaSContextSnapshot
	called   bool
}

// CollectSaaSContext 返回预设快照，并记录调用行为。
func (c *fakeSaaSContextCollector) CollectSaaSContext(_ context.Context, _ TriageRequest) (SaaSContextSnapshot, error) {
	c.called = true
	return c.snapshot, nil
}

// Generate 记录模型输入并返回预设回复。
func (m *fakeChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	content := "已记录。"
	if len(m.responses) > 0 {
		content = m.responses[0]
		m.responses = m.responses[1:]
	}
	return schema.AssistantMessage(content, nil), nil
}

// Stream 返回单条流式消息，当前测试不依赖流模式。
func (m *fakeChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream response", nil)}), nil
}

// containsName 判断 selected items 中是否包含指定名称。
func containsName(items []ContextItem, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

// containsBlock 判断 prompt blocks 中是否包含指定名称。
func containsBlock(blocks []ContextBlock, name string) bool {
	_, ok := findBlock(blocks, name)
	return ok
}

// findBlock 返回指定名称的上下文块，方便测试读取 metadata 和内容。
func findBlock(blocks []ContextBlock, name string) (ContextBlock, bool) {
	for _, block := range blocks {
		if block.Name == name {
			return block, true
		}
	}
	return ContextBlock{}, false
}
