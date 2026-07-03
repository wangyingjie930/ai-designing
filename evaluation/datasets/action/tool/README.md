# Tool Evaluation Datasets

这里放离线 Tool 评测集，也就是带标准答案的 golden cases。

每条样本应该能映射到 `evaluation/metrics/action/tool.ToolEvaluationSample`：

- `ExpectedCalls`：人工、规则或 LLM 辅助标注后的标准工具调用。
- `ActualCalls`：runner 跑 Agent 后回填的实际工具调用。
- `HasAvailableToolResult/FinalAnswerUsedToolResult`：最终回答是否使用工具结果的任务级标注。

线上实时事件不要放这里；实时事件属于 `observability/metrics/action/tool.ToolCallEvent`。
