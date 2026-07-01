package skillmode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/skill"
)

const DefaultSkillsDir = "reflection/skillmode/skills"

// Mode 标识本示例要演示的 Skill Middleware 使用模式。
type Mode string

const (
	ModeInline          Mode = "inline"
	ModeForkWithContext Mode = "fork_with_context"
	ModeFork            Mode = "fork"
)

// Scenario 描述一个有业务理由的 skill 使用场景。
type Scenario struct {
	Mode         Mode
	SkillName    string
	Title        string
	Rationale    string
	DefaultQuery string
	ContextMode  skill.ContextMode
	AgentName    string
}

// DefaultScenarios 返回三个模式对应的客服运营场景。
func DefaultScenarios() map[Mode]Scenario {
	return map[Mode]Scenario{
		ModeInline: {
			Mode:      ModeInline,
			SkillName: "support_reply_inline",
			Title:     "客服值班长回复规范",
			Rationale: "回复风格和升级边界是短规则，直接贴进主 agent 上下文最省成本。",
			DefaultQuery: strings.Join([]string{
				"客户说课程顾问 24 小时没有回复，情绪明显升级。",
				"请值班长先安抚客户，并判断是否需要升级给主管。",
			}, "\n"),
		},
		ModeForkWithContext: {
			Mode:        ModeForkWithContext,
			SkillName:   "compensation_review_with_context",
			Title:       "补偿策略专家复核",
			Rationale:   "补偿判断必须理解当前客诉事实和前文承诺，所以需要携带主对话上下文进入专家子 agent。",
			ContextMode: skill.ContextModeForkWithContext,
			AgentName:   "compensation_specialist_agent",
			DefaultQuery: strings.Join([]string{
				"客户两周内第二次被临时改课，前一次客服已承诺不会再发生。",
				"请判断这次是否应提供补偿，并给出值班长可执行的话术。",
			}, "\n"),
		},
		ModeFork: {
			Mode:        ModeFork,
			SkillName:   "compliance_review_isolated",
			Title:       "合规风险隔离审查",
			Rationale:   "合规审查要避免继承主对话里未经验证的承诺或情绪化措辞，因此使用干净上下文 fork。",
			ContextMode: skill.ContextModeFork,
			AgentName:   "compliance_guard_agent",
			DefaultQuery: strings.Join([]string{
				"客户要求客服承诺“保证下月成绩提升”，并要求写进补偿方案。",
				"请先做隔离合规审查，再给出可回复版本。",
			}, "\n"),
		},
	}
}

// ScenarioForMode 根据模式返回场景配置，空模式默认使用 inline。
func ScenarioForMode(mode Mode) (Scenario, error) {
	if strings.TrimSpace(string(mode)) == "" {
		mode = ModeInline
	}
	scenario, ok := DefaultScenarios()[mode]
	if !ok {
		return Scenario{}, fmt.Errorf("unknown skill mode: %s", mode)
	}
	return scenario, nil
}

// NewSkillBackend 从仓库里的真实 SKILL.md 文件创建官方 skill backend。
func NewSkillBackend(ctx context.Context, scenarios map[Mode]Scenario) (skill.Backend, error) {
	skillsDir, err := resolveDefaultSkillsDir()
	if err != nil {
		return nil, err
	}
	fsBackend, err := newLocalSkillFilesystem(skillsDir)
	if err != nil {
		return nil, err
	}
	return skill.NewBackendFromFilesystem(ctx, &skill.BackendFromFilesystemConfig{
		Backend: fsBackend,
		BaseDir: skillsDir,
	})
}

// resolveDefaultSkillsDir 兼容 go test 的包目录工作区和 go run 的仓库根目录工作区。
func resolveDefaultSkillsDir() (string, error) {
	if info, err := os.Stat(DefaultSkillsDir); err == nil && info.IsDir() {
		return filepath.Abs(DefaultSkillsDir)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot resolve skill source directory")
	}
	dir := filepath.Join(filepath.Dir(file), "skills")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir, nil
	}
	return "", fmt.Errorf("skill directory not found: %s", DefaultSkillsDir)
}
