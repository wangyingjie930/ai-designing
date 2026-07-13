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
	histories      [][]ConversationMessage
	err            error
}

// Generate 保存注入上下文，模拟独立主 Agent 回答。
func (a *recordingChatAgent) Generate(_ context.Context, messages []ConversationMessage, memoryContext string) (ChatResponse, error) {
	a.memoryContexts = append(a.memoryContexts, memoryContext)
	a.histories = append(a.histories, append([]ConversationMessage(nil), messages...))
	if a.err != nil {
		return ChatResponse{}, a.err
	}
	return ChatResponse{Content: "这是主 Agent 的回答。"}, nil
}

// newSessionRunnerForTest 装配共享 Transcript、Session Summary 和两个独立后台调度器。
func newSessionRunnerForTest(t *testing.T, root, sessionID string, resume bool, chat ChatAgent, extractorModel MemoryExtractor) (*Runner, *Extractor, *SessionStore) {
	t.Helper()
	autoStore, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	extractor, err := NewExtractor(autoStore, extractorModel)
	if err != nil {
		t.Fatal(err)
	}
	recaller, err := NewRecaller(autoStore, &fakeMemorySelector{})
	if err != nil {
		t.Fatal(err)
	}
	transcriptStore, err := NewTranscriptStore(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := NewSessionStore(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	config := lowSessionConfig()
	config.CompactTokens = 1
	config.MinimumRecentMessages = 1
	updater, err := NewSessionMemoryUpdater(sessionStore, &fakeSessionSummarizer{}, perMessageTokenEstimator(10), config)
	if err != nil {
		t.Fatal(err)
	}
	sessionScheduler, err := NewSessionScheduler(updater)
	if err != nil {
		t.Fatal(err)
	}
	compactor, err := NewSessionCompactor(sessionStore, sessionScheduler, perMessageTokenEstimator(10), config)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunnerWithSession(context.Background(), recaller, chat, extractor, RunnerSessionConfig{
		SessionID: sessionID, TranscriptStore: transcriptStore, Scheduler: sessionScheduler, Compactor: compactor, Resume: resume,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, extractor, sessionStore
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
	if len(extractorModel.inputs) != 0 || extractor.Cursor() != "" || len(runner.History()) != 0 {
		t.Fatalf("extract inputs = %+v cursor = %q history = %+v", extractorModel.inputs, extractor.Cursor(), runner.History())
	}
}

// TestRunnerWithSessionSeparatesTranscriptFromCompactedContext 验证 Compact 不会裁掉两个后台系统的事实输入。
func TestRunnerWithSessionSeparatesTranscriptFromCompactedContext(t *testing.T) {
	root := t.TempDir()
	extractorModel := &fakeMemoryExtractor{batches: [][]MemoryCandidate{{}, {}}}
	chat := &recordingChatAgent{}
	runner, _, _ := newSessionRunnerForTest(t, root, "session-1", false, chat, extractorModel)
	first, err := runner.RunTurn(context.Background(), "第一轮任务")
	if err != nil || first.Compacted {
		t.Fatalf("first = %+v err = %v", first, err)
	}
	if _, err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.WaitSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := runner.RunTurn(context.Background(), "继续任务")
	if err != nil || !second.Compacted {
		t.Fatalf("second = %+v err = %v", second, err)
	}
	if _, err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.WaitSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	transcript := runner.Transcript()
	contextMessages := runner.ContextMessages()
	if len(transcript) != 4 || len(contextMessages) >= len(transcript) || contextMessages[0].Kind != MessageKindCompactSummary {
		t.Fatalf("transcript = %+v context = %+v", transcript, contextMessages)
	}
	for _, message := range transcript {
		if message.Kind != MessageKindNormal {
			t.Fatalf("derived message leaked into transcript: %+v", message)
		}
	}
	if len(extractorModel.inputs) != 2 || len(extractorModel.inputs[0]) != 2 || len(extractorModel.inputs[1]) != 2 {
		t.Fatalf("auto memory inputs = %+v", extractorModel.inputs)
	}
	if len(chat.histories) != 2 || chat.histories[1][0].Kind != MessageKindCompactSummary {
		t.Fatalf("chat histories = %+v", chat.histories)
	}
}

// TestRunnerResumeRestoresSessionContext 验证同一 session-id 在新 Runner 中恢复完整 Transcript 和摘要上下文。
func TestRunnerResumeRestoresSessionContext(t *testing.T) {
	root := t.TempDir()
	first, _, _ := newSessionRunnerForTest(t, root, "resume-demo", false, &recordingChatAgent{}, &fakeMemoryExtractor{})
	if _, err := first.RunTurn(context.Background(), "当前任务是实现 Resume"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.WaitSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, _, _ := newSessionRunnerForTest(t, root, "resume-demo", true, &recordingChatAgent{}, &fakeMemoryExtractor{})
	if second.SessionID() != "resume-demo" || len(second.Transcript()) != 2 {
		t.Fatalf("session = %q transcript = %+v", second.SessionID(), second.Transcript())
	}
	contextMessages := second.ContextMessages()
	if len(contextMessages) == 0 || contextMessages[0].Kind != MessageKindCompactSummary {
		t.Fatalf("context = %+v", contextMessages)
	}
}

// TestRunnerResumeDoesNotReextractHistoricalTranscript 验证恢复会话只把新一轮交给 Auto Memory。
func TestRunnerResumeDoesNotReextractHistoricalTranscript(t *testing.T) {
	root := t.TempDir()
	first, _, _ := newSessionRunnerForTest(t, root, "resume-auto", false, &recordingChatAgent{}, &fakeMemoryExtractor{})
	if _, err := first.RunTurn(context.Background(), "历史任务"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.WaitSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	model := &fakeMemoryExtractor{batches: [][]MemoryCandidate{{}}}
	second, _, _ := newSessionRunnerForTest(t, root, "resume-auto", true, &recordingChatAgent{}, model)
	if _, err := second.RunTurn(context.Background(), "恢复后的新问题"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(model.inputs) != 1 || len(model.inputs[0]) != 2 {
		t.Fatalf("auto memory inputs = %+v", model.inputs)
	}
	if _, err := second.WaitSession(context.Background()); err != nil {
		t.Fatal(err)
	}
}
