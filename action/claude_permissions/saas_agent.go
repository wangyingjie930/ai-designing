package claudepermissions

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// SaaSAgentConfig 配置生产变更 Agent 的模型、权限模式、执行器和宿主 Hooks。
type SaaSAgentConfig struct {
	Model                  model.BaseChatModel
	Mode                   PermissionMode
	Rules                  []PermissionRule
	Broker                 *PermissionBroker
	Executor               SaaSExecutor
	PreToolUseHooks        []PreToolUseHook
	PermissionRequestHooks []PermissionRequestHook
	PostToolUseHooks       []PostToolUseHook
	StopHooks              []StopHook
	MaxIterations          int
}

// SaaSAgent 用 Eino ADK 运行生产变更工具，并在同一进程中保留暂停的工具调用。
type SaaSAgent struct {
	runner     *adk.Runner
	middleware *Middleware
}

// NewSaaSAgent 创建带 Claude Code 风格权限管线的非 coding Agent。
func NewSaaSAgent(ctx context.Context, config SaaSAgentConfig) (*SaaSAgent, error) {
	if config.Model == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	executor := config.Executor
	if executor == nil {
		executor = NewInMemorySaaSExecutor()
	}
	tools, err := NewSaaSTools(executor)
	if err != nil {
		return nil, err
	}
	rules := config.Rules
	if rules == nil {
		rules = DefaultSaaSPermissionRules()
	}
	middleware, err := NewMiddleware(MiddlewareConfig{
		Tools:                  DefaultSaaSToolPolicies(),
		Mode:                   config.Mode,
		Rules:                  rules,
		Broker:                 config.Broker,
		PreToolUseHooks:        config.PreToolUseHooks,
		PermissionRequestHooks: config.PermissionRequestHooks,
		PostToolUseHooks:       config.PostToolUseHooks,
		StopHooks:              config.StopHooks,
	})
	if err != nil {
		return nil, err
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "saas_claude_permission_agent",
		Description:   "带执行前审批和宿主 Hooks 的 SaaS 生产变更助理。",
		Instruction:   DefaultSaaSInstruction(),
		Model:         config.Model,
		MaxIterations: maxIterations,
		Handlers:      []adk.ChatModelAgentMiddleware{middleware},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return nil, err
	}
	return &SaaSAgent{
		runner:     adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}),
		middleware: middleware,
	}, nil
}

// DefaultSaaSInstruction 返回生产变更助理的中文主进程约束。
func DefaultSaaSInstruction() string {
	return strings.Join([]string{
		"你是 SaaS 生产变更助理，负责读取租户配置、生成计划并执行最小可逆变更。",
		"读取和生成计划可以直接执行；修改开关或发送通知时，权限层可能暂停当前工具调用等待人工确认。",
		"审批拒绝会作为结构化工具结果返回；此时不要声称已经执行，应调整方案或向用户说明原因。",
		"不得尝试删除租户，也不得绕过权限层或伪造工具执行结果。",
	}, "\n")
}

// PermissionRequests 暴露当前 Agent 的待审批请求通道。
func (a *SaaSAgent) PermissionRequests() <-chan PermissionRequest {
	if a == nil || a.middleware == nil {
		return nil
	}
	return a.middleware.PermissionRequests()
}

// ResolvePermission 恢复一个等待审批的工具调用，重复响应会返回 false。
func (a *SaaSAgent) ResolvePermission(response PermissionResponse) bool {
	return a != nil && a.middleware != nil && a.middleware.ResolvePermission(response)
}

// Query 运行一轮自然语言请求并提取最终助手回复。
func (a *SaaSAgent) Query(ctx context.Context, request SaaSRequest) (*SaaSResponse, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("saas agent is not initialized")
	}
	if strings.TrimSpace(request.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	iterator := a.runner.Query(ctx, request.Message)
	finalMessage := ""
	for {
		event, ok := iterator.Next()
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
			finalMessage = message.Content
		}
	}
	if finalMessage == "" {
		return nil, fmt.Errorf("agent finished without assistant response")
	}
	return &SaaSResponse{Message: finalMessage}, nil
}
