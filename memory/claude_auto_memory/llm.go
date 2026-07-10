package claudeautomemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// LLMExtractor 使用独立 prompt 把新增对话转换为结构化记忆候选。
type LLMExtractor struct {
	model model.BaseChatModel
}

// NewLLMExtractor 创建真实模型提取适配器。
func NewLLMExtractor(chatModel model.BaseChatModel) (*LLMExtractor, error) {
	if chatModel == nil {
		return nil, errors.New("chat model is required")
	}
	return &LLMExtractor{model: chatModel}, nil
}

// Extract 调用隔离的提取模型上下文，不把维护消息写入主会话。
func (e *LLMExtractor) Extract(ctx context.Context, messages []ConversationMessage) ([]MemoryCandidate, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("encode extraction messages: %w", err)
	}
	response, err := e.model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(extractionSystemPrompt()),
		schema.UserMessage("请从以下新增对话中提取长期记忆：\n" + string(payload)),
	})
	if err != nil {
		return nil, fmt.Errorf("extract memories: %w", err)
	}
	var candidates []MemoryCandidate
	if err := decodeJSONPayload(messageContent(response), &candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

// LLMSelector 使用当前问题和双索引选择少量相关主题。
type LLMSelector struct {
	model model.BaseChatModel
}

// NewLLMSelector 创建真实模型召回选择适配器。
func NewLLMSelector(chatModel model.BaseChatModel) (*LLMSelector, error) {
	if chatModel == nil {
		return nil, errors.New("chat model is required")
	}
	return &LLMSelector{model: chatModel}, nil
}

// Select 解析模型引用，丢弃非法 scope 和空 topic，并在模型边界截断到五项。
func (s *LLMSelector) Select(ctx context.Context, query string, manifest MemoryManifest) ([]MemoryRef, error) {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode memory manifest: %w", err)
	}
	response, err := s.model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(selectionSystemPrompt()),
		schema.UserMessage("当前用户问题：\n" + strings.TrimSpace(query) + "\n\n候选 manifest：\n" + string(payload)),
	})
	if err != nil {
		return nil, fmt.Errorf("select memories: %w", err)
	}
	var raw []MemoryRef
	if err := decodeJSONPayload(messageContent(response), &raw); err != nil {
		return nil, err
	}
	refs := make([]MemoryRef, 0, min(len(raw), maxRecalledMemories))
	for _, ref := range raw {
		ref.Topic = strings.TrimSpace(ref.Topic)
		if !ref.Scope.Valid() || ref.Topic == "" {
			continue
		}
		refs = append(refs, ref)
		if len(refs) == maxRecalledMemories {
			break
		}
	}
	return refs, nil
}

// LLMChatAgent 使用主回答 prompt 和可选记忆上下文调用真实聊天模型。
type LLMChatAgent struct {
	model model.BaseChatModel
}

// NewLLMChatAgent 创建只负责业务回答的模型适配器。
func NewLLMChatAgent(chatModel model.BaseChatModel) (*LLMChatAgent, error) {
	if chatModel == nil {
		return nil, errors.New("chat model is required")
	}
	return &LLMChatAgent{model: chatModel}, nil
}

// Generate 把召回结果放在独立边界中，并保持调用方会话切片不变。
func (a *LLMChatAgent) Generate(ctx context.Context, messages []ConversationMessage, memoryContext string) (string, error) {
	input := []*schema.Message{schema.SystemMessage(mainAgentSystemPrompt())}
	if strings.TrimSpace(memoryContext) != "" {
		input = append(input, schema.SystemMessage("<memory_context>\n"+strings.TrimSpace(memoryContext)+"\n</memory_context>"))
	}
	for _, message := range messages {
		switch message.Role {
		case RoleUser:
			input = append(input, schema.UserMessage(message.Content))
		case RoleAssistant:
			input = append(input, schema.AssistantMessage(message.Content, nil))
		default:
			return "", fmt.Errorf("unsupported conversation role %q", message.Role)
		}
	}
	response, err := a.model.Generate(ctx, input)
	if err != nil {
		return "", fmt.Errorf("generate main answer: %w", err)
	}
	content := messageContent(response)
	if content == "" {
		return "", errors.New("chat model returned empty content")
	}
	return content, nil
}

// decodeJSONPayload 兼容纯 JSON 和常见 Markdown fenced JSON 输出。
func decodeJSONPayload(content string, target any) error {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		firstNewline := strings.IndexByte(text, '\n')
		lastFence := strings.LastIndex(text, "```")
		if firstNewline < 0 || lastFence <= firstNewline {
			return errors.New("invalid fenced JSON")
		}
		text = strings.TrimSpace(text[firstNewline+1 : lastFence])
	}
	if err := json.Unmarshal([]byte(text), target); err != nil {
		return fmt.Errorf("decode model JSON: %w", err)
	}
	return nil
}

// messageContent 安全读取模型消息正文，统一处理 nil 和空白响应。
func messageContent(message *schema.Message) string {
	if message == nil {
		return ""
	}
	return strings.TrimSpace(message.Content)
}
