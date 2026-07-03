# Tool Evaluation Runners

这里放离线 Tool 评测 runner。

runner 的职责是：

1. 读取 `evaluation/datasets/action/tool` 里的 golden cases。
2. 执行对应 Agent 或工具链路，采集实际工具调用。
3. 组装 `ToolEvaluationSample`。
4. 调用 `ScoreCoreToolMetrics` 得到离线质量报告。
5. 调用 `EvaluateToolMetricGate` 判断是否通过发布门禁。

runner 不负责线上实时上报；实时上报属于 `observability/metrics/action/tool`。
