package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestRunAgentPrepareOnlyUsesDefaultDecisionPrompt 验证 cmd 默认能读到非 coding 决策 prompt。
func TestRunAgentPrepareOnlyUsesDefaultDecisionPrompt(t *testing.T) {
	clearTotEnv(t)

	output, err := runAgent(context.Background(), []string{"-prepare-only", "-env", filepath.Join(t.TempDir(), "missing.env")})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.Method != "mcts" || output.MaxDepth != 1 || output.NSim != 1 || output.PromptChars == 0 {
		t.Fatalf("output = %+v", output)
	}
}

// TestRunAgentUsesFastMCTSDefaults 验证 cmd 默认只跑一轮 MCTS，避免真实模型验证耗时过长。
func TestRunAgentUsesFastMCTSDefaults(t *testing.T) {
	clearTotEnv(t)

	oldFactory := newChatModel
	fake := &cmdFakeModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "帮我在会议室、大学教室、咖啡馆之间选择一个读书会场地。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Method != "mcts" || output.MaxDepth != 1 || output.NSim != 1 || output.RootVisits != 1 {
		t.Fatalf("output = %+v", output)
	}
	if fake.Count() != 3 {
		t.Fatalf("fake model calls = %d, want 3", fake.Count())
	}
}

// TestRunAgentCallsTotMCTSThroughADKRunner 验证 cmd 真实调用链是 ADK Runner -> ToT -> mctsReply。
func TestRunAgentCallsTotMCTSThroughADKRunner(t *testing.T) {
	clearTotEnv(t)

	oldFactory := newChatModel
	fake := &cmdFakeModel{}
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) {
		return fake, nil
	}
	defer func() { newChatModel = oldFactory }()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "帮我在会议室、大学教室、咖啡馆之间选择一个读书会场地。",
		"-method", "mcts",
		"-max-depth", "1",
		"-nsim", "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "agent" || output.Method != "mcts" || output.AnswerChars == 0 || output.RootChildren == 0 || output.RootVisits != 2 || !output.UsedADKCustomizedData {
		t.Fatalf("output = %+v", output)
	}
	if fake.Count() < 3 {
		t.Fatalf("fake model calls = %d, want >= 3", fake.Count())
	}
}

// TestLoadPromptFromFile 验证用户可以直接换一个外部 prompt 文件完成调用。
func TestLoadPromptFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(path, []byte("请帮我选择家庭旅行目的地。"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, err := loadPrompt(runConfig{PromptFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "请帮我选择家庭旅行目的地。" {
		t.Fatalf("prompt = %q", prompt)
	}
}

// clearTotEnv 清理外部 ToT 环境变量，保证 cmd 默认值测试不被本机 .env 或 shell 污染。
func clearTotEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"TOT_REASONING_METHOD",
		"TOT_ANSWER_APPROACH",
		"TOT_MAX_DEPTH",
		"TOT_BEAM_SIZE",
		"TOT_NSIM",
		"TOT_FOREST_SIZE",
		"TOT_RATING_SCALE",
		"TOT_SCOPE",
	} {
		t.Setenv(key, "")
	}
}

// cmdFakeModel 根据 ToT 内部 prompt 类型返回固定文本，用于验证 cmd 调用路径。
type cmdFakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 模拟 thinker、grader 和最终回答三类模型调用。
func (m *cmdFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	user := ""
	system := ""
	if len(input) > 0 {
		system = input[0].Content
		user = input[len(input)-1].Content
	}
	switch {
	case strings.Contains(user, "How should the thinking process continue?"):
		return schema.AssistantMessage(strings.Join([]string{
			"REFLECTION:",
			"这个场地决策需要比较成本、体验和风险。",
			"",
			"**Possible Options:**",
			"Option 1: 从预算和机动费用比较三个方案",
			"Option 2: 从参与者体验和二次报名目标比较三个方案",
			"Option 3: 从设备稳定和现场风险比较三个方案",
			"Option 4: TERMINATE",
		}, "\n"), nil), nil
	case strings.Contains(user, "Rate:") || strings.Contains(system, "评价"):
		return schema.AssistantMessage("Rating: 9\n理由：能推进活动决策。", nil), nil
	case strings.Contains(user, "Final Answer:"):
		return schema.AssistantMessage("最终答案：推荐商业会议室作为主方案，同时用预算表控制茶歇和物料。", nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前命令不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *cmdFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明 ToT 进行了多次内部调用。
func (m *cmdFakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
