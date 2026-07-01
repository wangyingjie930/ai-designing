package replay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ExperienceReplay 持有 L0 traces、L1 reflections 和 L2 lessons 三条并行经验流。
type ExperienceReplay struct {
	name             string
	description      string
	model            model.BaseChatModel
	traces           []ExecutionTrace
	reflections      []Reflection
	lessons          []Lesson
	lastExtractionAt int
	batch            int
	window           int
	spike            float64
	maxTokens        int
}

// NewExperienceReplay 创建经验回放核心，默认抽取节奏对齐 Python 示例。
func NewExperienceReplay(_ context.Context, config Config) (*ExperienceReplay, error) {
	if config.Model == nil {
		return nil, errors.New("model is required")
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultAgentName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = defaultAgentDescription
	}
	batch := config.Batch
	if batch <= 0 {
		batch = defaultBatch
	}
	window := config.Window
	if window <= 0 {
		window = defaultWindow
	}
	spike := config.Spike
	if spike <= 0 {
		spike = defaultSpike
	}
	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	return &ExperienceReplay{
		name:        name,
		description: description,
		model:       config.Model,
		batch:       batch,
		window:      window,
		spike:       spike,
		maxTokens:   maxTokens,
	}, nil
}

// Name 返回 ADK 和 trace 展示用的 agent 名称。
func (r *ExperienceReplay) Name() string {
	if r == nil {
		return ""
	}
	return r.name
}

// Description 返回 ADK agent 描述。
func (r *ExperienceReplay) Description() string {
	if r == nil {
		return ""
	}
	return r.description
}

// RecordTrace 对齐 Python record_trace：追加轨迹、失败反思、按失败率 spike 抽取跨任务 lesson。
func (r *ExperienceReplay) RecordTrace(ctx context.Context, trace ExecutionTrace) (RecordResult, error) {
	if r == nil {
		return RecordResult{}, errors.New("experience replay is nil")
	}
	trace = normalizeTrace(trace)
	r.traces = append(r.traces, trace)
	result := RecordResult{Recorded: trace}
	if trace.Outcome == OutcomeFailure {
		text, err := r.reflectOnFailure(ctx, trace)
		if err != nil {
			return RecordResult{}, err
		}
		reflection := Reflection{Task: trace.Task, Reflection: text, Error: trace.Error}
		r.reflections = append(r.reflections, reflection)
		result.Reflection = &reflection
	}
	if r.shouldExtractLessons() {
		lessons, err := r.extractCrossTaskInsights(ctx)
		if err != nil {
			return RecordResult{}, err
		}
		r.lessons = append(r.lessons, lessons...)
		r.lastExtractionAt = len(r.traces)
		result.NewLessons = lessons
	}
	return result, nil
}

// GetRelevantExperience 按 lesson effectiveness 排序，生成可直接放进下一次任务 prompt 的经验提示。
func (r *ExperienceReplay) GetRelevantExperience(_ string, maxItems int) string {
	if r == nil || len(r.lessons) == 0 {
		return ""
	}
	if maxItems <= 0 {
		maxItems = defaultMaxLessons
	}
	lessons := make([]Lesson, len(r.lessons))
	copy(lessons, r.lessons)
	sort.SliceStable(lessons, func(i, j int) bool {
		return lessons[i].Effectiveness() > lessons[j].Effectiveness()
	})
	lines := make([]string, 0, min(maxItems, len(lessons)))
	for i, lesson := range lessons {
		if i >= maxItems {
			break
		}
		if strings.TrimSpace(lesson.Insight) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("INSIGHT (%.0f%%): %s", lesson.Effectiveness()*100, strings.TrimSpace(lesson.Insight)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Review these lessons from past experience:\n\n" + strings.Join(lines, "\n")
}

// UpdateLessonEffectiveness 记录 lesson 被应用后的成败反馈，让排序逐步贴近真实收益。
func (r *ExperienceReplay) UpdateLessonEffectiveness(lessonIndex int, taskSucceeded bool) error {
	if r == nil {
		return errors.New("experience replay is nil")
	}
	if lessonIndex < 0 || lessonIndex >= len(r.lessons) {
		return fmt.Errorf("lesson index %d out of range", lessonIndex)
	}
	r.lessons[lessonIndex].ApplicationCount++
	if taskSucceeded {
		r.lessons[lessonIndex].SuccessWhenApplied++
	}
	return nil
}

// Advise 为新任务生成带历史经验的执行提醒。
func (r *ExperienceReplay) Advise(ctx context.Context, req Request) (*Response, error) {
	if r == nil {
		return nil, errors.New("experience replay is nil")
	}
	task := strings.TrimSpace(req.Task)
	if task == "" {
		task = taskFromMessages(req.Messages)
	}
	if task == "" {
		return nil, errors.New("task is required")
	}
	maxLessons := req.MaxLessons
	if maxLessons <= 0 {
		maxLessons = defaultMaxLessons
	}
	experience := r.GetRelevantExperience(task, maxLessons)
	advice, err := callModel(ctx, r.model, advisorSystemPrompt(), buildAdvicePrompt(task, experience), r.maxTokens)
	if err != nil {
		return nil, err
	}
	return &Response{
		Task:             task,
		RecordedTraces:   len(r.traces),
		ReflectionCount:  len(r.reflections),
		LessonCount:      len(r.lessons),
		ExperiencePrompt: experience,
		Advice:           advice,
	}, nil
}

// Traces 返回 L0 轨迹副本，避免调用方误改内部经验状态。
func (r *ExperienceReplay) Traces() []ExecutionTrace {
	if r == nil {
		return nil
	}
	copied := make([]ExecutionTrace, len(r.traces))
	copy(copied, r.traces)
	return copied
}

// Reflections 返回 L1 反思副本，便于测试和命令输出统计。
func (r *ExperienceReplay) Reflections() []Reflection {
	if r == nil {
		return nil
	}
	copied := make([]Reflection, len(r.reflections))
	copy(copied, r.reflections)
	return copied
}

// Lessons 返回 L2 lesson 副本，便于 cmd 输出和效果反馈入口使用。
func (r *ExperienceReplay) Lessons() []Lesson {
	if r == nil {
		return nil
	}
	copied := make([]Lesson, len(r.lessons))
	copy(copied, r.lessons)
	return copied
}

// LastExtractionAt 暴露上次 L2 抽取时的 trace 数量，便于验证抽取节奏。
func (r *ExperienceReplay) LastExtractionAt() int {
	if r == nil {
		return 0
	}
	return r.lastExtractionAt
}

// reflectOnFailure 调用模型生成单次失败的 ROOT_CAUSE、LESSON 和 PREVENTION。
func (r *ExperienceReplay) reflectOnFailure(ctx context.Context, trace ExecutionTrace) (string, error) {
	return callModel(ctx, r.model, reflectionSystemPrompt(), buildReflectionPrompt(trace), r.maxTokens)
}

// extractCrossTaskInsights 从最近失败样本中抽取可复用的跨任务 lesson。
func (r *ExperienceReplay) extractCrossTaskInsights(ctx context.Context) ([]Lesson, error) {
	recent := recentTraces(r.traces, 20)
	failures := make([]ExecutionTrace, 0, len(recent))
	sourceTasks := make([]string, 0, 10)
	for _, trace := range recent {
		if trace.Outcome != OutcomeFailure {
			continue
		}
		failures = append(failures, trace)
		if len(sourceTasks) < 10 && strings.TrimSpace(trace.Task) != "" {
			sourceTasks = append(sourceTasks, trace.Task)
		}
	}
	if len(failures) < 2 {
		return nil, nil
	}
	text, err := callModel(ctx, r.model, lessonSystemPrompt(), buildLessonPrompt(failures), r.maxTokens)
	if err != nil {
		return nil, err
	}
	return parseLessons(text, sourceTasks), nil
}

// shouldExtractLessons 实现 Python 示例里的批量阈值、双窗口和失败率 spike 判断。
func (r *ExperienceReplay) shouldExtractLessons() bool {
	newCount := len(r.traces) - r.lastExtractionAt
	if newCount < r.batch || len(r.traces) < 2*r.window {
		return false
	}
	recent := r.traces[len(r.traces)-r.window:]
	prior := r.traces[len(r.traces)-2*r.window : len(r.traces)-r.window]
	return failureRate(recent) > failureRate(prior)*r.spike
}

// callModel 统一封装 Eino BaseChatModel.Generate，并在调用点设置 token 上限。
func callModel(ctx context.Context, chatModel model.BaseChatModel, systemPrompt string, userPrompt string, maxTokens int) (string, error) {
	if chatModel == nil {
		return "", errors.New("chat model is required")
	}
	messages := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, schema.SystemMessage(systemPrompt))
	}
	messages = append(messages, schema.UserMessage(userPrompt))
	opts := make([]model.Option, 0, 1)
	if maxTokens > 0 {
		opts = append(opts, model.WithMaxTokens(maxTokens))
	}
	resp, err := chatModel.Generate(ctx, messages, opts...)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return strings.TrimSpace(resp.Content), nil
}

// normalizeTrace 清理输入轨迹，保证 outcome 和步骤文本可用于后续 prompt。
func normalizeTrace(trace ExecutionTrace) ExecutionTrace {
	trace.Task = strings.TrimSpace(trace.Task)
	trace.Error = strings.TrimSpace(trace.Error)
	if trace.Outcome == "" {
		trace.Outcome = OutcomeSuccess
	}
	steps := make([]Step, 0, len(trace.Steps))
	for _, step := range trace.Steps {
		step.Action = strings.TrimSpace(step.Action)
		step.Observation = strings.TrimSpace(step.Observation)
		if step.Action == "" && step.Observation == "" {
			continue
		}
		steps = append(steps, step)
	}
	trace.Steps = steps
	return trace
}

// recentTraces 返回最后 n 条轨迹；不足 n 条时返回全部。
func recentTraces(traces []ExecutionTrace, n int) []ExecutionTrace {
	if n <= 0 || len(traces) <= n {
		return traces
	}
	return traces[len(traces)-n:]
}

// failureRate 计算窗口内失败占比，空窗口按 0 处理。
func failureRate(traces []ExecutionTrace) float64 {
	if len(traces) == 0 {
		return 0
	}
	failures := 0
	for _, trace := range traces {
		if trace.Outcome == OutcomeFailure {
			failures++
		}
	}
	return float64(failures) / float64(len(traces))
}

// taskFromMessages 把 ADK 多消息输入压成当前复盘任务。
func taskFromMessages(messages []*schema.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := messageText(message)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n")
}

// messageText 读取普通文本消息；多模态消息只提取 text part，避免把二进制内容放进复盘提示。
func messageText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	parts := make([]string, 0, len(message.UserInputMultiContent))
	for _, part := range message.UserInputMultiContent {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}
