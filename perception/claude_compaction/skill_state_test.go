package claudecompaction

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSkillStateAutomaticallyCreatesInvokedSkillAttachmentOnCompact 验证 compact 自动从运行时状态补回已调用 skill。
func TestSkillStateAutomaticallyCreatesInvokedSkillAttachmentOnCompact(t *testing.T) {
	skillState := NewSkillState(SkillStateConfig{
		Now: func() time.Time { return time.Unix(500, 0).UTC() },
	})
	skillState.RecordInvokedSkill(InvokedSkill{
		AgentID:   "agent-main",
		Name:      "verify",
		Path:      "/skills/verify/SKILL.md",
		Content:   "先运行验证命令，再汇报结果。",
		InvokedAt: time.Unix(100, 0).UTC(),
	})
	model := &recordingSummarizer{summary: "<summary>继续处理验证任务。</summary>"}
	pipeline := NewQueryPipeline(QueryPipelineConfig{
		Compactor: NewCompactor(Config{
			Now: func() time.Time { return time.Unix(600, 0).UTC() },
		}),
		SkillState: skillState,
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:                true,
			ContextWindow:          700,
			MaxSummaryOutputTokens: 200,
			BufferTokens:           100,
		},
	})

	result, err := pipeline.Run(context.Background(), QueryPipelineRequest{
		Messages: []Message{
			TextMessage("old-user", RoleUser, strings.Repeat("旧问题 ", 40), WithTokens(260)),
			TextMessage("old-assistant", RoleAssistant, strings.Repeat("旧处理 ", 40), WithTokens(260)),
		},
		SummaryModel: model,
		Source:       QuerySourceMain,
		AgentID:      "agent-main",
		Trigger:      TriggerAuto,
	})
	if err != nil {
		t.Fatalf("query pipeline failed: %v", err)
	}
	attachment := firstAttachment(result.MessagesForQuery, AttachmentInvokedSkill)
	if attachment == nil {
		t.Fatalf("expected invoked skill attachment from SkillState: %#v", result.MessagesForQuery)
	}
	if attachment.Name != "verify" || attachment.Path != "/skills/verify/SKILL.md" {
		t.Fatalf("unexpected invoked skill attachment metadata: %#v", attachment)
	}
	if !strings.Contains(attachment.Content, "先运行验证命令") {
		t.Fatalf("unexpected invoked skill content: %s", attachment.Content)
	}
}

// TestSkillStateRestoresFromInvokedSkillAttachmentAcrossResume 验证 resume 后仍能从附件恢复 skill 状态。
func TestSkillStateRestoresFromInvokedSkillAttachmentAcrossResume(t *testing.T) {
	firstState := NewSkillState(SkillStateConfig{
		Now: func() time.Time { return time.Unix(700, 0).UTC() },
	})
	firstState.RecordInvokedSkill(InvokedSkill{
		Name:      "verify",
		Path:      "/skills/verify/SKILL.md",
		Content:   "恢复后也必须继续遵守验证 skill。",
		InvokedAt: time.Unix(100, 0).UTC(),
	})
	session := NewQuerySession([]Message{
		TextMessage("m1", RoleUser, strings.Repeat("旧上下文 ", 80), WithTokens(500)),
	})
	firstPipeline := NewQueryPipeline(QueryPipelineConfig{
		Compactor: NewCompactor(Config{
			Now: func() time.Time { return time.Unix(710, 0).UTC() },
		}),
		SkillState: firstState,
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:                true,
			ContextWindow:          700,
			MaxSummaryOutputTokens: 200,
			BufferTokens:           100,
		},
	})

	first, err := session.PrepareNextQuery(context.Background(), firstPipeline, QueryPipelineRequest{
		SummaryModel: &recordingSummarizer{summary: "<summary>第一次压缩。</summary>"},
		Source:       QuerySourceMain,
		Trigger:      TriggerAuto,
	})
	if err != nil {
		t.Fatalf("first compaction failed: %v", err)
	}
	if firstAttachment(first.WritebackMessages, AttachmentInvokedSkill) == nil {
		t.Fatalf("first compaction should write invoked skill attachment: %#v", first.WritebackMessages)
	}

	resumedState := NewSkillState(SkillStateConfig{
		Now: func() time.Time { return time.Unix(800, 0).UTC() },
	})
	resumedSession := NewQuerySessionWithSkillState(session.Messages(), resumedState)
	if got := resumedState.InvokedSkillsForAgent(""); len(got) != 1 || got[0].Name != "verify" {
		t.Fatalf("resume should restore invoked skill state, got %#v", got)
	}
	resumedSession.Append(TextMessage("new-heavy", RoleUser, strings.Repeat("新上下文 ", 80), WithTokens(500)))

	secondPipeline := NewQueryPipeline(QueryPipelineConfig{
		Compactor: NewCompactor(Config{
			Now: func() time.Time { return time.Unix(810, 0).UTC() },
		}),
		SkillState: resumedState,
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:                true,
			ContextWindow:          700,
			MaxSummaryOutputTokens: 200,
			BufferTokens:           100,
		},
	})
	second, err := resumedSession.PrepareNextQuery(context.Background(), secondPipeline, QueryPipelineRequest{
		SummaryModel: &recordingSummarizer{summary: "<summary>第二次压缩。</summary>"},
		Source:       QuerySourceMain,
		Trigger:      TriggerAuto,
	})
	if err != nil {
		t.Fatalf("second compaction failed: %v", err)
	}
	if firstAttachment(second.WritebackMessages, AttachmentInvokedSkill) == nil {
		t.Fatalf("restored skill state should be preserved across the next compaction: %#v", second.WritebackMessages)
	}
}

// firstAttachment 返回指定类型的第一条附件，方便测试 post-compact 恢复内容。
func firstAttachment(messages []Message, typ AttachmentType) *Attachment {
	for _, msg := range messages {
		if msg.Attachment != nil && msg.Attachment.Type == typ {
			attachment := *msg.Attachment
			return &attachment
		}
	}
	return nil
}
