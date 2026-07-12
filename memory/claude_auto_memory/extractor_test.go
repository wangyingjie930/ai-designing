package claudeautomemory

import (
	"context"
	"errors"
	"testing"
)

// fakeMemoryExtractor 按批次返回候选，用于验证增量游标而不访问外部模型。
type fakeMemoryExtractor struct {
	batches [][]MemoryCandidate
	inputs  [][]ConversationMessage
	err     error
}

// Extract 记录本次新增消息，并返回预设候选。
func (f *fakeMemoryExtractor) Extract(_ context.Context, messages []ConversationMessage) ([]MemoryCandidate, error) {
	f.inputs = append(f.inputs, append([]ConversationMessage(nil), messages...))
	if f.err != nil {
		return nil, f.err
	}
	index := len(f.inputs) - 1
	if index >= len(f.batches) {
		return nil, nil
	}
	return f.batches[index], nil
}

// TestExtractorProcessesOnlyNewMessages 验证成功后游标推进，下一次不重复提取旧消息。
func TestExtractorProcessesOnlyNewMessages(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeMemoryExtractor{batches: [][]MemoryCandidate{{{
		Type: MemoryTypeUser, Scope: ScopePrivate, Topic: "comment-style",
		Description: "用户偏好中文注释", Content: "新增代码写中文用途注释。",
	}}, {}}}
	extractor, err := NewExtractor(store, fake)
	if err != nil {
		t.Fatal(err)
	}
	history := []ConversationMessage{
		NewConversationMessage(RoleUser, "记住我喜欢中文注释"),
		NewConversationMessage(RoleAssistant, "好的"),
	}
	first := extractor.ExtractNew(context.Background(), history)
	if len(first.Written) != 1 || first.ProcessedMessages != 2 {
		t.Fatalf("first = %+v", first)
	}
	history = append(history,
		NewConversationMessage(RoleUser, "继续"),
		NewConversationMessage(RoleAssistant, "收到"),
	)
	second := extractor.ExtractNew(context.Background(), history)
	if second.ProcessedMessages != 2 || len(fake.inputs) != 2 || len(fake.inputs[1]) != 2 {
		t.Fatalf("second = %+v, inputs = %+v", second, fake.inputs)
	}
}

// TestExtractorUsesUUIDCursorAndIgnoresCompactSummary 验证压缩上下文不会污染长期记忆或破坏增量边界。
func TestExtractorUsesUUIDCursorAndIgnoresCompactSummary(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeMemoryExtractor{batches: [][]MemoryCandidate{{}}}
	extractor, err := NewExtractor(store, fake)
	if err != nil {
		t.Fatal(err)
	}
	first := NewConversationMessage(RoleUser, "记住中文注释")
	summary := NewConversationMessage(RoleUser, "当前正在处理临时任务")
	summary.Kind = MessageKindCompactSummary
	last := NewConversationMessage(RoleAssistant, "好的")
	result := extractor.ExtractNew(context.Background(), []ConversationMessage{first, summary, last})
	if result.ProcessedMessages != 2 || len(fake.inputs) != 1 || len(fake.inputs[0]) != 2 {
		t.Fatalf("result = %+v inputs = %+v", result, fake.inputs)
	}
	if extractor.Cursor() != last.ID {
		t.Fatalf("cursor = %q want %q", extractor.Cursor(), last.ID)
	}
}

// TestExtractorRetriesAfterModelFailure 验证模型失败不会吞掉尚未处理的消息。
func TestExtractorRetriesAfterModelFailure(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeMemoryExtractor{err: errors.New("model unavailable")}
	extractor, err := NewExtractor(store, fake)
	if err != nil {
		t.Fatal(err)
	}
	history := []ConversationMessage{NewConversationMessage(RoleUser, "记住这个")}
	result := extractor.ExtractNew(context.Background(), history)
	if result.ProcessedMessages != 0 || len(result.Warnings) != 1 || extractor.Cursor() != "" {
		t.Fatalf("result = %+v cursor = %q", result, extractor.Cursor())
	}
	fake.err = nil
	fake.batches = [][]MemoryCandidate{{}}
	retry := extractor.ExtractNew(context.Background(), history)
	if retry.ProcessedMessages != 1 || extractor.Cursor() != history[0].ID {
		t.Fatalf("retry = %+v cursor = %q", retry, extractor.Cursor())
	}
}

// TestExtractorKeepsValidCandidatesWhenOneCandidateFails 验证单条非法候选不会阻塞同批其他记忆。
func TestExtractorKeepsValidCandidatesWhenOneCandidateFails(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeMemoryExtractor{batches: [][]MemoryCandidate{{
		{Type: MemoryTypeUser, Scope: ScopeTeam, Topic: "bad", Description: "bad", Content: "bad"},
		{Type: MemoryTypeProject, Scope: ScopeTeam, Topic: "schema", Description: "工具约定", Content: "参数写描述。"},
	}}}
	extractor, err := NewExtractor(store, fake)
	if err != nil {
		t.Fatal(err)
	}
	result := extractor.ExtractNew(context.Background(), []ConversationMessage{NewConversationMessage(RoleUser, "团队约定")})
	if len(result.Written) != 1 || len(result.Warnings) != 1 || result.ProcessedMessages != 1 {
		t.Fatalf("result = %+v", result)
	}
}
