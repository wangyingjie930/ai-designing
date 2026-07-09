package claudecompaction

import (
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxTokensPerSkill = 800
	defaultSkillTokenBudget  = 2_400
)

// InvokedSkill 表示运行时已经调用过、压缩后需要继续保留的 skill 内容。
type InvokedSkill struct {
	AgentID   string
	Name      string
	Path      string
	Content   string
	InvokedAt time.Time
}

// SkillStateConfig 控制 invoked skill 在 compact attachment 中的预算和时间来源。
type SkillStateConfig struct {
	Now               func() time.Time
	MaxTokensPerSkill int
	TotalTokenBudget  int
}

// SkillState 记录运行时 skill 调用状态，模拟 Claude Code 的 invokedSkills / sentSkillNames 边界。
type SkillState struct {
	mu      sync.Mutex
	config  SkillStateConfig
	invoked map[string]InvokedSkill
}

// NewSkillState 创建运行时 skill 状态表。
func NewSkillState(config SkillStateConfig) *SkillState {
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.MaxTokensPerSkill <= 0 {
		config.MaxTokensPerSkill = defaultMaxTokensPerSkill
	}
	if config.TotalTokenBudget <= 0 {
		config.TotalTokenBudget = defaultSkillTokenBudget
	}
	return &SkillState{
		config:  config,
		invoked: make(map[string]InvokedSkill),
	}
}

// RecordInvokedSkill 记录一次 skill 调用；同 agent 同名 skill 以后一次内容为准。
func (s *SkillState) RecordInvokedSkill(skill InvokedSkill) {
	if s == nil || strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Content) == "" {
		return
	}
	if skill.InvokedAt.IsZero() {
		skill.InvokedAt = s.config.Now()
	}
	skill.AgentID = normalizeAgentID(skill.AgentID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invoked[skillStateKey(skill.AgentID, skill.Name)] = skill
}

// InvokedSkillsForAgent 返回指定 agent 已调用 skill，按最近调用优先并应用 token 预算。
func (s *SkillState) InvokedSkillsForAgent(agentID string) []RestoredSkill {
	if s == nil {
		return nil
	}
	agentID = normalizeAgentID(agentID)
	s.mu.Lock()
	skills := make([]InvokedSkill, 0, len(s.invoked))
	for _, skill := range s.invoked {
		if normalizeAgentID(skill.AgentID) == agentID {
			skills = append(skills, skill)
		}
	}
	maxTokensPerSkill := s.config.MaxTokensPerSkill
	totalBudget := s.config.TotalTokenBudget
	s.mu.Unlock()

	sort.SliceStable(skills, func(i int, j int) bool {
		return skills[i].InvokedAt.After(skills[j].InvokedAt)
	})

	var out []RestoredSkill
	usedTokens := 0
	for _, skill := range skills {
		content := truncateToApproxTokens(skill.Content, maxTokensPerSkill)
		tokens := EstimateTokens(content)
		if usedTokens+tokens > totalBudget {
			continue
		}
		usedTokens += tokens
		out = append(out, RestoredSkill{
			Name:    skill.Name,
			Path:    skill.Path,
			Content: content,
		})
	}
	return out
}

// RestoreFromMessages 从 post-compact invoked_skill 附件恢复运行时状态，支持 resume 后继续多次 compact。
func (s *SkillState) RestoreFromMessages(messages []Message, agentID string) {
	if s == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	for _, msg := range messages {
		if msg.Attachment == nil || msg.Attachment.Type != AttachmentInvokedSkill {
			continue
		}
		s.RecordInvokedSkill(InvokedSkill{
			AgentID:   agentID,
			Name:      msg.Attachment.Name,
			Path:      msg.Attachment.Path,
			Content:   msg.Attachment.Content,
			InvokedAt: msg.Timestamp,
		})
	}
}

// restoreReferencesWithSkillState 合并手动恢复引用和运行时 skill 状态，避免调用方再手填 invoked skills。
func restoreReferencesWithSkillState(refs RestoreReferences, skillState *SkillState, agentID string) RestoreReferences {
	out := refs.Clone()
	if skillState == nil {
		return out
	}
	out.InvokedSkills = mergeRestoredSkills(out.InvokedSkills, skillState.InvokedSkillsForAgent(agentID))
	return out
}

// mergeRestoredSkills 合并 skill 列表；同名同路径时保留靠前项，避免重复注入。
func mergeRestoredSkills(existing []RestoredSkill, added []RestoredSkill) []RestoredSkill {
	if len(existing) == 0 {
		return cloneRestoredSkills(added)
	}
	out := cloneRestoredSkills(existing)
	seen := make(map[string]bool, len(out))
	for _, skill := range out {
		seen[restoredSkillKey(skill)] = true
	}
	for _, skill := range added {
		key := restoredSkillKey(skill)
		if seen[key] {
			continue
		}
		out = append(out, skill)
		seen[key] = true
	}
	return out
}

// cloneRestoredSkills 复制 skill 恢复引用，隔离调用方切片修改。
func cloneRestoredSkills(skills []RestoredSkill) []RestoredSkill {
	if len(skills) == 0 {
		return nil
	}
	out := make([]RestoredSkill, len(skills))
	copy(out, skills)
	return out
}

// restoredSkillKey 生成 skill 去重键。
func restoredSkillKey(skill RestoredSkill) string {
	return skill.Name + "\x00" + skill.Path
}

// skillStateKey 生成 agent scoped skill 状态键。
func skillStateKey(agentID string, skillName string) string {
	return normalizeAgentID(agentID) + "\x00" + skillName
}

// normalizeAgentID 统一主线程 agent 的空值表示。
func normalizeAgentID(agentID string) string {
	return strings.TrimSpace(agentID)
}

// truncateToApproxTokens 按轻量 token 估算裁剪内容，保留 skill 文件开头的使用说明。
func truncateToApproxTokens(content string, maxTokens int) string {
	if maxTokens <= 0 || EstimateTokens(content) <= maxTokens {
		return content
	}
	maxRunes := maxTokens * 3
	if maxRunes <= 0 || utf8.RuneCountInString(content) <= maxRunes {
		return content
	}
	runes := []rune(content)
	return string(runes[:maxRunes])
}
