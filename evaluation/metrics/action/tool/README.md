# Offline Action Tool Metrics

这个包负责 **离线 Tool 评测**。它有 `ExpectedToolCall`，所以可以回答“Agent 做得对不对”。

```text
任务样本 ToolEvaluationSample
  -> ScoreCoreToolMetrics
  -> ToolMetricReport
  -> EvaluateToolMetricGate
  -> 通过 / 失败原因
```

## 最小用法

```go
import toolmetrics "ai-designing/evaluation/metrics/action/tool"

samples := []toolmetrics.ToolEvaluationSample{
	{
		TaskID: "customer-support-demo",
		ExpectedCalls: []toolmetrics.ExpectedToolCall{
			{ID: "lookup_customer", ToolName: "lookup_customer"},
		},
		ActualCalls: []toolmetrics.ActualToolCall{
			{
				CallID:                "call-1",
				ToolName:              "lookup_customer",
				Necessary:             true,
				CoveredExpectedCallID: "lookup_customer",
				SchemaChecked:         true,
				SchemaValid:           true,
				EntityBindingRequired: true,
				EntityBindingCorrect:  true,
				Executed:              true,
				ExecutionSuccessful:   true,
				ResultHasContent:      true,
				ResultRelevant:        true,
			},
		},
		HasAvailableToolResult:    true,
		FinalAnswerUsedToolResult: true,
	},
}

report := toolmetrics.ScoreCoreToolMetrics(samples)
gate := toolmetrics.EvaluateToolMetricGate(report, []toolmetrics.ToolMetricThreshold{
	{MetricID: toolmetrics.ToolMetricCallPrecision, Threshold: 0.9},
	{MetricID: toolmetrics.ToolMetricWrongToolRate, Threshold: 0.1},
})
```

## 离线和实时的边界

离线评测可以使用：

- `ExpectedToolCall`：评测集里的标准答案，通常来自人工 golden case、规则生成、LLM 辅助标注加人工审核。
- `ActualToolCall`：跑 Agent 后记录到的实际工具调用。
- `ScoreCoreToolMetrics`：对比 expected 和 actual，计算选择、参数、执行、结果和最终回答使用质量。
- `EvaluateToolMetricGate`：用阈值做发布前质量门禁。

实时观测不要引用 `ExpectedToolCall`。线上只有实际发生的工具事件，应使用 `observability/metrics/action/tool`。

## 字段怎么填

- `ExpectedCalls`：这条任务理论上应该调用哪些工具，用来算 `tool_call_recall`。
- `ActualCalls`：Agent 实际调用了什么工具，用来算大部分调用级指标。
- `Necessary`：这次实际调用是否必要，用来算 `tool_call_precision`。
- `CoveredExpectedCallID`：这次实际调用覆盖了哪个期望调用，用来算 `tool_call_recall`。
- `WrongTool`：明确标记工具选错；如果没标记，scorer 也会用 `ExpectedToolCall.ToolName/AcceptableTools` 自动推导。
- `SchemaChecked/SchemaValid`：只看参数形状、必填字段、枚举值是否合法。
- `EntityBindingRequired/EntityBindingCorrect`：看客户、租户、订单、时间范围等业务实体是否绑对。
- `Executed/ExecutionSuccessful`：看工具是否真的发起、是否业务成功。
- `ResultHasContent/ResultRelevant`：看工具成功返回的内容是否和当前任务相关。
- `HasAvailableToolResult/FinalAnswerUsedToolResult`：任务级字段，看最终回答有没有用上可用工具结果。

## 分母为 0

如果某个指标没有适用样本，结果会是 `not_applicable`。一旦这个指标配置了 gate 阈值，`EvaluateToolMetricGate` 会判失败，因为没有证据证明达标。

## 只能离线可靠计算的指标

- `tool_call_precision`
- `tool_call_recall`
- `wrong_tool_rate`
- `entity_binding_accuracy`
- `result_relevance`
- `tool_result_used_rate`
