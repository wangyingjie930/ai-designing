package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 compose 命令的根 trace 输入，只保留链路定位字段。
type runAgentTraceInput struct {
	Scenario   string `json:"scenario"`
	QueryChars int    `json:"query_chars"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	scenario string,
	queryChars int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "compose_router_agent_run",
		Type:      "ComposeRouterAgent",
		Component: components.Component("ComposeRouterAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		Scenario:   scenario,
		QueryChars: queryChars,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.Scenario == "" {
		output.Scenario = scenario
	}
	if output.QueryChars == 0 {
		output.QueryChars = queryChars
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
