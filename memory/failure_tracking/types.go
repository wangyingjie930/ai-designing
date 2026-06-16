package failuretracking

import (
	"context"
	"time"
)

const (
	defaultTopK           = 3
	defaultScoreThreshold = 0.7
)

// FailureEntry 是 Python dataclass FailureEntry 的 Go 版本，保存一次可复用失败经验。
type FailureEntry struct {
	Context      string   `json:"context"`
	ErrorType    string   `json:"error_type"`
	ErrorMessage string   `json:"error_message"`
	Fix          string   `json:"fix"`
	Heuristic    string   `json:"heuristic"`
	Tags         []string `json:"tags"`
	Timestamp    float64  `json:"timestamp"`
}

// RecordRequest 对应 Python record(context, error, fix) 的输入。
type RecordRequest struct {
	Context string `json:"context"`
	Error   string `json:"error"`
	Fix     string `json:"fix"`
}

// RecordResponse 返回本轮写入的失败经验。
type RecordResponse struct {
	Entry FailureEntry `json:"entry"`
}

// ConsultRequest 对应 Python consult(current_context, k) 的输入。
type ConsultRequest struct {
	CurrentContext string `json:"current_context"`
	TopK           int    `json:"top_k,omitempty"`
}

// ConsultResponse 返回超过阈值的相似失败经验。
type ConsultResponse struct {
	Matches []FailureMatch `json:"matches"`
}

// FailureMatch 在 FailureEntry 外补充分数，便于 Agent 判断是否应该复用经验。
type FailureMatch struct {
	Entry  FailureEntry `json:"entry"`
	Score  float64      `json:"score"`
	Reason string       `json:"reason,omitempty"`
}

// Config 汇总 failure journal 的持久化、LLM 生成和本地向量召回配置。
type Config struct {
	DBPath         string
	Generator      LessonGenerator
	Embedder       Embedder
	ScoreThreshold float64
	Now            func() time.Time
}

// LessonGenerator 隔离 heuristic/tags 的生成方式，生产可用 Eino model，测试可用本地规则。
type LessonGenerator interface {
	GenerateHeuristic(ctx context.Context, failureContext string, errorMessage string, fix string) (string, error)
	GenerateTags(ctx context.Context, failureContext string, errorMessage string) ([]string, error)
}

// Embedder 抽象向量化能力，后续可替换为真实 embedding 服务或向量库客户端。
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}
