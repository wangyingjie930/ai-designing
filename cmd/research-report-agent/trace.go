package main

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

// runTraceInput 是命令边界 trace 的摘要输入；不包含完整搜索结果或报告正文。
type runTraceInput struct {
	Mode           string `json:"mode"`
	SwarmRunID     string `json:"swarm_run_id"`
	TeamName       string `json:"team_name"`
	AgentName      string `json:"agent_name,omitempty"`
	TopicChars     int    `json:"topic_chars"`
	SearchProvider string `json:"search_provider"`
	WorkerCount    int    `json:"worker_count"`
	DBPath         string `json:"db_path,omitempty"`
}

// buildRunTraceInput 生成可上报的紧凑 trace 输入，避免泄漏调查报告正文。
func buildRunTraceInput(config runConfig, provider string) runTraceInput {
	workerCount := 0
	if config.Role == "leader" || config.PrepareOnly {
		workerCount = 3
	}
	return runTraceInput{
		Mode:           config.Role,
		SwarmRunID:     buildSwarmRunID(config.TeamName, config.DBPath),
		TeamName:       config.TeamName,
		AgentName:      config.AgentName,
		TopicChars:     len([]rune(config.Topic)),
		SearchProvider: provider,
		WorkerCount:    workerCount,
		DBPath:         config.DBPath,
	}
}

// runTraceOutput 是命令根 trace 的摘要输出，刻意不带 FinalReport/Topic 这类业务正文。
type runTraceOutput struct {
	Mode               string `json:"mode"`
	SwarmRunID         string `json:"swarm_run_id"`
	TeamName           string `json:"team_name"`
	SearchProvider     string `json:"search_provider"`
	WorkerCount        int    `json:"worker_count"`
	SourceCardCount    int    `json:"source_card_count"`
	ReportSectionCount int    `json:"report_section_count"`
	FailedWorkerCount  int    `json:"failed_worker_count"`
	DBPath             string `json:"db_path,omitempty"`
}

// withRunAgentTrace 只管理命令级 trace 生命周期，业务执行仍由传入闭包完成。
func withRunAgentTrace(ctx context.Context, input runTraceInput, run func(context.Context) (runOutput, error)) (runOutput, error) {
	callbackCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
		Name:      "research_report_agent_run",
		Type:      "ResearchReportAgent",
		Component: components.Component("ResearchReportAgent"),
	})
	callbackCtx = callbacks.OnStart(callbackCtx, input)

	output, err := run(callbackCtx)
	if err != nil {
		callbacks.OnError(callbackCtx, err)
		return runOutput{}, err
	}
	callbacks.OnEnd(callbackCtx, buildRunTraceOutput(input, output))
	return output, nil
}

func buildRunTraceOutput(input runTraceInput, output runOutput) runTraceOutput {
	return runTraceOutput{
		Mode:               output.Mode,
		SwarmRunID:         input.SwarmRunID,
		TeamName:           output.TeamName,
		SearchProvider:     output.SearchProvider,
		WorkerCount:        output.WorkerCount,
		SourceCardCount:    output.SourceCardCount,
		ReportSectionCount: output.ReportSectionCount,
		FailedWorkerCount:  output.FailedWorkerCount,
		DBPath:             output.DBPath,
	}
}

// buildSwarmRunID 用 leader/worker 共享的 mailbox 身份生成关联 ID，避免给核心 swarm 包增加 trace 字段。
func buildSwarmRunID(teamName, dbPath string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(teamName))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(dbPath))
	return fmt.Sprintf("%016x", hash.Sum64())
}
