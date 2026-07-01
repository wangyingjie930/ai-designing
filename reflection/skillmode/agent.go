package skillmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Config 配置一个演示 Skill Middleware 三种模式的客服运营 agent。
type Config struct {
	Mode                   Mode
	Name                   string
	Description            string
	Model                  model.BaseChatModel
	SubAgentModel          model.BaseChatModel
	MaxIterations          int
	SubAgentMaxIterations  int
	Scenarios              map[Mode]Scenario
	SkillBackend           skill.Backend
	CustomSkillToolName    string
	DisableChineseSkillTip bool
}

// Response 是 runner 执行后的稳定业务输出。
type Response struct {
	Mode        Mode   `json:"mode"`
	Scenario    string `json:"scenario"`
	SkillName   string `json:"skill_name"`
	Rationale   string `json:"rationale"`
	QueryChars  int    `json:"query_chars"`
	AnswerChars int    `json:"answer_chars"`
	Message     string `json:"message"`
}

// NewRunner 创建带 Skill Middleware 的 Eino ADK Runner。
func NewRunner(ctx context.Context, config Config) (*adk.Runner, error) {
	agent, err := NewAgent(ctx, config)
	if err != nil {
		return nil, err
	}
	return adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}), nil
}

// NewAgent 创建一个场景化 ChatModelAgent，并把官方 skill middleware 挂到 Handlers。
func NewAgent(ctx context.Context, config Config) (*adk.ChatModelAgent, error) {
	if config.Model == nil {
		return nil, errors.New("model is required")
	}
	scenarios := config.Scenarios
	if len(scenarios) == 0 {
		scenarios = DefaultScenarios()
	}
	scenario, err := scenarioFromMap(config.Mode, scenarios)
	if err != nil {
		return nil, err
	}
	backend, err := resolveSkillBackend(ctx, config, scenarios)
	if err != nil {
		return nil, err
	}
	skillConfig := &skill.Config{
		Backend: backend,
		AgentHub: NewScenarioAgentHub(ScenarioAgentHubConfig{
			Model:         fallbackModel(config.SubAgentModel, config.Model),
			MaxIterations: config.SubAgentMaxIterations,
		}),
		CustomSystemPrompt: skillSystemPrompt,
		CustomToolParams:   skillToolParams,
		BuildContent:       buildSkillContent,
	}
	if strings.TrimSpace(config.CustomSkillToolName) != "" {
		toolName := strings.TrimSpace(config.CustomSkillToolName)
		skillConfig.SkillToolName = &toolName
	}
	if config.DisableChineseSkillTip {
		skillConfig.CustomSystemPrompt = nil
	}
	handler, err := skill.NewMiddleware(ctx, skillConfig)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "customer_support_" + string(scenario.Mode) + "_skill_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Customer support agent demonstrating Eino ADK Skill Middleware mode: " + string(scenario.Mode)
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 6
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   description,
		Instruction:   mainAgentInstruction(scenario),
		Model:         config.Model,
		MaxIterations: maxIterations,
		Handlers:      []adk.ChatModelAgentMiddleware{handler},
	})
}

// resolveSkillBackend 允许生产注入 registry-backed backend，未配置时保留本地 SKILL.md 演示路径。
func resolveSkillBackend(ctx context.Context, config Config, scenarios map[Mode]Scenario) (skill.Backend, error) {
	if config.SkillBackend != nil {
		return config.SkillBackend, nil
	}
	return NewSkillBackend(ctx, scenarios)
}

// skillToolArguments 承载 skill 工具调用参数，task 是 fork 隔离模式下唯一可靠的业务输入。
type skillToolArguments struct {
	Skill string `json:"skill"`
	Task  string `json:"task"`
}

// skillToolParams 在官方 skill 参数上追加本轮任务文本，避免 fork 子 agent 只拿到 SKILL.md。
func skillToolParams(_ context.Context, defaults map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
	if defaults == nil {
		defaults = map[string]*schema.ParameterInfo{}
	}
	defaults["task"] = &schema.ParameterInfo{
		Type:     schema.String,
		Desc:     "当前要交给 Skill 处理的原始任务或待审查文本。fork 模式必须填写，因为子 Agent 不会继承主对话历史。",
		Required: true,
	}
	return defaults, nil
}

// buildSkillContent 把 SKILL.md 与本轮 task 合成实际传给 inline/fork skill 的内容。
func buildSkillContent(_ context.Context, loadedSkill skill.Skill, rawArgs string) (string, error) {
	var args skillToolArguments
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return "", fmt.Errorf("unmarshal skill tool arguments: %w", err)
		}
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		task = "（未提供；请在 skill 工具参数 task 中传入本轮任务文本。）"
	}
	return strings.Join([]string{
		"Launching skill: " + loadedSkill.Name,
		"Base directory for this skill: " + loadedSkill.BaseDirectory,
		"",
		strings.TrimSpace(loadedSkill.Content),
		"",
		"当前任务/待处理文本：",
		task,
	}, "\n"), nil
}

// QueryRunner 调用 ADK Runner，并返回最后一条 assistant 消息摘要。
func QueryRunner(ctx context.Context, runner *adk.Runner, query string) (*Response, error) {
	if runner == nil {
		return nil, errors.New("runner is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	iter := runner.Query(ctx, query)
	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil || event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, err
		}
		if message != nil && strings.TrimSpace(message.Content) != "" {
			final = strings.TrimSpace(message.Content)
		}
	}
	if final == "" {
		return nil, errors.New("runner finished without assistant output")
	}
	return &Response{
		QueryChars:  len([]rune(query)),
		AnswerChars: len([]rune(final)),
		Message:     final,
	}, nil
}

// BuildResponse 补全 QueryRunner 无法从 runner 中反推的场景元信息。
func BuildResponse(scenario Scenario, query string, message string) *Response {
	message = strings.TrimSpace(message)
	return &Response{
		Mode:        scenario.Mode,
		Scenario:    scenario.Title,
		SkillName:   scenario.SkillName,
		Rationale:   scenario.Rationale,
		QueryChars:  len([]rune(strings.TrimSpace(query))),
		AnswerChars: len([]rune(message)),
		Message:     message,
	}
}

// DefaultQuery 返回指定模式的默认客服运营输入。
func DefaultQuery(mode Mode) (string, error) {
	scenario, err := ScenarioForMode(mode)
	if err != nil {
		return "", err
	}
	return scenario.DefaultQuery, nil
}

// scenarioFromMap 从自定义场景表中取指定模式，空模式默认 inline。
func scenarioFromMap(mode Mode, scenarios map[Mode]Scenario) (Scenario, error) {
	if strings.TrimSpace(string(mode)) == "" {
		mode = ModeInline
	}
	scenario, ok := scenarios[mode]
	if !ok {
		return Scenario{}, fmt.Errorf("unknown skill mode: %s", mode)
	}
	return scenario, nil
}

// fallbackModel 返回候选模型，不存在时复用主模型。
func fallbackModel(candidate model.BaseChatModel, fallback model.BaseChatModel) model.BaseChatModel {
	if candidate != nil {
		return candidate
	}
	return fallback
}

// skillSystemPrompt 用中文说明官方 skill 工具的选择方式，避免模型把 skill 当普通知识库。
func skillSystemPrompt(_ context.Context, toolName string) string {
	return strings.Join([]string{
		"你可以按需调用 `" + toolName + "` 工具加载 Skill。",
		"只有当用户任务明显匹配某个可用 Skill 的描述时才调用。",
		"调用 `" + toolName + "` 时必须同时传入 skill 和 task；task 填当前用户请求、待审查文本或要交给 Skill 处理的原文。",
		"调用后必须根据 Skill 或专家子 Agent 的结果继续给最终答复。",
	}, "\n")
}

// mainAgentInstruction 定义主 agent 如何根据场景选择 skill。
func mainAgentInstruction(scenario Scenario) string {
	return strings.Join([]string{
		"你是客服运营值班长，负责处理客户升级、补偿和合规回复。",
		"本轮演示模式：" + string(scenario.Mode),
		"目标 Skill：" + scenario.SkillName,
		"使用理由：" + scenario.Rationale,
		"请先调用 skill 工具加载目标 Skill，并把当前用户请求原文放入 task 参数。",
		"如果本轮是 fork 隔离模式，task 是专家子 Agent 能看到的唯一业务输入。",
		"再结合工具结果给出简洁可执行回复。",
	}, "\n")
}
