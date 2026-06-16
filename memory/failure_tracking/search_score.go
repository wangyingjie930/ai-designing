package failuretracking

import (
	"fmt"
	"sort"
	"strings"
)

// entrySearchText 生成写入 SQLite embedding 字段的检索文本。
func entrySearchText(entry FailureEntry) string {
	return strings.Join([]string{
		entry.Context,
		entry.ErrorMessage,
		entry.Heuristic,
		entry.Fix,
		strings.Join(entry.Tags, " "),
	}, " | ")
}

// scoreFailureMatch 汇总向量相似度、关键词重叠和标签命中，供 SQLite 扫描召回复用。
func scoreFailureMatch(query string, queryVector []float64, text string, entry FailureEntry, vector []float64) FailureMatch {
	semantic := cosineSimilarity(queryVector, vector)
	lexical := keywordScore(query, text)
	boost := tagBoost(query, entry.Tags)
	score := semantic*0.65 + lexical*0.35 + boost
	if score > 1 {
		score = 1
	}
	return FailureMatch{
		Entry:  entry,
		Score:  score,
		Reason: fmt.Sprintf("semantic=%.3f keyword=%.3f tag_boost=%.3f", semantic, lexical, boost),
	}
}

// topMatches 统一按分数降序截断候选。
func topMatches(matches []FailureMatch, topK int) []FailureMatch {
	if topK <= 0 {
		topK = defaultTopK
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > topK {
		return matches[:topK]
	}
	return matches
}
