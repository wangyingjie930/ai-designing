package replay

import (
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultAgentName        = "experience_replay_agent"
	defaultAgentDescription = "Experience replay agent for recurring operational execution failures."
	defaultBatch            = 10
	defaultWindow           = 30
	defaultSpike            = 1.5
	defaultMaxTokens        = 1024
	defaultMaxLessons       = 5
)

// Outcome 表示一次执行轨迹的最终结果，当前只区分成功和失败两类。
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// Step 是 ExecutionTrace 内部的 L0 步骤单元，用来保留执行动作和可选观察。
type Step struct {
	Action      string `json:"action"`
	Observation string `json:"observation,omitempty"`
}

// ExecutionTrace 对齐 Python 版 L0：每次任务执行都会形成一条轨迹。
type ExecutionTrace struct {
	Task            string  `json:"task"`
	Steps           []Step  `json:"steps"`
	Outcome         Outcome `json:"outcome"`
	Error           string  `json:"error,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

// Reflection 保存单次失败后的 L1 反思文本，并保留任务和错误作为溯源字段。
type Reflection struct {
	Task       string `json:"task"`
	Reflection string `json:"reflection"`
	Error      string `json:"error,omitempty"`
}

// Lesson 对齐 Python 版 L2：跨任务提炼出的通用经验。
type Lesson struct {
	Insight            string   `json:"insight"`
	SourceTasks        []string `json:"source_tasks,omitempty"`
	Confidence         float64  `json:"confidence"`
	ApplicationCount   int      `json:"application_count"`
	SuccessWhenApplied int      `json:"success_when_applied"`
}

// Effectiveness 把历史应用反馈转成排序用软先验，未应用时回退到置信度。
func (l Lesson) Effectiveness() float64 {
	if l.ApplicationCount == 0 {
		return normalizeConfidence(l.Confidence)
	}
	return float64(l.SuccessWhenApplied) / float64(l.ApplicationCount)
}

// Config 汇总 replay 核心的模型、抽取节奏和 ADK 展示配置。
type Config struct {
	Name        string
	Description string
	Model       model.BaseChatModel
	Batch       int
	Window      int
	Spike       float64
	MaxTokens   int
}

// Request 是 ADK 和本地调用共用的输入，支持直接任务文本或 ADK 消息。
type Request struct {
	Task       string
	Messages   []*schema.Message
	MaxLessons int
}

// RecordResult 描述 record_trace 的副作用摘要，便于 cmd 和测试确认触发路径。
type RecordResult struct {
	Recorded   ExecutionTrace `json:"recorded"`
	Reflection *Reflection    `json:"reflection,omitempty"`
	NewLessons []Lesson       `json:"new_lessons,omitempty"`
}

// Response 汇总 replay agent 给新任务生成的经验提示和最终建议。
type Response struct {
	Task             string   `json:"task"`
	RecordedTraces   int      `json:"recorded_traces"`
	ReflectionCount  int      `json:"reflection_count"`
	LessonCount      int      `json:"lesson_count"`
	ExperiencePrompt string   `json:"experience_prompt"`
	Advice           string   `json:"advice"`
	NewLessons       []Lesson `json:"new_lessons,omitempty"`
}

// UserMessage 把结构化 replay 响应转成面向用户的最终 assistant 文本。
func (r *Response) UserMessage() string {
	if r == nil {
		return ""
	}
	lines := []string{}
	if strings.TrimSpace(r.ExperiencePrompt) != "" {
		lines = append(lines, strings.TrimSpace(r.ExperiencePrompt), "")
	}
	if strings.TrimSpace(r.Advice) != "" {
		lines = append(lines, strings.TrimSpace(r.Advice))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// normalizeConfidence 把 lesson 置信度限制在 0 到 1，空值按 Python 默认值 0.5 处理。
func normalizeConfidence(value float64) float64 {
	if value == 0 {
		return 0.5
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
