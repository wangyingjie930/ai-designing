package claudeautomemory

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Runner 固定召回、主回答和回答后提取的业务顺序，并持有纯净会话历史。
type Runner struct {
	recaller  *Recaller
	chat      ChatAgent
	extractor *Extractor
	history   []ConversationMessage
	mu        sync.Mutex
}

// NewRunner 装配自动记忆核心闭环，三个阶段缺一不可。
func NewRunner(recaller *Recaller, chat ChatAgent, extractor *Extractor) (*Runner, error) {
	if recaller == nil {
		return nil, errors.New("memory recaller is required")
	}
	if chat == nil {
		return nil, errors.New("main chat agent is required")
	}
	if extractor == nil {
		return nil, errors.New("memory extractor is required")
	}
	return &Runner{recaller: recaller, chat: chat, extractor: extractor}, nil
}

// RunTurn 执行 Recall -> Main Agent -> Extract，使新记忆从下一轮开始生效。
func (r *Runner) RunTurn(ctx context.Context, userInput string) (TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return TurnResult{}, errors.New("user input is required")
	}
	// 业务边界一：只为当前问题召回相关长期记忆，不把全部记忆塞入模型。
	recall := r.recaller.Recall(ctx, userInput)
	pending := append([]ConversationMessage(nil), r.history...)
	pending = append(pending, ConversationMessage{Role: RoleUser, Content: userInput})
	// 业务边界二：主 Agent 看不到提取器 prompt、候选索引和存储维护过程。
	answer, err := r.chat.Generate(ctx, pending, recall.Context)
	if err != nil {
		return TurnResult{}, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return TurnResult{}, errors.New("main chat agent returned an empty answer")
	}
	r.history = append(pending, ConversationMessage{Role: RoleAssistant, Content: answer})
	// 业务边界三：回答完成后再独立提取，所以本轮新记忆只会影响下一轮。
	extraction := r.extractor.ExtractNew(ctx, r.history)
	warnings := append([]error(nil), recall.Warnings...)
	warnings = append(warnings, extraction.Warnings...)
	return TurnResult{
		Answer: answer, Recalled: recall.Records, Written: extraction.Written, Warnings: warnings,
	}, nil
}

// History 返回会话历史副本，避免调用方把记忆维护消息写回主上下文。
func (r *Runner) History() []ConversationMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConversationMessage(nil), r.history...)
}
