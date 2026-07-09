package researchswarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// RoleAgentConfig 配置一个 teammate worker 内部的 Eino ADK ChatModelAgent。
type RoleAgentConfig struct {
	Store         *Store
	SearchClient  SearchClient
	TeamName      string
	AgentName     string
	Role          AgentRole
	Model         model.BaseChatModel
	MaxIterations int
}

// NewRoleAgent 创建按角色限制工具面的 Eino ADK Agent。
func NewRoleAgent(ctx context.Context, config RoleAgentConfig) (adk.Agent, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	if config.SearchClient == nil {
		config.SearchClient = NewFakeSearchClient()
	}
	if config.Role == "" {
		config.Role = roleFromAgentName(config.AgentName)
	}
	agentID := AgentID(config.AgentName, config.TeamName)
	tools, err := NewRoleTools(ctx, ToolConfig{
		Store:        config.Store,
		SearchClient: config.SearchClient,
		TeamName:     config.TeamName,
		AgentID:      agentID,
		Role:         config.Role,
	})
	if err != nil {
		return nil, err
	}
	chatModel := config.Model
	if chatModel == nil {
		chatModel = &deterministicRoleModel{role: config.Role}
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          agentID,
		Description:   "External-process research swarm teammate.",
		Instruction:   roleInstruction(config.Role, config.TeamName),
		Model:         chatModel,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
}

func roleInstruction(role AgentRole, teamName string) string {
	common := []string{
		"你是调查报告 swarm 中的一个外部进程 teammate。",
		"普通 assistant 文本不会被其他 teammate 看到；需要跨 agent 通信时必须调用 send_message。",
		"所有证据必须通过 source card 或 report section 落到 SQLite 后再被后续角色引用。",
		"当前 team_name 是 " + teamName + "。",
	}
	switch role {
	case RoleSearcher:
		return strings.Join(append(common,
			"你的职责是调用 web_search，挑选有价值的结果并调用 save_source_card。",
			"保存资料卡后更新当前 task 状态；不要默认通知 analyst。"), "\n")
	case RoleAnalyst:
		return strings.Join(append(common,
			"你的职责是读取 source cards，归纳事实、冲突点、证据强弱，并保存分析章节。",
			"分析完成后更新当前 task 状态；不要默认通知 writer。"), "\n")
	case RoleWriter:
		return strings.Join(append(common,
			"你的职责是读取 source cards 和分析章节，输出带 source id 引用的最终调查报告章节。"), "\n")
	default:
		return strings.Join(common, "\n")
	}
}

// deterministicRoleModel 是本地 demo 默认模型：走真实 ADK tool loop，但不依赖外部 LLM。
type deterministicRoleModel struct {
	role  AgentRole
	calls int
}

// Generate 通过固定工具调用序列模拟各角色的模型决策。
func (m *deterministicRoleModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	payload := extractTaskPayload(input)
	investigation := firstNonEmpty(payload.Prompt, payload.Topic, "调查报告主题")
	switch m.role {
	case RoleSearcher:
		return m.searcherMessage(payload.TaskID, investigation)
	case RoleAnalyst:
		return m.analystMessage(payload.TaskID, investigation)
	case RoleWriter:
		return m.writerMessage(payload.TaskID, investigation)
	default:
		return schema.AssistantMessage("角色未配置，未执行调查动作。", nil), nil
	}
}

// Stream 当前 demo 不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *deterministicRoleModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

func (m *deterministicRoleModel) searcherMessage(taskID int64, investigation string) (*schema.Message, error) {
	switch m.calls {
	case 1:
		return toolCallMessage("call_web_search", WebSearchToolName, toolArgs(map[string]any{
			"query":    investigation,
			"top_k":    2,
			"language": "zh",
		})), nil
	case 2:
		return toolCallMessage("call_save_source_1", SaveSourceCardToolName, toolArgs(map[string]any{
			"query":       investigation,
			"title":       investigation + "：外部资料进入报告前需要证据卡片化",
			"url":         "https://example.com/research/source-cards",
			"snippet":     "围绕“" + investigation + "”的搜索结果先沉淀为 source cards，可以让分析员和撰稿员通过证据 ID 引用事实，减少上下文口口相传导致的漂移。",
			"source":      "fake_search",
			"credibility": "medium",
		})), nil
	case 3:
		return toolCallMessage("call_save_source_2", SaveSourceCardToolName, toolArgs(map[string]any{
			"query":       investigation,
			"title":       investigation + "：多角色审查能降低单一 Agent 的检索误用风险",
			"url":         "https://example.com/research/multi-agent-review",
			"snippet":     "搜索员、分析员和撰稿员分离后，搜索、事实归类和表达生成由不同角色完成，和“" + investigation + "”相关的冲突事实更容易被标记。",
			"source":      "fake_search",
			"credibility": "medium",
		})), nil
	case 4:
		if taskID > 0 {
			return toolCallMessage("call_update_task", UpdateTaskToolName, fmt.Sprintf(`{"task_id":%d,"status":"completed","result":"saved source cards"}`, taskID)), nil
		}
	}
	return schema.AssistantMessage("搜索员已保存资料卡并通知分析员。", nil), nil
}

func (m *deterministicRoleModel) analystMessage(taskID int64, investigation string) (*schema.Message, error) {
	switch m.calls {
	case 1:
		return toolCallMessage("call_list_sources", ListSourceCardsToolName, `{"limit":10}`), nil
	case 2:
		return toolCallMessage("call_save_analysis", SaveReportSectionToolName, toolArgs(map[string]any{
			"section":      "事实分析",
			"content":      "事实归纳：围绕“" + investigation + "”的外部搜索结果先保存为资料卡，再由分析员按证据强弱归类，可以降低单一 Agent 直接复制搜索摘要的风险。冲突点：搜索结果仍可能过时或带偏见，需要报告保留待确认问题。",
			"evidence_ids": []int64{1, 2},
		})), nil
	case 3:
		if taskID > 0 {
			return toolCallMessage("call_update_task", UpdateTaskToolName, fmt.Sprintf(`{"task_id":%d,"status":"completed","result":"saved analysis section"}`, taskID)), nil
		}
	}
	return schema.AssistantMessage("分析员已保存事实分析并通知撰稿员。", nil), nil
}

func (m *deterministicRoleModel) writerMessage(taskID int64, investigation string) (*schema.Message, error) {
	switch m.calls {
	case 1:
		return toolCallMessage("call_list_sources", ListSourceCardsToolName, `{"limit":10}`), nil
	case 2:
		return toolCallMessage("call_list_sections", ListReportSectionsToolName, `{"limit":10}`), nil
	case 3:
		return toolCallMessage("call_save_final_report", SaveReportSectionToolName, toolArgs(map[string]any{
			"section":      "最终报告",
			"content":      "# 调查报告\n\n结论：围绕“" + investigation + "”的调查可以拆成搜索、分析、撰稿三个外部进程角色，通过 source cards 和 report sections 保留证据链，降低单一 Agent 直接把搜索摘要写成结论的风险。[S1][S2]\n\n关键发现：1. source card 让事实进入共享存储后再被引用；2. analyst 单独处理证据强弱和冲突点；3. writer 只基于已落库证据生成报告。\n\n待确认：真实搜索供应商的结果质量、时效性和字段映射仍需要接入侧评估。",
			"evidence_ids": []int64{1, 2},
		})), nil
	case 4:
		if taskID > 0 {
			return toolCallMessage("call_update_task", UpdateTaskToolName, fmt.Sprintf(`{"task_id":%d,"status":"completed","result":"saved final report"}`, taskID)), nil
		}
	}
	return schema.AssistantMessage("撰稿员已保存最终调查报告。", nil), nil
}

func toolArgs(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func toolCallMessage(id string, name string, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}})
}

func extractTaskPayload(messages []*schema.Message) TaskPayload {
	for _, message := range messages {
		if message == nil || message.Role != schema.User {
			continue
		}
		var payload TaskPayload
		if err := json.Unmarshal([]byte(message.Content), &payload); err == nil && (payload.TaskID > 0 || strings.TrimSpace(payload.Prompt) != "" || strings.TrimSpace(payload.Topic) != "") {
			return payload
		}
	}
	return TaskPayload{}
}

func roleFromAgentName(agentName string) AgentRole {
	switch agentName {
	case defaultSearchAgent:
		return RoleSearcher
	case defaultAnalystAgent:
		return RoleAnalyst
	case defaultWriterAgent:
		return RoleWriter
	default:
		return AgentRole(agentName)
	}
}
