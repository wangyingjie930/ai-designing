package failuretracking

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestFailureTrackingToolsExposeCompleteJSONSchema 验证模型看到的是完整工具参数契约，而不是裸字段名。
func TestFailureTrackingToolsExposeCompleteJSONSchema(t *testing.T) {
	journal, ctx := newTestJournal(t)

	searchTool, err := NewSearchFailuresTool(journal)
	if err != nil {
		t.Fatalf("NewSearchFailuresTool() error = %v", err)
	}
	searchSchema := toolSchemaJSON(t, ctx, searchTool)
	assertSchemaContains(t, searchSchema,
		`"required":["task_family"]`,
		`"description":"任务族，例如 payroll_run、hotel_frontdesk_recovery"`,
		`"description":"即将调用的高风险工具名，例如 create_payroll_batch"`,
		`"description":"本次工具调用涉及的机械状态键，例如 payroll_group_id"`,
	)

	recordTool, err := NewRecordFailureTool(journal)
	if err != nil {
		t.Fatalf("NewRecordFailureTool() error = %v", err)
	}
	recordSchema := toolSchemaJSON(t, ctx, recordTool)
	assertSchemaContains(t, recordSchema,
		`"required":["entry"]`,
		`"description":"完整六层失败日记条目"`,
		`"description":"失败边界，决定这件事是否值得沉淀"`,
		`"enum":["hard_failure","gate_failure","semantic_failure","safety_failure"]`,
		`"description":"审查状态，只有 approved 能进入召回"`,
		`"enum":["draft","needs_review","approved","archived"]`,
		`"description":"证据包：三平面失败快照和原始观察"`,
		`"description":"结构化召回条件，执行型失败优先依赖这些键"`,
	)
	assertSchemaNotContains(t, recordSchema, "created_at", "updated_at", "recalled_count")
}

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

func assertSchemaContains(t *testing.T, schema string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema missing %s\nschema=%s", want, schema)
		}
	}
}

func assertSchemaNotContains(t *testing.T, schema string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(schema, value) {
			t.Fatalf("schema unexpectedly contains %s\nschema=%s", value, schema)
		}
	}
}
