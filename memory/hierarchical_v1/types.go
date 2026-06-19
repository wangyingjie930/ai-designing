package hierarchicalv1

import (
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
)

// MemoryLayer 表示附件代码里的五层记忆策略，越靠前越像稳定治理规则。
type MemoryLayer string

const (
	MemoryLayerPolicy     MemoryLayer = "policy"
	MemoryLayerProject    MemoryLayer = "project"
	MemoryLayerUser       MemoryLayer = "user"
	MemoryLayerTask       MemoryLayer = "task"
	MemoryLayerScratchpad MemoryLayer = "scratchpad"
)

var assembleOrder = []MemoryLayer{
	MemoryLayerPolicy,
	MemoryLayerProject,
	MemoryLayerUser,
	MemoryLayerTask,
	MemoryLayerScratchpad,
}

// String 返回 layer 的稳定字符串值，方便命令行和 JSON 输出对齐。
func (l MemoryLayer) String() string {
	return string(l)
}

// Valid 判断 layer 是否属于五层 hierarchical memory 的闭集。
func (l MemoryLayer) Valid() bool {
	switch l {
	case MemoryLayerPolicy, MemoryLayerProject, MemoryLayerUser, MemoryLayerTask, MemoryLayerScratchpad:
		return true
	default:
		return false
	}
}

// MemorySource 表示一条记忆的来源，写入权限和证据校验会依赖它。
type MemorySource string

const (
	MemorySourceHuman          MemorySource = "human"
	MemorySourceTool           MemorySource = "tool"
	MemorySourceAgentInference MemorySource = "agent_inference"
	MemorySourceVerifiedTrace  MemorySource = "verified_trace"
	MemorySourceFailureReview  MemorySource = "failure_review"
)

// String 返回 source 的稳定字符串值，方便日志和 JSON 输出。
func (s MemorySource) String() string {
	return string(s)
}

// Valid 判断 source 是否属于当前记忆系统认可的来源。
func (s MemorySource) Valid() bool {
	switch s {
	case MemorySourceHuman, MemorySourceTool, MemorySourceAgentInference, MemorySourceVerifiedTrace, MemorySourceFailureReview:
		return true
	default:
		return false
	}
}

// MemoryEntry 是附件 MemoryEntry dataclass 的 Go 翻译，记录事实、证据、置信度和有效期。
type MemoryEntry struct {
	Key            string       `json:"key"`
	Value          any          `json:"value"`
	Layer          MemoryLayer  `json:"layer"`
	Source         MemorySource `json:"source"`
	EvidenceRefs   []string     `json:"evidence_refs,omitempty"`
	Confidence     float64      `json:"confidence"`
	TokenEstimate  int          `json:"token_estimate"`
	ValidFrom      string       `json:"valid_from"`
	ValidUntil     *string      `json:"valid_until,omitempty"`
	CreatedAt      string       `json:"created_at"`
	LastAccessedAt *string      `json:"last_accessed_at,omitempty"`
}

// IsExpired 按 valid_until 判断记忆是否过期，和附件里的 is_expired 保持一致。
func (e MemoryEntry) IsExpired() bool {
	return e.IsExpiredAt(time.Now().UTC())
}

// IsExpiredAt 使用注入时间判断过期状态，便于单测固定时间。
func (e MemoryEntry) IsExpiredAt(now time.Time) bool {
	if e.ValidUntil == nil {
		return false
	}
	deadline, err := parseMemoryTime(*e.ValidUntil)
	if err != nil {
		return false
	}
	return now.UTC().After(deadline)
}

// IsVerified 判断记忆是否有证据，或来源本身就是可信来源。
func (e MemoryEntry) IsVerified() bool {
	if len(e.EvidenceRefs) > 0 {
		return true
	}
	switch e.Source {
	case MemorySourceHuman, MemorySourceTool, MemorySourceVerifiedTrace, MemorySourceFailureReview:
		return true
	default:
		return false
	}
}

// LayerPolicy 是附件 LayerPolicy dataclass 的 Go 翻译，描述每层预算、TTL、写权限和后端标识。
type LayerPolicy struct {
	Layer           MemoryLayer
	TokenBudget     int
	TTL             *time.Duration
	AllowAgentWrite bool
	RequireEvidence bool
	Backend         string
}

// Config 汇总 hierarchical memory 的可覆盖策略和时间依赖。
type Config struct {
	Policies map[MemoryLayer]LayerPolicy
	DBPath   string
	Now      func() time.Time
}

// WriteRequest 是写入记忆的 ADK tool 输入，value 用对象承载，避免模型传入不稳定标量 schema。
type WriteRequest struct {
	Key           string         `json:"key"`
	Value         map[string]any `json:"value"`
	Layer         MemoryLayer    `json:"layer"`
	Source        MemorySource   `json:"source"`
	EvidenceRefs  []string       `json:"evidence_refs,omitempty"`
	Confidence    *float64       `json:"confidence,omitempty"`
	TokenEstimate int            `json:"token_estimate,omitempty"`
	ValidUntil    string         `json:"valid_until,omitempty"`
}

// WriteResponse 返回实际写入后的完整 MemoryEntry。
type WriteResponse struct {
	Entry MemoryEntry `json:"entry"`
}

// ProposeScratchpadRequest 是 scratchpad 提升候选生成工具的输入。
type ProposeScratchpadRequest struct {
	Key         string      `json:"key"`
	TargetLayer MemoryLayer `json:"target_layer"`
}

// ProposeScratchpadResponse 返回从 scratchpad 生成的 verified_trace 候选记忆。
type ProposeScratchpadResponse struct {
	Entry MemoryEntry `json:"entry"`
}

// AssembleContextRequest 是组装上下文的空输入。
type AssembleContextRequest struct{}

// AssembleContextResponse 返回按层和预算选出的记忆列表，并附带可读上下文文本。
type AssembleContextResponse struct {
	Entries []MemoryEntry `json:"entries"`
	Context string        `json:"context"`
}

// HealthReportRequest 是读取健康状态的空输入。
type HealthReportRequest struct{}

// LayerHealth 表示 health_report 中单层的监控摘要。
type LayerHealth struct {
	Backend         string   `json:"backend"`
	TokenBudget     int      `json:"token_budget"`
	TTLSeconds      *float64 `json:"ttl_seconds,omitempty"`
	AllowAgentWrite bool     `json:"allow_agent_write"`
	RequireEvidence bool     `json:"require_evidence"`
	EntryCount      int      `json:"entry_count"`
}

// HealthReport 对应附件 health_report 的返回结构。
type HealthReport struct {
	Layers map[string]LayerHealth `json:"layers"`
}

// AgentConfig 配置一个会使用五层 hierarchical memory 的非 coding 场景 Agent。
type AgentConfig struct {
	Name          string
	Description   string
	Instruction   string
	Model         model.BaseChatModel
	Memory        *HierarchicalMemory
	ExtraTools    []tool.BaseTool
	MaxIterations int
}

// AgentRequest 是膳食规划 Agent 的自然语言输入。
type AgentRequest struct {
	Message string `json:"message"`
}

// AgentResponse 返回 ADK Agent 的最终中文答复。
type AgentResponse struct {
	Message string `json:"message"`
}
