package tot

import (
	"fmt"
	"strings"
)

const (
	defaultReasoningName        = "tot_reasoning_agent"
	defaultReasoningDescription = "Tree-of-Thought reasoning agent implemented with Go Eino ADK."
)

// DefaultReasoningAgentMessage 是最终回答合成角色的系统提示词。
func DefaultReasoningAgentMessage() string {
	return "你是一个推理型 AI 助手。你会利用 Tree-of-Thought 推理过程，为用户问题生成高质量回答。"
}

// DefaultTreeOfThoughtMessage 是 thinker 角色的系统提示词，约束它生成下一步思考选项或终止。
func DefaultTreeOfThoughtMessage() string {
	return strings.Join([]string{
		"角色：深度思考 AI 助手。",
		"",
		"最终目标：为用户问题生成一条高效的思考轨迹。复杂问题要深入，简单问题要浅出。",
		"",
		"当前任务：给定用户问题和已有思考步骤，你只有两个选择：",
		"1. 终止：如果你认为问题已经探索充分，输出 TERMINATE。",
		"2. 继续思考：为下一步思考生成至少四个有差异的备选方向。",
		"",
		"终止规则：",
		"- 当已有探索足以产出高质量答案时，应尽快终止。",
		"- 不要在第一步就终止。",
		"- 终止时不要再给其他备选项。",
		"",
		"继续思考规则：",
		"- 选项必须是同一轨迹的下一步替代方案，而不是互相依赖的一串步骤。",
		"- 如果发现前面思考有错误，要提出纠错选项。",
		"- 多选题要先排除明显错误选项，再用上下文线索和逻辑判断。",
		"- 如果需要用 Python 验证数学、算法或模拟，请给出 ```python ... ``` 代码块，并打印你需要观察的结果。",
		"",
		"限制：",
		"- 不要建议访问互联网、外部文献、数据集、书籍或专家。",
		"- 不要建议现实世界实验、调研或收集额外数据。",
		"- 没有必要时不要使用 Python。",
		"- 不要只说“用 Python 计算”，必须给出可执行代码片段。",
		"",
		"输出格式：",
		"REFLECTION:",
		"<简短反思已有轨迹的优点、问题和是否充分>",
		"",
		"**Possible Options:**",
		"Option 1: <思考选项 1>",
		"Option 2: <思考选项 2>",
		"Option 3: <思考选项 3>",
		"Option 4: <思考选项 4>",
	}, "\n")
}

// DefaultExecutorMessage 是 interim execution 时 executor 角色的系统提示词。
func DefaultExecutorMessage() string {
	return "请回答思考轨迹中的最后一步，用来推进推理过程。回答尽量简洁，不要建议下一步。"
}

// buildProcessRatingMessage 构造过程评分提示词，对应 AG2 的 thinking trajectory rating。
func buildProcessRatingMessage(ratingScale int, groundTruth string) string {
	message := fmt.Sprintf(strings.Join([]string{
		"请按 1 到 %d 分评价这条思考轨迹，1 最差，%d 最好。",
		"",
		"优秀思考轨迹必须推动问题求解。",
		"好的轨迹还应当：",
		"- 符合对话语境。",
		"- 不包含事实错误。",
		"- 没有奇怪或无关内容。",
		"",
		"如果轨迹要求访问互联网、专家意见、外部资料，或要求收集题目没有提供的数据，应给低分。",
		"请给出评分和简短理由。",
	}, "\n"), ratingScale, ratingScale)
	if strings.TrimSpace(groundTruth) != "" {
		message += "\n--- Ground Truth ---\n" + groundTruth + "\n---\n"
	}
	return message
}

// buildOutcomeRatingMessage 构造最终答案评分提示词，对应 AG2 的 outcome rating。
func buildOutcomeRatingMessage(ratingScale int, groundTruth string) string {
	message := fmt.Sprintf(strings.Join([]string{
		"请按 1 到 %d 分评价最终答案，1 最差，%d 最好。",
		"",
		"优秀答案必须：",
		"- 直接回答原始问题。",
		"- 事实准确且完整。",
		"- 展示清晰逻辑。",
		"",
		"好的答案还应简洁、结构清楚、语气合适，并避免矛盾。",
		"如果答案依赖互联网、专家意见、外部资料，或要求收集题目没有提供的数据，应给低分。",
		"请给出评分和简短理由。",
	}, "\n"), ratingScale, ratingScale)
	if strings.TrimSpace(groundTruth) != "" {
		message += "\n--- Ground Truth ---\n" + groundTruth + "\n---\n"
	}
	return message
}

// buildBatchRatingMessage 构造批量评分提示词，让 grader 一次评价同一父节点下的多个选项。
func buildBatchRatingMessage(ratingScale int, groundTruth string) string {
	message := fmt.Sprintf(strings.Join([]string{
		"你会看到一条思考轨迹，以及多个下一步选项。",
		"请分别评价每个选项形成的新思考轨迹，评分范围 1 到 %d，1 最差，%d 最好。",
		"",
		"优秀思考轨迹必须推动问题求解，并且没有事实错误、无关内容或不合语境的问题。",
		"如果轨迹要求访问互联网、专家意见、外部资料，或要求收集题目没有提供的数据，应给低分。",
		"",
		"输出格式必须是：",
		"Option 1: <理由>",
		"Rating: <分数>",
		"",
		"Option 2: <理由>",
		"Rating: <分数>",
	}, "\n"), ratingScale, ratingScale)
	if strings.TrimSpace(groundTruth) != "" {
		message += "\n--- Ground Truth ---\n" + groundTruth + "\n---\n"
	}
	return message
}

// buildRewritePrompt 构造多轮消息压缩提示词，把历史讨论改写成单个可搜索的当前问题。
func buildRewritePrompt(messages string) string {
	return strings.Join([]string{
		"任务：下面是一组包含历史讨论的消息。请写一个 prompt，总结其中所有有用信息，并提出当前需要处理的问题。",
		"",
		"**Messages:**",
		messages,
		"",
		"**输出格式：**",
		"QUESTION: <最初用户问题>",
		"SUMMARY: <已有讨论摘要>",
		"ACTIVITY LOG:",
		"- <已经执行的动作>",
		"CURRENT_QUESTION: <当前/最后需要处理的问题；如果任务已完成，写“任务已完成，请输出最终答复并终止”。>",
	}, "\n")
}

// withoutPythonInstructions 在未配置代码执行时移除 Python 相关指令，避免 thinker 生成不可执行路线。
func withoutPythonInstructions(prompt string) string {
	lines := strings.Split(prompt, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "python") || strings.Contains(line, "```") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
