package guardrail

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Middleware 把 GuardrailSandwich 接入 Eino ADK 的模型前后和工具前后挂点。
type Middleware struct {
	*adk.BaseChatModelAgentMiddleware
	policy     *Policy
	classifier RiskClassifier
	approver   Approver
}

// ToolExecutionReport 是工具被 guardrail 拦截时返回给模型的稳定结构。
type ToolExecutionReport struct {
	Executed  bool      `json:"executed"`
	BlockedBy string    `json:"blocked_by,omitempty"`
	RiskLevel RiskLevel `json:"risk_level"`
	Approved  bool      `json:"approved"`
	Reason    string    `json:"reason,omitempty"`
}

// NewMiddleware 创建可复用的 ADK guardrail middleware。
func NewMiddleware(config MiddlewareConfig) (*Middleware, error) {
	policy, err := NewPolicy(config.Policy)
	if err != nil {
		return nil, err
	}
	classifier := config.Classifier
	if classifier == nil {
		classifier = DefaultRiskClassifier{}
	}
	return &Middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		policy:                       policy,
		classifier:                   classifier,
		approver:                     config.Approver,
	}, nil
}

// BeforeModelRewriteState 在模型调用前隐藏不允许暴露的工具。
func (m *Middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if m == nil || m.policy == nil || state == nil || len(m.policy.allowedTools) == 0 {
		return ctx, state, nil
	}
	filtered := make([]*schema.ToolInfo, 0, len(state.ToolInfos))
	for _, info := range state.ToolInfos {
		if info == nil {
			continue
		}
		if m.policy.allowedTools[normalizeToolName(info.Name)] {
			filtered = append(filtered, info)
		}
	}
	state.ToolInfos = filtered
	return ctx, state, nil
}

// AfterModelRewriteState 对最终进入状态的模型文本做兜底脱敏。
func (m *Middleware) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if m == nil || m.policy == nil || state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last != nil {
		last.Content = m.policy.Redact(last.Content)
	}
	return ctx, state, nil
}

// AfterAgent 对成功结束时的状态再做一次脱敏，避免最终回复重述敏感信息。
func (m *Middleware) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	if m == nil || m.policy == nil || state == nil {
		return ctx, nil
	}
	for _, msg := range state.Messages {
		if msg != nil {
			msg.Content = m.policy.Redact(msg.Content)
		}
	}
	return ctx, nil
}

// WrapInvokableToolCall 在标准工具执行前做输入过滤，执行后做输出脱敏。
func (m *Middleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		verdict, err := m.evaluateTool(ctx, toolNameFromContext(tCtx), argumentsInJSON)
		if err != nil {
			return "", err
		}
		if !verdict.Approved {
			return marshalBlockedReport(verdict)
		}
		output, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return "", err
		}
		return m.policy.Redact(output), nil
	}, nil
}

// WrapEnhancedInvokableToolCall 在增强工具执行前做输入过滤，执行后只脱敏文本 part。
func (m *Middleware) WrapEnhancedInvokableToolCall(ctx context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		argumentsInJSON := ""
		if argument != nil {
			argumentsInJSON = argument.Text
		}
		verdict, err := m.evaluateTool(ctx, toolNameFromContext(tCtx), argumentsInJSON)
		if err != nil {
			return nil, err
		}
		if !verdict.Approved {
			blocked, err := marshalBlockedReport(verdict)
			if err != nil {
				return nil, err
			}
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: blocked}}}, nil
		}
		output, err := endpoint(ctx, argument, opts...)
		if err != nil {
			return nil, err
		}
		return m.redactToolResult(output), nil
	}, nil
}

// WrapModel 保留默认模型包装行为，显式满足接口并方便后续扩展 trace。
func (m *Middleware) WrapModel(ctx context.Context, chatModel model.BaseModel[*schema.Message], mc *adk.ModelContext) (model.BaseModel[*schema.Message], error) {
	return chatModel, nil
}

// evaluateTool 统一执行输入过滤，确保所有工具类型使用同一套策略。
func (m *Middleware) evaluateTool(ctx context.Context, toolName string, argumentsInJSON string) (Verdict, error) {
	if m == nil || m.policy == nil {
		return Verdict{}, fmt.Errorf("guardrail middleware is not initialized")
	}
	return m.policy.Evaluate(ctx, toolName, argumentsInJSON, m.classifier, m.approver)
}

// redactToolResult 只改文本 part，避免破坏图片、文件等结构化输出。
func (m *Middleware) redactToolResult(result *schema.ToolResult) *schema.ToolResult {
	if m == nil || m.policy == nil || result == nil {
		return result
	}
	for index := range result.Parts {
		if result.Parts[index].Type == schema.ToolPartTypeText {
			result.Parts[index].Text = m.policy.Redact(result.Parts[index].Text)
		}
	}
	return result
}

// toolNameFromContext 从 Eino 的 ToolContext 取工具名，空上下文时保守返回空串。
func toolNameFromContext(tCtx *adk.ToolContext) string {
	if tCtx == nil {
		return ""
	}
	return tCtx.Name
}

// marshalBlockedReport 把拦截结果转成工具消息，允许 Agent 给用户解释原因。
func marshalBlockedReport(verdict Verdict) (string, error) {
	payload, err := json.Marshal(ToolExecutionReport{
		Executed:  false,
		BlockedBy: "input_filter",
		RiskLevel: verdict.RiskLevel,
		Approved:  verdict.Approved,
		Reason:    verdict.Reason,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}
