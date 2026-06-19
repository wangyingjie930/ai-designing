package tot

import (
	"context"
	"math/rand"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const epsilon = 1e-6

// Method 表示 ReasoningAgent 支持的推理树搜索策略，名称保持和 AG2 reason_config 对齐。
type Method string

const (
	MethodBeamSearch Method = "beam_search"
	MethodMCTS       Method = "mcts"
	MethodLATS       Method = "lats"
	MethodDFS        Method = "dfs"
)

// AnswerApproach 表示 beam search 结束后如何把候选轨迹合成最终答案。
type AnswerApproach string

const (
	AnswerApproachPool AnswerApproach = "pool"
	AnswerApproachBest AnswerApproach = "best"
)

// ReasonConfig 对齐 AG2 reason_config，控制树搜索、评分、森林规模和中间执行。
type ReasonConfig struct {
	Method              Method
	MaxDepth            int
	BeamSize            int
	AnswerApproach      AnswerApproach
	BatchGrading        bool
	NSim                int
	ExplorationConstant float64
	ForestSize          int
	RatingScale         int
	InterimExecution    bool
}

// Config 汇总 ReasoningAgent 的 Eino 模型、scope 和可选代码执行依赖。
type Config struct {
	Name          string
	Description   string
	Scope         string
	SystemMessage string
	Model         model.BaseChatModel
	GraderModel   model.BaseChatModel
	ReasonConfig  ReasonConfig
	CodeExecutor  CodeExecutor
	Rand          *rand.Rand
}

// Request 是本地调用入口，既支持单条 prompt，也支持 ADK 传入的消息列表。
type Request struct {
	Prompt   string
	Messages []*schema.Message
}

// Response 返回最终答案，并保留本次推理树和森林候选，方便调试、可视化和数据集抽取。
type Response struct {
	Answer        string
	Prompt        string
	GroundTruth   string
	Method        Method
	Root          *ThinkNode
	ForestAnswers []string
}

// SFTSample 是从最佳轨迹抽取出的监督微调样本。
type SFTSample struct {
	Instruction string `json:"instruction"`
	Response    string `json:"response"`
}

// PreferencePair 是从兄弟节点对比中抽取出的 RLHF 偏好样本。
type PreferencePair struct {
	Instruction          string `json:"instruction"`
	Reflection           string `json:"reflection"`
	PreferredResponse    string `json:"preferred_response"`
	DispreferredResponse string `json:"dispreferred_response"`
}

// CodeExecutor 抽象 AG2 UserProxyAgent 的 Python 执行能力；默认不配置，避免本地不安全执行。
type CodeExecutor interface {
	ExecutePython(ctx context.Context, content string) (string, error)
}
