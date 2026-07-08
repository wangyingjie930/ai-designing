package cot_v2

// StepKind 表示 CoT 中每个原子命题的工程类型。
type StepKind string

const (
	StepKindObserve   StepKind = "observe"
	StepKindDecompose StepKind = "decompose"
	StepKindDerive    StepKind = "derive"
	StepKindVerify    StepKind = "verify"
	StepKindDecide    StepKind = "decide"
)

// StepStatus 表示 ClaimStep 经过程序闸门后的状态。
type StepStatus string

const (
	StepStatusDraft       StepStatus = "draft"
	StepStatusPassed      StepStatus = "passed"
	StepStatusFailed      StepStatus = "failed"
	StepStatusNeedsReview StepStatus = "needs_review"
)

// EvidenceRef 只保存外部证据引用，不把证据正文塞进推理链，方便审计时按版本回放。
type EvidenceRef struct {
	SourceID    string `json:"source_id" jsonschema:"required,description=外部证据唯一引用，例如 payroll_snapshot:2026-06:v3"`
	SourceType  string `json:"source_type" jsonschema:"required,description=证据类型，例如 payroll_snapshot、policy、approval、log"`
	Version     string `json:"version,omitempty" jsonschema:"description=证据版本，例如 v7"`
	EffectiveAt string `json:"effective_at,omitempty" jsonschema:"description=证据生效时间或业务时点"`
	ContentHash string `json:"content_hash,omitempty" jsonschema:"description=证据内容哈希，用于防止审计回放时漂移"`
}

// StepDraft 是 LLM 可以输出的候选命题草稿；它还不是可放行的正式审计节点。
type StepDraft struct {
	Kind                   StepKind `json:"kind" jsonschema:"required,enum=observe,enum=decompose,enum=derive,enum=verify,enum=decide,description=步骤类型：观察、拆解、推导、验证或决策"`
	ClaimText              string   `json:"claim_text" jsonschema:"required,minLength=1,description=给人看的单一可验证命题，不能把多个判断塞进同一步"`
	SuggestedSubject       string   `json:"suggested_subject,omitempty" jsonschema:"description=模型建议的结构化主体，例如 employee:P"`
	SuggestedPredicate     string   `json:"suggested_predicate,omitempty" jsonschema:"description=模型建议的结构化谓词，例如 pay_delta_percent"`
	SuggestedObject        any      `json:"suggested_object,omitempty" jsonschema:"description=模型建议的结构化宾语或数值，可为字符串、数字、对象或数组"`
	SuggestedEvidenceQuery string   `json:"suggested_evidence_query,omitempty" jsonschema:"description=模型建议系统去检索的证据查询，不等于已经拥有证据"`
}

// StepDraftList 是约束模型输出的顶层 JSON 对象。
type StepDraftList struct {
	Steps []StepDraft `json:"steps" jsonschema:"required,minItems=1,description=候选命题草稿列表，后续必须由程序编译成 ClaimStep"`
}

// ClaimStep 是进入应用侧审计轨迹的正式节点，必须带结构化谓词、依赖、证据或验证器等工程字段。
type ClaimStep struct {
	StepID       string        `json:"step_id"`
	Kind         StepKind      `json:"kind"`
	ClaimText    string        `json:"claim_text"`
	Subject      string        `json:"subject"`
	Predicate    string        `json:"predicate"`
	Object       any           `json:"object,omitempty"`
	DependsOn    []string      `json:"depends_on,omitempty"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
	Action       string        `json:"action,omitempty"`
	Observation  any           `json:"observation,omitempty"`
	Validator    string        `json:"validator,omitempty"`
	Status       StepStatus    `json:"status"`
}

// ClaimTrace 是应用层审计轨迹，记录每个命题节点、最终决定和停止原因。
type ClaimTrace struct {
	TraceID       string      `json:"trace_id"`
	Steps         []ClaimStep `json:"steps"`
	FinalDecision string      `json:"final_decision,omitempty"`
	StopReason    string      `json:"stop_reason,omitempty"`
}
