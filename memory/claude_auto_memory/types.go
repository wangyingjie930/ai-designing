package claudeautomemory

import "context"

// MemoryType 表示 Claude 风格自动记忆的封闭语义分类。
type MemoryType string

const (
	MemoryTypeUser      MemoryType = "user"
	MemoryTypeFeedback  MemoryType = "feedback"
	MemoryTypeProject   MemoryType = "project"
	MemoryTypeReference MemoryType = "reference"
)

// Valid 判断记忆类型是否属于模型允许选择的封闭集合。
func (t MemoryType) Valid() bool {
	switch t {
	case MemoryTypeUser, MemoryTypeFeedback, MemoryTypeProject, MemoryTypeReference:
		return true
	default:
		return false
	}
}

// Scope 表示记忆只属于当前用户还是可以供团队共享。
type Scope string

const (
	ScopePrivate Scope = "private"
	ScopeTeam    Scope = "team"
)

// Valid 判断作用域是否为 private 或 team。
func (s Scope) Valid() bool {
	return s == ScopePrivate || s == ScopeTeam
}

// MemoryCandidate 是提取模型提交给确定性存储边界的候选记忆。
type MemoryCandidate struct {
	Type        MemoryType `json:"type"`
	Scope       Scope      `json:"scope"`
	Topic       string     `json:"topic"`
	Description string     `json:"description"`
	Content     string     `json:"content"`
}

// MemoryRef 是模型可选择的最小安全引用，不允许携带任意文件路径。
type MemoryRef struct {
	Scope Scope  `json:"scope"`
	Topic string `json:"topic"`
}

// MemoryRecord 表示已经落盘并通过存储层校验的完整记忆。
type MemoryRecord struct {
	Ref         MemoryRef  `json:"ref"`
	Type        MemoryType `json:"type"`
	Description string     `json:"description"`
	Content     string     `json:"content"`
	Path        string     `json:"path"`
}

// IndexEntry 是 MEMORY.md 暴露给召回模型的低成本候选摘要。
type IndexEntry struct {
	Ref         MemoryRef  `json:"ref"`
	Type        MemoryType `json:"type"`
	Description string     `json:"description"`
	Path        string     `json:"path"`
}

// MemoryManifest 同时承载 private 和 team 两份索引。
type MemoryManifest struct {
	Private []IndexEntry `json:"private"`
	Team    []IndexEntry `json:"team"`
}

// Role 表示自动记忆链路关心的对话角色。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ConversationMessage 是主会话与提取器之间共享的最小消息契约。
type ConversationMessage struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// MemoryExtractor 让语义提取模型与确定性的游标、存储逻辑解耦。
type MemoryExtractor interface {
	Extract(ctx context.Context, messages []ConversationMessage) ([]MemoryCandidate, error)
}

// ExtractionResult 汇总一次回答后提取的写入结果和非致命警告。
type ExtractionResult struct {
	Written           []MemoryRecord
	ProcessedMessages int
	Warnings          []error
}

// MemorySelector 根据当前问题和双索引选择少量相关主题引用。
type MemorySelector interface {
	Select(ctx context.Context, query string, manifest MemoryManifest) ([]MemoryRef, error)
}

// RecallResult 汇总安全读取到的记忆正文、注入文本和降级警告。
type RecallResult struct {
	Records  []MemoryRecord
	Context  string
	Warnings []error
}

// ChatAgent 是只负责业务回答的主 Agent 边界，不暴露记忆维护 prompt。
type ChatAgent interface {
	Generate(ctx context.Context, messages []ConversationMessage, memoryContext string) (string, error)
}

// TurnResult 汇总一轮回答、当轮召回、回答后写入和降级警告。
type TurnResult struct {
	Answer   string
	Recalled []MemoryRecord
	Written  []MemoryRecord
	Warnings []error
}
