package claudecompaction

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Role 表示一条消息在对话里的发送方角色。
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleSystem     Role = "system"
	RoleToolResult Role = "tool_result"
)

// MessageSubtype 标记消息在压缩链路里的特殊语义。
type MessageSubtype string

const (
	SubtypeText                MessageSubtype = "text"
	SubtypeToolUse             MessageSubtype = "tool_use"
	SubtypeToolResult          MessageSubtype = "tool_result"
	SubtypeAttachment          MessageSubtype = "attachment"
	SubtypeCompactBoundary     MessageSubtype = "compact_boundary"
	SubtypeHistorySnipBoundary MessageSubtype = "history_snip_boundary"
	SubtypeMicrocompactNote    MessageSubtype = "microcompact_boundary"
)

// BlockType 表示用户消息里不同模态内容的类型。
type BlockType string

const (
	BlockText     BlockType = "text"
	BlockImage    BlockType = "image"
	BlockDocument BlockType = "document"
)

// Block 是用户消息中的最小内容块，用于模拟 Claude Code 的多模态过滤。
type Block struct {
	Type BlockType
	Text string
}

// AttachmentType 表示压缩后由代码补回的上下文引用类型。
type AttachmentType string

const (
	AttachmentSkillListing         AttachmentType = "skill_listing"
	AttachmentSkillDiscovery       AttachmentType = "skill_discovery"
	AttachmentCompactFileReference AttachmentType = "compact_file_reference"
	AttachmentInvokedSkill         AttachmentType = "invoked_skill"
	AttachmentPlanReference        AttachmentType = "plan_reference"
	AttachmentPlanMode             AttachmentType = "plan_mode"
	AttachmentDeferredToolDelta    AttachmentType = "deferred_tool_delta"
	AttachmentMCPInstructionDelta  AttachmentType = "mcp_instruction_delta"
)

// Attachment 表示不需要交给总结模型、可由代码重新注入的上下文。
type Attachment struct {
	Type    AttachmentType
	Name    string
	Path    string
	Reason  string
	Content string
}

// Trigger 标记压缩是手动触发还是自动触发。
type Trigger string

const (
	TriggerManual Trigger = "manual"
	TriggerAuto   Trigger = "auto"
)

// CompactMetadata 记录 compact boundary 携带的边界信息。
type CompactMetadata struct {
	Trigger            Trigger
	PreTokens          int
	MessagesSummarized int
	UserContext        string
}

// Message 是本包用于还原 Claude Code 压缩链路的轻量消息模型。
type Message struct {
	ID               string
	Role             Role
	Subtype          MessageSubtype
	Content          string
	Blocks           []Block
	ToolName         string
	ToolUseID        string
	Tokens           int
	Attachment       *Attachment
	CompactMetadata  *CompactMetadata
	IsCompactSummary bool
	IsMeta           bool
	Timestamp        time.Time
}

// MessageOption 调整 Message 的测试或运行时字段。
type MessageOption func(*Message)

// TextBlock 构造文本内容块。
func TextBlock(text string) Block {
	return Block{Type: BlockText, Text: text}
}

// ImageBlock 构造图片内容块，压缩前会被替换成占位文本。
func ImageBlock(payload string) Block {
	return Block{Type: BlockImage, Text: payload}
}

// DocumentBlock 构造文档内容块，压缩前会被替换成占位文本。
func DocumentBlock(payload string) Block {
	return Block{Type: BlockDocument, Text: payload}
}

// TextMessage 构造普通文本消息。
func TextMessage(id string, role Role, content string, opts ...MessageOption) Message {
	msg := Message{
		ID:        id,
		Role:      role,
		Subtype:   SubtypeText,
		Content:   content,
		Tokens:    EstimateTokens(content),
		Timestamp: time.Now().UTC(),
	}
	return applyMessageOptions(msg, opts...)
}

// UserMessage 构造包含多个内容块的用户消息。
func UserMessage(id string, blocks []Block, opts ...MessageOption) Message {
	msg := Message{
		ID:        id,
		Role:      RoleUser,
		Subtype:   SubtypeText,
		Blocks:    cloneBlocks(blocks),
		Tokens:    EstimateTokens(blocksToText(blocks)),
		Timestamp: time.Now().UTC(),
	}
	return applyMessageOptions(msg, opts...)
}

// ToolUseMessage 构造一次工具调用消息。
func ToolUseMessage(id string, toolName string, toolUseID string, opts ...MessageOption) Message {
	msg := Message{
		ID:        id,
		Role:      RoleAssistant,
		Subtype:   SubtypeToolUse,
		Content:   toolName + ":" + toolUseID,
		ToolName:  toolName,
		ToolUseID: toolUseID,
		Tokens:    EstimateTokens(toolName + " " + toolUseID),
		Timestamp: time.Now().UTC(),
	}
	return applyMessageOptions(msg, opts...)
}

// ToolResultMessage 构造一次工具结果消息。
func ToolResultMessage(id string, toolUseID string, content string, opts ...MessageOption) Message {
	msg := Message{
		ID:        id,
		Role:      RoleToolResult,
		Subtype:   SubtypeToolResult,
		Content:   content,
		ToolUseID: toolUseID,
		Tokens:    EstimateTokens(content),
		Timestamp: time.Now().UTC(),
	}
	return applyMessageOptions(msg, opts...)
}

// AttachmentMessage 构造一条可由代码补回的附件消息。
func AttachmentMessage(id string, attachment Attachment, opts ...MessageOption) Message {
	msg := Message{
		ID:         id,
		Role:       RoleSystem,
		Subtype:    SubtypeAttachment,
		Content:    attachment.Content,
		Attachment: cloneAttachmentPtr(&attachment),
		Tokens:     EstimateTokens(attachment.Content),
		Timestamp:  time.Now().UTC(),
	}
	return applyMessageOptions(msg, opts...)
}

// CompactBoundary 构造压缩边界消息，用于后续只读取边界后的上下文。
func CompactBoundary(id string, metadata CompactMetadata) Message {
	return Message{
		ID:              id,
		Role:            RoleSystem,
		Subtype:         SubtypeCompactBoundary,
		Content:         "Conversation compacted",
		CompactMetadata: cloneCompactMetadataPtr(&metadata),
		Timestamp:       time.Now().UTC(),
	}
}

// WithTokens 覆盖轻量 token 估算，便于测试精确触发阈值。
func WithTokens(tokens int) MessageOption {
	return func(m *Message) {
		m.Tokens = tokens
	}
}

// WithTimestamp 固定消息时间，便于可重复测试和回放。
func WithTimestamp(ts time.Time) MessageOption {
	return func(m *Message) {
		m.Timestamp = ts.UTC()
	}
}

// FlattenContent 将文本消息和多模态 block 合并成模型可读文本。
func (m Message) FlattenContent() string {
	if len(m.Blocks) == 0 {
		return m.Content
	}
	return blocksToText(m.Blocks)
}

// TokenCount 返回消息 token 数；缺失时退化到轻量估算。
func (m Message) TokenCount() int {
	if m.Tokens > 0 {
		return m.Tokens
	}
	return EstimateTokens(m.FlattenContent())
}

// Clone 复制消息，避免压缩过程修改调用方持有的历史。
func (m Message) Clone() Message {
	m.Blocks = cloneBlocks(m.Blocks)
	m.Attachment = cloneAttachmentPtr(m.Attachment)
	m.CompactMetadata = cloneCompactMetadataPtr(m.CompactMetadata)
	return m
}

// EstimateTokens 使用轻量启发式估算 token，避免示例绑定真实 tokenizer。
func EstimateTokens(content string) int {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0
	}
	runes := utf8.RuneCountInString(trimmed)
	words := len(strings.Fields(trimmed))
	estimate := runes / 3
	if words > estimate {
		estimate = words
	}
	if estimate < 1 {
		return 1
	}
	return estimate
}

// applyMessageOptions 应用消息选项并补齐 token 估算。
func applyMessageOptions(msg Message, opts ...MessageOption) Message {
	for _, opt := range opts {
		opt(&msg)
	}
	if msg.Tokens == 0 && strings.TrimSpace(msg.FlattenContent()) != "" {
		msg.Tokens = EstimateTokens(msg.FlattenContent())
	}
	return msg
}

// blocksToText 将内容块转成摘要模型可见的文本。
func blocksToText(blocks []Block) string {
	var parts []string
	for _, block := range blocks {
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

// cloneMessages 复制消息列表，隔离压缩过程的内部改写。
func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, len(messages))
	for i, msg := range messages {
		out[i] = msg.Clone()
	}
	return out
}

// cloneBlocks 复制内容块列表。
func cloneBlocks(blocks []Block) []Block {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]Block, len(blocks))
	copy(out, blocks)
	return out
}

// cloneAttachmentPtr 复制附件指针，避免外部修改压缩结果。
func cloneAttachmentPtr(in *Attachment) *Attachment {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// cloneCompactMetadataPtr 复制压缩元信息指针。
func cloneCompactMetadataPtr(in *CompactMetadata) *CompactMetadata {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// totalTokens 汇总消息 token，用于阈值判断和压缩事件记录。
func totalTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		total += msg.TokenCount()
	}
	return total
}
