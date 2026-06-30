package critique

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// GeneratorCriticLoop 把“生成 -> 批评 -> 带反馈再生成”封装成活动方案质量迭代主流程。
type GeneratorCriticLoop struct {
	name             string
	description      string
	generatorModel   model.BaseChatModel
	criticModel      model.BaseChatModel
	maxIterations    int
	qualityThreshold float64
	maxTokens        int
	criticMaxTokens  int
	defaultToolFn    ToolFeedbackFunc
	history          []IterationRecord
}

// NewGeneratorCriticLoop 创建活动方案质量迭代 Agent，critic model 默认复用 generator model。
func NewGeneratorCriticLoop(_ context.Context, config Config) (*GeneratorCriticLoop, error) {
	if config.GeneratorModel == nil {
		return nil, errors.New("generator model is required")
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultAgentName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = defaultAgentDescription
	}
	criticModel := config.CriticModel
	if criticModel == nil {
		criticModel = config.GeneratorModel
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}
	qualityThreshold := config.QualityThreshold
	if qualityThreshold <= 0 {
		qualityThreshold = defaultQualityThreshold
	}
	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	criticMaxTokens := config.CriticMaxTokens
	if criticMaxTokens <= 0 {
		criticMaxTokens = defaultCriticMaxTokens
	}
	return &GeneratorCriticLoop{
		name:             name,
		description:      description,
		generatorModel:   config.GeneratorModel,
		criticModel:      criticModel,
		maxIterations:    maxIterations,
		qualityThreshold: normalizeScore(qualityThreshold),
		maxTokens:        maxTokens,
		criticMaxTokens:  criticMaxTokens,
		defaultToolFn:    config.ToolFn,
	}, nil
}

// Name 返回 ADK 和 trace 展示用的 agent 名称。
func (a *GeneratorCriticLoop) Name() string {
	if a == nil {
		return ""
	}
	return a.name
}

// Description 返回 ADK agent 描述。
func (a *GeneratorCriticLoop) Description() string {
	if a == nil {
		return ""
	}
	return a.description
}

// Generate 对齐 Python generate：把任务和可选反馈上下文交给生成器模型。
func (a *GeneratorCriticLoop) Generate(ctx context.Context, task string, contextText string) (string, error) {
	if a == nil {
		return "", errors.New("generator-critic loop is nil")
	}
	return callModel(ctx, a.generatorModel, generatorSystemPrompt(), buildGeneratePrompt(task, contextText), a.maxTokens)
}

// Critique 对齐 Python critique：评审当前方案，并解析 critic 的结构化 JSON。
func (a *GeneratorCriticLoop) Critique(ctx context.Context, task string, output string, toolFeedback string) (CritiqueResult, error) {
	if a == nil {
		return CritiqueResult{}, errors.New("generator-critic loop is nil")
	}
	text, err := callModel(ctx, a.criticModel, criticSystemPrompt(), buildCritiquePrompt(task, output, toolFeedback), a.criticMaxTokens)
	if err != nil {
		return CritiqueResult{}, err
	}
	return ParseCritiqueResult(text)
}

// Refine 运行核心业务闭环：首轮生成后反复批评和修正，直到达标或达到上限。
func (a *GeneratorCriticLoop) Refine(ctx context.Context, req Request) (*Response, error) {
	if a == nil {
		return nil, errors.New("generator-critic loop is nil")
	}
	task := strings.TrimSpace(req.Task)
	if task == "" {
		task = taskFromMessages(req.Messages)
	}
	if task == "" {
		return nil, errors.New("task is required")
	}
	output, err := a.Generate(ctx, task, "")
	if err != nil {
		return nil, err
	}

	history := make([]IterationRecord, 0, a.maxIterations)
	var critique CritiqueResult
	for i := 0; i < a.maxIterations; i++ {
		toolFeedback := ""
		if req.ToolFn != nil {
			toolFeedback = strings.TrimSpace(req.ToolFn(output))
		}
		critique, err = a.Critique(ctx, task, output, toolFeedback)
		if err != nil {
			return nil, err
		}
		approved := critique.Approved || critique.Score >= a.qualityThreshold
		record := IterationRecord{
			Iteration:    i,
			Score:        critique.Score,
			Approved:     approved,
			Output:       output,
			ToolFeedback: toolFeedback,
			Critique:     critique,
		}
		history = append(history, record)
		a.history = append(a.history, record)
		if approved {
			return &Response{
				Task:         task,
				Output:       output,
				Iterations:   i + 1,
				FinalScore:   critique.Score,
				Converged:    true,
				History:      history,
				LastCritique: critique,
			}, nil
		}
		if i < a.maxIterations-1 {
			contextText := critique.FeedbackText()
			if toolFeedback != "" {
				contextText += "\nTool: " + toolFeedback
			}
			output, err = a.Generate(ctx, task, contextText)
			if err != nil {
				return nil, err
			}
		}
	}

	return &Response{
		Task:         task,
		Output:       output,
		Iterations:   a.maxIterations,
		FinalScore:   critique.Score,
		Converged:    false,
		History:      history,
		LastCritique: critique,
	}, nil
}

// UserMessage 把 Response 转成人可读的 ADK assistant 文本，详细结构仍保留在 customized output。
func (r *Response) UserMessage() string {
	if r == nil {
		return ""
	}
	lines := []string{strings.TrimSpace(r.Output)}
	lines = append(lines, "", fmt.Sprintf("迭代轮次：%d，最终分数：%.2f，是否收敛：%t", r.Iterations, r.FinalScore, r.Converged))
	if !r.Converged {
		lines = append(lines, "最后反馈：", r.LastCritique.FeedbackText())
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

// taskFromMessages 把 ADK 多消息输入压成当前活动需求，避免 agent 只支持单轮字符串。
func taskFromMessages(messages []*schema.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := messageText(message)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n")
}

// messageText 读取普通文本消息；多模态消息只提取 text part，避免把二进制内容放进 critic。
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
