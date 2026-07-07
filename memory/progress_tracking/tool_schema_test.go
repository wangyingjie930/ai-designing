package progresstracking

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestProgressTrackingToolsExposeCompleteJSONSchema 验证模型看到完整工具参数契约，而不是裸字段名。
func TestProgressTrackingToolsExposeCompleteJSONSchema(t *testing.T) {
	tracker, ctx := newToolSchemaTracker(t)

	createTool, err := NewCreatePlanTool(tracker)
	if err != nil {
		t.Fatalf("NewCreatePlanTool() error = %v", err)
	}
	createSchema := toolSchemaJSON(t, ctx, createTool)
	assertSchemaContains(t, createSchema,
		`"required":["items"]`,
		`"description":"要初始化的可执行任务列表；每一项都应是可以被开始、完成或失败记录的具体动作"`,
		`"description":"只有用户明确要求重建已有计划时才传 true，避免覆盖已经写入 SQLite 的 checkpoint"`,
	)

	startTool, err := NewStartTaskTool(tracker)
	if err != nil {
		t.Fatalf("NewStartTaskTool() error = %v", err)
	}
	startSchema := toolSchemaJSON(t, ctx, startTool)
	assertSchemaContains(t, startSchema,
		`"required":["index"]`,
		`"description":"要标记为 in_progress 的任务下标，使用 0-based index"`,
		`"minimum":0`,
	)

	completeTool, err := NewCompleteTaskTool(tracker)
	if err != nil {
		t.Fatalf("NewCompleteTaskTool() error = %v", err)
	}
	completeSchema := toolSchemaJSON(t, ctx, completeTool)
	assertSchemaContains(t, completeSchema,
		`"required":["index","result"]`,
		`"description":"要标记完成的任务下标，使用 0-based index"`,
		`"description":"任务完成后的真实结果或证据摘要；不能留空，也不要把下一步计划当成结果"`,
		`"description":"本次任务产出的文件、材料或凭证引用，例如 venue-contract.pdf"`,
	)

	failTool, err := NewFailTaskTool(tracker)
	if err != nil {
		t.Fatalf("NewFailTaskTool() error = %v", err)
	}
	failSchema := toolSchemaJSON(t, ctx, failTool)
	assertSchemaContains(t, failSchema,
		`"required":["index","error"]`,
		`"description":"要标记失败的任务下标，使用 0-based index"`,
		`"description":"任务失败或阻塞的具体原因；用于恢复后优先处理问题"`,
	)

	resumeTool, err := NewResumptionContextTool(tracker)
	if err != nil {
		t.Fatalf("NewResumptionContextTool() error = %v", err)
	}
	if info, err := resumeTool.Info(ctx); err != nil || info.ParamsOneOf == nil {
		t.Fatalf("resume tool should expose JSON schema info=%+v err=%v", info, err)
	}
}

// newToolSchemaTracker 创建只用于 schema 推断的临时 tracker，避免测试污染真实 checkpoint。
func newToolSchemaTracker(t *testing.T) (*ProgressTracker, context.Context) {
	t.Helper()
	ctx := context.Background()
	tracker, err := NewProgressTracker(ctx, Config{
		DBPath: filepath.Join(t.TempDir(), "progress.sqlite"),
		PlanID: "tool-schema",
	})
	if err != nil {
		t.Fatalf("NewProgressTracker() error = %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return tracker, ctx
}

// toolSchemaJSON 将 Eino tool 参数 schema 转成字符串，方便断言 required、enum 和字段说明。
func toolSchemaJSON(t *testing.T, ctx context.Context, tool tool.BaseTool) string {
	t.Helper()
	info, err := tool.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("ParamsOneOf is nil")
	}
	schema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema() error = %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal schema error = %v", err)
	}
	return string(raw)
}

// assertSchemaContains 确认 schema 中包含模型决策所需的关键契约片段。
func assertSchemaContains(t *testing.T, schema string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema missing %s\nschema=%s", want, schema)
		}
	}
}
