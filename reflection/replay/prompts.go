package replay

import (
	"fmt"
	"strings"
)

// reflectionSystemPrompt 约束 L1 反思只分析失败执行，不泛化到跨任务经验。
func reflectionSystemPrompt() string {
	return strings.Join([]string{
		"你是运营执行复盘助手，负责分析一次失败执行的步骤。",
		"只根据给定任务、步骤和错误做 L1 反思，不要编造未提供事实。",
		"请输出 ROOT_CAUSE、LESSON、PREVENTION 三段。",
	}, "\n")
}

// lessonSystemPrompt 约束 L2 抽取只输出可解析的 INSIGHT 行。
func lessonSystemPrompt() string {
	return strings.Join([]string{
		"你是跨任务经验抽取器，负责从多个失败任务中找通用模式。",
		"只输出 1 到 3 行，每行格式必须是：INSIGHT: [lesson]",
		"lesson 要适用于下一次类似运营执行，而不是复述单个错误。",
	}, "\n")
}

// advisorSystemPrompt 约束最终 agent 固定在非 coding 的运营复盘建议场景。
func advisorSystemPrompt() string {
	return strings.Join([]string{
		"你是活动运营复盘 Agent，负责把历史经验转成下一次执行提醒。",
		"场景固定为非 coding 的运营、活动、销售承接和客户沟通执行。",
		"优先使用已验证有效的 lesson，输出短而可执行的提醒。",
	}, "\n")
}

// buildReflectionPrompt 对齐 Python _reflect_on_failure 的输入格式。
func buildReflectionPrompt(trace ExecutionTrace) string {
	stepLines := make([]string, 0, len(trace.Steps))
	for i, step := range trace.Steps {
		action := strings.TrimSpace(step.Action)
		if strings.TrimSpace(step.Observation) != "" {
			action += " | Observation: " + strings.TrimSpace(step.Observation)
		}
		stepLines = append(stepLines, fmt.Sprintf("Step %d: %s", i+1, emptyAsNone(action)))
	}
	if len(stepLines) == 0 {
		stepLines = append(stepLines, "Step 1: 无步骤记录")
	}
	return strings.Join([]string{
		"Analyze this failed execution:",
		"Task: " + emptyAsNone(trace.Task),
		"Steps:",
		strings.Join(stepLines, "\n"),
		"Error: " + emptyAsNone(trace.Error),
		"",
		"Provide:",
		"1. ROOT_CAUSE",
		"2. LESSON",
		"3. PREVENTION",
	}, "\n")
}

// buildLessonPrompt 对齐 Python _extract_cross_task_insights 的失败摘要输入。
func buildLessonPrompt(failures []ExecutionTrace) string {
	lines := make([]string, 0, len(failures))
	for i, trace := range failures {
		if i >= 10 {
			break
		}
		lines = append(lines, fmt.Sprintf("- %s | %s", emptyAsNone(trace.Task), emptyAsNone(trace.Error)))
	}
	if len(lines) == 0 {
		lines = append(lines, "- 无失败样本")
	}
	return strings.Join([]string{
		"Find cross-task patterns:",
		"",
		"FAILURES:",
		strings.Join(lines, "\n"),
		"",
		"Identify 1-3 GENERAL lessons.",
		"Format: INSIGHT: [lesson]",
	}, "\n")
}

// buildAdvicePrompt 把排序后的经验注入下一次任务提醒。
func buildAdvicePrompt(task string, experience string) string {
	return strings.Join([]string{
		emptyAsNone(experience),
		"",
		"New task:",
		emptyAsNone(task),
		"",
		"请基于这些历史经验，输出下一次执行前必须检查的提醒。",
	}, "\n")
}

// emptyAsNone 让 prompt 在缺少可选输入时仍保持稳定结构。
func emptyAsNone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "无"
	}
	return value
}
