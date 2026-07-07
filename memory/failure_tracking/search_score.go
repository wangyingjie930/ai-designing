package failuretracking

import (
	"fmt"
	"sort"
	"strings"
)

// entrySearchText 生成写入 SQLite embedding 字段的检索文本。
func entrySearchText(entry FailureEntry) string {
	return strings.Join([]string{
		entry.TaskFamily,
		entry.Category,
		string(entry.Boundary),
		entry.Severity,
		entry.Symptom,
		entry.RootCause,
		strings.Join(entry.Repair, " "),
		strings.Join(entry.Lesson, " "),
		strings.Join(entry.DoNot, " "),
		strings.Join(entry.Evidence.WorkspaceRefs, " "),
		strings.Join(entry.Evidence.NarrativeRefs, " "),
		strings.Join(entry.Evidence.StateRefs, " "),
		strings.Join(entry.Evidence.ObservationRefs, " "),
		strings.Join(entry.Recall.TaskFamilies, " "),
		strings.Join(entry.Recall.Tools, " "),
		strings.Join(entry.Recall.MechanicalKeys, " "),
		strings.Join(entry.Recall.Categories, " "),
	}, " | ")
}

// scoreRecallMatch 按文档要求优先使用结构化召回键，语义相似度只做辅助排序。
func scoreRecallMatch(req RecallRequest, query string, queryVector []float64, text string, entry FailureEntry, vector []float64) FailureMatch {
	structured, reasons := structuredRecallScore(req, entry)
	semantic := cosineSimilarity(queryVector, vector)
	lexical := keywordScore(query, text)
	semanticScore := semantic*0.65 + lexical*0.35
	score := structured + semanticScore*0.2
	if structured == 0 {
		score = semanticScore
		reasons = append(reasons, fmt.Sprintf("semantic=%.3f", semanticScore))
	} else {
		reasons = append(reasons, fmt.Sprintf("semantic_tiebreaker=%.3f", semanticScore))
	}
	return FailureMatch{
		Entry:           entry,
		Score:           score,
		StructuredScore: structured,
		SemanticScore:   semanticScore,
		Reason:          strings.Join(reasons, " "),
	}
}

func structuredRecallScore(req RecallRequest, entry FailureEntry) (float64, []string) {
	var score float64
	var reasons []string
	if req.TaskFamily != "" && containsFold(entry.Recall.TaskFamilies, req.TaskFamily) {
		score += 3
		reasons = append(reasons, "task_family")
	}
	if req.TaskFamily != "" && strings.EqualFold(entry.TaskFamily, req.TaskFamily) && !containsFold(entry.Recall.TaskFamilies, req.TaskFamily) {
		score += 2
		reasons = append(reasons, "entry_task_family")
	}
	if req.Tool != "" && containsFold(entry.Recall.Tools, req.Tool) {
		score += 3
		reasons = append(reasons, "tool")
	}
	keyOverlap := overlapCount(req.MechanicalKeys, entry.Recall.MechanicalKeys)
	if keyOverlap > 0 {
		score += float64(keyOverlap)
		reasons = append(reasons, fmt.Sprintf("mechanical_keys=%d", keyOverlap))
	}
	categoryOverlap := overlapCount(req.Categories, entry.Recall.Categories)
	if categoryOverlap > 0 {
		score += float64(categoryOverlap)
		reasons = append(reasons, fmt.Sprintf("categories=%d", categoryOverlap))
	}
	if len(req.Categories) > 0 && containsFold(req.Categories, entry.Category) && !containsFold(entry.Recall.Categories, entry.Category) {
		score += 1
		reasons = append(reasons, "entry_category")
	}
	return score, reasons
}

// topMatches 统一按结构化分、总分降序截断候选。
func topMatches(matches []FailureMatch, topK int) []FailureMatch {
	if topK <= 0 {
		topK = defaultTopK
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].StructuredScore != matches[j].StructuredScore {
			return matches[i].StructuredScore > matches[j].StructuredScore
		}
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > topK {
		return matches[:topK]
	}
	return matches
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func overlapCount(left []string, right []string) int {
	count := 0
	seen := map[string]bool{}
	for _, value := range left {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		for _, candidate := range right {
			if strings.EqualFold(value, strings.TrimSpace(candidate)) {
				count++
				break
			}
		}
	}
	return count
}
