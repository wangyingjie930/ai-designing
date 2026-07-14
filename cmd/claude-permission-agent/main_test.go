package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	claudepermissions "ai-designing/action/claude_permissions"
)

// TestPrepareOnlyPrintsClaudePermissionPipeline 验证无需模型配置也能检查权限模式和工具分类。
func TestPrepareOnlyPrintsClaudePermissionPipeline(t *testing.T) {
	var output bytes.Buffer
	result, err := runAgent(context.Background(), []string{"-prepare-only", "-mode", "plan"}, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != claudepermissions.PermissionModePlan || result.ToolCount != 5 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(output.String(), "delete_tenant=destructive") {
		t.Fatalf("output = %s", output.String())
	}
}

// TestReadApprovalSupportsUpdatedArguments 验证人工审批可以像 Claude Code 一样修改参数后恢复原调用。
func TestReadApprovalSupportsUpdatedArguments(t *testing.T) {
	request := claudepermissions.PermissionRequest{
		ID:            "permission-1",
		ToolName:      "apply_feature_flag",
		ArgumentsJSON: `{"enabled":true}`,
		Reason:        "tool requires confirmation",
	}
	var output bytes.Buffer
	response := readApproval(strings.NewReader("e\n{\"enabled\":false}\n"), &output, request)
	if response.Behavior != claudepermissions.PermissionAllow {
		t.Fatalf("behavior = %q", response.Behavior)
	}
	if response.UpdatedArgumentsJSON != `{"enabled":false}` || response.RequestID != request.ID {
		t.Fatalf("response = %+v", response)
	}
}
