package claudeautomemory

import (
	"context"
	"sync"
	"testing"
	"time"
)

// blockingExtractorModel 只阻塞第一次提取，并记录每次实际收到的增量消息。
type blockingExtractorModel struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	inputs  [][]ConversationMessage
}

// newBlockingExtractorModel 创建可由测试精确释放的提取模型。
func newBlockingExtractorModel() *blockingExtractorModel {
	return &blockingExtractorModel{started: make(chan struct{}), release: make(chan struct{})}
}

// Extract 记录增量消息；第一次调用等待 release，后续 trailing 调用立即完成。
func (m *blockingExtractorModel) Extract(_ context.Context, messages []ConversationMessage) ([]MemoryCandidate, error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, append([]ConversationMessage(nil), messages...))
	call := len(m.inputs)
	m.mu.Unlock()
	if call == 1 {
		m.once.Do(func() { close(m.started) })
		<-m.release
	}
	return nil, nil
}

// Inputs 返回模型实际处理的增量批次副本。
func (m *blockingExtractorModel) Inputs() [][]ConversationMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	inputs := make([][]ConversationMessage, len(m.inputs))
	for index := range m.inputs {
		inputs[index] = append([]ConversationMessage(nil), m.inputs[index]...)
	}
	return inputs
}

// TestExtractionSchedulerScheduleDoesNotWaitForModel 验证提交只启动后台任务，不等待被阻塞的提取模型。
func TestExtractionSchedulerScheduleDoesNotWaitForModel(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newBlockingExtractorModel()
	extractor, err := NewExtractor(store, model)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewExtractionScheduler(extractor)
	if err != nil {
		t.Fatal(err)
	}

	returned := make(chan struct{})
	go func() {
		scheduler.Schedule(context.Background(), testConversation(2))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("Schedule blocked on the extraction model")
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("background extraction did not start")
	}
	close(model.release)
	if _, err := scheduler.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestExtractionSchedulerCoalescesBusyCallsToLatestSnapshot 验证忙时只保留最新快照并执行一次 trailing extraction。
func TestExtractionSchedulerCoalescesBusyCallsToLatestSnapshot(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newBlockingExtractorModel()
	extractor, _ := NewExtractor(store, model)
	scheduler, _ := NewExtractionScheduler(extractor)
	transcript := testConversation(6)

	scheduler.Schedule(context.Background(), transcript[:2])
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("first extraction did not start")
	}
	scheduler.Schedule(context.Background(), transcript[:4])
	scheduler.Schedule(context.Background(), transcript)
	close(model.release)
	drained, err := scheduler.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inputs := model.Inputs()
	if len(inputs) != 2 || len(inputs[0]) != 2 || len(inputs[1]) != 4 {
		t.Fatalf("incremental inputs = %+v", inputs)
	}
	if drained.Batches != 2 || drained.ProcessedMessages != 6 {
		t.Fatalf("drained = %+v", drained)
	}
}

// testConversation 构造角色交替的稳定对话快照。
func testConversation(count int) []ConversationMessage {
	messages := make([]ConversationMessage, 0, count)
	for index := 0; index < count; index++ {
		role := RoleUser
		if index%2 == 1 {
			role = RoleAssistant
		}
		messages = append(messages, NewConversationMessage(role, string(rune('A'+index))))
	}
	return messages
}
