package claudetasklist

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const defaultAgentMaxIterations = 12

// AgentConfig 配置 Task Agent 使用的模型、机械任务真相源和单轮迭代上限。
type AgentConfig struct {
	Model         model.BaseChatModel
	Store         *Store
	MaxIterations int
}

// AgentResult 返回最终 assistant 正文和本轮真实模型工具调用数量。
type AgentResult struct {
	Content       string
	ToolCallCount int
}

// Agent 持有 Eino Runner；会话语义由调用方传入，任务状态只由共享 Store 持久化。
type Agent struct {
	runner *adk.Runner
}

// NewAgent 建立主 Agent 的 ReAct 执行边界，把四个 Task 工具绑定到同一 Store，构造失败不进入对话循环。
func NewAgent(ctx context.Context, config AgentConfig) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("task agent model is required")
	}
	if config.Store == nil {
		return nil, errors.New("task store is required")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	tools, err := BuildTools(config.Store)
	if err != nil {
		return nil, err
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultAgentMaxIterations
	}
	chatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "claude_task_agent",
		Description:   "使用文件任务列表跟踪复杂工作的 Claude Code 风格主 Agent。",
		Instruction:   DefaultInstruction(),
		Model:         config.Model,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create Claude task agent: %w", err)
	}
	return &Agent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: chatAgent})}, nil
}

// Run 是 Task Agent 主流程：完整消息只作为本轮语义输入，事件流中的 assistant 工具调用形成计数；任一事件或消息解码错误立即越过回答边界返回，但此前成功工具写入不回滚。
func (a *Agent) Run(ctx context.Context, messages []*schema.Message) (AgentResult, error) {
	if a == nil || a.runner == nil {
		return AgentResult{}, errors.New("task agent is not initialized")
	}
	if len(messages) == 0 {
		return AgentResult{}, errors.New("task agent messages are empty")
	}

	iterator := a.runner.Run(ctx, messages)
	var result AgentResult
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return AgentResult{}, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil || event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return AgentResult{}, err
		}
		if message == nil {
			continue
		}
		result.ToolCallCount += len(message.ToolCalls)
		if strings.TrimSpace(message.Content) != "" {
			result.Content = message.Content
		}
	}
	if strings.TrimSpace(result.Content) == "" {
		return AgentResult{}, errors.New("agent finished without assistant output")
	}
	return result, nil
}
