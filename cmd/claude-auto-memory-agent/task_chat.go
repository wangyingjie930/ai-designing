package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	claudeautomemory "ai-designing/memory/claude_auto_memory"
	claudetasklist "ai-designing/memory/claude_tasklist"
)

// taskRunner 隔离命令层与 Task Agent 的具体实现，只保留完整上下文驱动一次工具循环的能力。
type taskRunner interface {
	Run(context.Context, []*schema.Message) (claudetasklist.AgentResult, error)
}

// taskChatAgent 把 Auto Memory 的 Context View 转为 Eino 消息，并把任务工具计数带回主会话。
type taskChatAgent struct {
	runner taskRunner
}

// Generate 保留真实对话角色，把长期记忆和 Compact 摘要作为系统上下文交给 Task Agent。
func (a *taskChatAgent) Generate(ctx context.Context, messages []claudeautomemory.ConversationMessage, memoryContext string) (claudeautomemory.ChatResponse, error) {
	if a == nil || a.runner == nil {
		return claudeautomemory.ChatResponse{}, errors.New("task chat agent is not initialized")
	}
	if len(messages) == 0 {
		return claudeautomemory.ChatResponse{}, errors.New("task chat messages are empty")
	}

	taskMessages, err := buildTaskMessages(messages, memoryContext)
	if err != nil {
		return claudeautomemory.ChatResponse{}, err
	}
	result, err := a.runner.Run(ctx, taskMessages)
	if err != nil {
		return claudeautomemory.ChatResponse{}, err
	}
	return claudeautomemory.ChatResponse{
		Content: result.Content, ToolCallCount: result.ToolCallCount,
	}, nil
}

// buildTaskMessages 只转换当前 Context View；Task Agent 不额外保存会话历史，避免 Resume 后出现两份上下文真相。
func buildTaskMessages(messages []claudeautomemory.ConversationMessage, memoryContext string) ([]*schema.Message, error) {
	taskMessages := make([]*schema.Message, 0, len(messages)+1)
	if memoryContext = strings.TrimSpace(memoryContext); memoryContext != "" {
		taskMessages = append(taskMessages, schema.SystemMessage("<memory_context>\n"+memoryContext+"\n</memory_context>"))
	}

	for index, message := range messages {
		content := strings.TrimSpace(message.Content)
		if !message.Kind.Valid() {
			return nil, fmt.Errorf("message %d has invalid kind %q", index, message.Kind)
		}
		if content == "" {
			return nil, fmt.Errorf("message %d content is empty", index)
		}
		if message.Kind == claudeautomemory.MessageKindCompactSummary {
			// Compact Summary 是派生恢复上下文，不能伪装成一条真实用户消息。
			taskMessages = append(taskMessages, schema.SystemMessage(content))
			continue
		}
		if !message.Role.Valid() {
			return nil, fmt.Errorf("message %d has invalid role %q", index, message.Role)
		}
		switch message.Role {
		case claudeautomemory.RoleUser:
			taskMessages = append(taskMessages, schema.UserMessage(content))
		case claudeautomemory.RoleAssistant:
			taskMessages = append(taskMessages, schema.AssistantMessage(content, nil))
		}
	}
	return taskMessages, nil
}
