package hierarchical

import (
	"context"
	"encoding/json"
	"errors"
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
	AddMemoryToolName         = "hierarchical_add_memory"
	RetrieveMemoryToolName    = "hierarchical_retrieve_memory"
	ContextMemoryToolName     = "hierarchical_memory_context"
	ConsolidateMemoryToolName = "hierarchical_consolidate_memory"
)

// Toolset 把 HierarchicalMemory 的 add/retrieve/context/consolidate 暴露给 Eino ADK。
type Toolset struct {
	Memory *HierarchicalMemory
}

// AgentConfig 配置一个使用三层记忆的非 coding 场景 Agent。
type AgentConfig struct {
	Name          string
	Description   string
	Instruction   string
	Model         model.BaseChatModel
	Memory        *HierarchicalMemory
	ExtraTools    []tool.BaseTool
	MaxIterations int
}

// AgentRequest 是业务 Agent 的自然语言输入，场景事实必须从消息进入而不是写死在代码里。
type AgentRequest struct {
	Message string `json:"message"`
}

// AgentResponse 返回 ADK Agent 的最终中文答复。
type AgentResponse struct {
	Message string `json:"message"`
}

// Agent 用 Eino ADK ChatModelAgent 编排旅行规划场景和 hierarchical memory 工具。
type Agent struct {
	runner *adk.Runner
}

// NewAddMemoryTool 创建“写入 working memory”的 ADK 工具。
func NewAddMemoryTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[AddRequest, *AddResponse](
		AddMemoryToolName,
		"把用户给出的稳定偏好、约束、行程事实或本轮反思写入 working memory。source 使用 user/tool/reflection/file 等真实来源。",
		toolset.AddMemory,
	)
}

// NewRetrieveMemoryTool 创建“从 SQLite long-term 召回并提升到 working”的 ADK 工具。
func NewRetrieveMemoryTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[RetrieveRequest, *RetrieveResponse](
		RetrieveMemoryToolName,
		"根据自然语言 query 从 SQLite long-term memory 召回相关信息，并自动提升到 working memory。",
		toolset.RetrieveMemory,
	)
}

// NewContextMemoryTool 创建“查看当前 working 上下文”的 ADK 工具。
func NewContextMemoryTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[ContextRequest, *ToolContextResponse](
		ContextMemoryToolName,
		"读取当前可见 working memory 快照和 token 预算。session 是内部淘汰缓冲，不会通过工具暴露。",
		toolset.ContextMemory,
	)
}

// NewConsolidateMemoryTool 创建“会话收束并持久化重要记忆”的 ADK 工具。
func NewConsolidateMemoryTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[ConsolidateRequest, *ConsolidateResponse](
		ConsolidateMemoryToolName,
		"把 importance > 0.6 或 source=reflection 的记忆持久化到 SQLite long-term memory。",
		toolset.ConsolidateMemory,
	)
}

// AddMemory 是 add tool 的执行边界，只负责把模型选择的事实写入三层记忆。
func (t Toolset) AddMemory(ctx context.Context, req AddRequest) (*AddResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.Add(ctx, req)
}

// RetrieveMemory 是 retrieve tool 的执行边界，只负责长期召回和提升。
func (t Toolset) RetrieveMemory(ctx context.Context, req RetrieveRequest) (*RetrieveResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.Retrieve(ctx, req)
}

// ContextMemory 是 context tool 的执行边界，只返回 LLM 可见的 working 记忆。
func (t Toolset) ContextMemory(context.Context, ContextRequest) (*ToolContextResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	snapshot := t.Memory.Context()
	return &ToolContextResponse{
		Scope:             snapshot.Scope,
		Working:           snapshot.Working,
		WorkingTokenCount: snapshot.WorkingTokenCount,
		WorkingBudget:     snapshot.WorkingBudget,
	}, nil
}

// ConsolidateMemory 是 consolidate tool 的执行边界，触发真实 embedding 写入 SQLite。
func (t Toolset) ConsolidateMemory(ctx context.Context, _ ConsolidateRequest) (*ConsolidateResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.Consolidate(ctx)
}

// NewTravelPlanningAgent 创建旅行/家庭行程规划 Agent，用非 coding 场景演示三层记忆。
func NewTravelPlanningAgent(ctx context.Context, config AgentConfig) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("agent model is required")
	}
	if config.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	tools, err := buildHierarchicalTools(config.Memory, config.ExtraTools)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "travel_planning_hierarchical_memory_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Travel planning agent using working/session/long-term memory backed by SQLite."
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = DefaultTravelPlanningInstruction()
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 10
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   description,
		Instruction:   instruction,
		Model:         config.Model,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create travel planning agent: %w", err)
	}
	return &Agent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// Query 处理一条自然语言行程规划消息，工具调用由模型根据当前上下文自主决定。
func (a *Agent) Query(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	if a == nil || a.runner == nil {
		return nil, errors.New("agent is not initialized")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, errors.New("agent request message is empty")
	}
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	query := strings.Join([]string{
		"请处理下面这条旅行/家庭行程规划消息。",
		"你必须自己决定如何调用工具，不要等待外部代码替你喂答案。",
		"回答前先调用 " + RetrieveMemoryToolName + " 召回相关长期偏好或历史约束，再调用 " + ContextMemoryToolName + " 查看可见 working memory。",
		"如果消息中出现稳定偏好、硬约束、预算边界、同行人限制、已确认决策或你的关键反思，调用 " + AddMemoryToolName + " 写入。",
		"当本轮信息已经足够重要或需要跨会话复用时，调用 " + ConsolidateMemoryToolName + " 持久化。",
		"",
		string(payload),
	}, "\n")

	iter := a.runner.Query(ctx, query)
	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, err
		}
		if message != nil && strings.TrimSpace(message.Content) != "" {
			final = message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		return nil, errors.New("agent finished without assistant output")
	}
	return &AgentResponse{Message: final}, nil
}

// buildHierarchicalTools 按固定工具边界组装 ADK tools，并允许调用方追加外部工具。
func buildHierarchicalTools(memory *HierarchicalMemory, extra []tool.BaseTool) ([]tool.BaseTool, error) {
	addTool, err := NewAddMemoryTool(memory)
	if err != nil {
		return nil, err
	}
	retrieveTool, err := NewRetrieveMemoryTool(memory)
	if err != nil {
		return nil, err
	}
	contextTool, err := NewContextMemoryTool(memory)
	if err != nil {
		return nil, err
	}
	consolidateTool, err := NewConsolidateMemoryTool(memory)
	if err != nil {
		return nil, err
	}
	tools := []tool.BaseTool{retrieveTool, contextTool, addTool, consolidateTool}
	tools = append(tools, extra...)
	return tools, nil
}

// DefaultTravelPlanningInstruction 定义非 coding 场景的工具使用规则和记忆边界。
func DefaultTravelPlanningInstruction() string {
	return strings.Join([]string{
		"你是一个中文旅行规划助手，目标是把用户零散的偏好、约束和历史决策变成可执行建议。",
		"每轮先召回长期记忆，再查看可见 working memory；只能把工具返回的信息当作历史依据，不要编造不存在的偏好。",
		"需要保存的内容包括：长期偏好、预算和时间硬约束、同行人限制、已确认选择、被否定的方案、跨会话仍有价值的反思。",
		"不要保存一次性寒暄、明显临时的问题、没有复用价值的普通措辞。",
		"给建议时要说明依据来自本轮消息还是历史记忆；如果信息不足，列出需要用户补充的最小问题。",
		"当本轮产生了重要约束或反思，回答结束前要 consolidation，让 SQLite long-term memory 可供下次召回。",
	}, "\n")
}
