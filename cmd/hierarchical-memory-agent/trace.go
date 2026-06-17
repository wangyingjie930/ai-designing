package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 hierarchical memory 命令的根 trace 输入，只保留链路定位字段。
type runAgentTraceInput struct {
	DBPath        string `json:"db_path"`
	Scope         string `json:"scope"`
	Rounds        int    `json:"rounds"`
	WorkingBudget int    `json:"working_budget"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	dbPath string,
	scope string,
	rounds int,
	workingBudget int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "hierarchical_memory_agent_run",
		Type:      "HierarchicalMemoryAgent",
		Component: components.Component("HierarchicalMemoryAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		DBPath:        dbPath,
		Scope:         scope,
		Rounds:        rounds,
		WorkingBudget: workingBudget,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.DBPath == "" {
		output.DBPath = dbPath
	}
	if output.Scope == "" {
		output.Scope = scope
	}
	if output.Rounds == 0 {
		output.Rounds = rounds
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
