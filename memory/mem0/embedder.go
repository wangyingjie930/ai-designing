package mem0

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

const defaultEmbeddingDimension = 64

// Embedder 把文本转换成向量，便于后续替换成真实 embedding 服务。
type Embedder interface {
	Embed(ctx context.Context, text string, purpose string) ([]float64, error)
	EmbedBatch(ctx context.Context, texts []string, purpose string) ([][]float64, error)
}

// FakeEmbedder 使用确定性 token hashing 构造假向量，保证本地 SQLite demo 可重复运行。
type FakeEmbedder struct {
	Dimension int
}

// NewFakeEmbedder 创建无需外部服务的确定性 embedding 实现。
func NewFakeEmbedder(dimension int) FakeEmbedder {
	if dimension <= 0 {
		dimension = defaultEmbeddingDimension
	}
	return FakeEmbedder{Dimension: dimension}
}

// Embed 将文本 token 哈希到固定维度向量，保留相似文本的基础重叠信号。
func (e FakeEmbedder) Embed(_ context.Context, text string, _ string) ([]float64, error) {
	dimension := e.normalizedDimension()
	vector := make([]float64, dimension)
	for _, token := range tokenize(text) {
		idx := int(hashToken(token) % uint32(dimension))
		vector[idx] += 1
	}
	normalizeVector(vector)
	return vector, nil
}

// EmbedBatch 批量生成假向量，复刻 mem0 add/search 中批量 embedding 的调用形态。
func (e FakeEmbedder) EmbedBatch(ctx context.Context, texts []string, purpose string) ([][]float64, error) {
	vectors := make([][]float64, 0, len(texts))
	for _, text := range texts {
		vector, err := e.Embed(ctx, text, purpose)
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, vector)
	}
	return vectors, nil
}

// normalizedDimension 统一修正非法维度，避免调用侧传 0 导致空向量。
func (e FakeEmbedder) normalizedDimension() int {
	if e.Dimension <= 0 {
		return defaultEmbeddingDimension
	}
	return e.Dimension
}

// cosineSimilarity 计算两个向量的余弦相似度，用于 SQLite 内存候选排序。
func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	var dot, normA, normB float64
	for i := 0; i < limit; i++ {
		dot += a[i] * b[i]
	}
	for _, value := range a {
		normA += value * value
	}
	for _, value := range b {
		normB += value * value
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	score := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// keywordScore 给中文字符和英文 token 的重叠一个轻量分数，补足假 embedding 的语义不足。
func keywordScore(query, memory string) float64 {
	queryTokens := tokenize(query)
	memoryTokens := tokenize(memory)
	if len(queryTokens) == 0 || len(memoryTokens) == 0 {
		return 0
	}
	memorySet := make(map[string]bool, len(memoryTokens))
	for _, token := range memoryTokens {
		memorySet[token] = true
	}
	var matched int
	seen := make(map[string]bool, len(queryTokens))
	for _, token := range queryTokens {
		if seen[token] {
			continue
		}
		seen[token] = true
		if memorySet[token] {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(seen))
}

// tokenize 将中英文文本压成可复用 token，中文按字符、英文数字按连续片段。
func tokenize(text string) []string {
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, strings.ToLower(current.String()))
		current.Reset()
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			tokens = append(tokens, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// lemmatizeForKeyword 生成 SQLite 中保存的简化关键词字段，便于排查搜索命中原因。
func lemmatizeForKeyword(text string) string {
	return strings.Join(tokenize(text), " ")
}

// hashToken 将 token 稳定映射到向量维度。
func hashToken(token string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()
}

// normalizeVector 把词频向量归一化，避免长文本天然获得更高相似度。
func normalizeVector(vector []float64) {
	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] = vector[i] / norm
	}
}
