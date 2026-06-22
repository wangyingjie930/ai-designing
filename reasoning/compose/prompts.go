package compose

import (
	"fmt"
	"strings"
)

// DefaultRouterSystemPrompt 约束复杂度路由器输出稳定 JSON，避免命令层猜测自然语言。
func DefaultRouterSystemPrompt() string {
	return strings.Join([]string{
		"你是复杂度路由器，负责把用户问题分到最合适的处理路径。",
		"",
		"分类标准：",
		"- simple：事实明确、风险低、无需多步推理，可以直接答。",
		"- moderate：需要按一条可审计路径拆解，约束不多，但仍要校验。",
		"- complex：有多个可能路径、目标冲突、证据不完整、风险较高，适合并行探索和假设检验。",
		"",
		"recommended_path 只能是 direct_response、chain_of_thought、parallel_exploration。",
		"confidence 是你对路由判断的置信度，0 到 1。",
		"只输出 JSON，不要 Markdown 代码块或额外解释。",
		"",
		"格式：",
		`{"complexity":"simple","confidence":0.88,"reason":"...","recommended_path":"direct_response"}`,
	}, "\n")
}

// buildRouterPrompt 把场景和用户问题合成路由输入。
func buildRouterPrompt(query string, scenario string) string {
	lines := []string{"请判断下面问题的复杂度，并选择处理路径。"}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "", "场景："+strings.TrimSpace(scenario))
	}
	lines = append(lines, "", "问题：", strings.TrimSpace(query))
	return strings.Join(lines, "\n")
}

// DefaultDirectSystemPrompt 约束简单问题走短答，不引入多余推理结构。
func DefaultDirectSystemPrompt() string {
	return strings.Join([]string{
		"你是直接响应 Agent，只处理简单、低风险、事实清楚的问题。",
		"请直接给出可执行回答，不要展开多路径推理，也不要假装做了工具验证。",
		"如果问题明显不适合直接答，用一句话说明需要升级到更复杂路径。",
	}, "\n")
}

// buildDirectPrompt 构造 direct response 的用户输入。
func buildDirectPrompt(query string, scenario string) string {
	lines := []string{}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "场景："+strings.TrimSpace(scenario), "")
	}
	lines = append(lines, "请直接回答：", strings.TrimSpace(query))
	return strings.Join(lines, "\n")
}

// DefaultBestPathSystemPrompt 约束复杂路径探索后的确认器输出稳定 JSON。
func DefaultBestPathSystemPrompt() string {
	return strings.Join([]string{
		"你是最佳路径确认器，负责判断多路径探索产出的候选答案是否足够进入验证阶段。",
		"",
		"判断标准：",
		"- 如果候选答案覆盖了关键目标、硬约束和主要风险，confirmed=true。",
		"- 如果候选答案遗漏关键事实、依赖未说明假设、或路径仍明显摇摆，confirmed=false。",
		"- confidence 是你对该判断的置信度，0 到 1。",
		"",
		"只输出 JSON，不要 Markdown 代码块或额外解释。",
		"",
		"格式：",
		`{"confirmed":true,"confidence":0.82,"reason":"..."}`,
	}, "\n")
}

// buildBestPathPrompt 把原问题和 ToT 候选答案交给确认器。
func buildBestPathPrompt(query string, scenario string, candidate string) string {
	lines := []string{}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "场景："+strings.TrimSpace(scenario), "")
	}
	lines = append(lines,
		"原问题：",
		strings.TrimSpace(query),
		"",
		"多路径探索产出的候选答案：",
		emptyAsNone(candidate),
		"",
		"请判断这个候选答案是否已经形成可验证的最佳路径。",
	)
	return strings.Join(lines, "\n")
}

// buildHypothesisProblem 把候选答案改写成 hypothesis agent 可反证的问题。
func buildHypothesisProblem(query string, scenario string, candidate string, source PathKind, probeEnv bool) string {
	mode := "普通假设检验"
	if probeEnv {
		mode = "probe env 假设检验"
	}
	lines := []string{
		fmt.Sprintf("验证模式：%s", mode),
		fmt.Sprintf("候选答案来源：%s", source),
	}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "场景："+strings.TrimSpace(scenario))
	}
	lines = append(lines,
		"",
		"原问题：",
		strings.TrimSpace(query),
		"",
		"待验证候选答案：",
		emptyAsNone(candidate),
		"",
		"请围绕“这个候选答案是否足以作为最终方案”提出可反证假设。",
		"证据只能来自原问题、候选答案、已有事实，或明确标记为需要人工补查的缺失检查。",
		"如果验证无法收敛，请保留未被反证的假设，供上层决定是否升级给人。",
	)
	return strings.Join(lines, "\n")
}

// emptyAsNone 让 prompt 在候选答案缺失时仍保持稳定结构。
func emptyAsNone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "无"
	}
	return value
}
