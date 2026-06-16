package mem0

// Message 表示 mem0 add 接口接收的一条对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// AddRequest 承载一次写入长期记忆所需的对话、作用域和元数据。
type AddRequest struct {
	Messages []Message      `json:"messages"`
	UserID   string         `json:"user_id,omitempty"`
	AgentID  string         `json:"agent_id,omitempty"`
	RunID    string         `json:"run_id,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Infer    *bool          `json:"infer,omitempty"`
	Prompt   string         `json:"prompt,omitempty"`
}

// AddResponse 对齐 mem0 add 返回形态，集中返回本轮新增的 memory 事件。
type AddResponse struct {
	Results []AddResult `json:"results"`
}

// AddResult 描述一次 add 流程实际写入的单条记忆。
type AddResult struct {
	ID       string `json:"id"`
	Memory   string `json:"memory"`
	Event    string `json:"event"`
	ActorID  string `json:"actor_id,omitempty"`
	Role     string `json:"role,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
	SkipNote string `json:"skip_note,omitempty"`
}

// SearchRequest 承载一次自然语言记忆召回请求。
type SearchRequest struct {
	Query     string         `json:"query"`
	TopK      int            `json:"top_k,omitempty"`
	Filters   map[string]any `json:"filters,omitempty"`
	Threshold *float64       `json:"threshold,omitempty"`
	Explain   bool           `json:"explain,omitempty"`
}

// SearchResponse 对齐 mem0 search 返回形态，集中返回召回列表。
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchResult 表示一条可回灌给 Agent prompt 的长期记忆。
type SearchResult struct {
	ID           string         `json:"id"`
	Memory       string         `json:"memory"`
	Hash         string         `json:"hash,omitempty"`
	UserID       string         `json:"user_id,omitempty"`
	AgentID      string         `json:"agent_id,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
	ActorID      string         `json:"actor_id,omitempty"`
	Role         string         `json:"role,omitempty"`
	CreatedAt    string         `json:"created_at,omitempty"`
	UpdatedAt    string         `json:"updated_at,omitempty"`
	Score        float64        `json:"score"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	ScoreDetails *ScoreDetails  `json:"score_details,omitempty"`
}

// ScoreDetails 暴露假 embedding 与关键词混排后的调试分数。
type ScoreDetails struct {
	SemanticScore    float64 `json:"semantic_score"`
	KeywordScore     float64 `json:"keyword_score"`
	RawScore         float64 `json:"raw_score"`
	MaxPossibleScore float64 `json:"max_possible_score"`
	FinalScore       float64 `json:"final_score"`
	Threshold        float64 `json:"threshold"`
}
