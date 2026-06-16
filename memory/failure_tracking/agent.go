package failuretracking

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
	SearchFailuresToolName = "failure_tracking_search"
	RecordFailureToolName  = "failure_tracking_record"
)

// Toolset 把 FailureJournal 的 search/record 暴露给 Eino ADK。
type Toolset struct {
	Journal *FailureJournal
}

// AgentConfig 配置一个会使用 failure journal 的非 coding 场景 Agent。
type AgentConfig struct {
	Name          string
	Description   string
	Instruction   string
	Model         model.BaseChatModel
	Journal       *FailureJournal
	ExtraTools    []tool.BaseTool
	MaxIterations int
}

// Agent 用 Eino ADK 编排酒店恢复场景和 failure journal 工具。
type Agent struct {
	runner *adk.Runner
}

// AgentRequest 是业务 Agent 的自然语言输入，不预先拆分 context/error/fix。
type AgentRequest struct {
	Message string `json:"message"`
}

// AgentResponse 返回业务 Agent 对当前消息的处理结果。
type AgentResponse struct {
	Message string `json:"message"`
}

// NewSearchFailuresTool 创建“行动前搜索相似失败经验”的 ADK 工具。
func NewSearchFailuresTool(journal *FailureJournal) (tool.BaseTool, error) {
	if journal == nil {
		return nil, errors.New("failure journal is required")
	}
	toolset := Toolset{Journal: journal}
	return toolutils.InferTool[ConsultRequest, *ConsultResponse](
		SearchFailuresToolName,
		"在行动前查询相似失败经验。输入 current_context 和可选 top_k，返回 score>=0.7 的经验。",
		toolset.Search,
	)
}

// NewRecordFailureTool 创建“沉淀当前失败经验”的 ADK 工具。
func NewRecordFailureTool(journal *FailureJournal) (tool.BaseTool, error) {
	if journal == nil {
		return nil, errors.New("failure journal is required")
	}
	toolset := Toolset{Journal: journal}
	return toolutils.InferTool[RecordRequest, *RecordResponse](
		RecordFailureToolName,
		"把当前失败沉淀为 append-only 经验。仅当 context、error、fix 都明确时调用。",
		toolset.Record,
	)
}

// Search 是 search tool 的执行边界，只负责读取历史失败经验。
func (t Toolset) Search(ctx context.Context, req ConsultRequest) (*ConsultResponse, error) {
	if t.Journal == nil {
		return nil, errors.New("failure journal is required")
	}
	return t.Journal.Consult(ctx, req)
}

// Record 是 record tool 的执行边界，只负责追加一条新的失败经验。
func (t Toolset) Record(ctx context.Context, req RecordRequest) (*RecordResponse, error) {
	if t.Journal == nil {
		return nil, errors.New("failure journal is required")
	}
	return t.Journal.Record(ctx, req)
}

// NewHotelRecoveryAgent 创建酒店前台客诉恢复 Agent，用非 coding 场景演示 failure tracking。
func NewHotelRecoveryAgent(ctx context.Context, config AgentConfig) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("agent model is required")
	}
	if config.Journal == nil {
		return nil, errors.New("failure journal is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	searchTool, err := NewSearchFailuresTool(config.Journal)
	if err != nil {
		return nil, err
	}
	recordTool, err := NewRecordFailureTool(config.Journal)
	if err != nil {
		return nil, err
	}
	tools := []tool.BaseTool{searchTool, recordTool}
	tools = append(tools, config.ExtraTools...)

	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "hotel_ops_failure_tracking_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Hotel operations agent that autonomously searches and records operational failure lessons."
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = DefaultHotelRecoveryInstruction()
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
		return nil, fmt.Errorf("create hotel recovery agent: %w", err)
	}
	return &Agent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// Query 处理一条自然语言业务消息，search/record 是否调用由 LLM 根据工具约束自主决定。
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
		"请处理下面这条酒店运营消息。",
		"你必须自己决定是否以及如何调用工具，不要等待外部代码替你查询或记录。",
		"每轮行动前必须先调用 " + SearchFailuresToolName + "，current_context 用当前消息中的自然语言事实概括。",
		"如果 search 没有返回可复用经验，并且当前消息包含已验证的失败现象、失败原因/错误类别和最终修复动作，必须调用 " + RecordFailureToolName + " 沉淀新经验。",
		"如果 search 返回了相似经验，必须优先复用其中的 fix/heuristic；除非当前消息出现新的已验证差异，不要重复 record。",
		"最终答复请给出：已检索到的历史经验是否命中、当前处置建议、是否沉淀了新经验。",
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

// DefaultHotelRecoveryInstruction 定义酒店恢复 Agent 的工具顺序和记录边界。
func DefaultHotelRecoveryInstruction() string {
	return strings.Join([]string{
		"你是一个酒店运营 Agent，负责处理客诉、房态、值班经理升级和内部复盘。",
		"每次回答前必须先调用 failure_tracking_search，确认是否有相似失败经验。",
		"如果当前消息是已解决复盘，并且包含失败现象、错误类别或失败原因、最终修复动作，这就是可沉淀的新经验；当 search 没有命中时必须调用 failure_tracking_record。",
		"如果当前消息是新客诉或现场处置请求，必须先复用 search 返回的历史 fix/heuristic，再给处置建议。",
		"不要记录猜测、未验证方案、单纯情绪或没有修复动作的事件。",
		"回答要面向酒店运营人员：说明是否命中历史经验、建议动作、升级边界、以及是否新记录了经验。",
	}, "\n")
}
