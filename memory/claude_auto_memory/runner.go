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
	scheduler *ExtractionScheduler
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
	scheduler, err := NewExtractionScheduler(extractor)
	if err != nil {
		return nil, err
	}
	return &Runner{recaller: recaller, chat: chat, scheduler: scheduler}, nil
}

// RunTurn 执行 Recall -> Main Agent -> Schedule，提交后台抽取后立即返回主回答。
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
	// 业务边界三：只提交会话快照，后台模型和文件 I/O 不进入主回答延迟。
	r.scheduler.Schedule(ctx, r.history)
	return TurnResult{
		Answer: answer, Recalled: recall.Records, Warnings: append([]error(nil), recall.Warnings...),
	}, nil
}

// Drain 等待当前及 trailing 后台提取，供脚本模式、测试和进程退出阶段使用。
func (r *Runner) Drain(ctx context.Context) (DrainResult, error) {
	return r.scheduler.Drain(ctx)
}

// History 返回会话历史副本，避免调用方把记忆维护消息写回主上下文。
func (r *Runner) History() []ConversationMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConversationMessage(nil), r.history...)
}
