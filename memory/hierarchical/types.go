package hierarchical

import "time"

const (
	defaultScope                   = "default"
	defaultWorkingBudget           = 150_000
	defaultRetrieveTopK            = 5
	persistImportanceThreshold     = 0.6
	defaultMemoryImportance        = 0.5
	longTermRetrievalSource        = "longterm_retrieval"
	defaultEmbeddingRequestTimeout = 30 * time.Second
)

// MemoryTier 是 Python MemoryTier IntEnum 的 Go 版本，数值保持与原示例一致。
type MemoryTier int

const (
	MemoryTierWorking  MemoryTier = 1
	MemoryTierSession  MemoryTier = 2
	MemoryTierLongTerm MemoryTier = 3
)

// String 返回 tier 的稳定可读名称，方便 CLI 和工具结果检查。
func (t MemoryTier) String() string {
	switch t {
	case MemoryTierWorking:
		return "working"
	case MemoryTierSession:
		return "session"
	case MemoryTierLongTerm:
		return "longterm"
	default:
		return "unknown"
	}
}

// Valid 判断 tier 是否属于当前三层记忆支持的闭集。
func (t MemoryTier) Valid() bool {
	switch t {
	case MemoryTierWorking, MemoryTierSession, MemoryTierLongTerm:
		return true
	default:
		return false
	}
}

// MemoryEntry 是 Python MemoryEntry dataclass 的 Go 翻译，额外带上 SQLite 持久化所需 ID。
type MemoryEntry struct {
	ID           string         `json:"id,omitempty"`
	Content      string         `json:"content"`
	Tier         MemoryTier     `json:"tier"`
	TierName     string         `json:"tier_name,omitempty"`
	Source       string         `json:"source"`
	CreatedAt    time.Time      `json:"created_at"`
	LastAccessed time.Time      `json:"last_accessed"`
	AccessCount  int            `json:"access_count"`
	Importance   float64        `json:"importance"`
	TokenCount   int            `json:"token_count"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Config 汇总三层记忆运行时依赖，SQLite 路径、scope、预算和 embedding 都由外部注入。
type Config struct {
	DBPath        string
	Scope         string
	WorkingBudget int
	Embedder      Embedder
	Now           func() time.Time
}

// SearchResult 表示一次长期记忆召回结果，返回分数和原始长期记忆快照。
type SearchResult struct {
	Entry MemoryEntry `json:"entry"`
	Text  string      `json:"text"`
	Score float64     `json:"score"`
}

// AddRequest 是 hierarchical_add_memory 工具和本地 Add 方法共享的输入。
type AddRequest struct {
	Content    string         `json:"content"`
	Source     string         `json:"source"`
	Importance *float64       `json:"importance,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// AddResponse 返回新增 working memory 以及当前 working token 消耗。
type AddResponse struct {
	Entry             MemoryEntry `json:"entry"`
	WorkingTokenCount int         `json:"working_token_count"`
}

// RetrieveRequest 是 hierarchical_retrieve_memory 工具的输入，k 不传时走可配置默认值。
type RetrieveRequest struct {
	Query string `json:"query"`
	K     int    `json:"k,omitempty"`
}

// RetrieveResponse 返回长期记忆召回结果，并说明召回后 working tier 的预算占用。
type RetrieveResponse struct {
	Results           []SearchResult `json:"results"`
	WorkingTokenCount int            `json:"working_token_count"`
}

// ContextRequest 预留给 ADK tool 的空输入，避免无 schema 工具。
type ContextRequest struct{}

// ContextResponse 是内部/CLI 调试快照，包含 working 和 session，方便人观察淘汰过程。
type ContextResponse struct {
	Scope             string        `json:"scope"`
	Working           []MemoryEntry `json:"working"`
	Session           []MemoryEntry `json:"session"`
	WorkingTokenCount int           `json:"working_token_count"`
	WorkingBudget     int           `json:"working_budget"`
}

// ToolContextResponse 是暴露给 LLM 的工具响应，只包含可见 working memory。
type ToolContextResponse struct {
	Scope             string        `json:"scope"`
	Working           []MemoryEntry `json:"working"`
	WorkingTokenCount int           `json:"working_token_count"`
	WorkingBudget     int           `json:"working_budget"`
}

// ConsolidateRequest 预留给会话收束工具，当前不需要额外参数。
type ConsolidateRequest struct{}

// ConsolidateResponse 返回本次真正写入或更新到 SQLite long-term tier 的记忆。
type ConsolidateResponse struct {
	Persisted int           `json:"persisted"`
	Entries   []MemoryEntry `json:"entries"`
}
