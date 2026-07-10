package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestRunAgentPrepareOnlyCreatesStorageWithoutModel 验证确定性准备路径不需要 API key。
func TestRunAgentPrepareOnlyCreatesStorageWithoutModel(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	output, err := runAgent(context.Background(), []string{
		"-prepare-only", "-memory-dir", memoryDir, "-env-file", filepath.Join(t.TempDir(), ".env"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.MemoryDir != memoryDir {
		t.Fatalf("output = %+v", output)
	}
	for _, path := range []string{filepath.Join(memoryDir, "MEMORY.md"), filepath.Join(memoryDir, "team", "MEMORY.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected index %s: %v", path, err)
		}
	}
}

// TestRunAgentUsesOneModelForThreeIsolatedRoles 验证命令装配真实三阶段并跑通三轮闭环。
func TestRunAgentUsesOneModelForThreeIsolatedRoles(t *testing.T) {
	oldFactory := newChatModel
	scripted := &interviewScriptModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return scripted, nil
	}
	defer func() { newChatModel = oldFactory }()

	envPath := writeTestFile(t, ".env", "OPENAI_API_KEY=test-key\nLLM_MODEL=test-model\n")
	roundsPath := writeTestFile(t, "rounds.txt", strings.Join([]string{
		"请记住，我个人更喜欢新增代码写中文用途注释。",
		"团队约定：所有新工具参数都要提供 jsonschema_description。",
		"以后在这个项目里新增工具要注意什么？",
	}, "\n---\n"))
	memoryDir := filepath.Join(t.TempDir(), "memory")
	output, err := runAgent(context.Background(), []string{
		"-env-file", envPath, "-memory-dir", memoryDir, "-rounds-file", roundsPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Rounds != 3 || output.Written != 2 || output.Recalled != 3 || output.AnswerChars == 0 {
		t.Fatalf("output = %+v", output)
	}
	if scripted.extractCalls != 3 || scripted.selectCalls != 3 || scripted.mainCalls != 3 {
		t.Fatalf("calls extract=%d select=%d main=%d", scripted.extractCalls, scripted.selectCalls, scripted.mainCalls)
	}
}

// TestParseRoundMessages 验证面试场景既支持分隔文本，也支持 JSON 字符串数组。
func TestParseRoundMessages(t *testing.T) {
	rounds, err := parseRoundMessages("第一轮\n---\n第二轮\n---\n第三轮")
	if err != nil || len(rounds) != 3 {
		t.Fatalf("rounds = %+v, err = %v", rounds, err)
	}
	jsonRounds, err := parseRoundMessages(`["A","B",""]`)
	if err != nil || len(jsonRounds) != 2 {
		t.Fatalf("json rounds = %+v, err = %v", jsonRounds, err)
	}
}

// interviewScriptModel 根据三个 prompt marker 模拟真实共享模型的隔离角色。
type interviewScriptModel struct {
	extractCalls int
	selectCalls  int
	mainCalls    int
}

// Generate 为选择、回答、提取三类调用返回确定性脚本。
func (m *interviewScriptModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(input) == 0 || input[0] == nil {
		return nil, fmt.Errorf("missing system prompt")
	}
	system := input[0].Content
	switch {
	case strings.Contains(system, "[AUTO_MEMORY_SELECT]"):
		m.selectCalls++
		switch m.selectCalls {
		case 1:
			return schema.AssistantMessage(`[]`, nil), nil
		case 2:
			return schema.AssistantMessage(`[{"scope":"private","topic":"comment-style"}]`, nil), nil
		default:
			return schema.AssistantMessage(`[{"scope":"private","topic":"comment-style"},{"scope":"team","topic":"tool-schema"}]`, nil), nil
		}
	case strings.Contains(system, "[AUTO_MEMORY_MAIN]"):
		m.mainCalls++
		return schema.AssistantMessage(fmt.Sprintf("第 %d 轮回答。", m.mainCalls), nil), nil
	case strings.Contains(system, "[AUTO_MEMORY_EXTRACT]"):
		m.extractCalls++
		switch m.extractCalls {
		case 1:
			return schema.AssistantMessage(`[{"type":"user","scope":"private","topic":"comment-style","description":"用户偏好中文注释","content":"新增函数和结构体前写中文用途注释。"}]`, nil), nil
		case 2:
			return schema.AssistantMessage(`[{"type":"project","scope":"team","topic":"tool-schema","description":"工具参数描述约定","content":"所有新工具参数都要提供 jsonschema_description。"}]`, nil), nil
		default:
			return schema.AssistantMessage(`[]`, nil), nil
		}
	default:
		return nil, fmt.Errorf("unknown system prompt: %s", system)
	}
}

// Stream 满足 BaseChatModel；命令核心链路使用 Generate。
func (m *interviewScriptModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// writeTestFile 在临时目录写入命令测试输入。
func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
