package claude_native_thinking

import (
	"context"
	"strings"

	jsonschema "github.com/eino-contrib/jsonschema"
)

// TrajectoryConfig 描述一条 Claude-Code 风格推理轨迹的稳定请求配置。
type TrajectoryConfig struct {
	SystemPrompt string
	Reasoning    ThinkingConfig
	SchemaName   string
	Schema       *jsonschema.Schema
	MaxTokens    int
}

// Trajectory 保存上一轮 Responses output item，并在下一轮请求里原样续传推理状态。
type Trajectory struct {
	config TrajectoryConfig
	output []ResponseOutputItem
}

// NewTrajectory 创建推理轨迹编排器；启用 thinking 时默认要求返回 encrypted reasoning。
func NewTrajectory(config TrajectoryConfig) *Trajectory {
	config.Reasoning = normalizeTrajectoryReasoning(config.Reasoning)
	return &Trajectory{config: config}
}

// NextRequest 生成下一轮请求；如果已有上一轮 output，就把 reasoning/message item 放回 input。
func (t *Trajectory) NextRequest(userPrompt string) GenerateRequest {
	if t == nil {
		return GenerateRequest{UserPrompt: strings.TrimSpace(userPrompt)}
	}
	req := GenerateRequest{
		SystemPrompt: t.config.SystemPrompt,
		UserPrompt:   strings.TrimSpace(userPrompt),
		Reasoning:    normalizeTrajectoryReasoning(t.config.Reasoning),
		SchemaName:   t.config.SchemaName,
		Schema:       t.config.Schema,
		MaxTokens:    t.config.MaxTokens,
	}
	if len(t.output) > 0 {
		req.PreviousOutputItems = copyOutputItems(t.output)
	}
	return req
}

// Record 接收一轮模型结果，保留合法的 reasoning/message 轨迹供下一轮续传。
func (t *Trajectory) Record(result *GenerateResult) {
	if t == nil || result == nil {
		return
	}
	t.output = NormalizeTrajectoryOutput(result.Output)
}

// RunTurn 执行一轮真实模型调用，成功后自动记录 output item。
func (t *Trajectory) RunTurn(ctx context.Context, client *ResponsesClient, userPrompt string) (*GenerateResult, error) {
	req := t.NextRequest(userPrompt)
	result, err := client.GenerateStepDrafts(ctx, req)
	if err != nil {
		return nil, err
	}
	t.Record(result)
	return result, nil
}

// OutputItems 返回当前轨迹状态的副本，调用方可持久化但不应打印 redacted thinking。
func (t *Trajectory) OutputItems() []ResponseOutputItem {
	if t == nil {
		return nil
	}
	return copyOutputItems(t.output)
}

// Reset 清空当前轨迹，等价于 Claude Code 在签名失效或换 key 后丢弃 protected thinking。
func (t *Trajectory) Reset() {
	if t == nil {
		return
	}
	t.output = nil
}

// NormalizeTrajectoryOutput 过滤不能安全续传的 thinking 尾块/孤儿块，保留完整 reasoning->message 轨迹。
func NormalizeTrajectoryOutput(output []ResponseOutputItem) []ResponseOutputItem {
	items := copyOutputItems(output)
	for len(items) > 0 && isReasoningItem(items[len(items)-1]) {
		items = items[:len(items)-1]
	}
	if !hasNonReasoningItem(items) {
		return nil
	}
	return items
}

func normalizeTrajectoryReasoning(config ThinkingConfig) ThinkingConfig {
	if config.Type == ThinkingDisabled {
		return config
	}
	if config.Type != "" || strings.TrimSpace(config.Effort) != "" || strings.TrimSpace(config.Summary) != "" {
		config.IncludeRedactedCOT = true
	}
	return config
}

func isReasoningItem(item ResponseOutputItem) bool {
	return item.Type == "reasoning"
}

func hasNonReasoningItem(items []ResponseOutputItem) bool {
	for _, item := range items {
		if !isReasoningItem(item) {
			return true
		}
	}
	return false
}

func copyOutputItems(items []ResponseOutputItem) []ResponseOutputItem {
	if len(items) == 0 {
		return nil
	}
	copied := make([]ResponseOutputItem, len(items))
	for i, item := range items {
		copied[i] = item
		copied[i].Summary = append([]ReasoningSummary(nil), item.Summary...)
		copied[i].Content = append([]ResponseContent(nil), item.Content...)
	}
	return copied
}
