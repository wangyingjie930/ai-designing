package selfheal

import (
	"context"
)

const (
	defaultAgentName        = "self_heal_agent"
	defaultAgentDescription = "Deterministic self-heal loop with Eino model nodes and controlled side-effect tools."
	defaultMaxIterations    = 3
	defaultMaxTokens        = 2048
)

// Status 表示自愈循环的稳定终态，调用方只需要基于这些枚举做后续分支。
type Status string

const (
	StatusFixed        Status = "fixed"
	StatusBlocked      Status = "blocked"
	StatusRolledBack   Status = "rolled_back"
	StatusHumanHandoff Status = "human_handoff"
)

// FailureSignal 描述一次待修复的失败信号，非 coding 场景里 affected_files 可理解为受影响配置或流程面。
type FailureSignal struct {
	Kind          string   `json:"kind"`
	Severity      int      `json:"severity"`
	ErrorText     string   `json:"error_text"`
	AffectedFiles []string `json:"affected_files,omitempty"`
}

// FixProposal 保存修复生成节点的结构化输出，FixDiff 可以是文本补丁、配置变更或业务规则差异。
type FixProposal struct {
	Summary string `json:"summary,omitempty"`
	FixDiff string `json:"fix_diff"`
}

// CriticVerdict 表示风险评审节点是否阻断当前修复。
type CriticVerdict struct {
	Block  bool   `json:"block"`
	Reason string `json:"reason,omitempty"`
}

// HealAttempt 记录一轮自愈尝试，方便命令输出、trace 和人工复盘。
type HealAttempt struct {
	Iteration  int            `json:"iteration"`
	Diagnosis  string         `json:"diagnosis"`
	FixDiff    string         `json:"fix_diff"`
	CommitID   string         `json:"commit_id,omitempty"`
	NewFailure *FailureSignal `json:"new_failure,omitempty"`
}

// Response 汇总自愈循环的最终状态和关键证据。
type Response struct {
	Status     Status        `json:"status"`
	Iterations int           `json:"iterations"`
	Commits    []string      `json:"commits,omitempty"`
	History    []HealAttempt `json:"history,omitempty"`
}

// Diagnoser 是自愈循环里的智能诊断节点，通常由 Eino ChatModel 适配而来。
type Diagnoser func(context.Context, FailureSignal, []HealAttempt) (string, error)

// FixGenerator 是自愈循环里的修复生成节点，输出必须能被 applier 稳定消费。
type FixGenerator func(context.Context, string, FailureSignal, []HealAttempt) (FixProposal, error)

// Critic 是自愈循环里的风险评审节点，只有 block=true 时才阻断应用。
type Critic func(context.Context, FixProposal, FailureSignal, []HealAttempt) (CriticVerdict, error)

// Applier 是唯一真正产生副作用的应用边界，返回值必须能用于回滚。
type Applier func(context.Context, FixProposal) (string, error)

// Verifier 是修复后的验证边界，返回 nil 表示通过，否则返回新的失败信号。
type Verifier func(context.Context, FixProposal) (*FailureSignal, error)

// Rollbacker 是回滚边界，只接受 applier 产出的 commit/checkpoint ID。
type Rollbacker func(context.Context, string) error

// Config 汇总自愈 Agent 的模型节点、工具边界和迭代上限。
type Config struct {
	Name          string
	Description   string
	MaxIterations int
	Diagnoser     Diagnoser
	FixGenerator  FixGenerator
	Critic        Critic
	Applier       Applier
	Verifier      Verifier
	Rollback      Rollbacker
	MaxTokens     int
}
