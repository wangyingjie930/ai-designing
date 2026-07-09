package researchswarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestRoleToolsExposeExpectedNames 验证不同 teammate 只看到自己角色需要的工具。
func TestRoleToolsExposeExpectedNames(t *testing.T) {
	store := openTestStore(t)
	for _, tc := range []struct {
		role  AgentRole
		names []string
	}{
		{RoleSearcher, []string{WebSearchToolName, SaveSourceCardToolName, SendMessageToolName, UpdateTaskToolName}},
		{RoleAnalyst, []string{ListSourceCardsToolName, SaveReportSectionToolName, SendMessageToolName, UpdateTaskToolName}},
		{RoleWriter, []string{ListSourceCardsToolName, ListReportSectionsToolName, SaveReportSectionToolName, SendMessageToolName, UpdateTaskToolName}},
	} {
		tools, err := NewRoleTools(context.Background(), ToolConfig{
			Store:        store,
			SearchClient: NewFakeSearchClient(),
			TeamName:     "team-a",
			AgentID:      string(tc.role) + "@team-a",
			Role:         tc.role,
		})
		requireNoError(t, err)
		if names := baseToolNames(t, tools); !sameStringSet(names, tc.names) {
			t.Fatalf("role %s tools = %v, want %v", tc.role, names, tc.names)
		}
	}
}

// TestSendMessageToolSchemaHasDescriptions 验证模型可见工具 schema 带字段说明，避免裸 JSON 参数。
func TestSendMessageToolSchemaHasDescriptions(t *testing.T) {
	store := openTestStore(t)
	tools, err := NewRoleTools(context.Background(), ToolConfig{
		Store:        store,
		SearchClient: NewFakeSearchClient(),
		TeamName:     "team-a",
		AgentID:      "searcher@team-a",
		Role:         RoleSearcher,
	})
	requireNoError(t, err)

	var send tool.BaseTool
	for _, candidate := range tools {
		info, err := candidate.Info(context.Background())
		requireNoError(t, err)
		if info.Name == SendMessageToolName {
			send = candidate
			break
		}
	}
	if send == nil {
		t.Fatal("send_message tool not found")
	}
	schemaText := toolSchemaText(t, send)
	for _, want := range []string{`"required":["to_agent","kind","content"]`, "跨 agent 消息接收者", "消息正文"} {
		if !strings.Contains(schemaText, want) {
			t.Fatalf("schema missing %q\n%s", want, schemaText)
		}
	}
}

// TestLeaderToolsDoNotExposeArtifactPolling 验证 report_director 不再通过轮询工具等待产物。
func TestLeaderToolsDoNotExposeArtifactPolling(t *testing.T) {
	store := openTestStore(t)
	team, err := CreateTeam(context.Background(), TeamConfig{
		TeamName: "team-a",
		Topic:    "AI Agent 外部搜索风险",
		Store:    store,
		Spawner:  &passiveSpawner{},
	})
	requireNoError(t, err)
	tools, err := NewLeaderTools(context.Background(), team)
	requireNoError(t, err)

	names := baseToolNames(t, tools)
	if !sameStringSet(names, []string{SpawnTeammateToolName}) {
		t.Fatalf("leader tools = %v, want only spawn_teammate", names)
	}
}

func baseToolNames(t *testing.T, tools []tool.BaseTool) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		info, err := candidate.Info(context.Background())
		requireNoError(t, err)
		names = append(names, info.Name)
	}
	return names
}

func toolSchemaText(t *testing.T, candidate tool.BaseTool) string {
	t.Helper()
	info, err := candidate.Info(context.Background())
	requireNoError(t, err)
	if info.ParamsOneOf == nil {
		t.Fatal("ParamsOneOf is nil")
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	requireNoError(t, err)
	raw, err := json.Marshal(js)
	requireNoError(t, err)
	return string(raw)
}

func containsAllStrings(values []string, expected []string) bool {
	for _, want := range expected {
		if !containsString(values, want) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	return containsAllStrings(left, right) && containsAllStrings(right, left)
}
