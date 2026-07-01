package main

import (
	"ai-designing/reflection/skillmode"
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runAgentTraceInput 是 Skill mode 命令的根 trace 输入，只保留定位和统计字段。
type runAgentTraceInput struct {
	SkillMode  skillmode.Mode `json:"skill_mode"`
	Scenario   string         `json:"scenario"`
	SkillName  string         `json:"skill_name"`
	QueryChars int            `json:"query_chars"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入闭包完成。
func withRunAgentTrace(
	ctx context.Context,
	scenario skillmode.Scenario,
	queryChars int,
	run func(context.Context) (runOutput, error),
) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "skill_mode_agent_run",
		Type:      "SkillModeAgent",
		Component: components.Component("SkillModeAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, runAgentTraceInput{
		SkillMode:  scenario.Mode,
		Scenario:   scenario.Title,
		SkillName:  scenario.SkillName,
		QueryChars: queryChars,
	})

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	if output.SkillMode == "" {
		output.SkillMode = scenario.Mode
	}
	if output.Scenario == "" {
		output.Scenario = scenario.Title
	}
	if output.SkillName == "" {
		output.SkillName = scenario.SkillName
	}
	if output.QueryChars == 0 {
		output.QueryChars = queryChars
	}
	callbacks.OnEnd(callbackCtx, output)
	return output, nil
}
