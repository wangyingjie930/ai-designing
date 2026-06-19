package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/callbacks"

	"ai-designing/cmd/internal/e2etest"
)

// TestParseRoundMessages 验证多轮输入文件的分隔规则，避免 demo 退回单轮。
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

// TestRunAgentPrepareOnly 验证命令确定性路径不需要真实模型也能跑通五层写入。
func TestRunAgentPrepareOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hierarchical-v1.sqlite")
	output, err := runAgent(context.Background(), []string{
		"-prepare-only",
		"-db", dbPath,
		"-write-layer", "user",
		"-write-source", "human",
		"-write-key", "avoid",
		"-write-value", `{"text":"不吃香菜"}`,
		"-token-estimate", "5",
		"-read-key", "avoid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" {
		t.Fatalf("mode=%s", output.Mode)
	}
	if output.LayerItems["user"] != 1 {
		t.Fatalf("layer items=%+v", output.LayerItems)
	}
	if output.ContextChars == 0 {
		t.Fatalf("expected non-empty context: %+v", output)
	}
	if output.DBPath != dbPath {
		t.Fatalf("db path=%q, want %q", output.DBPath, dbPath)
	}
}

// TestRunAgentPrepareOnlyRejectsPolicyInference 验证 CLI 也会暴露 policy 写权限保护。
func TestRunAgentPrepareOnlyRejectsPolicyInference(t *testing.T) {
	_, err := runAgent(context.Background(), []string{
		"-prepare-only",
		"-db", filepath.Join(t.TempDir(), "hierarchical-v1.sqlite"),
		"-write-layer", "policy",
		"-write-source", "agent_inference",
		"-write-key", "raw_milk",
		"-write-value", `{"text":"不要使用生乳"}`,
		"-evidence", "agent-note",
	})
	if err == nil {
		t.Fatal("expected policy write rejection")
	}
}

// TestParseRunConfigDefaults 验证无参数真实运行会自动读取非 coding 多轮输入。
func TestParseRunConfigDefaults(t *testing.T) {
	t.Setenv("HIERARCHICAL_V1_MEMORY_DB", "")
	config, err := parseRunConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Messages) != 3 {
		t.Fatalf("messages=%d, want default meal rounds", len(config.Messages))
	}
	if config.WriteSource != "human" {
		t.Fatalf("write source=%s", config.WriteSource)
	}
	if config.DBPath != e2etest.ResolvePath(defaultDBPath) {
		t.Fatalf("db=%q, want %q", config.DBPath, e2etest.ResolvePath(defaultDBPath))
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 只记录摘要，不把用户消息或 memory 内容塞进去。
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

	output, err := withRunAgentTrace(ctx, "/tmp/hierarchical-v1.sqlite", 3, "agent", func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", DBPath: "/tmp/hierarchical-v1.sqlite", Rounds: 3, AnswerChars: 42}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "hierarchical_v1_agent_run" || endName != "hierarchical_v1_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.DBPath != "/tmp/hierarchical-v1.sqlite" || input.Rounds != 3 || input.Mode != "agent" {
		t.Fatalf("trace input=%+v", input)
	}
	if output.AnswerChars != 42 {
		t.Fatalf("output=%+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type=%T", endOutput)
	}
}

// TestHierarchicalV1AgentEndToEnd 用真实 LLM 跑完整 Agent，默认跳过避免普通 go test 误触发外部调用。
func TestHierarchicalV1AgentEndToEnd(t *testing.T) {
	if !e2etest.Enabled() {
		t.Skip("set CMD_E2E=1 to run real hierarchical v1 agent E2E")
	}
	output, err := runAgent(context.Background(), []string{
		"-rounds-file", "memory/hierarchical_v1/examples/meal_rounds.txt",
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
