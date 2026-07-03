package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 turnloop 销售 demo 的根 trace 输入，只记录摘要，避免上传完整客户话术。
type runAgentTraceInput struct {
	FirstMessageChars  int `json:"first_message_chars"`
	SecondMessageChars int `json:"second_message_chars"`
	PreemptTimeoutMS   int `json:"preempt_timeout_ms"`
}

// withRunAgentTrace 只负责命令级 trace 生命周期，TurnLoop 内部执行继续走 Eino callback 树。
func withRunAgentTrace(
	ctx context.Context,
	input runAgentTraceInput,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "turnloop_sales_agent_run",
		Type:      "TurnLoopSalesAgent",
		Component: components.Component("TurnLoopSalesAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, input)

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
