package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"

	progresstracking "ai-designing/memory/progress_tracking"
)

// planTriggerFakeAgent 在测试里模拟 Agent 每次被触发后写入对应任务 checkpoint。
type planTriggerFakeAgent struct {
	tracker *progresstracking.ProgressTracker
	calls   []string
}

// Query 记录主流程生成的触发消息，并用 tracker 模拟 start/complete 工具调用效果。
func (a *planTriggerFakeAgent) Query(ctx context.Context, req progresstracking.AgentRequest) (*progresstracking.AgentResponse, error) {
	index := len(a.calls)
	a.calls = append(a.calls, req.Message)
	if err := a.tracker.Start(ctx, index); err != nil {
		return nil, err
	}
	result := fmt.Sprintf("第 %d 轮编辑完成", index+1)
	if err := a.tracker.Complete(ctx, index, result, []string{fmt.Sprintf("round-%d.md", index+1)}); err != nil {
		return nil, err
	}
	return &progresstracking.AgentResponse{Message: result}, nil
}

// TestTriggerGeneratedPlanRounds 验证主流程会查询生成计划，并只触发前三个未完成任务。
func TestTriggerGeneratedPlanRounds(t *testing.T) {
	ctx := context.Background()
	tracker, err := progresstracking.NewProgressTracker(ctx, progresstracking.Config{
		DBPath: filepath.Join(t.TempDir(), "progress.sqlite"),
		PlanID: "event-demo",
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	defer tracker.Close()
	if err := tracker.CreatePlan(ctx, []string{"任务一", "任务二", "任务三", "任务四"}); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}

	fake := &planTriggerFakeAgent{tracker: tracker}
	answerChars, err := triggerGeneratedPlanRounds(ctx, fake, tracker)
	if err != nil {
		t.Fatalf("triggerGeneratedPlanRounds() error = %v", err)
	}
	if len(fake.calls) != 3 || answerChars == 0 {
		t.Fatalf("calls = %d, answerChars = %d", len(fake.calls), answerChars)
	}
	items := tracker.Items()
	for index := 0; index < 3; index++ {
		if items[index].Status != progresstracking.TaskStatusCompleted {
			t.Fatalf("item %d status = %s, want completed", index, items[index].Status)
		}
	}
	if items[3].Status != progresstracking.TaskStatusPending {
		t.Fatalf("item 3 status = %s, want pending", items[3].Status)
	}
}

// TestRunAgentTraceIsConcise 验证命令根 trace 只记录摘要，不把用户消息或 prompt 塞进上报。
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

	output, err := withRunAgentTrace(ctx, "/tmp/progress.sqlite", "event-demo", 3, func(context.Context) (runOutput, error) {
		return runOutput{Mode: "agent", Items: 3, AnswerChars: 42}, nil
	})
	if err != nil {
		t.Fatalf("withRunAgentTrace() error = %v", err)
	}
	if startName != "progress_tracking_agent_run" || endName != "progress_tracking_agent_run" {
		t.Fatalf("trace names start=%q end=%q", startName, endName)
	}
	input, ok := startInput.(runAgentTraceInput)
	if !ok {
		t.Fatalf("start input type = %T", startInput)
	}
	if input.DBPath != "/tmp/progress.sqlite" || input.PlanID != "event-demo" || input.TriggerRounds != 3 {
		t.Fatalf("trace input = %+v", input)
	}
	if output.DBPath != "/tmp/progress.sqlite" || output.PlanID != "event-demo" {
		t.Fatalf("output = %+v", output)
	}
	if _, ok := endOutput.(runOutput); !ok {
		t.Fatalf("end output type = %T", endOutput)
	}
}

// TestPrepareOnlyCreatesAndUpdatesPlan 验证 CLI 的确定性路径只操作 SQLite，不需要模型参数。
func TestPrepareOnlyCreatesAndUpdatesPlan(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "progress.sqlite")
	itemsFile := filepath.Join(t.TempDir(), "items.json")
	if err := os.WriteFile(itemsFile, []byte(`["确认场地","发布报名页"]`), 0o644); err != nil {
		t.Fatalf("write items file: %v", err)
	}

	output, err := runAgent(context.Background(), []string{
		"-prepare-only",
		"-db", dbPath,
		"-plan-id", "event-ops",
		"-items-file", itemsFile,
	})
	if err != nil {
		t.Fatalf("runAgent(create) error = %v", err)
	}
	if output.Mode != "prepare-only" || output.Items != 2 {
		t.Fatalf("output = %+v", output)
	}

	output, err = runAgent(context.Background(), []string{
		"-prepare-only",
		"-db", dbPath,
		"-plan-id", "event-ops",
		"-complete-index", "0",
		"-complete-result", "场地合同已确认",
		"-complete-files", "venue-contract.pdf",
	})
	if err != nil {
		t.Fatalf("runAgent(complete) error = %v", err)
	}
	if output.Items != 2 {
		t.Fatalf("output = %+v", output)
	}
}

// TestBuildPlanTriggerMessage 验证主流程触发消息包含工具调用边界和真实计划 index。
func TestBuildPlanTriggerMessage(t *testing.T) {
	message := buildPlanTriggerMessage(2, 3, "编辑报名页发布文案")
	for _, want := range []string{
		"第 3 次自动触发",
		"第 2 项：编辑报名页发布文案",
		"progress_resumption_context",
		"progress_start_task 标记 index=2",
		"progress_complete_task",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

// TestParseItemsSupportsJSONAndText 验证任务输入不是固定格式，便于外部系统接入。
func TestParseItemsSupportsJSONAndText(t *testing.T) {
	jsonItems, err := parseItems(`["确认预算，含茶歇","发布报名页"]`)
	if err != nil {
		t.Fatalf("parseItems(json) error = %v", err)
	}
	if len(jsonItems) != 2 || jsonItems[0] != "确认预算，含茶歇" {
		t.Fatalf("json items = %+v", jsonItems)
	}
	textItems, err := parseItems("确认场地\n准备物料")
	if err != nil {
		t.Fatalf("parseItems(text) error = %v", err)
	}
	if strings.Join(textItems, "|") != "确认场地|准备物料" {
		t.Fatalf("text items = %+v", textItems)
	}
}
