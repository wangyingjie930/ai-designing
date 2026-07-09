package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	researchswarm "ai-designing/action/research_swarm"

	"github.com/cloudwego/eino/callbacks"
)

// TestRunPrepareOnlyDoesNotRequireModelOrWorkers 验证预检路径不需要模型、搜索 key 或 worker 进程。
func TestRunPrepareOnlyDoesNotRequireModelOrWorkers(t *testing.T) {
	out, err := runAgent(context.Background(), []string{"-prepare-only", "-db", filepath.Join(t.TempDir(), "prepare.sqlite")})
	requireNoError(t, err)
	if out.Mode != "prepare" || out.WorkerCount != 3 || out.SearchProvider != "fake" {
		t.Fatalf("bad prepare output: %#v", out)
	}
}

// TestParseRunConfigLoadsDotEnvBeforeTraceConfig 验证命令入口优先使用本地 .env，避免外层旧 CozeLoop 鉴权污染。
func TestParseRunConfigLoadsDotEnvBeforeTraceConfig(t *testing.T) {
	t.Setenv("COZELOOP_API_TOKEN", "outer-token")
	t.Setenv("COZELOOP_WORKSPACE_ID", "outer-workspace")
	t.Setenv("COZELOOP_ENABLED", "true")
	tmp := t.TempDir()
	t.Chdir(tmp)
	envPath := filepath.Join(tmp, ".env")
	err := os.WriteFile(envPath, []byte("COZELOOP_API_TOKEN=dotenv-token\nCOZELOOP_WORKSPACE_ID=dotenv-workspace\nCOZELOOP_ENABLED=false\n"), 0o600)
	requireNoError(t, err)

	_, err = parseRunConfig([]string{"-prepare-only", "-db", filepath.Join(tmp, "prepare.sqlite")})
	requireNoError(t, err)
	if got := os.Getenv("COZELOOP_API_TOKEN"); got != "dotenv-token" {
		t.Fatalf("COZELOOP_API_TOKEN = %q, want dotenv-token", got)
	}
	if got := os.Getenv("COZELOOP_WORKSPACE_ID"); got != "dotenv-workspace" {
		t.Fatalf("COZELOOP_WORKSPACE_ID = %q, want dotenv-workspace", got)
	}
	if got := os.Getenv("COZELOOP_ENABLED"); got != "false" {
		t.Fatalf("COZELOOP_ENABLED = %q, want false from .env", got)
	}
}

// TestTraceInstallPolicyIncludesWorker 验证 worker 子进程也会在命令边界安装 trace。
func TestTraceInstallPolicyIncludesWorker(t *testing.T) {
	cases := []struct {
		name   string
		config runConfig
		want   bool
	}{
		{name: "leader", config: runConfig{Role: "leader"}, want: true},
		{name: "worker", config: runConfig{Role: "worker"}, want: true},
		{name: "prepare", config: runConfig{Role: "leader", PrepareOnly: true}, want: false},
		{name: "unsupported", config: runConfig{Role: "unknown"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInstallRunTrace(tc.config); got != tc.want {
				t.Fatalf("shouldInstallRunTrace() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildRunTraceInputCorrelatesLeaderAndWorker 验证独立进程 trace 能通过同一个 swarm_run_id 关联。
func TestBuildRunTraceInputCorrelatesLeaderAndWorker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.sqlite")
	leader := buildRunTraceInput(runConfig{
		Role:     "leader",
		Topic:    "调查主题",
		TeamName: "trace-team",
		DBPath:   dbPath,
	}, string(researchswarm.SearchProviderFake))
	worker := buildRunTraceInput(runConfig{
		Role:      "worker",
		TeamName:  "trace-team",
		AgentName: "searcher",
		DBPath:    dbPath,
	}, string(researchswarm.SearchProviderFake))

	if leader.SwarmRunID == "" {
		t.Fatalf("leader swarm_run_id should not be empty")
	}
	if leader.SwarmRunID != worker.SwarmRunID {
		t.Fatalf("swarm_run_id mismatch: leader=%q worker=%q", leader.SwarmRunID, worker.SwarmRunID)
	}
	if leader.WorkerCount != 3 || worker.WorkerCount != 0 || worker.AgentName != "searcher" {
		t.Fatalf("bad trace input leader=%#v worker=%#v", leader, worker)
	}
}

// TestRunAgentJSONSummaryIsCompact 验证 JSON 摘要包含报告统计，但不泄漏完整 mailbox。
func TestRunAgentJSONSummaryIsCompact(t *testing.T) {
	output, err := runAgent(context.Background(), []string{
		"-role", "leader",
		"-topic", "调查 AI Agent 外部搜索在客服工单中的风险控制价值",
		"-team", "cmd-test",
		"-db", filepath.Join(t.TempDir(), "run.sqlite"),
		"-json",
	})
	requireNoError(t, err)
	if output.Mode != "leader" || output.SourceCardCount == 0 || output.ReportSectionCount == 0 || output.FailedWorkerCount != 0 {
		t.Fatalf("output = %#v", output)
	}
	raw, err := json.Marshal(output)
	requireNoError(t, err)
	text := string(raw)
	if !strings.Contains(text, "final_report") || strings.Contains(text, "content_json") {
		t.Fatalf("unexpected json output: %s", text)
	}
}

// TestRunAgentTraceIsConcise 验证命令级 trace 只带运行摘要，不上传 topic 和报告正文。
func TestRunAgentTraceIsConcise(t *testing.T) {
	ctx := context.Background()
	var startName string
	var endName string
	var startInput callbacks.CallbackInput
	var endOutput callbacks.CallbackOutput
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info != nil {
				startName = info.Name
			}
			startInput = input
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info != nil {
				endName = info.Name
			}
			endOutput = output
			return ctx
		}).
		Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Name: "test_root", Type: "test", Component: "test"}, handler)

	config := runConfig{
		Role:           "leader",
		Topic:          "不要出现在 trace 里的完整调查主题",
		TeamName:       "trace-team",
		DBPath:         "/tmp/research-trace.sqlite",
		SearchProvider: string(researchswarm.SearchProviderFake),
	}
	output, err := withRunAgentTrace(ctx, buildRunTraceInput(config, string(researchswarm.SearchProviderFake)), func(context.Context) (runOutput, error) {
		return runOutput{
			Mode:               "leader",
			TeamName:           "trace-team",
			SearchProvider:     "fake",
			WorkerCount:        3,
			SourceCardCount:    2,
			ReportSectionCount: 2,
			FailedWorkerCount:  0,
			FinalReport:        "不要出现在 trace 里的完整最终报告",
			DBPath:             "/tmp/research-trace.sqlite",
		}, nil
	})
	requireNoError(t, err)
	if output.FinalReport == "" {
		t.Fatalf("run output should still keep final report for CLI response")
	}
	if startName != "research_report_agent_run" || endName != "research_report_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	startRaw, err := json.Marshal(startInput)
	requireNoError(t, err)
	endRaw, err := json.Marshal(endOutput)
	requireNoError(t, err)
	joined := string(startRaw) + string(endRaw)
	for _, leaked := range []string{"完整调查主题", "完整最终报告", "final_report"} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("trace leaked %q: start=%s end=%s", leaked, string(startRaw), string(endRaw))
		}
	}
}

// TestResearchReportAgentMultiProcessE2E 用真实 go run 子进程验证 leader 一条命令能拉起 worker。
func TestResearchReportAgentMultiProcessE2E(t *testing.T) {
	if os.Getenv("CMD_E2E") != "1" {
		t.Skip("set CMD_E2E=1 to run real multi-process command")
	}
	out, err := runAgent(context.Background(), []string{
		"-role", "leader",
		"-topic", "调查 AI Agent 外部搜索在客服工单中的风险控制价值",
		"-team", "cmd-e2e",
		"-db", filepath.Join(t.TempDir(), "e2e.sqlite"),
		"-json",
	})
	requireNoError(t, err)
	if out.SourceCardCount == 0 || out.ReportSectionCount == 0 || !strings.Contains(out.FinalReport, "调查报告") {
		t.Fatalf("e2e output = %#v", out)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
