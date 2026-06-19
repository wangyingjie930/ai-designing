package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 hierarchical_v1 命令的根 trace 输入，只保留链路定位字段。
type runAgentTraceInput struct {
	DBPath string `json:"db_path"`
	Rounds int    `json:"rounds"`
	Mode   string `json:"mode"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	dbPath string,
	rounds int,
	mode string,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "hierarchical_v1_agent_run",
		Type:      "HierarchicalV1Agent",
		Component: components.Component("HierarchicalV1Agent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		DBPath: dbPath,
		Rounds: rounds,
		Mode:   mode,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.DBPath == "" {
		output.DBPath = dbPath
	}
	if output.Rounds == 0 {
		output.Rounds = rounds
	}
	if output.Mode == "" {
		output.Mode = mode
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
