package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 guardrail demo 的根 trace 输入，只保留可定位的摘要字段。
type runAgentTraceInput struct {
	QueryChars      int  `json:"query_chars"`
	ApproveExternal bool `json:"approve_external"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	queryChars int,
	approveExternal bool,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "guardrail_agent_run",
		Type:      "GuardrailAgent",
		Component: components.Component("GuardrailAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		QueryChars:      queryChars,
		ApproveExternal: approveExternal,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
