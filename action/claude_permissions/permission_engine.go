package claudepermissions

import "strings"

// PermissionEngine 集中实现规则优先级和权限模式转换，不依赖 Eino 或终端 UI。
type PermissionEngine struct {
	tools map[string]ToolPolicy
}

// NewPermissionEngine 创建权限内核，并统一工具名的比较规则。
func NewPermissionEngine(tools []ToolPolicy) *PermissionEngine {
	indexed := make(map[string]ToolPolicy, len(tools))
	for _, candidate := range tools {
		name := normalizeToolName(candidate.Name)
		if name == "" {
			continue
		}
		candidate.Name = name
		indexed[name] = candidate
	}
	return &PermissionEngine{tools: indexed}
}

// Evaluate 按“安全硬限制 -> 显式规则 -> Hook -> 模式默认值”的顺序裁决工具调用。
func (e *PermissionEngine) Evaluate(input EvaluationInput) PermissionDecision {
	toolName := normalizeToolName(input.ToolName)
	policy, ok := e.tools[toolName]
	if !ok {
		return PermissionDecision{Behavior: PermissionDeny, Reason: "tool is not registered", Source: "tool_policy"}
	}
	if policy.Kind == ToolKindDestructive {
		return PermissionDecision{Behavior: PermissionDeny, Reason: "destructive operation is blocked by safety policy", Source: "safety"}
	}
	if input.HookDecision == PermissionDeny {
		return PermissionDecision{Behavior: PermissionDeny, Reason: firstReason(input.HookReason, "denied by PreToolUse hook"), Source: "pre_tool_hook"}
	}

	matched, hasRule := strongestMatchingRule(input.Rules, toolName, input.ArgumentsJSON)
	if hasRule && matched.Behavior != PermissionAllow {
		return PermissionDecision{Behavior: matched.Behavior, Reason: firstReason(matched.Reason, "matched explicit permission rule"), Source: "rule"}
	}

	mode := input.Mode
	if mode == "" {
		mode = PermissionModeDefault
	}
	if mode == PermissionModePlan && policy.Kind != ToolKindRead {
		return PermissionDecision{Behavior: PermissionDeny, Reason: "plan mode only allows read tools", Source: "mode"}
	}
	if hasRule {
		return PermissionDecision{Behavior: matched.Behavior, Reason: firstReason(matched.Reason, "matched explicit permission rule"), Source: "rule"}
	}
	if input.HookDecision == PermissionAllow {
		return PermissionDecision{Behavior: PermissionAllow, Reason: firstReason(input.HookReason, "allowed by PreToolUse hook"), Source: "pre_tool_hook"}
	}
	if input.HookDecision == PermissionAsk {
		return applyDontAsk(mode, PermissionDecision{Behavior: PermissionAsk, Reason: firstReason(input.HookReason, "confirmation requested by PreToolUse hook"), Source: "pre_tool_hook"})
	}

	decision := defaultDecision(mode, policy.Kind)
	return applyDontAsk(mode, decision)
}

// BlanketDeniedTools 返回整工具 deny 规则，用于在模型调用前收缩可见工具集合。
func (e *PermissionEngine) BlanketDeniedTools(rules []PermissionRule) map[string]bool {
	denied := make(map[string]bool)
	for _, rule := range rules {
		name := normalizeToolName(rule.ToolName)
		if rule.Behavior == PermissionDeny && name != "" && strings.TrimSpace(rule.ArgumentContains) == "" {
			denied[name] = true
		}
	}
	return denied
}

// defaultDecision 把权限模式和非 coding 工具类型映射成默认裁决。
func defaultDecision(mode PermissionMode, kind ToolKind) PermissionDecision {
	if mode == PermissionModeBypassPermissions {
		return PermissionDecision{Behavior: PermissionAllow, Reason: "allowed by bypassPermissions mode", Source: "mode"}
	}
	if kind == ToolKindRead {
		return PermissionDecision{Behavior: PermissionAllow, Reason: "read tool is auto allowed", Source: "tool_policy"}
	}
	if mode == PermissionModeAcceptEdits && kind == ToolKindEdit {
		return PermissionDecision{Behavior: PermissionAllow, Reason: "reversible change is allowed by acceptEdits mode", Source: "mode"}
	}
	return PermissionDecision{Behavior: PermissionAsk, Reason: "tool requires confirmation", Source: "tool_policy"}
}

// applyDontAsk 保证无交互模式不会遗留无法消费的 PermissionRequest。
func applyDontAsk(mode PermissionMode, decision PermissionDecision) PermissionDecision {
	if mode == PermissionModeDontAsk && decision.Behavior == PermissionAsk {
		return PermissionDecision{Behavior: PermissionDeny, Reason: "dontAsk mode denies operations that require confirmation", Source: "mode"}
	}
	return decision
}

// strongestMatchingRule 使用 deny > ask > allow 的固定优先级，避免规则顺序影响安全结果。
func strongestMatchingRule(rules []PermissionRule, toolName string, argumentsJSON string) (PermissionRule, bool) {
	for _, behavior := range []PermissionBehavior{PermissionDeny, PermissionAsk, PermissionAllow} {
		for _, rule := range rules {
			if rule.Behavior != behavior || !ruleMatches(rule, toolName, argumentsJSON) {
				continue
			}
			return rule, true
		}
	}
	return PermissionRule{}, false
}

// ruleMatches 同时支持精确工具名、全工具通配符和参数子串约束。
func ruleMatches(rule PermissionRule, toolName string, argumentsJSON string) bool {
	ruleTool := normalizeToolName(rule.ToolName)
	if ruleTool != "*" && ruleTool != toolName {
		return false
	}
	needle := strings.TrimSpace(rule.ArgumentContains)
	return needle == "" || strings.Contains(argumentsJSON, needle)
}

// normalizeToolName 避免配置中的空格和大小写造成权限漂移。
func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// firstReason 返回首个非空原因，保证所有拒绝和询问都可解释。
func firstReason(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
