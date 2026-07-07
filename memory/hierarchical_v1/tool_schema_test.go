package hierarchicalv1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestHierarchicalV1ToolsExposeCompleteJSONSchema 验证模型看到完整参数契约，而不是裸字段名。
func TestHierarchicalV1ToolsExposeCompleteJSONSchema(t *testing.T) {
	memory, err := NewHierarchicalMemory(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	writeTool, err := NewWriteMemoryTool(memory)
	if err != nil {
		t.Fatalf("NewWriteMemoryTool() error = %v", err)
	}
	writeSchema := toolSchemaJSON(t, ctx, writeTool)
	assertSchemaContains(t, writeSchema,
		`"required":["key","value","layer","source"]`,
		`"description":"稳定业务键；更新同一个偏好、项目或任务时必须复用已有 key 做 upsert"`,
		`"description":"结构化记忆内容，必须是 JSON object，避免把不稳定标量直接写入长期记忆"`,
		`"enum":["policy","project","user","task","scratchpad"]`,
		`"description":"记忆来源；policy/project 不接受 agent_inference 直接写入"`,
		`"enum":["human","tool","agent_inference","verified_trace","failure_review"]`,
		`"description":"证据引用，例如 user:round-2-confirmed 或 tool:inventory-check-001；需要证据的层必须填写可信来源或证据"`,
	)

	promoteTool, err := NewPromoteScratchpadTool(memory)
	if err != nil {
		t.Fatalf("NewPromoteScratchpadTool() error = %v", err)
	}
	promoteSchema := toolSchemaJSON(t, ctx, promoteTool)
	assertSchemaContains(t, promoteSchema,
		`"required":["key","evidence_refs"]`,
		`"description":"要提升的 scratchpad key；工程代码会根据稳定 key 前缀决定正式层级"`,
		`"description":"本次提升新增的证据引用；必须证明 scratchpad 内容已经被用户、工具或审查链路确认"`,
	)
	assertSchemaNotContains(t, promoteSchema, "target_layer")

	for _, factory := range []func(*HierarchicalMemory) (tool.BaseTool, error){
		NewAssembleContextTool,
		NewHealthReportTool,
	} {
		created, err := factory(memory)
		if err != nil {
			t.Fatalf("tool factory error = %v", err)
		}
		if info, err := created.Info(ctx); err != nil || info.ParamsOneOf == nil {
			t.Fatalf("tool should expose JSON schema info=%+v err=%v", info, err)
		}
	}
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
