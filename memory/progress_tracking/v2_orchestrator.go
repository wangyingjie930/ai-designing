package progresstracking

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// StatefulToolExecutor 是执行真实工具的最小边界，编排器只负责状态引用解析和账本收口。
type StatefulToolExecutor interface {
	ExecuteStatefulTool(ctx context.Context, toolName string, args map[string]any) (map[string]any, error)
}

// StatefulToolFunc 让普通函数可以作为 StatefulToolExecutor 使用，便于测试和轻量集成。
type StatefulToolFunc func(ctx context.Context, toolName string, args map[string]any) (map[string]any, error)

func (f StatefulToolFunc) ExecuteStatefulTool(ctx context.Context, toolName string, args map[string]any) (map[string]any, error) {
	return f(ctx, toolName, args)
}

// MechanicalOutputBinding 描述工具成功后如何把返回值写回机械态。
type MechanicalOutputBinding struct {
	Key        string `json:"key"`
	FromResult string `json:"from_result"`
	Scope      string `json:"scope,omitempty"`
}

// StatefulToolCall 是一次可审计工具调用：输入可引用 STATE.xxx，输出可绑定新机械真值。
type StatefulToolCall struct {
	ToolName     string                    `json:"tool_name"`
	Args         map[string]any            `json:"args,omitempty"`
	Outputs      []MechanicalOutputBinding `json:"outputs,omitempty"`
	Event        string                    `json:"event,omitempty"`
	Reason       string                    `json:"reason,omitempty"`
	EvidenceRefs []string                  `json:"evidence_refs,omitempty"`
	NextAction   string                    `json:"next_action,omitempty"`
}

// StatefulToolResult 返回工具原始结果和本次调用实际读写过的状态坐标。
type StatefulToolResult struct {
	ToolResult map[string]any `json:"tool_result,omitempty"`
	StateDelta StateDelta     `json:"state_delta,omitempty"`
}

// StatefulToolOrchestrator 把叙事态 STATE 引用、机械态真值、真实工具和账本事件串起来。
type StatefulToolOrchestrator struct {
	Tracker      *LongHorizonTracker
	Executor     StatefulToolExecutor
	RuntimeLayer string
}

// Execute 在工具执行前解析 STATE.xxx，执行成功后写回机械态并追加 Read/Write 账本事件。
func (o StatefulToolOrchestrator) Execute(ctx context.Context, call StatefulToolCall) (StatefulToolResult, error) {
	if o.Tracker == nil {
		return StatefulToolResult{}, errors.New("long horizon tracker is required")
	}
	if o.Executor == nil {
		return StatefulToolResult{}, errors.New("stateful tool executor is required")
	}
	toolName := strings.TrimSpace(call.ToolName)
	if toolName == "" {
		return StatefulToolResult{}, errors.New("tool name is required")
	}

	resolvedArgs, reads, err := o.resolveArgs(ctx, call.Args)
	if err != nil {
		return StatefulToolResult{}, err
	}
	toolResult, err := o.Executor.ExecuteStatefulTool(ctx, toolName, resolvedArgs)
	if err != nil {
		return StatefulToolResult{}, err
	}
	writes, err := o.writeOutputBindings(ctx, toolName, call.Outputs, toolResult)
	if err != nil {
		return StatefulToolResult{}, err
	}
	stateDelta := StateDelta{Read: sortedStateRefs(reads), Write: sortedStateRefs(writes)}
	if err := o.appendToolEvent(ctx, call, toolName, stateDelta); err != nil {
		return StatefulToolResult{}, err
	}
	return StatefulToolResult{ToolResult: toolResult, StateDelta: stateDelta}, nil
}

func (o StatefulToolOrchestrator) resolveArgs(ctx context.Context, args map[string]any) (map[string]any, map[string]struct{}, error) {
	reads := map[string]struct{}{}
	resolved := make(map[string]any, len(args))
	for key, value := range args {
		resolvedValue, err := o.resolveArgValue(ctx, value, reads)
		if err != nil {
			return nil, nil, err
		}
		resolved[key] = resolvedValue
	}
	return resolved, reads, nil
}

func (o StatefulToolOrchestrator) resolveArgValue(ctx context.Context, value any, reads map[string]struct{}) (any, error) {
	switch typed := value.(type) {
	case string:
		if !isMechanicalStateRef(typed) {
			return typed, nil
		}
		key := normalizeMechanicalStateKey(typed)
		mechanicalValue, err := o.Tracker.ResolveMechanicalValue(ctx, typed)
		if err != nil {
			return nil, err
		}
		reads["STATE."+key] = struct{}{}
		return mechanicalValue.Value, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := o.resolveArgValue(ctx, item, reads)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			resolved, err := o.resolveArgValue(ctx, item, reads)
			if err != nil {
				return nil, err
			}
			out = append(out, resolved)
		}
		return out, nil
	default:
		return value, nil
	}
}

func (o StatefulToolOrchestrator) writeOutputBindings(ctx context.Context, toolName string, bindings []MechanicalOutputBinding, result map[string]any) (map[string]struct{}, error) {
	writes := map[string]struct{}{}
	for _, binding := range bindings {
		key := normalizeMechanicalStateKey(binding.Key)
		if key == "" {
			return nil, errors.New("mechanical output key is required")
		}
		resultKey := strings.TrimSpace(binding.FromResult)
		if resultKey == "" {
			resultKey = key
		}
		value, ok := result[resultKey]
		if !ok {
			return nil, fmt.Errorf("tool result field %s not found", resultKey)
		}
		if err := o.Tracker.WriteMechanicalValue(ctx, MechanicalValue{
			Key:          key,
			Scope:        binding.Scope,
			ValueRef:     "STATE." + key,
			Value:        value,
			Provider:     toolName,
			RuntimeLayer: o.runtimeLayer(toolName),
			Trust:        TrustToolOutput,
		}); err != nil {
			return nil, err
		}
		writes["STATE."+key] = struct{}{}
	}
	return writes, nil
}

func (o StatefulToolOrchestrator) appendToolEvent(ctx context.Context, call StatefulToolCall, toolName string, stateDelta StateDelta) error {
	event := strings.TrimSpace(call.Event)
	if event == "" {
		event = "执行工具: " + toolName
	}
	evidenceRefs := normalizeStringList(call.EvidenceRefs)
	if len(evidenceRefs) == 0 {
		evidenceRefs = []string{"tool:" + toolName}
	}
	return o.Tracker.AppendEvent(ctx, AppendProgressEventRequest{
		Event:        event,
		Decision:     DecisionApprove,
		Reason:       call.Reason,
		EvidenceRefs: evidenceRefs,
		StateDelta:   stateDelta,
		NextAction:   call.NextAction,
	})
}

func (o StatefulToolOrchestrator) runtimeLayer(toolName string) string {
	layer := strings.TrimSpace(o.RuntimeLayer)
	if layer == "" {
		return "stateful_tool_orchestrator." + toolName
	}
	return layer + "." + toolName
}

func isMechanicalStateRef(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "STATE.") && normalizeMechanicalStateKey(value) != ""
}

func sortedStateRefs(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
