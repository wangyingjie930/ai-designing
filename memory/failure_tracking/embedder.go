package failuretracking

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

const defaultEmbeddingDimension = 64

// HashEmbedder 用确定性 token hashing 模拟 embedding，方便本地 demo 和单测不依赖外部服务。
type HashEmbedder struct {
	Dimension int
}

// NewHashEmbedder 创建可重复的本地向量化器。
func NewHashEmbedder(dimension int) HashEmbedder {
	if dimension <= 0 {
		dimension = defaultEmbeddingDimension
	}
	return HashEmbedder{Dimension: dimension}
}

// Embed 将中英文 token 映射到固定维度向量，用于轻量语义召回。
func (e HashEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	dimension := e.Dimension
	if dimension <= 0 {
		dimension = defaultEmbeddingDimension
	}
	vector := make([]float64, dimension)
	for _, token := range tokenize(text) {
		idx := int(hashToken(token) % uint32(dimension))
		vector[idx] += 1
	}
	normalizeVector(vector)
	return vector, nil
}

// cosineSimilarity 计算两个向量的余弦相似度，作为 consult 的语义分之一。
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

// keywordScore 给关键词重叠一个显式分，避免短中文场景只靠 hash 向量时召回不稳定。
func keywordScore(query string, text string) float64 {
	queryTokens := tokenize(query)
	textTokens := tokenize(text)
	if len(queryTokens) == 0 || len(textTokens) == 0 {
		return 0
	}
	textSet := make(map[string]bool, len(textTokens))
	for _, token := range textTokens {
		textSet[token] = true
	}
	seen := make(map[string]bool, len(queryTokens))
	var matched int
	for _, token := range queryTokens {
		if seen[token] {
			continue
		}
		seen[token] = true
		if textSet[token] {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(seen))
}

// tagBoost 用 tags 做轻量加分，让人工或模型生成的语义标签真正影响召回排序。
func tagBoost(query string, tags []string) float64 {
	normalizedQuery := strings.ToLower(query)
	var hits int
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if strings.Contains(normalizedQuery, tag) {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	boost := float64(hits) * 0.08
	if boost > 0.2 {
		return 0.2
	}
	return boost
}

// tokenize 将中文按字、英文数字按连续片段切分，兼顾中文场景和错误码。
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

// hashToken 把 token 稳定映射到向量维度。
func hashToken(token string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()
}

// normalizeVector 归一化词频向量，避免长文本天然占优。
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
