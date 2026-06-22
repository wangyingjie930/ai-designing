package hypothesis

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultAgentName          = "iterative_hypothesis_agent"
	defaultAgentDescription   = "Iterative hypothesis testing agent implemented with Go Eino ADK."
	defaultMaxIterations      = 2
	defaultMaxHypotheses      = 2
	defaultMaxEvidence        = 1
	defaultPlannerMaxTokens   = 1024
	defaultEvidenceMaxTokens  = 1024
	defaultEvaluatorMaxTokens = 512
)

// HypothesisStatus 表示单个候选解释在反证循环中的状态。
type HypothesisStatus string

const (
	StatusProposed     HypothesisStatus = "proposed"
	StatusTesting      HypothesisStatus = "testing"
	StatusConfirmed    HypothesisStatus = "confirmed"
	StatusFalsified    HypothesisStatus = "falsified"
	StatusInconclusive HypothesisStatus = "inconclusive"
)

// EvidenceEffect 表示一条证据对目标假设的作用。
type EvidenceEffect string

const (
	EffectSupports EvidenceEffect = "supports"
	EffectRefutes  EvidenceEffect = "refutes"
	EffectNeutral  EvidenceEffect = "neutral"
)

// Evidence 是证据生成器收集到的一条可审计证据。
type Evidence struct {
	Description        string         `json:"description"`
	Source             string         `json:"source"`
	Effect             EvidenceEffect `json:"effect"`
	TargetHypothesisID string         `json:"target_hypothesis_id"`
	Timestamp          string         `json:"timestamp"`
}

// Hypothesis 是当前问题的一个候选解释，保存先验、后验、状态和证据轨迹。
type Hypothesis struct {
	ID               string           `json:"id"`
	Description      string           `json:"description"`
	Prior            float64          `json:"prior"`
	Posterior        float64          `json:"posterior"`
	Status           HypothesisStatus `json:"status"`
	Evidence         []Evidence       `json:"evidence"`
	FalsifiedBy      string           `json:"falsified_by,omitempty"`
	CreatedIteration int              `json:"created_iteration"`
}

// TreeSnapshot 是假设树的可序列化视图。
type TreeSnapshot struct {
	Hypotheses     []Hypothesis `json:"hypotheses"`
	ActiveCount    int          `json:"active_count"`
	ConfirmedCount int          `json:"confirmed_count"`
	SurvivorCount  int          `json:"survivor_count"`
}

// Proposal 是 planner 为当前问题提出的候选解释。
type Proposal struct {
	Description string  `json:"description"`
	Prior       float64 `json:"prior"`
}

// EvidenceCandidate 是 generator 为某个假设找到的待评估证据。
type EvidenceCandidate struct {
	Description string `json:"description"`
	Source      string `json:"source"`
}

// Evaluation 是 evaluator 对一条证据和目标假设关系的判断。
type Evaluation struct {
	Effect         EvidenceEffect `json:"effect"`
	PosteriorDelta float64        `json:"posterior_delta"`
}

// PlannerFunc 提出新假设，初始轮次给出候选集，后续轮次可按新证据补充替代解释。
type PlannerFunc func(ctx context.Context, problem string, existing []Hypothesis, iteration int) ([]Proposal, error)

// GeneratorFunc 为单个活跃假设收集证据，重点寻找能反证该假设的信息。
type GeneratorFunc func(ctx context.Context, hypothesis Hypothesis) ([]EvidenceCandidate, error)

// EvaluatorFunc 判断证据支持、反驳还是不影响目标假设。
type EvaluatorFunc func(ctx context.Context, hypothesis Hypothesis, evidence EvidenceCandidate) (Evaluation, error)

// LoopOutcome 描述一次假设循环是否收敛、是否需要人工介入以及退出原因。
type LoopOutcome struct {
	Converged      bool   `json:"converged"`
	NeedsHITL      bool   `json:"needs_hitl"`
	ConfirmedID    string `json:"confirmed_id,omitempty"`
	IterationsUsed int    `json:"iterations_used"`
	Reason         string `json:"reason"`
}

// Request 是 ADK 和本地调用共用的输入，既支持单条问题，也支持消息列表。
type Request struct {
	Problem  string
	Messages []*schema.Message
}

// Response 汇总最终答案、假设树和循环结果，ADK customized output 会保留这个结构。
type Response struct {
	Problem     string       `json:"problem"`
	Scenario    string       `json:"scenario,omitempty"`
	Tree        TreeSnapshot `json:"tree"`
	Outcome     LoopOutcome  `json:"outcome"`
	FinalAnswer string       `json:"final_answer"`
}

// Config 汇总 hypothesis agent 的模型、角色函数、场景和循环上限。
type Config struct {
	Name               string
	Description        string
	Scenario           string
	Model              model.BaseChatModel
	PlannerModel       model.BaseChatModel
	GeneratorModel     model.BaseChatModel
	EvaluatorModel     model.BaseChatModel
	Planner            PlannerFunc
	Generator          GeneratorFunc
	Evaluator          EvaluatorFunc
	MaxIterations      int
	MaxHypotheses      int
	MaxEvidence        int
	PlannerMaxTokens   int
	EvidenceMaxTokens  int
	EvaluatorMaxTokens int
}
