package replay

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestLessonEffectivenessUsesConfidenceThenFeedback 验证 lesson 未被应用时使用置信度，被应用后改用真实成功率。
func TestLessonEffectivenessUsesConfidenceThenFeedback(t *testing.T) {
	lesson := Lesson{Insight: "先确认报名入口是否可用", Confidence: 0.7}
	if got := lesson.Effectiveness(); got != 0.7 {
		t.Fatalf("effectiveness=%v, want 0.7", got)
	}
	lesson.ApplicationCount = 4
	lesson.SuccessWhenApplied = 3
	if got := lesson.Effectiveness(); got != 0.75 {
		t.Fatalf("effectiveness=%v, want 0.75", got)
	}
}

// TestRecordTraceReflectsFailureAndExtractsSpike 验证失败轨迹会触发 L1 反思，失败率 spike 后触发 L2 lesson 抽取。
func TestRecordTraceReflectsFailureAndExtractsSpike(t *testing.T) {
	core, err := NewExperienceReplay(context.Background(), Config{
		Model:  &fakeReplayModel{},
		Batch:  2,
		Window: 2,
		Spike:  1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	traces := []ExecutionTrace{
		{Task: "第一场直播活动", Outcome: OutcomeSuccess, Steps: []Step{{Action: "按计划发送海报"}}},
		{Task: "第二场社群转化", Outcome: OutcomeSuccess, Steps: []Step{{Action: "提前确认报名链路"}}},
		{Task: "第三场公开课", Outcome: OutcomeFailure, Error: "报名链接过期", Steps: []Step{{Action: "活动前一天才检查链接"}}},
		{Task: "第四场训练营", Outcome: OutcomeFailure, Error: "销售没有拿到高意向名单", Steps: []Step{{Action: "直播后才整理线索"}}},
	}
	var last RecordResult
	for _, trace := range traces {
		last, err = core.RecordTrace(context.Background(), trace)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(core.Reflections()) != 2 {
		t.Fatalf("reflections=%d, want 2", len(core.Reflections()))
	}
	if len(last.NewLessons) != 2 {
		t.Fatalf("new lessons=%d, want 2: %#v", len(last.NewLessons), last.NewLessons)
	}
	if !strings.Contains(last.NewLessons[0].Insight, "报名链路") {
		t.Fatalf("first lesson=%+v", last.NewLessons[0])
	}
	if got := core.LastExtractionAt(); got != len(traces) {
		t.Fatalf("last extraction=%d, want %d", got, len(traces))
	}
}

// TestGetRelevantExperienceSortsByEffectiveness 验证新任务会优先看到历史上真正有效的 lesson。
func TestGetRelevantExperienceSortsByEffectiveness(t *testing.T) {
	core, err := NewExperienceReplay(context.Background(), Config{Model: &fakeReplayModel{}})
	if err != nil {
		t.Fatal(err)
	}
	core.lessons = []Lesson{
		{Insight: "复盘文案风格", Confidence: 0.9, ApplicationCount: 5, SuccessWhenApplied: 1},
		{Insight: "活动前 24 小时检查报名链路和企微承接人", Confidence: 0.6, ApplicationCount: 4, SuccessWhenApplied: 4},
		{Insight: "补充直播后高意向名单分发动作", Confidence: 0.8},
	}
	text := core.GetRelevantExperience("下一场 AI 公开课转化复盘", 2)
	first := strings.Index(text, "活动前 24 小时")
	second := strings.Index(text, "补充直播后")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("experience not sorted by effectiveness:\n%s", text)
	}
	if strings.Contains(text, "复盘文案风格") {
		t.Fatalf("max_items not respected:\n%s", text)
	}
}

// TestADKRunnerReturnsReplayAdvice 验证 Eino ADK Runner 会把 relevant experience 注入最终建议并返回结构化结果。
func TestADKRunnerReturnsReplayAdvice(t *testing.T) {
	runner, core, err := NewRunner(context.Background(), Config{Model: &fakeReplayModel{}})
	if err != nil {
		t.Fatal(err)
	}
	core.lessons = []Lesson{{Insight: "活动前 24 小时检查报名链路", Confidence: 0.9}}
	iter := runner.Query(context.Background(), "请为下一场 AI 公开课生成复盘后的执行提醒。")
	var got *Response
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output == nil {
			continue
		}
		if response, ok := event.Output.CustomizedOutput.(*Response); ok {
			got = response
		}
	}
	if got == nil {
		t.Fatal("missing customized response")
	}
	if !strings.Contains(got.ExperiencePrompt, "活动前 24 小时") || !strings.Contains(got.Advice, "报名链路") {
		t.Fatalf("response=%+v", got)
	}
}

var _ adk.Agent = (*ADKAgent)(nil)

// fakeReplayModel 用固定文本模拟 L1 反思、L2 lesson 抽取和最终复盘建议。
type fakeReplayModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 按 prompt 类型返回可解析的模型文本，避免单元测试依赖真实大模型。
func (m *fakeReplayModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	user := ""
	if len(input) > 0 {
		user = input[len(input)-1].Content
	}
	switch {
	case strings.Contains(user, "Analyze this failed execution"):
		return schema.AssistantMessage("ROOT_CAUSE: 缺少提前检查\nLESSON: 关键链路要提前确认\nPREVENTION: 设置 D-1 检查清单", nil), nil
	case strings.Contains(user, "Find cross-task patterns"):
		return schema.AssistantMessage(strings.Join([]string{
			"INSIGHT: 活动前 24 小时检查报名链路和承接人。",
			"INSIGHT: 直播后立刻把高意向名单分发给销售。",
		}, "\n"), nil), nil
	case strings.Contains(user, "Review these lessons"):
		return schema.AssistantMessage("建议：先检查报名链路，再确认企微承接人和直播后名单分发。", nil), nil
	default:
		return nil, errors.New("unexpected replay prompt: " + user)
	}
}

// Stream 当前测试不使用流式输出，只满足 Eino BaseChatModel 接口。
func (m *fakeReplayModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}
