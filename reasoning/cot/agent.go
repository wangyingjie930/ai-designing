package cot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ReasoningAgent 用 Eino ChatModel 实现“生成推理链 -> 找最弱步骤 -> 逐步校验”的主流程。
type ReasoningAgent struct {
	name            string
	description     string
	scenario        string
	model           model.BaseChatModel
	verifierModel   model.BaseChatModel
	maxTokens       int
	verifyMaxTokens int
}

// NewReasoningAgent 创建 CoT verifier agent，并把 verifier model 默认指向主模型。
func NewReasoningAgent(_ context.Context, config Config) (*ReasoningAgent, error) {
	if config.Model == nil {
		return nil, errors.New("cot agent model is required")
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultAgentName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = defaultAgentDescription
	}
	verifierModel := config.VerifierModel
	if verifierModel == nil {
		verifierModel = config.Model
	}
	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	verifyMaxTokens := config.VerifyMaxTokens
	if verifyMaxTokens <= 0 {
		verifyMaxTokens = defaultVerifyMaxTokens
	}
	return &ReasoningAgent{
		name:            name,
		description:     description,
		scenario:        strings.TrimSpace(config.Scenario),
		model:           config.Model,
		verifierModel:   verifierModel,
		maxTokens:       maxTokens,
		verifyMaxTokens: verifyMaxTokens,
	}, nil
}

// Name 返回 ADK 和 trace 展示用的 agent 名称。
func (a *ReasoningAgent) Name() string {
	if a == nil {
		return ""
	}
	return a.name
}

// Description 返回 ADK agent 描述。
func (a *ReasoningAgent) Description() string {
	if a == nil {
		return ""
	}
	return a.description
}

// ReasonWithCOT 对齐 Python reason_with_cot：调用模型生成结构化 ChainOfThought。
func (a *ReasoningAgent) ReasonWithCOT(ctx context.Context, question string) (ChainOfThought, error) {
	if a == nil {
		return ChainOfThought{}, errors.New("cot agent is nil")
	}
	text, err := callModel(ctx, a.model, DefaultSystemPrompt(), buildReasonPrompt(question, a.scenario), a.maxTokens)
	if err != nil {
		return ChainOfThought{}, err
	}
	chain, err := ParseChain(text)
	if err != nil {
		return ChainOfThought{}, fmt.Errorf("parse reasoning chain: %w", err)
	}
	return chain, nil
}

// VerifyChain 对齐 Python verify_chain：逐步把原问题、prior 和当前 step 交给 verifier。
func (a *ReasoningAgent) VerifyChain(ctx context.Context, question string, chain ChainOfThought) ([]VerificationIssue, error) {
	if a == nil {
		return nil, errors.New("cot agent is nil")
	}
	issues := make([]VerificationIssue, 0)
	for i, step := range chain.Steps {
		prior := marshalChainForPrompt(chain.Steps[:i])
		reply, err := callModel(ctx, a.verifierModel, "", buildVerifyPrompt(question, prior, step), a.verifyMaxTokens)
		if err != nil {
			return nil, fmt.Errorf("verify step %d: %w", step.StepNumber, err)
		}
		if strings.Contains(strings.ToUpper(reply), "INVALID") {
			issues = append(issues, VerificationIssue{
				Step:  step.StepNumber,
				Issue: strings.TrimSpace(reply),
			})
		}
	}
	return issues, nil
}

// Solve 运行完整主流程，并返回最终答案、校验结果和 weakest step 摘要。
func (a *ReasoningAgent) Solve(ctx context.Context, req Request) (*Response, error) {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		question = questionFromMessages(req.Messages)
	}
	if question == "" {
		return nil, errors.New("question is required")
	}
	chain, err := a.ReasonWithCOT(ctx, question)
	if err != nil {
		return nil, err
	}
	issues, err := a.VerifyChain(ctx, question, chain)
	if err != nil {
		return nil, err
	}
	return &Response{
		Question:    question,
		Chain:       chain,
		Issues:      issues,
		WeakestStep: chain.WeakestStep(),
		Verified:    len(issues) == 0,
		FinalAnswer: chain.FinalAnswer,
	}, nil
}

// UserMessage 把 Response 转成人可读的 ADK assistant 文本，详细结构仍保留在 customized output。
func (r *Response) UserMessage() string {
	if r == nil {
		return ""
	}
	lines := []string{strings.TrimSpace(r.FinalAnswer)}
	if r.WeakestStep != nil {
		lines = append(lines, "", fmt.Sprintf("最弱步骤：Step %d，confidence=%.2f", r.WeakestStep.StepNumber, r.WeakestStep.Confidence))
	}
	if len(r.Issues) == 0 {
		lines = append(lines, "校验结果：所有步骤通过。")
	} else {
		lines = append(lines, fmt.Sprintf("校验结果：发现 %d 个问题。", len(r.Issues)))
		for _, issue := range r.Issues {
			lines = append(lines, fmt.Sprintf("- Step %d: %s", issue.Step, issue.Issue))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// callModel 统一封装 Eino BaseChatModel.Generate，并在调用点设置 token 上限。
func callModel(ctx context.Context, chatModel model.BaseChatModel, systemPrompt string, userPrompt string, maxTokens int) (string, error) {
	if chatModel == nil {
		return "", errors.New("chat model is required")
	}
	messages := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, schema.SystemMessage(systemPrompt))
	}
	messages = append(messages, schema.UserMessage(userPrompt))
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

// questionFromMessages 把 ADK 多消息输入压成当前问题，避免 agent 只支持单轮字符串。
func questionFromMessages(messages []*schema.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := messageText(message)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n")
}

// messageText 读取普通文本消息；多模态消息只提取 text part，避免把二进制内容放进推理链。
func messageText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	parts := make([]string, 0, len(message.UserInputMultiContent))
	for _, part := range message.UserInputMultiContent {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}
