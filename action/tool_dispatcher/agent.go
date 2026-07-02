package tooldispatcher

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// RenewalRiskAgentConfig 配置续费风险分诊 Agent 的模型、工具库和迭代上限。
type RenewalRiskAgentConfig struct {
	Model         model.BaseChatModel
	DynamicTools  []tool.BaseTool
	MaxIterations int
}

// RenewalRiskAgent 封装 Eino ADK Runner，外部只需要提交自然语言续费风险请求。
type RenewalRiskAgent struct {
	runner *adk.Runner
}

// RenewalRiskRequest 是客户成功经理提交给 Agent 的自然语言任务。
type RenewalRiskRequest struct {
	Message string `json:"message"`
}

// RenewalRiskResponse 是 Agent 最终给客户成功经理的处理建议。
type RenewalRiskResponse struct {
	Message string `json:"message"`
}

// NewRenewalRiskAgent 创建使用 Eino dynamic toolsearch 的续费风险分诊 Agent。
func NewRenewalRiskAgent(ctx context.Context, config RenewalRiskAgentConfig) (*RenewalRiskAgent, error) {
	if config.Model == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	dynamicTools := config.DynamicTools
	if len(dynamicTools) == 0 {
		var err error
		dynamicTools, err = NewRenewalRiskTools()
		if err != nil {
			return nil, err
		}
	}
	searchMiddleware, err := toolsearch.New(ctx, &toolsearch.Config{
		DynamicTools: dynamicTools,
	})
	if err != nil {
		return nil, err
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "renewal_risk_tool_dispatcher_agent",
		Description:   "客户成功续费风险分诊 Agent，通过 tool_search 按需加载续费处理工具。",
		Instruction:   DefaultRenewalRiskInstruction(),
		Model:         config.Model,
		MaxIterations: maxIterations,
		Handlers:      []adk.ChatModelAgentMiddleware{searchMiddleware},
	})
	if err != nil {
		return nil, err
	}
	return &RenewalRiskAgent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// DefaultRenewalRiskInstruction 返回 Agent 的业务边界和工具使用策略。
func DefaultRenewalRiskInstruction() string {
	return strings.Join([]string{
		"你是客户成功团队的续费风险分诊助理。",
		"工具库较大时，不要凭记忆直接调用业务工具；必须先调用 tool_search 搜索或 select 需要的工具。",
		"处理续费风险时，优先加载并调用客户快照、续费合同、挽留方案相关工具。",
		"最终输出给客户成功经理：风险判断、关键证据、下一步动作和需要人工确认的事项。",
		"不要把内部工具实现细节当成客户可见结论。",
	}, "\n")
}

// Query 运行 ADK Runner，并返回最后一条 assistant 文本回复。
func (a *RenewalRiskAgent) Query(ctx context.Context, req RenewalRiskRequest) (*RenewalRiskResponse, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("agent is not initialized")
	}
	query := strings.TrimSpace(req.Message)
	if query == "" {
		return nil, fmt.Errorf("message is required")
	}
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
			final = strings.TrimSpace(message.Content)
		}
	}
	if final == "" {
		return nil, fmt.Errorf("agent finished without assistant response")
	}
	return &RenewalRiskResponse{Message: final}, nil
}
