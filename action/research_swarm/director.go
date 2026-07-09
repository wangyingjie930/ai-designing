package researchswarm

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
	SpawnTeammateToolName = "spawn_teammate"
)

// LeaderAgentConfig 配置 report_director 这个主控 ADK Agent。
type LeaderAgentConfig struct {
	Team          *TeamRuntime
	Model         model.BaseChatModel
	MaxIterations int
}

type spawnTeammateToolRequest struct {
	Name        string `json:"name" jsonschema:"required" jsonschema_description:"要创建的 teammate 名称，创建后可通过 send_message 定向通信"`
	Role        string `json:"role,omitempty" jsonschema_description:"teammate 的调查角色，例如 searcher、analyst、writer"`
	Description string `json:"description" jsonschema:"required" jsonschema_description:"3-5 个词的任务说明，作为任务标题"`
	Prompt      string `json:"prompt" jsonschema:"required" jsonschema_description:"交给 teammate 执行的完整任务"`
}

type spawnTeammateToolResponse struct {
	Name    string `json:"name"`
	AgentID string `json:"agent_id"`
	Role    string `json:"role"`
	TaskID  int64  `json:"task_id,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// NewLeaderAgent 创建 report_director；它通过工具而不是 Go 代码显式 spawn teammate。
func NewLeaderAgent(ctx context.Context, config LeaderAgentConfig) (adk.Agent, error) {
	if config.Team == nil {
		return nil, fmt.Errorf("team runtime is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	tools, err := NewLeaderTools(ctx, config.Team)
	if err != nil {
		return nil, err
	}
	chatModel := config.Model
	if chatModel == nil {
		chatModel = &deterministicDirectorModel{}
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 12
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          config.Team.LeaderID,
		Description:   "Research report director that coordinates teammates through tools.",
		Instruction:   leaderInstruction(config.Team),
		Model:         chatModel,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
}

// NewLeaderTools 暴露给 report_director 的模型可见工具面。
func NewLeaderTools(ctx context.Context, team *TeamRuntime) ([]tool.BaseTool, error) {
	if team == nil || team.Store == nil {
		return nil, fmt.Errorf("team runtime is required")
	}
	spawn, err := toolutils.InferTool[spawnTeammateToolRequest, *spawnTeammateToolResponse](
		SpawnTeammateToolName,
		"Spawn one teammate process for the current team. Use this instead of assuming a fixed roster.",
		func(ctx context.Context, req spawnTeammateToolRequest) (*spawnTeammateToolResponse, error) {
			role := AgentRole(strings.TrimSpace(req.Role))
			if role == "" {
				role = roleFromAgentName(req.Name)
			}
			handle, err := team.SpawnTeammate(ctx, SpawnTeammateRequest{
				Name:        req.Name,
				Role:        role,
				Description: req.Description,
				Prompt:      req.Prompt,
			})
			if err != nil {
				return nil, err
			}
			return &spawnTeammateToolResponse{
				Name:    handle.Name,
				AgentID: handle.AgentID,
				Role:    string(handle.Role),
				TaskID:  handle.TaskID,
				PID:     handle.Process.PID,
			}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{spawn}, nil
}

func leaderInstruction(team *TeamRuntime) string {
	return strings.Join([]string{
		"你是调查报告 swarm 的 report_director。",
		"当前 team_name 是 " + team.TeamName + "。",
		"需要 teammate 时必须调用 spawn_teammate，不要假设 team 创建时已有固定成员。",
		"不要主动轮询产物；leader 会把 teammate completion 事件作为下一轮输入交给你。",
		"最终报告必须来自 teammate 写入 SQLite 的 report section。",
	}, "\n")
}

func runLeaderAgent(ctx context.Context, team *TeamRuntime, chatModel model.BaseChatModel, input LeaderDirectorInput) error {
	agent, err := NewLeaderAgent(ctx, LeaderAgentConfig{Team: team, Model: chatModel})
	if err != nil {
		return err
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	query, err := json.Marshal(input)
	if err != nil {
		return err
	}
	iter := runner.Query(ctx, string(query))
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return fmt.Errorf("run leader agent: %w", event.Err)
		}
	}
	return nil
}

// deterministicDirectorModel 是离线 demo 默认主控模型；它用工具调用模拟真实模型的 spawn 决策。
type deterministicDirectorModel struct {
	spawnedSearch  bool
	spawnedAnalyst bool
	spawnedWriter  bool
}

// Generate 让默认命令在无外部 LLM 时仍通过 director 工具面跑完整调查链路。
func (m *deterministicDirectorModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	directorInput := extractLeaderDirectorInput(input)
	topic := firstNonEmpty(directorInput.Topic, "调查报告主题")
	switch {
	case directorInput.Type == "start" && !m.spawnedSearch:
		m.spawnedSearch = true
		return toolCallMessage("call_spawn_searcher", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        defaultSearchAgent,
			"role":        string(RoleSearcher),
			"description": string(RoleSearcher),
			"prompt":      topic,
		})), nil
	case directorInput.Event != nil && directorInput.Event.AgentName == defaultSearchAgent && !m.spawnedAnalyst:
		m.spawnedAnalyst = true
		return toolCallMessage("call_spawn_analyst", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        defaultAnalystAgent,
			"role":        string(RoleAnalyst),
			"description": string(RoleAnalyst),
			"prompt":      topic,
		})), nil
	case directorInput.Event != nil && directorInput.Event.AgentName == defaultAnalystAgent && !m.spawnedWriter:
		m.spawnedWriter = true
		return toolCallMessage("call_spawn_writer", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        defaultWriterAgent,
			"role":        string(RoleWriter),
			"description": string(RoleWriter),
			"prompt":      topic,
		})), nil
	case directorInput.Event != nil && directorInput.Event.AgentName == defaultWriterAgent:
		return schema.AssistantMessage("调查报告已完成。", nil), nil
	default:
		return schema.AssistantMessage("等待 teammate completion 事件。", nil), nil
	}
}

// Stream 当前 demo 不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *deterministicDirectorModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

func extractLeaderDirectorInput(messages []*schema.Message) LeaderDirectorInput {
	for _, message := range messages {
		if message == nil || message.Role != schema.User {
			continue
		}
		var input LeaderDirectorInput
		if err := json.Unmarshal([]byte(message.Content), &input); err == nil && strings.TrimSpace(input.Type) != "" {
			return input
		}
	}
	return LeaderDirectorInput{}
}
