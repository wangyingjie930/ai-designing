package claudeautomemory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// staticChatModel 依次返回预设文本并保留每次模型输入。
type staticChatModel struct {
	contents []string
	inputs   [][]*schema.Message
}

// Generate 返回下一条预设 assistant 消息。
func (m *staticChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	index := len(m.inputs) - 1
	if index >= len(m.contents) {
		return nil, fmt.Errorf("missing scripted response %d", index)
	}
	return schema.AssistantMessage(m.contents[index], nil), nil
}

// Stream 满足 BaseChatModel；自动记忆适配器只使用非流式结构化调用。
func (m *staticChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// TestExtractionPromptKeepsTypeAndScopeAsModelDecisions 验证 prompt 明确四类、两域和唯一硬约束。
func TestExtractionPromptKeepsTypeAndScopeAsModelDecisions(t *testing.T) {
	prompt := extractionSystemPrompt()
	for _, want := range []string{"user", "feedback", "project", "reference", "private", "team", "user 类型永远写入 private"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

// TestLLMExtractorParsesFencedJSONArray 验证提取适配器支持模型常见的 fenced JSON。
func TestLLMExtractorParsesFencedJSONArray(t *testing.T) {
	fake := &staticChatModel{contents: []string{"```json\n[{\"type\":\"project\",\"scope\":\"team\",\"topic\":\"tool-schema\",\"description\":\"工具 schema 约定\",\"content\":\"参数要写描述\"}]\n```"}}
	extractor, err := NewLLMExtractor(fake)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := extractor.Extract(context.Background(), []ConversationMessage{{Role: RoleUser, Content: "团队约定"}})
	if err != nil || len(candidates) != 1 || candidates[0].Scope != ScopeTeam {
		t.Fatalf("candidates = %+v, err = %v", candidates, err)
	}
	if !strings.Contains(fake.inputs[0][0].Content, autoMemoryExtractMarker) {
		t.Fatalf("system prompt = %s", fake.inputs[0][0].Content)
	}
}

// TestLLMSelectorRejectsInvalidScopeAndCapsAtFive 验证选择适配器先清理结构化结果再交给存储层。
func TestLLMSelectorRejectsInvalidScopeAndCapsAtFive(t *testing.T) {
	content := `[{"scope":"global","topic":"bad"},{"scope":"private","topic":"a"},{"scope":"team","topic":"b"},{"scope":"private","topic":"c"},{"scope":"team","topic":"d"},{"scope":"private","topic":"e"},{"scope":"team","topic":"f"}]`
	fake := &staticChatModel{contents: []string{content}}
	selector, err := NewLLMSelector(fake)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := selector.Select(context.Background(), "问题", MemoryManifest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != maxRecalledMemories || refs[0].Topic != "a" || refs[4].Topic != "e" {
		t.Fatalf("refs = %+v", refs)
	}
}

// TestLLMChatAgentInjectsMemoryWithoutMutatingHistory 验证记忆作为独立系统上下文，不污染主历史。
func TestLLMChatAgentInjectsMemoryWithoutMutatingHistory(t *testing.T) {
	fake := &staticChatModel{contents: []string{"记得使用中文注释。"}}
	agent, err := NewLLMChatAgent(fake)
	if err != nil {
		t.Fatal(err)
	}
	history := []ConversationMessage{{Role: RoleUser, Content: "我的偏好是什么？"}}
	answer, err := agent.Generate(context.Background(), history, "<memory scope=\"private\">中文注释</memory>")
	if err != nil || answer == "" {
		t.Fatalf("answer = %q, err = %v", answer, err)
	}
	if len(history) != 1 {
		t.Fatalf("history was mutated: %+v", history)
	}
	joined := joinSchemaMessageContent(fake.inputs[0])
	if !strings.Contains(joined, autoMemoryMainMarker) || !strings.Contains(joined, "<memory_context>") || !strings.Contains(joined, "中文注释") {
		t.Fatalf("model input = %s", joined)
	}
}

// TestLLMSessionSummarizerUsesIsolatedPrompt 验证 Session 摘要不会复用长期记忆提取指令。
func TestLLMSessionSummarizerUsesIsolatedPrompt(t *testing.T) {
	summary := strings.Replace(defaultSessionMemoryTemplate, "# 当前状态\n", "# 当前状态\n正在接入会话摘要。\n", 1)
	fake := &staticChatModel{contents: []string{summary}}
	summarizer, err := NewLLMSessionSummarizer(fake)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := summarizer.Summarize(context.Background(), defaultSessionMemoryTemplate, []ConversationMessage{
		NewConversationMessage(RoleUser, "开始接入会话摘要"),
		NewConversationMessage(RoleAssistant, "收到"),
	})
	if err != nil || updated != summary {
		t.Fatalf("updated = %q err = %v", updated, err)
	}
	joined := joinSchemaMessageContent(fake.inputs[0])
	if !strings.Contains(joined, sessionMemoryUpdateMarker) || strings.Contains(joined, autoMemoryExtractMarker) {
		t.Fatalf("model input = %s", joined)
	}
}

// TestLLMSessionSummarizerStripsMarkdownFence 验证常见模型围栏不会污染持久化摘要。
func TestLLMSessionSummarizerStripsMarkdownFence(t *testing.T) {
	summary := strings.Replace(defaultSessionMemoryTemplate, "# 当前状态\n", "# 当前状态\n正在更新。\n", 1)
	fake := &staticChatModel{contents: []string{"```markdown\n" + summary + "```"}}
	summarizer, err := NewLLMSessionSummarizer(fake)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := summarizer.Summarize(context.Background(), defaultSessionMemoryTemplate, []ConversationMessage{
		NewConversationMessage(RoleUser, "更新摘要"), NewConversationMessage(RoleAssistant, "收到"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != summary {
		t.Fatalf("updated = %q", updated)
	}
}

// joinSchemaMessageContent 拼接模型消息正文，便于测试隔离 prompt 是否存在。
func joinSchemaMessageContent(messages []*schema.Message) string {
	var contents []string
	for _, message := range messages {
		if message != nil {
			contents = append(contents, message.Content)
		}
	}
	return strings.Join(contents, "\n")
}
