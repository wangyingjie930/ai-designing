package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/callbacks"

	"ai-designing/cmd/internal/e2etest"
)

// TestHierarchicalMemoryAgentEndToEnd 用真实 LLM 和真实 embedding 跑完整 Agent，默认跳过避免普通 go test 误触发外部调用。
func TestHierarchicalMemoryAgentEndToEnd(t *testing.T) {
	if !e2etest.Enabled() {
		t.Skip("set CMD_E2E=1 to run real hierarchical memory agent E2E")
	}
	dbPath := filepath.Join(t.TempDir(), "hierarchical-memory.sqlite")
	output, err := runAgent(context.Background(), []string{
		"-db", dbPath,
		"-scope", "e2e-travel",
		"-rounds-file", "memory/hierarchical/examples/travel_rounds.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.AnswerChars == 0 {
		t.Fatalf("expected non-empty answer: %+v", output)
	}
	if output.Rounds != 3 {
		t.Fatalf("rounds=%d, want 3", output.Rounds)
	}
}

// TestParseRoundMessages 验证多轮输入文件的分隔规则，避免 demo 又退回单轮。
func TestParseRoundMessages(t *testing.T) {
	rounds, err := parseRoundMessages("第一轮\n---\n第二轮\n继续补充\n---\n第三轮")
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 3 {
		t.Fatalf("rounds=%d, want 3: %#v", len(rounds), rounds)
	}
	jsonRounds, err := parseRoundMessages(`["A", "B", ""]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonRounds) != 2 {
		t.Fatalf("json rounds=%d, want 2: %#v", len(jsonRounds), jsonRounds)
	}
}

// TestParseRunConfigDefaults 验证 GoLand 无参数运行时不会因为缺少 -db 或 message 直接退出。
func TestParseRunConfigDefaults(t *testing.T) {
	t.Setenv("HIERARCHICAL_MEMORY_DB", "")
	t.Setenv("HIERARCHICAL_MEMORY_SCOPE", "")
	config, err := parseRunConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.DBPath != e2etest.ResolvePath(defaultDBPath) {
		t.Fatalf("db=%q, want %q", config.DBPath, e2etest.ResolvePath(defaultDBPath))
	}
	if len(config.Messages) != 3 {
		t.Fatalf("messages=%d, want default demo rounds", len(config.Messages))
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 只记录摘要，不把多轮用户消息塞进上报。
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

	output, err := withRunAgentTrace(ctx, "/tmp/hierarchical.sqlite", "trip", 3, 150000, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Working: 4, LongTerm: 2, AnswerChars: 42}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "hierarchical_memory_agent_run" || endName != "hierarchical_memory_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.DBPath != "/tmp/hierarchical.sqlite" || input.Scope != "trip" || input.Rounds != 3 || input.WorkingBudget != 150000 {
		t.Fatalf("trace input = %+v", input)
	}
	if output.DBPath != "/tmp/hierarchical.sqlite" || output.Scope != "trip" || output.Rounds != 3 {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
}
