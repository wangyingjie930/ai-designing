package compaction

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

const (
	promptMiddlewareName = "token_limit_compaction_middleware"
	promptMiddlewareType = "TokenLimitCompactionMiddleware"
)

var componentOfPromptMiddleware = components.Component("Middleware")

// PromptViewMiddleware 定义模型调用前处理 session 历史的中间件。
type PromptViewMiddleware interface {
	BuildPromptView(ctx context.Context, support SupportContext, turns []Turn) (PromptView, *CompactionEvent, error)
}

// TokenLimitCompactionMiddleware 在 token 超过动态阈值后才触发语义压缩。
type TokenLimitCompactionMiddleware struct {
	compactor *SupportCompactor
}

// TokenLimitCompactionInput 是中间件进入 CozeLoop/Eino callback 时的轻量输入。
type TokenLimitCompactionInput struct {
	SupportContext SupportContext
	TurnsBefore    int
}

// TokenLimitCompactionOutput 是中间件结束 CozeLoop/Eino callback 时的压缩决策摘要。
type TokenLimitCompactionOutput struct {
	TokenPressure       TokenPressure
	CompactionTriggered bool
	CompactionLevel     int
	TurnsBefore         int
	TurnsAfter          int
	TokensBefore        int
	TokensAfter         int
}

// NewTokenLimitCompactionMiddleware 创建按 token 压力触发的 prompt 中间件。
func NewTokenLimitCompactionMiddleware(config Config) *TokenLimitCompactionMiddleware {
	return &TokenLimitCompactionMiddleware{
		compactor: NewSupportCompactor(config),
	}
}

// BuildPromptView 根据 token 压力选择原始历史或压缩历史作为模型上下文。
func (m *TokenLimitCompactionMiddleware) BuildPromptView(ctx context.Context, support SupportContext, turns []Turn) (PromptView, *CompactionEvent, error) {
	if m == nil || m.compactor == nil {
		return PromptView{}, nil, errors.New("token limit compaction middleware requires a compactor")
	}

	// 这里手动接入 Eino callback，让 CozeLoop 能把普通 Go 中间件展示为 trace 节点。
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      promptMiddlewareName,
		Type:      promptMiddlewareType,
		Component: componentOfPromptMiddleware,
	})
	callbackCtx = callbacks.OnStart(callbackCtx, TokenLimitCompactionInput{
		SupportContext: support,
		TurnsBefore:    len(turns),
	})

	classified := m.compactor.classifyTurns(turns)
	pressure := m.compactor.tokenPressureForTotal(support, totalTokens(classified))
	if !pressure.ShouldCompact {
		callbacks.OnEnd(callbackCtx, tokenLimitCompactionOutput(pressure, nil, len(classified), len(classified)))
		return m.compactor.buildPromptView(support, classified, nil, pressure), nil, nil
	}

	compacted, event, err := m.compactor.compactClassified(callbackCtx, support, classified, pressure)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return PromptView{}, nil, err
	}
	callbacks.OnEnd(callbackCtx, tokenLimitCompactionOutput(pressure, event, len(classified), len(compacted)))
	return m.compactor.buildPromptView(support, compacted, event, pressure), event, nil
}

// tokenLimitCompactionOutput 只上报决策摘要，避免 trace 里塞入完整历史和 prompt。
func tokenLimitCompactionOutput(pressure TokenPressure, event *CompactionEvent, turnsBefore int, turnsAfter int) TokenLimitCompactionOutput {
	output := TokenLimitCompactionOutput{
		TokenPressure: pressure,
		TurnsBefore:   turnsBefore,
		TurnsAfter:    turnsAfter,
		TokensBefore:  pressure.TotalTokens,
		TokensAfter:   pressure.TotalTokens,
	}
	if event != nil {
		output.CompactionTriggered = true
		output.CompactionLevel = event.Level
		output.TokensBefore = event.TokensBefore
		output.TokensAfter = event.TokensAfter
	}
	return output
}
