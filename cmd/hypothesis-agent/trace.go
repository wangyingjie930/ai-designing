package main

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 hypothesis 命令的根 trace 输入，只保留链路定位字段。
type runAgentTraceInput struct {
	Scenario      string `json:"scenario"`
	ProblemChars  int    `json:"problem_chars"`
	MaxIterations int    `json:"max_iterations"`
	MaxHypotheses int    `json:"max_hypotheses"`
	MaxEvidence   int    `json:"max_evidence"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入的 run 闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	scenario string,
	problemChars int,
	maxIterations int,
	maxHypotheses int,
	maxEvidence int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "hypothesis_agent_run",
		Type:      "HypothesisAgent",
		Component: components.Component("HypothesisAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		Scenario:      scenario,
		ProblemChars:  problemChars,
		MaxIterations: maxIterations,
		MaxHypotheses: maxHypotheses,
		MaxEvidence:   maxEvidence,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.Scenario == "" {
		output.Scenario = scenario
	}
	if output.ProblemChars == 0 {
		output.ProblemChars = problemChars
	}
	if output.MaxIterations == 0 {
		output.MaxIterations = maxIterations
	}
	if output.MaxHypotheses == 0 {
		output.MaxHypotheses = maxHypotheses
	}
	if output.MaxEvidence == 0 {
		output.MaxEvidence = maxEvidence
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
