package hierarchical

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
)

// Embedder 抽象真实 embedding 调用；生产路径必须注入 HTTPEmbedder 或等价真实服务客户端。
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// estimateTokenCount 用 Python 示例的 len(content)//4 思路估算预算消耗。
func estimateTokenCount(content string) int {
	content = strings.TrimSpace(content)
	if content == "" {
		return 0
	}
	count := len([]rune(content)) / 4
	if count == 0 {
		return 1
	}
	return count
}

// addTierName 给工具响应补充可读 tier 名称，避免调用方只看到数字 enum。
func addTierName(entry MemoryEntry) MemoryEntry {
	entry.TierName = entry.Tier.String()
	return entry
}

// clampImportance 把模型或调用方给出的重要性压回 0 到 1 的业务区间。
func clampImportance(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

// requestImportance 区分“没传 importance”和“明确传 0.0”的情况。
func requestImportance(value *float64) float64 {
	if value == nil {
		return defaultMemoryImportance
	}
	return clampImportance(*value)
}

// cloneEntry 深拷贝可变 metadata，避免工具返回值被外部改写后污染内存态。
func cloneEntry(entry MemoryEntry) MemoryEntry {
	entry = addTierName(entry)
	if entry.Metadata != nil {
		next := make(map[string]any, len(entry.Metadata))
		for key, value := range entry.Metadata {
			next[key] = value
		}
		entry.Metadata = next
	}
	return entry
}

// cloneEntries 批量深拷贝记忆快照，保持 Memory 的内部 slice 不被外部持有。
func cloneEntries(entries []MemoryEntry) []MemoryEntry {
	out := make([]MemoryEntry, len(entries))
	for idx, entry := range entries {
		out[idx] = cloneEntry(entry)
	}
	return out
}

// cosineSimilarity 计算两个真实 embedding 向量的余弦相似度。
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

// contentHash 给同一 scope/source/content 生成幂等 upsert key，避免 consolidate 重复灌库。
func contentHash(scope string, source string, content string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(scope),
		strings.TrimSpace(source),
		strings.TrimSpace(content),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
