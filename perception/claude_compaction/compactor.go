package claudecompaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SummaryModel 是压缩摘要模型的最小接口，方便接入真实 LLM 或测试 fake。
type SummaryModel interface {
	Summarize(ctx context.Context, req SummaryRequest) (string, error)
}

// SummaryRequest 是传给摘要模型的输入，已经过边界裁剪、媒体替换和附件过滤。
type SummaryRequest struct {
	Messages           []Message
	Prompt             string
	Trigger            Trigger
	CustomInstructions string
}

// Config 控制 Claude 风格压缩的时间、提示词和默认恢复策略。
type Config struct {
	Now                func() time.Time
	CustomInstructions string
}

// CompactRequest 描述一次压缩请求的完整上下文。
type CompactRequest struct {
	Messages          []Message
	SummaryModel      SummaryModel
	Trigger           Trigger
	SuppressFollowUp  bool
	TranscriptPath    string
	MessagesToKeep    []Message
	RestoreReferences RestoreReferences
	SessionMemory     *SessionMemory
}

// CompactResult 是压缩后的模型上下文视图。
type CompactResult struct {
	Messages        []Message
	Boundary        Message
	Summary         Message
	PreCompactToken int
	ModelCalled     bool
}

// SessionMemory 表示后台增量维护的短期会话摘要文件。
type SessionMemory struct {
	Enabled                 bool
	Content                 string
	LastSummarizedMessageID string
}

// RestoreReferences 汇总压缩后由代码重新注入的上下文。
type RestoreReferences struct {
	Files           []RestoredFile
	InvokedSkills   []RestoredSkill
	Plan            string
	PlanMode        string
	DeferredTools   []string
	MCPInstructions []string
}

// RestoredFile 表示最近读过、压缩后需要恢复的文件片段。
type RestoredFile struct {
	Path    string
	Reason  string
	Content string
}

// RestoredSkill 表示压缩前已经调用过、压缩后需要继续遵守的 skill。
type RestoredSkill struct {
	Name    string
	Content string
}

// Compactor 还原 Claude Code 的上下文压缩编排：模型只总结，边界和恢复由代码管理。
type Compactor struct {
	config Config
}

// NewCompactor 创建 Claude 风格 compactor。
func NewCompactor(config Config) *Compactor {
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Compactor{config: config}
}

// Compact 执行压缩：优先使用 session memory，否则调用摘要模型生成 compact summary。
func (c *Compactor) Compact(ctx context.Context, req CompactRequest) (CompactResult, error) {
	if len(req.Messages) == 0 {
		return CompactResult{}, errors.New("claude compaction requires messages")
	}
	if req.Trigger == "" {
		req.Trigger = TriggerAuto
	}
	if c == nil {
		c = NewCompactor(Config{})
	}
	if req.SessionMemory != nil && req.SessionMemory.Enabled && strings.TrimSpace(req.SessionMemory.Content) != "" {
		return c.compactFromSessionMemory(req), nil
	}
	if req.SummaryModel == nil {
		return CompactResult{}, errors.New("claude compaction requires summary model when session memory is unavailable")
	}

	prepared := PrepareMessagesForSummary(req.Messages)
	summary, err := req.SummaryModel.Summarize(ctx, SummaryRequest{
		Messages:           prepared,
		Prompt:             BuildCompactPrompt(c.config.CustomInstructions),
		Trigger:            req.Trigger,
		CustomInstructions: c.config.CustomInstructions,
	})
	if err != nil {
		return CompactResult{}, fmt.Errorf("summarize compact context: %w", err)
	}
	return c.buildResult(req, summary, nil, true), nil
}

// BuildPostCompactMessages 按 Claude Code 的顺序组装压缩后上下文。
func BuildPostCompactMessages(boundary Message, summary Message, messagesToKeep []Message, attachments []Message) []Message {
	out := []Message{boundary.Clone(), summary.Clone()}
	out = append(out, cloneMessages(messagesToKeep)...)
	out = append(out, cloneMessages(attachments)...)
	return out
}

// compactFromSessionMemory 用后台维护的 session notes 直接生成压缩结果，避免重复总结全部历史。
func (c *Compactor) compactFromSessionMemory(req CompactRequest) CompactResult {
	messagesToKeep := c.messagesAfterID(req.Messages, req.SessionMemory.LastSummarizedMessageID)
	return c.buildResult(req, req.SessionMemory.Content, messagesToKeep, false)
}

// buildResult 统一创建 compact boundary、summary message 和恢复附件。
func (c *Compactor) buildResult(req CompactRequest, summary string, sessionMemoryKeep []Message, modelCalled bool) CompactResult {
	postCompactKeep := cloneMessages(req.MessagesToKeep)
	if sessionMemoryKeep != nil {
		postCompactKeep = append(postCompactKeep, cloneMessages(sessionMemoryKeep)...)
	}
	prepared := PrepareMessagesForSummary(req.Messages)
	boundary := c.compactBoundary(req.Trigger, totalTokens(req.Messages), len(prepared))
	summaryMessage := c.summaryMessage(summary, req.SuppressFollowUp, req.TranscriptPath, len(sessionMemoryKeep) > 0)
	attachments := c.restoreAttachments(req.RestoreReferences)
	messages := BuildPostCompactMessages(boundary, summaryMessage, postCompactKeep, attachments)
	return CompactResult{
		Messages:        messages,
		Boundary:        boundary,
		Summary:         summaryMessage,
		PreCompactToken: totalTokens(req.Messages),
		ModelCalled:     modelCalled,
	}
}

// compactBoundary 创建新的压缩边界；后续模型请求只应从这里之后恢复。
func (c *Compactor) compactBoundary(trigger Trigger, preTokens int, summarized int) Message {
	id := fmt.Sprintf("compact-boundary-%d", c.config.Now().UnixNano())
	msg := CompactBoundary(id, CompactMetadata{
		Trigger:            trigger,
		PreTokens:          preTokens,
		MessagesSummarized: summarized,
	})
	msg.Timestamp = c.config.Now()
	return msg
}

// summaryMessage 创建 synthetic user summary，模拟 Claude Code 压缩后的继续会话消息。
func (c *Compactor) summaryMessage(summary string, suppress bool, transcriptPath string, recentPreserved bool) Message {
	content := CompactSummaryContent(summary, suppress, transcriptPath, recentPreserved)
	msg := TextMessage(
		fmt.Sprintf("compact-summary-%d", c.config.Now().UnixNano()),
		RoleUser,
		content,
		WithTimestamp(c.config.Now()),
	)
	msg.IsCompactSummary = true
	msg.IsMeta = true
	return msg
}

// restoreAttachments 把不适合交给总结模型的运行上下文按代码规则补回。
func (c *Compactor) restoreAttachments(refs RestoreReferences) []Message {
	var out []Message
	for i, file := range refs.Files {
		out = append(out, AttachmentMessage(
			fmt.Sprintf("compact-file-%d-%d", c.config.Now().UnixNano(), i),
			Attachment{
				Type:    AttachmentCompactFileReference,
				Path:    file.Path,
				Reason:  file.Reason,
				Content: file.Content,
			},
			WithTimestamp(c.config.Now()),
		))
	}
	for i, skill := range refs.InvokedSkills {
		out = append(out, AttachmentMessage(
			fmt.Sprintf("invoked-skill-%d-%d", c.config.Now().UnixNano(), i),
			Attachment{
				Type:    AttachmentInvokedSkill,
				Name:    skill.Name,
				Content: skill.Content,
			},
			WithTimestamp(c.config.Now()),
		))
	}
	if strings.TrimSpace(refs.Plan) != "" {
		out = append(out, AttachmentMessage(
			fmt.Sprintf("plan-reference-%d", c.config.Now().UnixNano()),
			Attachment{Type: AttachmentPlanReference, Content: refs.Plan},
			WithTimestamp(c.config.Now()),
		))
	}
	if strings.TrimSpace(refs.PlanMode) != "" {
		out = append(out, AttachmentMessage(
			fmt.Sprintf("plan-mode-%d", c.config.Now().UnixNano()),
			Attachment{Type: AttachmentPlanMode, Content: refs.PlanMode},
			WithTimestamp(c.config.Now()),
		))
	}
	for i, tool := range refs.DeferredTools {
		if strings.TrimSpace(tool) == "" {
			continue
		}
		out = append(out, AttachmentMessage(
			fmt.Sprintf("deferred-tool-%d-%d", c.config.Now().UnixNano(), i),
			Attachment{Type: AttachmentDeferredToolDelta, Name: tool, Content: tool},
			WithTimestamp(c.config.Now()),
		))
	}
	for i, instruction := range refs.MCPInstructions {
		if strings.TrimSpace(instruction) == "" {
			continue
		}
		out = append(out, AttachmentMessage(
			fmt.Sprintf("mcp-instruction-%d-%d", c.config.Now().UnixNano(), i),
			Attachment{Type: AttachmentMCPInstructionDelta, Content: instruction},
			WithTimestamp(c.config.Now()),
		))
	}
	return out
}

// messagesAfterID 返回指定消息之后的尾部消息；找不到时保守返回空，避免重复注入旧历史。
func (c *Compactor) messagesAfterID(messages []Message, id string) []Message {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	for i, msg := range messages {
		if msg.ID == id {
			return cloneMessages(messages[i+1:])
		}
	}
	return nil
}
