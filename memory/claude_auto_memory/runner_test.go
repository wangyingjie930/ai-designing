package claudeautomemory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// queryMemorySelector 在问题包含“偏好”时选择 private 候选，用于证明下一轮生效。
type queryMemorySelector struct{}

// Select 根据测试问题返回索引中的第一个 private 引用。
func (q *queryMemorySelector) Select(_ context.Context, query string, manifest MemoryManifest) ([]MemoryRef, error) {
	if strings.Contains(query, "偏好") && len(manifest.Private) > 0 {
		return []MemoryRef{manifest.Private[0].Ref}, nil
	}
	return nil, nil
}

// recordingChatAgent 记录主 Agent 收到的记忆上下文，并返回稳定回答。
type recordingChatAgent struct {
	memoryContexts []string
	err            error
}

// Generate 保存注入上下文，模拟独立主 Agent 回答。
func (a *recordingChatAgent) Generate(_ context.Context, _ []ConversationMessage, memoryContext string) (string, error) {
	a.memoryContexts = append(a.memoryContexts, memoryContext)
	if a.err != nil {
		return "", a.err
	}
	return "这是主 Agent 的回答。", nil
}

// TestRunnerNewMemoryAffectsNextTurnNotCurrentTurn 验证回答后写入不会倒灌当前轮，但下一轮可召回。
func TestRunnerNewMemoryAffectsNextTurnNotCurrentTurn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extractorModel := &fakeMemoryExtractor{batches: [][]MemoryCandidate{{{
		Type: MemoryTypeUser, Scope: ScopePrivate, Topic: "comment-style",
		Description: "用户偏好中文注释", Content: "新增函数写中文注释。",
	}}, {}}}
	extractor, err := NewExtractor(store, extractorModel)
	if err != nil {
		t.Fatal(err)
	}
	recaller, err := NewRecaller(store, &queryMemorySelector{})
	if err != nil {
		t.Fatal(err)
	}
	chat := &recordingChatAgent{}
	runner, err := NewRunner(recaller, chat, extractor)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.RunTurn(context.Background(), "记住我喜欢中文注释")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Recalled) != 0 || chat.memoryContexts[0] != "" {
		t.Fatalf("first = %+v contexts = %+v", first, chat.memoryContexts)
	}
	drained, err := runner.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(drained.Written) != 1 {
		t.Fatalf("drained = %+v", drained)
	}
	second, err := runner.RunTurn(context.Background(), "我写代码有什么偏好？")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Recalled) != 1 || !strings.Contains(chat.memoryContexts[1], "中文注释") {
		t.Fatalf("second = %+v contexts = %+v", second, chat.memoryContexts)
	}
	if _, err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestRunnerReturnsBeforeBlockedExtraction 验证后台提取卡住时，主回答仍然立即返回。
func TestRunnerReturnsBeforeBlockedExtraction(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newBlockingExtractorModel()
	extractor, _ := NewExtractor(store, model)
	recaller, _ := NewRecaller(store, &fakeMemorySelector{})
	runner, err := NewRunner(recaller, &recordingChatAgent{}, extractor)
	if err != nil {
		t.Fatal(err)
	}
	type turnOutcome struct {
		result TurnResult
		err    error
	}
	returned := make(chan turnOutcome, 1)
	go func() {
		result, runErr := runner.RunTurn(context.Background(), "请记住这个偏好")
		returned <- turnOutcome{result: result, err: runErr}
	}()

	select {
	case outcome := <-returned:
		if outcome.err != nil || outcome.result.Answer == "" {
			t.Fatalf("outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		close(model.release)
		<-returned
		t.Fatal("RunTurn waited for blocked extraction")
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("background extraction did not start")
	}
	close(model.release)
	if _, err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestRunnerSkipsExtractionWhenMainAgentFails 验证主回答失败时不会错误推进提取生命周期。
func TestRunnerSkipsExtractionWhenMainAgentFails(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extractorModel := &fakeMemoryExtractor{}
	extractor, _ := NewExtractor(store, extractorModel)
	recaller, _ := NewRecaller(store, &fakeMemorySelector{})
	runner, err := NewRunner(recaller, &recordingChatAgent{err: errors.New("main model unavailable")}, extractor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), "问题"); err == nil {
		t.Fatal("expected main agent error")
	}
	if len(extractorModel.inputs) != 0 || extractor.Cursor() != 0 || len(runner.History()) != 0 {
		t.Fatalf("extract inputs = %+v cursor = %d history = %+v", extractorModel.inputs, extractor.Cursor(), runner.History())
	}
}
