package failuretracking

import (
	"context"
	"time"
)

const (
	defaultTopK           = 3
	defaultScoreThreshold = 0.7
)

// Boundary 表示文档里的失败边界：先判断这件事是否值得沉淀。
type Boundary string

const (
	BoundaryHard     Boundary = "hard_failure"
	BoundaryGate     Boundary = "gate_failure"
	BoundarySemantic Boundary = "semantic_failure"
	BoundarySafety   Boundary = "safety_failure"
)

// Status 表示失败日记的审查生命周期，只有 approved 可以进入召回。
type Status string

const (
	StatusDraft       Status = "draft"
	StatusNeedsReview Status = "needs_review"
	StatusApproved    Status = "approved"
	StatusArchived    Status = "archived"
)

const (
	CategoryToolFailure             = "tool_failure"
	CategoryRetrievalFailure        = "retrieval_failure"
	CategoryPlanningFailure         = "planning_failure"
	CategoryGoalDrift               = "goal_drift"
	CategoryContextContamination    = "context_contamination"
	CategoryMechanicalStateMismatch = "mechanical_state_mismatch"
	CategoryBoundaryLeak            = "boundary_leak"
	CategoryHITLBypass              = "hitl_bypass"
	CategoryPolicyViolation         = "policy_violation"
	CategoryUnknown                 = "unknown"
)

// EvidenceBundle 保存三平面证据和原始观察，方便以后追回失败现场。
type EvidenceBundle struct {
	WorkspaceRefs   []string `json:"workspace_refs" jsonschema_description:"失败发生的任务节点或工作区引用，例如 workspace:M3/create_payroll_batch"`
	NarrativeRefs   []string `json:"narrative_refs" jsonschema_description:"当时 Agent 目标、工作集或关键判断的叙事引用"`
	StateRefs       []string `json:"state_refs" jsonschema_description:"机械状态来源引用，例如 state:payroll_group_id@tenant-A"`
	ObservationRefs []string `json:"observation_refs" jsonschema_description:"原始观察引用，例如工具输出、验证报告、错误堆栈或人工审查记录"`
}

// RecallTrigger 是结构化召回键；执行型失败优先靠这些键，而不是只靠 embedding。
type RecallTrigger struct {
	TaskFamilies   []string `json:"task_families" jsonschema_description:"这条经验适用的任务族，例如 payroll_run"`
	Tools          []string `json:"tools" jsonschema_description:"应该召回这条经验的工具名，例如 create_payroll_batch"`
	MechanicalKeys []string `json:"mechanical_keys" jsonschema_description:"应该触发召回的机械状态键，例如 payroll_group_id"`
	Categories     []string `json:"categories" jsonschema_description:"应该触发召回的失败类别，例如 boundary_leak"`
}

// FailureEntry 保存一条已经蒸馏过的失败经验：现象、根因、补救、教训、证据和召回条件分开存。
type FailureEntry struct {
	FailureID     string         `json:"failure_id,omitempty" jsonschema_description:"失败日记唯一 ID，例如 fj-payroll-20260612-001；不确定时留空让系统生成"`
	TaskFamily    string         `json:"task_family" jsonschema:"required" jsonschema_description:"任务族，例如 payroll_run、hotel_frontdesk_recovery"`
	Boundary      Boundary       `json:"boundary" jsonschema:"required,enum=hard_failure,enum=gate_failure,enum=semantic_failure,enum=safety_failure" jsonschema_description:"失败边界，决定这件事是否值得沉淀"`
	Category      string         `json:"category" jsonschema:"required,enum=tool_failure,enum=retrieval_failure,enum=planning_failure,enum=goal_drift,enum=context_contamination,enum=mechanical_state_mismatch,enum=boundary_leak,enum=hitl_bypass,enum=policy_violation,enum=unknown" jsonschema_description:"失败分类，用于后续结构化召回"`
	Severity      string         `json:"severity" jsonschema:"required,enum=low,enum=medium,enum=high,enum=critical" jsonschema_description:"严重程度；高风险经验更应该进入审查和召回"`
	Status        Status         `json:"status" jsonschema:"required,enum=draft,enum=needs_review,enum=approved,enum=archived" jsonschema_description:"审查状态，只有 approved 能进入召回"`
	TenantScope   string         `json:"tenant_scope,omitempty" jsonschema_description:"租户、组织或环境隔离范围；跨租户或权限类失败必须填写"`
	Source        string         `json:"source,omitempty" jsonschema_description:"失败日记来源，例如 verification_gate、human_review、agent_generated_draft"`
	Symptom       string         `json:"symptom" jsonschema:"required" jsonschema_description:"表面发生了什么，不要写成泛泛而谈的故事"`
	RootCause     string         `json:"root_cause" jsonschema:"required" jsonschema_description:"可行动根因，说明具体状态、工具、权限或上下文哪里出了问题"`
	Repair        []string       `json:"repair" jsonschema:"required" jsonschema_description:"这次如何修复；每一项应是可执行动作"`
	Lesson        []string       `json:"lesson" jsonschema:"required" jsonschema_description:"下次应该改变什么行为；会作为召回危险卡的一部分"`
	DoNot         []string       `json:"do_not" jsonschema:"required" jsonschema_description:"下次明确禁止的动作，例如不要从自然语言摘要复制机械 ID"`
	Evidence      EvidenceBundle `json:"evidence" jsonschema:"required" jsonschema_description:"证据包：三平面失败快照和原始观察"`
	Recall        RecallTrigger  `json:"recall" jsonschema:"required" jsonschema_description:"结构化召回条件，执行型失败优先依赖这些键"`
	RetentionTier string         `json:"retention_tier,omitempty" jsonschema:"enum=hot,enum=warm,enum=cold" jsonschema_description:"留存档位：hot 保留完整 trace，warm 保留压缩条目和证据引用，cold 长期保留高价值经验"`
	ReviewedBy    string         `json:"reviewed_by,omitempty" jsonschema_description:"审查者或独立审查链路；status=approved 时必须有可信来源"`
	ReviewNote    string         `json:"review_note,omitempty" jsonschema_description:"审查说明，解释为什么可以或不能进入召回库"`
	CreatedAt     float64        `json:"created_at,omitempty" jsonschema:"-"`
	UpdatedAt     float64        `json:"updated_at,omitempty" jsonschema:"-"`
	RecalledCount int            `json:"recalled_count" jsonschema:"-"`
}

// IsRecallable 体现文档里的审查规则：只有 approved 的失败经验才能进入下一次任务。
func (e FailureEntry) IsRecallable() bool {
	return e.Status == StatusApproved
}

// RecordRequest 写入一条失败日记草稿或已审查条目。
type RecordRequest struct {
	Entry FailureEntry `json:"entry" jsonschema:"required" jsonschema_description:"完整六层失败日记条目"`
}

// RecordResponse 返回本轮写入的失败经验。
type RecordResponse struct {
	Entry FailureEntry `json:"entry"`
}

// RecallRequest 表示任务启动、规划或高风险工具调用前的结构化召回请求。
type RecallRequest struct {
	TaskFamily     string   `json:"task_family" jsonschema:"required" jsonschema_description:"任务族，例如 payroll_run、hotel_frontdesk_recovery"`
	Tool           string   `json:"tool,omitempty" jsonschema_description:"即将调用的高风险工具名，例如 create_payroll_batch"`
	MechanicalKeys []string `json:"mechanical_keys,omitempty" jsonschema_description:"本次工具调用涉及的机械状态键，例如 payroll_group_id"`
	Categories     []string `json:"categories,omitempty" jsonschema_description:"失败类别，例如 boundary_leak、mechanical_state_mismatch"`
	Query          string   `json:"query,omitempty" jsonschema_description:"语义召回辅助文本；不能替代 task_family、tool、mechanical_keys 等结构化键"`
	TopK           int      `json:"top_k,omitempty" jsonschema:"minimum=1,maximum=10,default=3" jsonschema_description:"最多返回多少条经验"`
}

// RecallResponse 返回经过 approved 状态过滤和结构化排序后的失败经验。
type RecallResponse struct {
	Matches []FailureMatch `json:"matches"`
}

// ReviewRequest 更新失败日记的审查状态。
type ReviewRequest struct {
	FailureID     string `json:"failure_id" jsonschema:"required" jsonschema_description:"要审查的失败日记 ID"`
	Status        Status `json:"status" jsonschema:"required,enum=needs_review,enum=approved,enum=archived" jsonschema_description:"审查后的状态；Review 不允许重新写回 draft"`
	ReviewedBy    string `json:"reviewed_by,omitempty" jsonschema_description:"审查者或独立审查链路；status=approved 时必填"`
	ReviewNote    string `json:"review_note,omitempty" jsonschema_description:"审查说明，说明通过、退回或归档原因"`
	RetentionTier string `json:"retention_tier,omitempty" jsonschema:"enum=hot,enum=warm,enum=cold" jsonschema_description:"更新后的留存档位"`
}

// ReviewResponse 返回审查后的失败日记。
type ReviewResponse struct {
	Entry FailureEntry `json:"entry"`
}

// FailureMatch 在 FailureEntry 外补充分数，便于 Agent 判断是否应该复用经验。
type FailureMatch struct {
	Entry           FailureEntry `json:"entry"`
	Score           float64      `json:"score"`
	StructuredScore float64      `json:"structured_score"`
	SemanticScore   float64      `json:"semantic_score,omitempty"`
	Reason          string       `json:"reason,omitempty"`
}

// Config 汇总 failure journal 的持久化、本地向量召回和时间来源配置。
type Config struct {
	DBPath         string
	Embedder       Embedder
	ScoreThreshold float64
	Now            func() time.Time
}

// Embedder 抽象向量化能力，用于结构化召回分数相同后的语义辅助排序。
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}
