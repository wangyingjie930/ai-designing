package cot

import (
	"fmt"
	"strings"
)

// DefaultSystemPrompt 约束模型输出可解析 JSON，避免命令层再做脆弱的自然语言猜测。
func DefaultSystemPrompt() string {
	return strings.Join([]string{
		"你是一个面向现实决策的显性推理链 Agent。",
		"你需要把问题拆成少量可审计步骤，每一步写清判断、依据和置信度。",
		"这些步骤是给用户复核的外显决策说明，不要写模型内部隐藏思维。",
		"",
		"要求：",
		"- 场景必须是用户给出的现实问题，不要转成 coding 任务。",
		"- 每一步都必须能被后续 verifier 独立检查。",
		"- confidence 必须是 0 到 1 之间的小数；越不确定越低。",
		"- final_answer 必须直接解决用户问题。",
		"",
		"只输出 JSON，不要输出 Markdown 代码块或额外解释。格式：",
		`{"steps":[{"step_number":1,"content":"...","confidence":0.86}],"final_answer":"..."}`,
	}, "\n")
}

// buildReasonPrompt 把场景名和用户问题合成单次推理输入，场景只用于限定任务边界。
func buildReasonPrompt(question string, scenario string) string {
	lines := []string{
		"请为下面的问题生成可校验的显性推理链，并给出最终答案。",
	}
	if strings.TrimSpace(scenario) != "" {
		lines = append(lines, "", "场景："+strings.TrimSpace(scenario))
	}
	lines = append(lines, "", "问题：", strings.TrimSpace(question))
	return strings.Join(lines, "\n")
}

// buildVerifyPrompt 构造逐步校验 prompt，原问题必须出现，否则 Step 1 的事实依据无法核验。
func buildVerifyPrompt(question string, prior string, step ReasoningStep) string {
	return fmt.Sprintf(strings.Join([]string{
		"你是推理链校验器。请判断当前步骤是否被原问题和前序步骤支持。",
		"",
		"校验规则：",
		"- 如果当前步骤自洽、没有明显跳步、没有违反硬约束，第一行输出 VALID。",
		"- 如果当前步骤包含错误、遗漏关键约束、或结论跳跃，第一行输出 INVALID。",
		"- 第二行用一句中文说明原因。",
		"",
		"原问题：",
		"%s",
		"",
		"前序步骤：",
		"%s",
		"",
		"当前步骤：",
		"Step %d: %s",
		"confidence: %.2f",
	}, "\n"), emptyAsNone(question), emptyAsNone(prior), step.StepNumber, step.Content, step.Confidence)
}

// emptyAsNone 让 verifier prompt 在第一步时仍保持稳定结构。
func emptyAsNone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "无"
	}
	return value
}
