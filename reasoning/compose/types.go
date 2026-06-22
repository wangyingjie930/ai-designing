package compose

import (
	"ai-designing/reasoning/cot"
	"ai-designing/reasoning/hypothesis"
	"ai-designing/reasoning/tot"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultAgentName              = "adaptive_reasoning_agent"
	defaultAgentDescription       = "Adaptive reasoning router composed from direct response, CoT, ToT and hypothesis testing."
	defaultRouterMaxTokens        = 512
	defaultDirectMaxTokens        = 1024
	defaultConfirmationMaxTokens  = 512
	defaultConfidenceThreshold    = 0.75
	defaultBestPathThreshold      = 0.70
	defaultHypothesisMaxIteration = 2
)

// Complexity 表示复杂度路由器给出的任务难度分层。
type Complexity string

const (
	ComplexitySimple   Complexity = "simple"
	ComplexityModerate Complexity = "moderate"
	ComplexityComplex  Complexity = "complex"
)

// PathKind 表示本次处理实际经过的推理或验证路径。
type PathKind string

const (
	PathDirect     PathKind = "direct_response"
	PathCOT        PathKind = "chain_of_thought"
	PathTOT        PathKind = "parallel_exploration"
	PathHypothesis PathKind = "hypothesis_testing"
	PathEscalate   PathKind = "escalate_to_human"
)

// RouteDecision 保存复杂度路由器的结构化输出。
type RouteDecision struct {
	Complexity      Complexity `json:"complexity"`
	Confidence      float64    `json:"confidence"`
	Reason          string     `json:"reason"`
	RecommendedPath PathKind   `json:"recommended_path"`
}

// BestPathDecision 保存复杂问题多路径探索后的最佳路径确认结果。
type BestPathDecision struct {
	Confirmed  bool    `json:"confirmed"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// PathStep 是响应里的路径审计记录，用来证明图中的哪个节点真的运行过。
type PathStep struct {
	Kind    PathKind `json:"kind"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
}

// Request 是 ADK 和本地调用共用的输入，既支持单条问题，也支持消息列表。
type Request struct {
	Query    string
	Messages []*schema.Message
}

// Response 汇总路由、各子 Agent 结果、最终答复和是否升级给人。
type Response struct {
	Query            string                 `json:"query"`
	Scenario         string                 `json:"scenario,omitempty"`
	Decision         RouteDecision          `json:"decision"`
	BestPath         *BestPathDecision      `json:"best_path,omitempty"`
	Path             []PathStep             `json:"path"`
	DirectAnswer     string                 `json:"direct_answer,omitempty"`
	COT              *cot.Response          `json:"cot,omitempty"`
	TOT              *tot.Response          `json:"tot,omitempty"`
	Hypothesis       *hypothesis.Response   `json:"hypothesis,omitempty"`
	UsedHypothesis   bool                   `json:"used_hypothesis"`
	Escalated        bool                   `json:"escalated"`
	EscalationReason string                 `json:"escalation_reason,omitempty"`
	FinalAnswer      string                 `json:"final_answer"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// Config 汇总组合 Agent 的模型、子 Agent 配置和路由阈值。
type Config struct {
	Name                  string
	Description           string
	Scenario              string
	Model                 model.BaseChatModel
	RouterModel           model.BaseChatModel
	DirectModel           model.BaseChatModel
	ConfirmationModel     model.BaseChatModel
	COTConfig             cot.Config
	TOTConfig             tot.Config
	HypothesisConfig      hypothesis.Config
	RouterMaxTokens       int
	DirectMaxTokens       int
	ConfirmationMaxTokens int
	ConfidenceThreshold   float64
	BestPathThreshold     float64
}
