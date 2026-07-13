package claudetasklist

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TestTaskToolsExposeCompleteJSONSchema 验证四工具把必填项、中文语义和状态枚举完整暴露给模型。
func TestTaskToolsExposeCompleteJSONSchema(t *testing.T) {
	store, err := NewStore(t.TempDir(), "tool-schema")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := BuildTools(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("tools=%d want=4", len(tools))
	}
	ctx := context.Background()
	wants := []struct {
		name  string
		parts []string
		empty bool
	}{
		{
			name: TaskCreateToolName,
			parts: []string{
				`"required":["subject","description"]`,
				`"description":"任务标题，使用简洁、可验证的动作表述"`,
				`"description":"任务的完整目标、边界和完成标准"`,
				`"description":"任务进行中展示的现在进行式文案"`,
				`"description":"可选扩展信息；不能承载任务核心状态"`,
			},
		},
		{
			name: TaskGetToolName,
			parts: []string{
				`"required":["taskId"]`,
				`"description":"要读取或更新的任务 ID"`,
			},
		},
		{
			name: TaskUpdateToolName,
			parts: []string{
				`"required":["taskId"]`,
				`"description":"要读取或更新的任务 ID"`,
				`"description":"新状态；deleted 表示删除任务"`,
				`"enum":["pending","in_progress","completed","deleted"]`,
				`"description":"要新增的后续任务 ID；当前任务会阻塞这些任务"`,
				`"description":"要新增的前置任务 ID；这些任务未完成时会阻塞当前任务"`,
				`"description":"按键合并扩展信息；值为 null 时删除对应键"`,
			},
		},
		{name: TaskListToolName, empty: true},
	}

	for index, want := range wants {
		info, schemaJSON := taskToolSchemaJSON(t, ctx, tools[index])
		if info.Name != want.name {
			t.Fatalf("tool[%d] name=%q want=%q", index, info.Name, want.name)
		}
		if !containsChinese(info.Desc) {
			t.Fatalf("tool %s should have a Chinese description: %q", info.Name, info.Desc)
		}
		for _, part := range want.parts {
			if !strings.Contains(schemaJSON, part) {
				t.Fatalf("%s schema missing %s\nschema=%s", want.name, part, schemaJSON)
			}
		}
		if want.empty {
			assertEmptyObjectSchema(t, info.Name, schemaJSON)
		}
	}
}

// taskToolSchemaJSON 读取 Eino 工具信息并序列化模型实际可见的参数 Schema。
func taskToolSchemaJSON(t *testing.T, ctx context.Context, current tool.BaseTool) (*schema.ToolInfo, string) {
	t.Helper()
	info, err := current.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error=%v", err)
	}
	if info.ParamsOneOf == nil {
		t.Fatalf("%s ParamsOneOf is nil", info.Name)
	}
	params, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("%s ToJSONSchema() error=%v", info.Name, err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("%s marshal schema error=%v", info.Name, err)
	}
	return info, string(raw)
}

// assertEmptyObjectSchema 确认无参数 TaskList 仍向模型提供非 nil 的空对象参数契约。
func assertEmptyObjectSchema(t *testing.T, toolName, schemaJSON string) {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		t.Fatalf("%s decode schema error=%v", toolName, err)
	}
	if schema["type"] != "object" {
		t.Fatalf("%s schema type=%v schema=%s", toolName, schema["type"], schemaJSON)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		t.Fatalf("%s properties=%v schema=%s", toolName, schema["properties"], schemaJSON)
	}
}

// containsChinese 判断工具说明是否真正包含中文，而不是只有英文占位文本。
func containsChinese(value string) bool {
	for _, current := range value {
		if current >= '\u4e00' && current <= '\u9fff' {
			return true
		}
	}
	return false
}
