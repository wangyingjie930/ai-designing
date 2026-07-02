package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 tool dispatcher demo 的根 trace 输入，只保留低敏摘要。
type runAgentTraceInput struct {
	QueryChars   int `json:"query_chars"`
	DynamicTools int `json:"dynamic_tools"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	queryChars int,
	dynamicTools int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "tool_dispatcher_agent_run",
		Type:      "ToolDispatcherAgent",
		Component: components.Component("ToolDispatcherAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		QueryChars:   queryChars,
		DynamicTools: dynamicTools,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.QueryChars == 0 {
		output.QueryChars = queryChars
	}
	if output.DynamicTools == 0 {
		output.DynamicTools = dynamicTools
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
