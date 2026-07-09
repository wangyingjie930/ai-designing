package claude_native_thinking

import jsonschema "github.com/eino-contrib/jsonschema"

const (
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
)

// ThinkingMode 对齐 Claude Code 的 thinking 开关，但落到 OpenAI 时只控制 reasoning 参数。
type ThinkingMode string

const (
	ThinkingDisabled ThinkingMode = "disabled"
	ThinkingEnabled  ThinkingMode = "enabled"
	ThinkingAdaptive ThinkingMode = "adaptive"
)

// ThinkingConfig 描述 OpenAI 原生 reasoning 能力；summary 是可见摘要，encrypted_content 是不可见续写状态。
type ThinkingConfig struct {
	Type               ThinkingMode
	Effort             string
	Summary            string
	IncludeRedactedCOT bool
}

// ResponsesClientConfig 保存 OpenAI Responses API 的最小连接配置。
type ResponsesClientConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// GenerateRequest 是一次 Claude-Code 风格 native thinking 调用的输入。
type GenerateRequest struct {
	SystemPrompt        string
	UserPrompt          string
	Input               []map[string]any
	PreviousResponseID  string
	PreviousOutputItems []ResponseOutputItem
	Reasoning           ThinkingConfig
	SchemaName          string
	Schema              *jsonschema.Schema
	MaxTokens           int
}

// GenerateResult 保存可见文本、thinking 摘要和 redacted thinking 状态。
type GenerateResult struct {
	ResponseID string
	Text       string
	Blocks     []ContentBlock
	Output     []ResponseOutputItem
	Usage      Usage
}

// Usage 暴露 OpenAI 返回的 token 摘要，reasoning tokens 单独保留便于观察成本。
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
}

// BlockType 模拟 Claude Code 的 content block 分类。
type BlockType string

const (
	BlockThinking         BlockType = "thinking"
	BlockRedactedThinking BlockType = "redacted_thinking"
	BlockText             BlockType = "text"
)

// ContentBlock 是本包对外暴露的 Claude-Code 风格块；不会暴露原始 OpenAI reasoning tokens。
type ContentBlock struct {
	Type             BlockType `json:"type"`
	Thinking         string    `json:"thinking,omitempty"`
	Signature        string    `json:"signature,omitempty"`
	Data             string    `json:"data,omitempty"`
	EncryptedContent string    `json:"-"`
	Text             string    `json:"text,omitempty"`
}

// ResponseOutputItem 保留 Responses API output item，用于下一轮继续模型推理状态。
type ResponseOutputItem struct {
	ID               string             `json:"id,omitempty"`
	Type             string             `json:"type"`
	Role             string             `json:"role,omitempty"`
	Status           string             `json:"status,omitempty"`
	EncryptedContent string             `json:"encrypted_content,omitempty"`
	Summary          []ReasoningSummary `json:"summary,omitempty"`
	Content          []ResponseContent  `json:"content,omitempty"`
}

// ReasoningSummary 是 OpenAI 返回的 reasoning.summary 条目。
type ReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponseContent 是 message output item 里的文本内容。
type ResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
