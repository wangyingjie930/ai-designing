package hypothesis

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// modelCallRole 描述一次模型调用在 Eino trace 中展示的业务角色名。
type modelCallRole struct {
	Name string
	Type string
}

var (
	plannerModelRole   = modelCallRole{Name: "hypothesis_planner", Type: "HypothesisPlanner"}
	generatorModelRole = modelCallRole{Name: "hypothesis_generator", Type: "HypothesisGenerator"}
	evaluatorModelRole = modelCallRole{Name: "hypothesis_evaluator", Type: "HypothesisEvaluator"}
)

// callRoleModel 在调用真实 ChatModel 前切换 RunInfo，让 trace 能区分 planner/generator/evaluator。
func callRoleModel(ctx context.Context, role modelCallRole, chatModel model.BaseChatModel, systemPrompt string, userPrompt string, maxTokens int) (string, error) {
	ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      role.Name,
		Type:      role.Type,
		Component: components.ComponentOfChatModel,
	})
	return callModel(ctx, chatModel, systemPrompt, userPrompt, maxTokens)
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
