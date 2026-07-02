package selfheal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ModelDiagnoser 把 Eino ChatModel 适配成自愈诊断节点。
type ModelDiagnoser struct {
	Model     model.BaseChatModel
	MaxTokens int
}

// ModelFixGenerator 把 Eino ChatModel 适配成自愈修复生成节点。
type ModelFixGenerator struct {
	Model     model.BaseChatModel
	MaxTokens int
}

// ModelCritic 把 Eino ChatModel 适配成自愈风险评审节点。
type ModelCritic struct {
	Model     model.BaseChatModel
	MaxTokens int
}

// Diagnose 让模型基于当前失败和历史尝试输出根因诊断。
func (n ModelDiagnoser) Diagnose(ctx context.Context, failure FailureSignal, history []HealAttempt) (string, error) {
	prompt, err := marshalNodePayload(map[string]any{
		"failure": failure,
		"history": history,
	})
	if err != nil {
		return "", err
	}
	return callModel(ctx, n.Model, diagnoserSystemPrompt(), prompt, n.MaxTokens)
}

// GenerateFix 让模型输出可被 applier 消费的结构化修复建议。
func (n ModelFixGenerator) GenerateFix(ctx context.Context, diagnosis string, failure FailureSignal, history []HealAttempt) (FixProposal, error) {
	prompt, err := marshalNodePayload(map[string]any{
		"diagnosis": diagnosis,
		"failure":   failure,
		"history":   history,
	})
	if err != nil {
		return FixProposal{}, err
	}
	text, err := callModel(ctx, n.Model, fixGeneratorSystemPrompt(), prompt, n.MaxTokens)
	if err != nil {
		return FixProposal{}, err
	}
	return ParseFixProposal(text)
}

// Review 让模型只判断当前修复是否应该阻断，不让模型决定应用或回滚。
func (n ModelCritic) Review(ctx context.Context, proposal FixProposal, failure FailureSignal, history []HealAttempt) (CriticVerdict, error) {
	prompt, err := marshalNodePayload(map[string]any{
		"proposal": proposal,
		"failure":  failure,
		"history":  history,
	})
	if err != nil {
		return CriticVerdict{}, err
	}
	text, err := callModel(ctx, n.Model, criticSystemPrompt(), prompt, n.MaxTokens)
	if err != nil {
		return CriticVerdict{}, err
	}
	return ParseCriticVerdict(text)
}

// callModel 统一封装 Eino BaseChatModel.Generate，并控制每个智能节点的 token 上限。
func callModel(ctx context.Context, chatModel model.BaseChatModel, systemPrompt string, userPrompt string, maxTokens int) (string, error) {
	if chatModel == nil {
		return "", errors.New("chat model is required")
	}
	messages := []*schema.Message{schema.SystemMessage(systemPrompt), schema.UserMessage(userPrompt)}
	opts := make([]model.Option, 0, 1)
	if maxTokens > 0 {
		opts = append(opts, model.WithMaxTokens(maxTokens))
	}
	resp, err := chatModel.Generate(ctx, messages, opts...)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return strings.TrimSpace(resp.Content), nil
}

// marshalNodePayload 生成给模型节点的结构化上下文，避免手写拼接字段漏项。
func marshalNodePayload(payload any) (string, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal node payload: %w", err)
	}
	return string(data), nil
}

// diagnoserSystemPrompt 说明诊断节点职责，只输出根因和可验证假设。
func diagnoserSystemPrompt() string {
	return strings.Join([]string{
		"你是自愈诊断器，负责根据失败信号和历史尝试判断业务失败的根因。",
		"你只做诊断，不要生成补丁，不要声明已经修复。",
		"输出简洁中文，包含根因、缺失约束和下一步修复方向。",
	}, "\n")
}

// fixGeneratorSystemPrompt 说明修复生成节点职责，要求返回结构化补丁建议。
func fixGeneratorSystemPrompt() string {
	return strings.Join([]string{
		"你是自愈修复生成器，负责把诊断转换成可应用的业务配置补丁。",
		"必须返回 JSON：{\"summary\":\"...\",\"fix_diff\":\"...\"}。",
		"fix_diff 要写清具体变更项，供确定性 applier 消费；不要声称已经应用。",
	}, "\n")
}

// criticSystemPrompt 说明风险评审节点职责，只决定是否阻断当前补丁。
func criticSystemPrompt() string {
	return strings.Join([]string{
		"你是自愈风险评审器，负责判断当前补丁是否应该被阻断。",
		"必须返回 JSON：{\"block\":false,\"reason\":\"...\"} 或 {\"block\":true,\"reason\":\"...\"}。",
		"只有补丁明显越权、缺少关键约束或扩大风险时才 block=true。",
	}, "\n")
}
