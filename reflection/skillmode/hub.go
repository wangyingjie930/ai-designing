package skillmode

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ScenarioAgentHubConfig 配置 fork/fork_with_context 模式使用的专家子 agent。
type ScenarioAgentHubConfig struct {
	Model         model.BaseChatModel
	MaxIterations int
}

// ScenarioAgentHub 按 skill frontmatter 中的 agent 名称创建专家子 agent。
type ScenarioAgentHub struct {
	model         model.BaseChatModel
	maxIterations int
}

// NewScenarioAgentHub 创建 Skill Middleware fork 模式需要的 AgentHub。
func NewScenarioAgentHub(config ScenarioAgentHubConfig) *ScenarioAgentHub {
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 2
	}
	return &ScenarioAgentHub{
		model:         config.Model,
		maxIterations: maxIterations,
	}
}

// Get 返回一个干净的 ChatModelAgent，供官方 Skill Middleware 执行 fork 技能。
func (h *ScenarioAgentHub) Get(ctx context.Context, name string, opts *skill.AgentHubOptions) (adk.Agent, error) {
	chatModel := h.model
	if opts != nil && opts.Model != nil {
		chatModel = opts.Model
	}
	if chatModel == nil {
		return nil, errors.New("sub-agent model is required")
	}
	agentName := strings.TrimSpace(name)
	if agentName == "" {
		agentName = "skill_mode_specialist_agent"
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          agentName,
		Description:   "Skill 模式专家子 Agent，用于演示 fork 和 fork_with_context。",
		Instruction:   subAgentInstruction(agentName),
		Model:         chatModel,
		MaxIterations: h.maxIterations,
	})
	if err != nil {
		return nil, err
	}
	return agent, nil
}

// subAgentInstruction 定义专家子 agent 的业务边界。
func subAgentInstruction(agentName string) string {
	return strings.Join([]string{
		"你是 Skill 模式专家子 Agent：" + agentName + "。",
		"你只处理当前 skill 给出的任务，不要替主 agent 编造未给出的事实。",
		"输出要短，优先给可执行结论和理由。",
	}, "\n")
}

var _ skill.AgentHub = (*ScenarioAgentHub)(nil)
var _ model.BaseChatModel = (*noopModel)(nil)

// noopModel 只用于编译期接口断言，实际运行不会创建。
type noopModel struct{}

// Generate 满足 BaseChatModel 接口，实际不会被调用。
func (*noopModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("noop model should not run")
}

// Stream 满足 BaseChatModel 接口，实际不会被调用。
func (*noopModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("noop model should not run")
}
