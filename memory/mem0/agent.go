package mem0

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
	AddMemoryToolName    = "mem0_add_memory"
	SearchMemoryToolName = "mem0_search_memory"
)

// Toolset 把 Memory 的 add/search 暴露给 Eino ADK 工具节点。
type Toolset struct {
	Memory *Memory
}

// AgentConfig 配置一个会主动搜索和写入长期记忆的简单 ADK Agent。
type AgentConfig struct {
	Name          string
	Description   string
	Instruction   string
	Model         model.BaseChatModel
	Memory        *Memory
	ExtraTools    []tool.BaseTool
	MaxIterations int
}

// AgentRequest 是 demo Agent 每次处理用户消息时的业务输入。
type AgentRequest struct {
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	Message string `json:"message"`
}

// AgentResponse 返回 ADK 最终回复文本。
type AgentResponse struct {
	Message string `json:"message"`
}

// Agent 用 Eino ADK ChatModelAgent 编排 mem0 search/add 工具调用。
type Agent struct {
	runner *adk.Runner
}

// NewAddMemoryTool 创建供 ADK 调用的 mem0 add 工具。
func NewAddMemoryTool(memory *Memory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[AddRequest, *AddResponse](
		AddMemoryToolName,
		"把用户偏好、事实、稳定约束或本轮决策写入本地 mem0 SQLite 记忆。必须传 user_id/agent_id/run_id 至少一个作用域 ID。",
		toolset.AddMemory,
	)
}

// NewSearchMemoryTool 创建供 ADK 调用的 mem0 search 工具。
func NewSearchMemoryTool(memory *Memory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[SearchRequest, *SearchResponse](
		SearchMemoryToolName,
		"按自然语言 query 从本地 mem0 SQLite 记忆中召回相关事实。filters 必须包含 user_id/agent_id/run_id 至少一个作用域 ID。",
		toolset.SearchMemory,
	)
}

// AddMemory 作为 ADK tool 的实际执行函数，边界内只做记忆写入。
func (t Toolset) AddMemory(ctx context.Context, req AddRequest) (*AddResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("memory is required")
	}
	return t.Memory.Add(ctx, req)
}

// SearchMemory 作为 ADK tool 的实际执行函数，边界内只做记忆召回。
func (t Toolset) SearchMemory(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("memory is required")
	}
	return t.Memory.Search(ctx, req)
}

// NewAgent 创建会先查记忆、再回答、最后按需写记忆的简单 Eino ADK Agent。
func NewAgent(ctx context.Context, config AgentConfig) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("agent model is required")
	}
	if config.Memory == nil {
		return nil, errors.New("memory is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	searchTool, err := NewSearchMemoryTool(config.Memory)
	if err != nil {
		return nil, err
	}
	addTool, err := NewAddMemoryTool(config.Memory)
	if err != nil {
		return nil, err
	}
	tools := []tool.BaseTool{searchTool, addTool}
	tools = append(tools, config.ExtraTools...)
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "mem0_sqlite_memory_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Simple Eino ADK agent using mem0-like add/search tools backed by SQLite."
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = DefaultAgentInstruction()
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
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
		return nil, fmt.Errorf("create mem0 agent: %w", err)
	}
	return &Agent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// Query 运行 ADK Agent，并返回最后一条 assistant 文本。
func (a *Agent) Query(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	if a == nil || a.runner == nil {
		return nil, errors.New("agent is not initialized")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, errors.New("message is empty")
	}
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	query := strings.Join([]string{
		"请处理下面这条用户消息。",
		"必须先调用 " + SearchMemoryToolName + "，filters 使用请求里的 user_id/agent_id/run_id。",
		"回答用户后，如果本轮出现可长期复用的偏好、事实、约束或明确决策，必须调用 " + AddMemoryToolName + " 保存本轮 user/assistant 关键信息。",
		"保存时 messages 至少包含当前 user 消息；如果你已经形成稳定建议，也可以加入 assistant 摘要。",
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

// DefaultAgentInstruction 定义 mem0 demo Agent 的工具顺序和记忆写入边界。
func DefaultAgentInstruction() string {
	return strings.Join([]string{
		"你是一个带长期记忆的中文助手。",
		"每次回答前必须先调用 mem0_search_memory，按当前 user_id/agent_id/run_id 查询相关长期记忆。",
		"回答时只能把工具返回的记忆当作背景，不要编造不存在的历史。",
		"当用户表达稳定偏好、长期事实、持续目标、明确禁忌或可复用决策时，回答完成前必须调用 mem0_add_memory 保存。",
		"写入记忆时使用 infer=true，让 mem0 add 流程自己从 messages 中抽取结构化 memory。",
		"不要保存一次性寒暄、纯临时问题或明显不稳定的信息。",
	}, "\n")
}
