package critique

import (
	"fmt"
	"strings"
)

// generatorSystemPrompt 约束生成器只处理活动方案和运营文案，不漂移到 coding 场景。
func generatorSystemPrompt() string {
	return strings.Join([]string{
		"你是活动方案生成器，负责把业务目标转成可执行的活动方案或运营文案。",
		"场景固定为非 coding 的活动运营：公开课、社群活动、线下沙龙、用户增长活动、报名转化文案。",
		"输出必须是结构化 JSON，方便工具稳定检查执行骨架；不要输出 Markdown 代码块或额外解释。",
		"JSON 字段必须包含 goal、audience、budget、channels、timeline、cta、risk_plan。",
		`budget 格式为 {"amount":5000,"items":["海报设计","社群分发"]}；channels、timeline、risk_plan 必须是字符串数组。`,
		"字段内容必须可直接给运营同学复核，包含目标人群、渠道节奏、预算或资源假设、CTA、风险兜底。",
		"如果收到上一轮问题、建议或工具检查结果，必须逐项修正，不要只做措辞润色。",
	}, "\n")
}

// criticSystemPrompt 约束批评器输出结构化 JSON，便于代码做稳定收敛判断。
func criticSystemPrompt() string {
	return strings.Join([]string{
		"你是活动方案批评器，负责评审结构化活动方案是否解决业务痛点。",
		"只输出 JSON，不要输出 Markdown 代码块或额外解释。",
		"评分维度：目标匹配、执行完整性、渠道节奏、转化动作、风险兜底、工具检查反馈是否被修复。",
		"approved 表示方案已经可交给运营执行；score 必须是 0 到 1 之间的小数。",
		`格式：{"approved":false,"issues":["..."],"suggestions":["..."],"score":0.82}`,
	}, "\n")
}

// buildGeneratePrompt 构造生成器输入；有上下文时表示这是带反馈的再生成。
func buildGeneratePrompt(task string, contextText string) string {
	lines := []string{
		"请根据下面的活动运营需求生成高质量方案或文案。",
		"",
		"活动需求：",
		strings.TrimSpace(task),
	}
	if strings.TrimSpace(contextText) != "" {
		lines = append(lines,
			"",
			"上一轮反馈：",
			strings.TrimSpace(contextText),
			"",
			"请基于反馈输出修正版，不要重复上一版的问题。",
		)
	}
	return strings.Join(lines, "\n")
}

// buildCritiquePrompt 构造 critic 输入，把原始任务、当前输出和工具反馈放在同一审查上下文里。
func buildCritiquePrompt(task string, output string, toolFeedback string) string {
	return fmt.Sprintf(strings.Join([]string{
		"请评审当前活动方案是否已经解决原始需求。",
		"",
		"原始活动需求：",
		"%s",
		"",
		"当前方案：",
		"%s",
		"",
		"工具检查反馈：",
		"%s",
	}, "\n"), emptyAsNone(task), emptyAsNone(output), emptyAsNone(toolFeedback))
}

// emptyAsNone 让 prompt 在缺少可选输入时仍保持稳定结构。
func emptyAsNone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "无"
	}
	return value
}
