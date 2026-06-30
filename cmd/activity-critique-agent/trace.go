package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 activity critique 命令的根 trace 输入，只保留链路定位和配置摘要。
type runAgentTraceInput struct {
	TaskChars        int     `json:"task_chars"`
	MaxIterations    int     `json:"max_iterations"`
	QualityThreshold float64 `json:"quality_threshold"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	taskChars int,
	maxIterations int,
	qualityThreshold float64,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "activity_critique_agent_run",
		Type:      "ActivityCritiqueAgent",
		Component: components.Component("ActivityCritiqueAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		TaskChars:        taskChars,
		MaxIterations:    maxIterations,
		QualityThreshold: qualityThreshold,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.TaskChars == 0 {
		output.TaskChars = taskChars
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
