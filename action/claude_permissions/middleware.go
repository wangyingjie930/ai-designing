package claudepermissions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// MiddlewareConfig 汇总 Claude Code 风格权限中间件的静态策略和宿主 Hook。
type MiddlewareConfig struct {
	Tools                  []ToolPolicy
	Mode                   PermissionMode
	Rules                  []PermissionRule
	Broker                 *PermissionBroker
	PreToolUseHooks        []PreToolUseHook
	PermissionRequestHooks []PermissionRequestHook
	PostToolUseHooks       []PostToolUseHook
	StopHooks              []StopHook
}

// Middleware 把工具可见性、执行前权限、人工审批和执行后 Hook 接入 Eino ADK。
type Middleware struct {
	*adk.BaseChatModelAgentMiddleware
	engine        *PermissionEngine
	mode          PermissionMode
	rules         []PermissionRule
	broker        *PermissionBroker
	coordinator   *PermissionCoordinator
	preToolHooks  []PreToolUseHook
	postToolHooks []PostToolUseHook
	stopHooks     []StopHook
}

// ToolExecutionReport 是拒绝执行时返回给模型的稳定工具结果。
type ToolExecutionReport struct {
	Executed  bool               `json:"executed"`
	BlockedBy string             `json:"blocked_by"`
	Behavior  PermissionBehavior `json:"behavior"`
	Reason    string             `json:"reason,omitempty"`
	Source    string             `json:"source,omitempty"`
	RequestID string             `json:"request_id,omitempty"`
}

// NewMiddleware 创建权限中间件，并补齐默认 broker 供宿主接入审批界面。
func NewMiddleware(config MiddlewareConfig) (*Middleware, error) {
	if len(config.Tools) == 0 {
		return nil, fmt.Errorf("at least one tool policy is required")
	}
	for _, policy := range config.Tools {
		if strings.TrimSpace(policy.Name) == "" {
			return nil, fmt.Errorf("tool policy name is required")
		}
		if policy.Checker == nil {
			return nil, fmt.Errorf("tool %s must implement CheckPermissions", policy.Name)
		}
	}
	mode := config.Mode
	if mode == "" {
		mode = PermissionModeDefault
	}
	if !isPermissionMode(mode) {
		return nil, fmt.Errorf("unsupported permission mode %q", mode)
	}
	broker := config.Broker
	if broker == nil {
		broker = NewPermissionBroker(8)
	}
	engine := NewPermissionEngine(config.Tools)
	return &Middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		engine:                       engine,
		mode:                         mode,
		rules:                        append([]PermissionRule(nil), config.Rules...),
		broker:                       broker,
		coordinator:                  NewPermissionCoordinator(broker, config.PermissionRequestHooks...),
		preToolHooks:                 append([]PreToolUseHook(nil), config.PreToolUseHooks...),
		postToolHooks:                append([]PostToolUseHook(nil), config.PostToolUseHooks...),
		stopHooks:                    append([]StopHook(nil), config.StopHooks...),
	}, nil
}

// PermissionRequests 暴露待审批请求，终端或 Web 宿主可像 Claude Code 一样展示确认界面。
func (m *Middleware) PermissionRequests() <-chan PermissionRequest {
	if m == nil || m.broker == nil {
		return nil
	}
	return m.broker.Requests()
}

// ResolvePermission 把宿主的审批结果送回被暂停的原工具调用。
func (m *Middleware) ResolvePermission(response PermissionResponse) bool {
	return m != nil && m.broker != nil && m.broker.Resolve(response)
}

// BeforeModelRewriteState 在模型调用前隐藏被整工具 deny 的定义。
func (m *Middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if m == nil || m.engine == nil || state == nil {
		return ctx, state, nil
	}
	denied := m.engine.BlanketDeniedTools(m.rules)
	if len(denied) == 0 {
		return ctx, state, nil
	}
	visible := make([]*schema.ToolInfo, 0, len(state.ToolInfos))
	for _, info := range state.ToolInfos {
		if info == nil || denied["*"] || denied[normalizeToolName(info.Name)] {
			continue
		}
		visible = append(visible, info)
	}
	state.ToolInfos = visible
	return ctx, state, nil
}

// WrapInvokableToolCall 把普通 Eino 工具执行包进权限和 Hook 管线。
func (m *Middleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, toolContext *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	if endpoint == nil {
		return nil, fmt.Errorf("tool endpoint is required")
	}
	return func(callCtx context.Context, argumentsJSON string, opts ...tool.Option) (string, error) {
		toolName := toolNameFromContext(toolContext)
		approvedArguments, report, err := m.authorize(callCtx, toolName, argumentsJSON)
		if err != nil {
			return "", err
		}
		if report != nil {
			return marshalToolExecutionReport(*report)
		}
		output, err := endpoint(callCtx, approvedArguments, opts...)
		if err != nil {
			return "", err
		}
		return m.runPostToolUseHooks(callCtx, toolName, approvedArguments, output)
	}, nil
}

// WrapEnhancedInvokableToolCall 把增强工具的文本参数和文本结果接入同一权限管线。
func (m *Middleware) WrapEnhancedInvokableToolCall(ctx context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, toolContext *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	if endpoint == nil {
		return nil, fmt.Errorf("enhanced tool endpoint is required")
	}
	return func(callCtx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		argumentsJSON := ""
		if argument != nil {
			argumentsJSON = argument.Text
		}
		toolName := toolNameFromContext(toolContext)
		approvedArguments, report, err := m.authorize(callCtx, toolName, argumentsJSON)
		if err != nil {
			return nil, err
		}
		if report != nil {
			text, err := marshalToolExecutionReport(*report)
			if err != nil {
				return nil, err
			}
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: text}}}, nil
		}
		updatedArgument := &schema.ToolArgument{Text: approvedArguments}
		if argument != nil {
			copiedArgument := *argument
			updatedArgument = &copiedArgument
			updatedArgument.Text = approvedArguments
		}
		result, err := endpoint(callCtx, updatedArgument, opts...)
		if err != nil || result == nil {
			return result, err
		}
		for index := range result.Parts {
			if result.Parts[index].Type != schema.ToolPartTypeText {
				continue
			}
			result.Parts[index].Text, err = m.runPostToolUseHooks(callCtx, toolName, approvedArguments, result.Parts[index].Text)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}, nil
}

// AfterAgent 在 Agent 准备结束时运行 Stop Hooks，任何拒绝都会阻止本次正常结束。
func (m *Middleware) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	if m == nil || state == nil || len(m.stopHooks) == 0 {
		return ctx, nil
	}
	finalMessage := ""
	for index := len(state.Messages) - 1; index >= 0; index-- {
		message := state.Messages[index]
		if message != nil && message.Role == schema.Assistant && strings.TrimSpace(message.Content) != "" {
			finalMessage = message.Content
			break
		}
	}
	for _, hook := range m.stopHooks {
		if hook == nil {
			continue
		}
		if err := hook.BeforeStop(ctx, finalMessage); err != nil {
			return ctx, fmt.Errorf("stop hook: %w", err)
		}
	}
	return ctx, nil
}

// authorize 依次执行 PreToolUse、权限内核和 PermissionRequest，并返回最终参数。
func (m *Middleware) authorize(ctx context.Context, toolName string, argumentsJSON string) (string, *ToolExecutionReport, error) {
	if m == nil || m.engine == nil || m.coordinator == nil {
		return "", nil, fmt.Errorf("permission middleware is not initialized")
	}
	currentArguments := argumentsJSON
	hookDecision := PermissionBehavior("")
	hookReason := ""
	for _, hook := range m.preToolHooks {
		if hook == nil {
			continue
		}
		result, err := hook.BeforeToolUse(ctx, PreToolUseInput{ToolName: toolName, ArgumentsJSON: currentArguments})
		if err != nil {
			return "", nil, fmt.Errorf("pre tool use hook: %w", err)
		}
		if result.UpdatedArgumentsJSON != "" {
			if err := validateArgumentsJSON(result.UpdatedArgumentsJSON); err != nil {
				return "", nil, fmt.Errorf("pre tool use hook updated arguments: %w", err)
			}
			currentArguments = result.UpdatedArgumentsJSON
		}
		if strongerBehavior(result.Behavior, hookDecision) {
			hookDecision = result.Behavior
			hookReason = result.Reason
		}
	}
	toolCheck, err := m.engine.CheckToolPermissions(ctx, ToolPermissionCheckInput{
		ToolName:      toolName,
		ArgumentsJSON: currentArguments,
		Mode:          m.mode,
	})
	if err != nil {
		return "", nil, err
	}
	if toolCheck.UpdatedArgumentsJSON != "" {
		currentArguments = toolCheck.UpdatedArgumentsJSON
	}

	decision := m.engine.Evaluate(EvaluationInput{
		Mode:          m.mode,
		ToolName:      toolName,
		ArgumentsJSON: currentArguments,
		HookDecision:  hookDecision,
		HookReason:    hookReason,
		ToolCheck:     toolCheck,
		Rules:         m.rules,
	})
	if decision.Behavior == PermissionDeny {
		return "", deniedReport(decision, ""), nil
	}
	if decision.Behavior == PermissionAllow {
		return currentArguments, nil, nil
	}

	response, err := m.coordinator.Resolve(ctx, PermissionRequest{
		ToolName:      toolName,
		ArgumentsJSON: currentArguments,
		Reason:        decision.Reason,
	})
	if err != nil {
		return "", nil, fmt.Errorf("resolve permission: %w", err)
	}
	if response.Behavior == PermissionDeny {
		return "", &ToolExecutionReport{
			Executed: false, BlockedBy: "permission", Behavior: PermissionDeny,
			Reason: firstReason(response.Reason, "permission request denied"), Source: "permission_response", RequestID: response.RequestID,
		}, nil
	}
	if response.UpdatedArgumentsJSON != "" {
		if err := validateArgumentsJSON(response.UpdatedArgumentsJSON); err != nil {
			return "", nil, fmt.Errorf("permission response updated arguments: %w", err)
		}
		currentArguments = response.UpdatedArgumentsJSON
	}
	return currentArguments, nil, nil
}

// runPostToolUseHooks 让成功工具输出依次经过所有执行后 Hook。
func (m *Middleware) runPostToolUseHooks(ctx context.Context, toolName string, argumentsJSON string, output string) (string, error) {
	current := output
	for _, hook := range m.postToolHooks {
		if hook == nil {
			continue
		}
		updated, err := hook.AfterToolUse(ctx, PostToolUseInput{ToolName: toolName, ArgumentsJSON: argumentsJSON, Output: current})
		if err != nil {
			return "", fmt.Errorf("post tool use hook: %w", err)
		}
		current = updated
	}
	return current, nil
}

// deniedReport 把权限内核拒绝转成模型可消费的工具结果。
func deniedReport(decision PermissionDecision, requestID string) *ToolExecutionReport {
	return &ToolExecutionReport{
		Executed: false, BlockedBy: "permission", Behavior: PermissionDeny,
		Reason: decision.Reason, Source: decision.Source, RequestID: requestID,
	}
}

// marshalToolExecutionReport 序列化稳定的拒绝结果。
func marshalToolExecutionReport(report ToolExecutionReport) (string, error) {
	payload, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// validateArgumentsJSON 防止 Hook 或审批界面用无效 JSON 破坏原工具协议。
func validateArgumentsJSON(arguments string) error {
	if !json.Valid([]byte(arguments)) {
		return fmt.Errorf("arguments are not valid JSON")
	}
	return nil
}

// strongerBehavior 使用 deny > ask > allow 的顺序合并多个 PreToolUse Hook。
func strongerBehavior(candidate PermissionBehavior, current PermissionBehavior) bool {
	priority := map[PermissionBehavior]int{"": 0, PermissionAllow: 1, PermissionAsk: 2, PermissionDeny: 3}
	return priority[candidate] > priority[current]
}

// toolNameFromContext 从 Eino 工具上下文读取工具名。
func toolNameFromContext(toolContext *adk.ToolContext) string {
	if toolContext == nil {
		return ""
	}
	return toolContext.Name
}

// isPermissionMode 限制模式为 Claude Code 对外语义集合。
func isPermissionMode(mode PermissionMode) bool {
	switch mode {
	case PermissionModeDefault, PermissionModePlan, PermissionModeAcceptEdits, PermissionModeDontAsk, PermissionModeBypassPermissions:
		return true
	default:
		return false
	}
}
