package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 replay retrospective 命令的根 trace 输入，只保留链路定位和配置摘要。
type runAgentTraceInput struct {
	TraceCount   int `json:"trace_count"`
	FailureCount int `json:"failure_count"`
	LessonWindow int `json:"lesson_window"`
	TaskChars    int `json:"task_chars"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	traceCount int,
	failureCount int,
	lessonWindow int,
	taskChars int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "replay_retrospective_agent_run",
		Type:      "ReplayRetrospectiveAgent",
		Component: components.Component("ReplayRetrospectiveAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		TraceCount:   traceCount,
		FailureCount: failureCount,
		LessonWindow: lessonWindow,
		TaskChars:    taskChars,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.RecordedTraces == 0 {
		output.RecordedTraces = traceCount
	}
	if output.FailureCount == 0 {
		output.FailureCount = failureCount
	}
	if output.Mode == "" {
		output.Mode = "agent"
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
