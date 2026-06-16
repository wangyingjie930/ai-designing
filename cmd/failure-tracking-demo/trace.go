package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 demo 根 trace 的输入摘要，串起两轮自然语言业务会话。
type runAgentTraceInput struct {
	DBPath string `json:"db_path"`
	Rounds int    `json:"rounds"`
}

// runAgentTraceOutput 是 demo 根 trace 的输出摘要，用来确认整条链路正常收口。
type runAgentTraceOutput struct {
	DBPath      string `json:"db_path"`
	Rounds      int    `json:"rounds"`
	AnswerChars int    `json:"answer_chars"`
	Entries     int    `json:"entries"`
}

// withRunAgentTrace 只负责 runAgent 的 trace 生命周期，避免主流程散落 callback 状态管理。
func withRunAgentTrace(
	ctx context.Context,
	dbPath string,
	rounds int,
	run func(context.Context) (runAgentTraceOutput, error),
) (runAgentTraceOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "failure_tracking_demo_run",
		Type:      "FailureTrackingDemo",
		Component: components.Component("FailureTrackingDemo"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		DBPath: dbPath,
		Rounds: rounds,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runAgentTraceOutput{}, err
	}
	if output.DBPath == "" {
		output.DBPath = dbPath
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
