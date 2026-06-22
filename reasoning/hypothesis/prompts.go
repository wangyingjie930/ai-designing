package hypothesis

import (
	"fmt"
	"strings"
)

// DefaultPlannerSystemPrompt 约束 planner 只提出可被反证的现实候选解释。
func DefaultPlannerSystemPrompt() string {
	return strings.Join([]string{
		"你是假设规划器，负责为现实问题提出可检验的候选解释。",
		"目标不是找一个看起来合理的答案，而是提出能被证据支持或反证的强备选。",
		"",
		"要求：",
		"- 场景必须保持为用户给出的非 coding 现实问题。",
		"- description 写清根因或解释，不要写行动建议。",
		"- prior 是 0 到 1 之间的小数，表示初始可信度。",
		"- 后续迭代已有足够假设时可以返回空数组。",
		"- 只输出 JSON，不要 Markdown 代码块或额外解释。",
		"",
		"格式：",
		`{"hypotheses":[{"description":"...","prior":0.42}]}`,
	}, "\n")
}

// DefaultGeneratorSystemPrompt 约束 generator 为目标假设寻找支持和反证证据。
func DefaultGeneratorSystemPrompt() string {
	return strings.Join([]string{
		"你是证据生成器，负责围绕一个目标假设收集可审计证据。",
		"你尤其要寻找可能反驳目标假设的证据，避免只挑确认自己判断的信息。",
		"",
		"要求：",
		"- 证据只能来自用户问题、已有事实、或明确标记为需要补查的信息。",
		"- source 用简短来源名，例如 case_brief、known_fact、missing_check。",
		"- 不要编造外部数据；缺证据时写成需要补查的证据。",
		"- 只输出 JSON，不要 Markdown 代码块或额外解释。",
		"",
		"格式：",
		`{"evidence":[{"description":"...","source":"case_brief"}]}`,
	}, "\n")
}

// DefaultEvaluatorSystemPrompt 约束 evaluator 给出证据对假设的反证关系和后验变化。
func DefaultEvaluatorSystemPrompt() string {
	return strings.Join([]string{
		"你是反证评估器，负责判断一条证据对目标假设的影响。",
		"请优先考虑证据是否足以反驳假设，而不是只寻找支持理由。",
		"",
		"要求：",
		"- effect 只能是 supports、refutes、neutral。",
		"- posterior_delta 是 -1 到 1 之间的小数，支持为正，反驳为负，中性接近 0。",
		"- 如果证据直接违背假设，effect 必须是 refutes。",
		"- 只输出 JSON，不要 Markdown 代码块或额外解释。",
		"",
		"格式：",
		`{"effect":"supports","posterior_delta":0.25}`,
	}, "\n")
}

// buildPlannerPrompt 把问题、场景和已有假设压成 planner 的稳定输入。
func buildPlannerPrompt(problem string, scenario string, existing []Hypothesis, iteration int, maxHypotheses int) string {
	lines := []string{
		fmt.Sprintf("迭代轮次：%d", iteration),
	}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "场景："+strings.TrimSpace(scenario))
	}
	lines = append(lines,
		"",
		"问题：",
		strings.TrimSpace(problem),
		"",
		"已有假设摘要（只用于避免重复，不是本轮 candidate）：",
		formatHypothesisSummaryForPrompt(existing),
		"",
		fmt.Sprintf("请提出本轮需要加入的候选假设，最多 %d 个。", maxHypotheses),
	)
	return strings.Join(lines, "\n")
}

// buildGeneratorPrompt 把目标假设和完整问题交给 generator，确保证据不脱离原始事实。
func buildGeneratorPrompt(problem string, scenario string, h Hypothesis, maxEvidence int) string {
	lines := []string{}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "场景："+strings.TrimSpace(scenario), "")
	}
	lines = append(lines,
		"问题：",
		strings.TrimSpace(problem),
		"",
		"目标假设：",
		fmt.Sprintf("%s: %s", h.ID, h.Description),
		"",
		"当前后验概率：",
		fmt.Sprintf("%.2f", h.Posterior),
		"",
		fmt.Sprintf("请给出本轮要评估的证据，最多 %d 条。", maxEvidence),
	)
	return strings.Join(lines, "\n")
}

// buildEvaluatorPrompt 把目标假设和单条证据交给 evaluator，得到 effect 和后验变化。
func buildEvaluatorPrompt(problem string, scenario string, h Hypothesis, evidence EvidenceCandidate) string {
	lines := []string{}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "场景："+strings.TrimSpace(scenario), "")
	}
	lines = append(lines,
		"问题：",
		strings.TrimSpace(problem),
		"",
		"目标假设：",
		fmt.Sprintf("%s: %s", h.ID, h.Description),
		"",
		"当前后验概率：",
		fmt.Sprintf("%.2f", h.Posterior),
		"",
		"证据：",
		strings.TrimSpace(evidence.Description),
		"",
		"证据来源：",
		strings.TrimSpace(evidence.Source),
		"",
		"请判断这条证据对目标假设的作用。",
	)
	return strings.Join(lines, "\n")
}

// formatHypothesisSummaryForPrompt 只给 planner 数量和状态摘要，避免旧 candidate 原文再次进入 planner 输入。
func formatHypothesisSummaryForPrompt(hypotheses []Hypothesis) string {
	if len(hypotheses) == 0 {
		return "无"
	}
	statusCounts := map[HypothesisStatus]int{}
	for _, h := range hypotheses {
		statusCounts[h.Status]++
	}
	lines := []string{fmt.Sprintf("已存在 %d 个候选，不要重复提出已存在解释。", len(hypotheses))}
	for _, status := range []HypothesisStatus{StatusProposed, StatusTesting, StatusConfirmed, StatusFalsified, StatusInconclusive} {
		if count := statusCounts[status]; count > 0 {
			lines = append(lines, fmt.Sprintf("%s=%d", status, count))
		}
	}
	return strings.Join(lines, "\n")
}
