package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 self-heal 命令的根 trace 输入，只保留链路定位和配置摘要。
type runAgentTraceInput struct {
	Scenario        string `json:"scenario"`
	FailureKind     string `json:"failure_kind"`
	AffectedFiles   int    `json:"affected_files"`
	MaxIterations   int    `json:"max_iterations"`
	FailureSeverity int    `json:"failure_severity"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	input runAgentTraceInput,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "self_heal_agent_run",
		Type:      "SelfHealAgent",
		Component: components.Component("SelfHealAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, input)

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.Scenario == "" {
		output.Scenario = input.Scenario
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
