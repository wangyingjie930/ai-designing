# Realtime Action Tool Metrics

这个包负责 **实时 Tool 观测**。它只记录线上实际发生的 `ToolCallEvent`，不包含 `ExpectedToolCall`。

```text
线上 ToolCallEvent
  -> ScoreRealtimeToolMetrics
  -> RealtimeToolMetricReport
  -> 实时成功率 / 错误率 / 超时率 / p95 延迟 / 错误类型分布
```

## 文件职责

```text
definition.go  # 实时指标定义：ID、公式、分子分母、字段依赖、备注
event.go       # ToolCallEvent：线上实际工具调用事件
realtime.go    # ScoreRealtimeToolMetrics：实时窗口聚合
```

## 最小用法

```go
import (
	"time"

	realtimetool "ai-designing/observability/metrics/action/tool"
)

report := realtimetool.ScoreRealtimeToolMetrics([]realtimetool.ToolCallEvent{
	{
		CallID:              "call-1",
		ToolName:            "lookup_customer",
		SchemaChecked:       true,
		SchemaValid:         true,
		ExecutionStatus:     realtimetool.ToolExecutionStatusSuccess,
		Latency:             120 * time.Millisecond,
		FinalAnswerUsedFact: true,
	},
})
```

## 实时能算什么

- `schema_valid_rate = schema_valid_tool_events / schema_checked_tool_events`
- `tool_success_rate = successful_tool_events / tool_call_events`
- `timeout_rate = timeout_tool_events / tool_call_events`
- `error_rate = failed_tool_events / tool_call_events`
- `tool_result_used_rate = successful_tool_events_used_in_final_answer / successful_tool_events`
- `LatencyP95`：用事件里的 `Latency` 算 nearest-rank p95。
- `ErrorCountByType`：按 `ErrorType` 聚合错误类型。

这些指标的完整含义、字段依赖和容易混淆点都在 `RealtimeToolMetricDefinitions()` 里。

## 实时不要算什么

实时链路没有标准答案，所以不要在这里算：

- `tool_call_precision`
- `tool_call_recall`
- `wrong_tool_rate`
- `entity_binding_accuracy`
- `result_relevance`

这些指标需要 `ExpectedToolCall` 或人工/规则标注，应放在 `evaluation/metrics/action/tool` 离线评测里。
