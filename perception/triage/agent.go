package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	PrepareTriageContextToolName = "prepare_saas_triage_context"
	ReadTenantHandleToolName     = "read_tenant_handle"
)

// AgentConfig 配置 Eino ADK 多租户 SaaS 客服分诊 Agent。
type AgentConfig struct {
	Name             string
	Description      string
	Instruction      string
	Planner          *SaaSTriagePlanner
	TriageConfig     TriageConfig
	ContextCollector SaaSContextCollector
	ContextPolicy    *SaaSContextPolicy
	ResourceStore    ResourceStore
	ExtraTools       []tool.BaseTool
	MaxIterations    int
}

// NewPrepareTriageContextTool 暴露 deterministic context triage 流程给 ADK。
func NewPrepareTriageContextTool(planner *SaaSTriagePlanner) (tool.BaseTool, error) {
	if planner == nil {
		planner = NewSaaSTriagePlanner(TriageConfig{})
	}
	return toolutils.InferTool[TriageRequest, *PreparedTriageContext](
		PrepareTriageContextToolName,
		"Classify SaaS customer-support context into P0/P1/P2/P3, enforce tenant identity, keep P3 as tenant-scoped handles, and return triage trace.",
		planner.PrepareTriageContext,
	)
}

// NewReadTenantHandleTool 暴露 P3 handle 回取能力，并在工具层校验租户归属。
func NewReadTenantHandleTool(store ResourceStore) (tool.BaseTool, error) {
	if store == nil {
		return nil, fmt.Errorf("resource store is required")
	}
	return toolutils.InferTool[ReadTenantHandleRequest, *ResourceDocument](
		ReadTenantHandleToolName,
		"Read one P3 resource handle after validating that handle tenant matches the current tenant_id.",
		store.ReadTenantHandle,
	)
}

// NewSaaSTriageAgent 创建使用 Eino ADK ChatModelAgent 的多租户客服 Agent。
func NewSaaSTriageAgent(ctx context.Context, chatModel model.BaseChatModel, config AgentConfig) (*adk.ChatModelAgent, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}

	planner := config.Planner
	if planner == nil {
		planner = NewSaaSTriagePlannerWithConfig(SaaSTriagePlannerConfig{
			TriageConfig:  config.TriageConfig,
			Collector:     config.ContextCollector,
			ContextPolicy: config.ContextPolicy,
		})
	}
	prepareTool, err := NewPrepareTriageContextTool(planner)
	if err != nil {
		return nil, err
	}
	tools := make([]tool.BaseTool, 0, 2+len(config.ExtraTools))
	tools = append(tools, prepareTool)
	if config.ResourceStore != nil {
		readTool, err := NewReadTenantHandleTool(config.ResourceStore)
		if err != nil {
			return nil, err
		}
		tools = append(tools, readTool)
	}
	tools = append(tools, config.ExtraTools...)

	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "saas_support_context_triage_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Multi-tenant SaaS customer-support agent with Context Triage, tenant-scoped P3 handles, and structured trace."
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = DefaultAgentInstruction()
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   description,
		Instruction:   instruction,
		Model:         chatModel,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
			},
		},
	})
}

// RunSaaSTriageWithAgent 运行 ADK Agent，并返回最终客服回复。
func RunSaaSTriageWithAgent(ctx context.Context, agent adk.Agent, request TriageRequest) (string, error) {
	if agent == nil {
		return "", fmt.Errorf("agent is required")
	}
	payload, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", err
	}
	query := strings.Join([]string{
		"请处理下面这个多租户 SaaS 客服请求。",
		"必须先调用 " + PrepareTriageContextToolName + "，基于工具返回的 selected/deferred/health/decision 再回答。",
		"如果需要读取 P3 原文，只能调用 " + ReadTenantHandleToolName + "，并传入当前 tenant_id。",
		"",
		string(payload),
	}, "\n")

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, query)
	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return "", err
		}
		if message != nil && strings.TrimSpace(message.Content) != "" {
			final = message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		return "", fmt.Errorf("agent finished without assistant output")
	}
	return final, nil
}

// DefaultAgentInstruction 定义客服 Agent 的工具顺序、租户隔离和回答契约。
func DefaultAgentInstruction() string {
	return strings.Join([]string{
		"你是一个多租户 B2B SaaS 客服 Agent。",
		"每次回答前必须先调用 prepare_saas_triage_context；不要绕过 deterministic triage 直接使用用户提供的长材料。",
		"P0/P1 是当前推理必须依赖的证据；P2 是摘要和索引；P3 只能作为 handle 暴露，默认不预加载原文。",
		"tenant_id、user_id、session_id 是 P0 硬约束。任何资料或 handle 的 tenant_id 不一致时必须拒绝使用，不要交给模型自己猜。",
		"需要展开 P3 时，只能调用 read_tenant_handle，并且 tenant_id 必须等于当前 runtime.tenant_id。",
		"回答时说明：分诊结论、已使用证据、客服处置建议、是否升级人工/二线、下一步建议读取的 handle。",
		"如果 triage health 有 warning，要说明可能影响；不要编造没有进入 selected 或工具返回中的事实。",
	}, "\n")
}
