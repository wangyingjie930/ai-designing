package compaction

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeChatModel 是测试用的 Eino ChatModel。
type fakeChatModel struct {
	mu        sync.Mutex
	inputs    [][]*schema.Message
	responses []string
}

// Generate 记录模型输入并返回预设回复。
func (m *fakeChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	content := "已记录，我会继续处理。"
	if len(m.responses) > 0 {
		content = m.responses[0]
		m.responses = m.responses[1:]
	}
	return schema.AssistantMessage(content, nil), nil
}

// Stream 返回单条流式消息，当前测试不依赖流模式。
func (m *fakeChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream response", nil)}), nil
}

// Inputs 返回模型收到的全部输入副本。
func (m *fakeChatModel) Inputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]*schema.Message, len(m.inputs))
	copy(out, m.inputs)
	return out
}
