package cot

import (
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultAgentName        = "cot_verifier_agent"
	defaultAgentDescription = "Chain-of-Thought verifier agent implemented with Go Eino ADK."
	defaultMaxTokens        = 4096
	defaultVerifyMaxTokens  = 512
)

// ReasoningStep 是一条可审计的显性推理步骤，confidence 用来暴露当前步骤的自评风险。
type ReasoningStep struct {
	StepNumber int     `json:"step_number"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

// ChainOfThought 保存推理步骤和最终答案，对齐用户给出的 Python dataclass 结构。
type ChainOfThought struct {
	Steps       []ReasoningStep `json:"steps"`
	FinalAnswer string          `json:"final_answer"`
}

// AddStep 追加一步显性推理；未传 confidence 时沿用 Python 版本的默认 1.0。
func (c *ChainOfThought) AddStep(content string, confidence ...float64) {
	if c == nil {
		return
	}
	score := 1.0
	if len(confidence) > 0 {
		score = confidence[0]
	}
	c.Steps = append(c.Steps, ReasoningStep{
		StepNumber: len(c.Steps) + 1,
		Content:    strings.TrimSpace(content),
		Confidence: normalizeConfidence(score),
	})
}

// WeakestStep 返回置信度最低的步骤，便于 agent 优先复核最薄弱的判断。
func (c ChainOfThought) WeakestStep() *ReasoningStep {
	if len(c.Steps) == 0 {
		return nil
	}
	weakest := c.Steps[0]
	for _, step := range c.Steps[1:] {
		if step.Confidence < weakest.Confidence {
			weakest = step
		}
	}
	return &weakest
}

// VerificationIssue 表示 verifier 对某一步给出的 INVALID 反馈。
type VerificationIssue struct {
	Step  int    `json:"step"`
	Issue string `json:"issue"`
}

// Request 是 ADK 和本地调用共用的输入，既支持单条问题，也支持消息列表。
type Request struct {
	Question string
	Messages []*schema.Message
}

// Response 汇总最终答案、推理链、校验结果和最弱步骤，便于命令行和 ADK customized output 读取。
type Response struct {
	Question    string              `json:"question"`
	Chain       ChainOfThought      `json:"chain"`
	Issues      []VerificationIssue `json:"issues"`
	WeakestStep *ReasoningStep      `json:"weakest_step,omitempty"`
	Verified    bool                `json:"verified"`
	FinalAnswer string              `json:"final_answer"`
}

// Config 汇总 CoT Agent 的模型、场景和生成长度配置。
type Config struct {
	Name            string
	Description     string
	Scenario        string
	Model           model.BaseChatModel
	VerifierModel   model.BaseChatModel
	MaxTokens       int
	VerifyMaxTokens int
}

// normalizeConfidence 把模型或调用方传入的置信度限制在 0 到 1，避免 weakest step 被异常值干扰。
func normalizeConfidence(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
