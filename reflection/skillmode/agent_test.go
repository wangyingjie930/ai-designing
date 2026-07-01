package skillmode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestDefaultScenariosExposeThreeSkillModes 验证三个示例不是机械换参数，而是各有业务理由。
func TestDefaultScenariosExposeThreeSkillModes(t *testing.T) {
	scenarios := DefaultScenarios()
	wants := map[Mode]skill.ContextMode{
		ModeInline:          "",
		ModeForkWithContext: skill.ContextModeForkWithContext,
		ModeFork:            skill.ContextModeFork,
	}
	if len(scenarios) != len(wants) {
		t.Fatalf("scenarios len = %d, want %d", len(scenarios), len(wants))
	}
	for mode, contextMode := range wants {
		scenario, ok := scenarios[mode]
		if !ok {
			t.Fatalf("missing scenario mode %q", mode)
		}
		if scenario.ContextMode != contextMode {
			t.Fatalf("%s context = %q, want %q", mode, scenario.ContextMode, contextMode)
		}
		for label, value := range map[string]string{
			"skill name": scenario.SkillName,
			"rationale":  scenario.Rationale,
			"query":      scenario.DefaultQuery,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s %s is empty", mode, label)
			}
		}
	}
}

// TestSkillFilesExistOnDisk 验证示例 Skill 以真实 SKILL.md 文件存在，便于直接阅读和维护。
func TestSkillFilesExistOnDisk(t *testing.T) {
	skillsDir, err := resolveDefaultSkillsDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range DefaultScenarios() {
		path := filepath.Join(skillsDir, scenario.SkillName, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, want := range []string{"---", "name: " + scenario.SkillName, "description:"} {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing %q:\n%s", path, want, content)
			}
		}
		if scenario.ContextMode != "" && !strings.Contains(content, "context: "+string(scenario.ContextMode)) {
			t.Fatalf("%s missing context %q:\n%s", path, scenario.ContextMode, content)
		}
		if scenario.AgentName != "" && !strings.Contains(content, "agent: "+scenario.AgentName) {
			t.Fatalf("%s missing agent %q:\n%s", path, scenario.AgentName, content)
		}
		parts := strings.Split(content, "---")
		if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
			t.Fatalf("%s has empty skill body:\n%s", path, content)
		}
	}
}

// TestSkillBackendLoadsScenarioFrontmatter 验证文件系统里的 skill 能被官方 Skill Middleware 发现。
func TestSkillBackendLoadsScenarioFrontmatter(t *testing.T) {
	ctx := context.Background()
	backend, err := NewSkillBackend(ctx, DefaultScenarios())
	if err != nil {
		t.Fatalf("NewSkillBackend() error = %v", err)
	}
	matters, err := backend.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	seen := map[string]skill.FrontMatter{}
	for _, matter := range matters {
		seen[matter.Name] = matter
	}
	if seen["support_reply_inline"].Context != "" {
		t.Fatalf("inline context = %q", seen["support_reply_inline"].Context)
	}
	if seen["compensation_review_with_context"].Context != skill.ContextModeForkWithContext {
		t.Fatalf("fork_with_context context = %q", seen["compensation_review_with_context"].Context)
	}
	if seen["compliance_review_isolated"].Context != skill.ContextModeFork {
		t.Fatalf("fork context = %q", seen["compliance_review_isolated"].Context)
	}
}

// TestADKRunnerUsesSkillTool 验证真实 ADK Runner 会通过 Skill Middleware 调用 skill，再由主 agent 汇总。
func TestADKRunnerUsesSkillTool(t *testing.T) {
	ctx := context.Background()
	fake := &skillModeFakeModel{targetSkill: "support_reply_inline"}
	runner, err := NewRunner(ctx, Config{
		Mode:          ModeInline,
		Model:         fake,
		SubAgentModel: fake,
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	response, err := QueryRunner(ctx, runner, "客户抱怨课程顾问 24 小时未回复，请给值班长处理建议。")
	if err != nil {
		t.Fatalf("QueryRunner() error = %v", err)
	}
	if !strings.Contains(response.Message, "已按 support_reply_inline 完成处理") {
		t.Fatalf("response = %q", response.Message)
	}
	if fake.skillToolCalls != 1 {
		t.Fatalf("skill tool calls = %d, want 1", fake.skillToolCalls)
	}
}

// TestForkScenarioPassesTaskTextToIsolatedSubAgent 验证 fork 隔离上下文时仍会显式传入本轮任务文本。
func TestForkScenarioPassesTaskTextToIsolatedSubAgent(t *testing.T) {
	ctx := context.Background()
	query := "请审查客服回复：我们保证你下月成绩提升，并额外补偿两节课。"
	fake := &skillModeFakeModel{targetSkill: "compliance_review_isolated", task: query}
	runner, err := NewRunner(ctx, Config{
		Mode:          ModeFork,
		Model:         fake,
		SubAgentModel: fake,
		MaxIterations: 5,
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := QueryRunner(ctx, runner, query); err != nil {
		t.Fatalf("QueryRunner() error = %v", err)
	}
	if fake.subAgentCalls != 1 {
		t.Fatalf("sub agent calls = %d, want 1", fake.subAgentCalls)
	}
	if got := strings.Join(fake.subAgentInputs, "\n"); !strings.Contains(got, query) {
		t.Fatalf("sub agent input missing task text %q:\n%s", query, got)
	}
}

// TestForkWithContextScenarioUsesSubAgent 验证 fork_with_context 会进入 AgentHub 提供的专家子 agent。
func TestForkWithContextScenarioUsesSubAgent(t *testing.T) {
	ctx := context.Background()
	fake := &skillModeFakeModel{targetSkill: "compensation_review_with_context"}
	runner, err := NewRunner(ctx, Config{
		Mode:          ModeForkWithContext,
		Model:         fake,
		SubAgentModel: fake,
		MaxIterations: 5,
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	response, err := QueryRunner(ctx, runner, "客户已连续两次被改课，需要结合前文判断是否补偿。")
	if err != nil {
		t.Fatalf("QueryRunner() error = %v", err)
	}
	if !strings.Contains(response.Message, "专家复核") {
		t.Fatalf("response = %q", response.Message)
	}
	if fake.subAgentCalls != 1 {
		t.Fatalf("sub agent calls = %d, want 1", fake.subAgentCalls)
	}
}

// skillModeFakeModel 模拟主 agent 调 skill 工具，fork 模式下再模拟专家子 agent 输出。
type skillModeFakeModel struct {
	targetSkill    string
	task           string
	parentCalls    int
	subAgentCalls  int
	skillToolCalls int
	subAgentInputs []string
}

// Generate 根据系统提示和历史消息区分主 agent 与子 agent。
func (m *skillModeFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	system := firstContentByRole(input, schema.System)
	if strings.Contains(system, "Skill 模式专家子 Agent") {
		m.subAgentCalls++
		m.subAgentInputs = append(m.subAgentInputs, joinMessageContents(input))
		return schema.AssistantMessage("专家复核：建议补偿 1 次体验课，并要求 2 小时内回访。", nil), nil
	}

	m.parentCalls++
	if firstMessageByRole(input, schema.Tool) == nil {
		m.skillToolCalls++
		task := m.task
		if strings.TrimSpace(task) == "" {
			task = firstContentByRole(input, schema.User)
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_skill_mode",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "skill",
				Arguments: m.skillArguments(task),
			},
		}}), nil
	}
	if m.targetSkill == "compensation_review_with_context" {
		return schema.AssistantMessage("已结合专家复核完成处理。", nil), nil
	}
	return schema.AssistantMessage("已按 "+m.targetSkill+" 完成处理。", nil), nil
}

// Stream 当前测试不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *skillModeFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// skillArguments 模拟模型调用 skill 工具时把本轮任务作为显式参数传入。
func (m *skillModeFakeModel) skillArguments(task string) string {
	args := struct {
		Skill string `json:"skill"`
		Task  string `json:"task"`
	}{
		Skill: m.targetSkill,
		Task:  task,
	}
	data, _ := json.Marshal(args)
	return string(data)
}

// joinMessageContents 汇总消息正文，便于断言子 agent 是否收到关键业务输入。
func joinMessageContents(messages []*schema.Message) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

// firstContentByRole 返回指定角色的第一条内容。
func firstContentByRole(messages []*schema.Message, role schema.RoleType) string {
	if message := firstMessageByRole(messages, role); message != nil {
		return message.Content
	}
	return ""
}

// firstMessageByRole 返回指定角色的第一条消息。
func firstMessageByRole(messages []*schema.Message, role schema.RoleType) *schema.Message {
	for _, message := range messages {
		if message.Role == role {
			return message
		}
	}
	return nil
}
