package critique

import (
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultAgentName        = "activity_critique_agent"
	defaultAgentDescription = "Generator-critic reflection agent for activity plans and marketing copy."
	defaultMaxTokens        = 4096
	defaultCriticMaxTokens  = 1024
	defaultMaxIterations    = 3
	defaultQualityThreshold = 0.9
)

// CritiqueResult 保存 critic 对当前活动方案的结构化评审结果。
type CritiqueResult struct {
	Approved    bool     `json:"approved"`
	Issues      []string `json:"issues"`
	Suggestions []string `json:"suggestions"`
	Score       float64  `json:"score"`
}

// FeedbackText 把问题和建议整理成下一轮生成器可直接使用的反馈文本。
func (r CritiqueResult) FeedbackText() string {
	parts := make([]string, 0, 2+len(r.Issues)+len(r.Suggestions))
	if len(r.Issues) > 0 {
		parts = append(parts, "Issues found:")
		for _, issue := range r.Issues {
			if strings.TrimSpace(issue) != "" {
				parts = append(parts, "  - "+strings.TrimSpace(issue))
			}
		}
	}
	if len(r.Suggestions) > 0 {
		parts = append(parts, "Suggestions:")
		for _, suggestion := range r.Suggestions {
			if strings.TrimSpace(suggestion) != "" {
				parts = append(parts, "  - "+strings.TrimSpace(suggestion))
			}
		}
	}
	if len(parts) == 0 {
		return "No issues found."
	}
	return strings.Join(parts, "\n")
}

// IterationRecord 记录每一轮生成和批评的摘要，便于 cmd 输出和 trace 观察收敛过程。
type IterationRecord struct {
	Iteration    int            `json:"iteration"`
	Score        float64        `json:"score"`
	Approved     bool           `json:"approved"`
	Output       string         `json:"output,omitempty"`
	ToolFeedback string         `json:"tool_feedback,omitempty"`
	Critique     CritiqueResult `json:"critique"`
}

// Request 是 ADK 和本地调用共用的输入，支持单条任务文本或消息列表。
type Request struct {
	Task     string
	Messages []*schema.Message
	ToolFn   ToolFeedbackFunc
}

// Response 汇总最终输出、迭代次数、最终评分和收敛状态。
type Response struct {
	Task         string            `json:"task"`
	Output       string            `json:"output"`
	Iterations   int               `json:"iterations"`
	FinalScore   float64           `json:"final_score"`
	Converged    bool              `json:"converged"`
	History      []IterationRecord `json:"history"`
	LastCritique CritiqueResult    `json:"last_critique"`
}

// Config 汇总 generator-critic loop 的模型、阈值和生成长度配置。
type Config struct {
	Name             string
	Description      string
	GeneratorModel   model.BaseChatModel
	CriticModel      model.BaseChatModel
	ToolFn           ToolFeedbackFunc
	MaxIterations    int
	QualityThreshold float64
	MaxTokens        int
	CriticMaxTokens  int
}

// ToolFeedbackFunc 表示外部确定性检查工具，用来补充 critic 的事实输入。
type ToolFeedbackFunc func(output string) string

// normalizeScore 把 critic 分数限制在 0 到 1，避免异常 JSON 影响收敛判断。
func normalizeScore(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
