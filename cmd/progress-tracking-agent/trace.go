package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 progress tracking 命令的根 trace 输入，只保留链路定位字段。
type runAgentTraceInput struct {
	DBPath        string `json:"db_path"`
	PlanID        string `json:"plan_id"`
	TriggerRounds int    `json:"trigger_rounds"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	dbPath string,
	planID string,
	triggerRounds int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "progress_tracking_agent_run",
		Type:      "ProgressTrackingAgent",
		Component: components.Component("ProgressTrackingAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		DBPath:        dbPath,
		PlanID:        planID,
		TriggerRounds: triggerRounds,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.DBPath == "" {
		output.DBPath = dbPath
	}
	if output.PlanID == "" {
		output.PlanID = planID
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
