package progresstracking

import "time"

const (
	defaultLongHorizonTaskID   = "default"
	defaultRecentLedgerLimit   = 5
	defaultGoalContractVersion = 1
)

const (
	TaskStatusBlocked     TaskStatus = "blocked"
	TaskStatusNeedsReview TaskStatus = "needs_review"
	TaskStatusNeedsRework TaskStatus = "needs_rework"
)

// Decision 是进度账本允许记录的有限决策集合，避免模型自由发明动作类别。
type Decision string

const (
	DecisionApprove  Decision = "approve"
	DecisionDeny     Decision = "deny"
	DecisionEscalate Decision = "escalate"
	DecisionDefer    Decision = "defer"
	DecisionRetry    Decision = "retry"
)

// TrustLevel 描述机械态值的来源可信度，工具输出和系统记录优先级最高。
type TrustLevel string

const (
	TrustToolOutput   TrustLevel = "tool_output"
	TrustSystemRecord TrustLevel = "system_record"
	TrustUserInput    TrustLevel = "user_input"
	TrustInferred     TrustLevel = "inferred"
)

// DriftLevel 表示漂移哨兵对当前任务偏移程度的分级判断。
type DriftLevel string

const (
	DriftLevelOK       DriftLevel = "ok"
	DriftLevelWatch    DriftLevel = "watch"
	DriftLevelRecenter DriftLevel = "recenter"
	DriftLevelPause    DriftLevel = "pause"
)

// GoalContract 是长任务的目标锚，冻结成功标准、非目标和约束。
type GoalContract struct {
	GoalID          string   `json:"goal_id"`
	UserGoal        string   `json:"user_goal"`
	SuccessCriteria []string `json:"success_criteria"`
	NonGoals        []string `json:"non_goals"`
	Constraints     []string `json:"constraints"`
	Version         int      `json:"version"`
}

// Milestone 是调度平面的近目标，带当前阶段的验收条件。
type Milestone struct {
	MilestoneID   string     `json:"milestone_id"`
	Title         string     `json:"title"`
	Acceptance    []string   `json:"acceptance"`
	Status        TaskStatus `json:"status"`
	ActiveSubgoal string     `json:"active_subgoal,omitempty"`
}

// StateDelta 记录一次账本事件读写了哪些状态坐标。
type StateDelta struct {
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
}

// ProgressEvent 是 append-only 进度账本的一条记录，回答发生了什么、为何这样做、凭什么判断。
type ProgressEvent struct {
	Sequence       int64      `json:"sequence"`
	Event          string     `json:"event"`
	Decision       Decision   `json:"decision,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	EvidenceRefs   []string   `json:"evidence_refs,omitempty"`
	StateDelta     StateDelta `json:"state_delta,omitempty"`
	CompensateOp   string     `json:"compensate_op,omitempty"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	NextAction     string     `json:"next_action,omitempty"`
	CreatedAt      string     `json:"created_at"`
}

// MechanicalValue 是机械状态平面的可复用真值，保留来源、层级、范围和可信度。
type MechanicalValue struct {
	Key          string     `json:"key"`
	Scope        string     `json:"scope"`
	ValueRef     string     `json:"value_ref"`
	Value        any        `json:"value,omitempty"`
	Provider     string     `json:"provider"`
	RuntimeLayer string     `json:"runtime_layer"`
	Trust        TrustLevel `json:"trust"`
}

// DriftSignal 保存漂移哨兵对目标相关度、里程碑推进、证据健康和错误压力的判断。
type DriftSignal struct {
	Level             DriftLevel `json:"level"`
	GoalRelevance     float64    `json:"goal_relevance"`
	MilestoneProgress float64    `json:"milestone_progress"`
	EvidenceHealth    float64    `json:"evidence_health"`
	ErrorPressure     float64    `json:"error_pressure"`
	Reason            string     `json:"reason"`
	CreatedAt         string     `json:"created_at,omitempty"`
}

// ResumePacket 是断点恢复交给 Agent 的最小可靠上下文。
type ResumePacket struct {
	Goal                GoalContract    `json:"goal"`
	CurrentMilestone    Milestone       `json:"current_milestone"`
	WorkingCollection   map[string]any  `json:"working_collection"`
	RecentLedger        []ProgressEvent `json:"recent_ledger"`
	OpenBlockers        []string        `json:"open_blockers"`
	MechanicalStateKeys []string        `json:"mechanical_state_keys"`
	LastDriftSignal     *DriftSignal    `json:"last_drift_signal,omitempty"`
	NextAction          string          `json:"next_action,omitempty"`
}

// LongHorizonConfig 配置 v2 长任务 tracker 的 SQLite 路径、任务标识和时钟依赖。
type LongHorizonConfig struct {
	DBPath string
	TaskID string
	Now    func() time.Time
}

// InitializeLongHorizonRequest 初始化一次长任务的锚、里程碑、当前工作集和下一步动作。
type InitializeLongHorizonRequest struct {
	Goal               GoalContract   `json:"goal"`
	Milestones         []Milestone    `json:"milestones"`
	CurrentMilestoneID string         `json:"current_milestone_id"`
	WorkingCollection  map[string]any `json:"working_collection,omitempty"`
	OpenBlockers       []string       `json:"open_blockers,omitempty"`
	NextAction         string         `json:"next_action,omitempty"`
}

// AppendProgressEventRequest 是追加账本事件的受控输入，事实字段由 wrapper 侧确定性捕获。
type AppendProgressEventRequest struct {
	Event          string     `json:"event"`
	Decision       Decision   `json:"decision,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	EvidenceRefs   []string   `json:"evidence_refs,omitempty"`
	StateDelta     StateDelta `json:"state_delta,omitempty"`
	CompensateOp   string     `json:"compensate_op,omitempty"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	NextAction     string     `json:"next_action,omitempty"`
}
