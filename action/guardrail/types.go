package guardrail

import "context"

// RiskLevel 表示一次工具调用在业务安全上的风险级别。
type RiskLevel string

const (
	RiskLevelLow      RiskLevel = "low"
	RiskLevelMedium   RiskLevel = "medium"
	RiskLevelHigh     RiskLevel = "high"
	RiskLevelCritical RiskLevel = "critical"
)

// SafetyPolicy 定义 Agent 在工具使用和内容输出上的安全边界。
type SafetyPolicy struct {
	AllowedTools      []string
	BlockedPatterns   []string
	AutoApprove       []RiskLevel
	RequireHuman      []RiskLevel
	SensitivePatterns []string
}

// ApprovalRequest 是高风险工具真正执行前提交给人工审批器的上下文。
type ApprovalRequest struct {
	ToolName        string
	ArgumentsInJSON string
	RiskLevel       RiskLevel
	Reason          string
}

// ApprovalDecision 表示人工审批器对高风险动作的判断。
type ApprovalDecision struct {
	Approved bool
	Reason   string
}

// Approver 抽象人工审批系统，命令行 demo 可以用固定函数，生产可接工单或审批流。
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

// ApproverFunc 让普通函数可以直接作为审批器注入。
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)

// Approve 调用底层函数完成审批。
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	return f(ctx, req)
}

// RiskClassifier 负责把工具名和参数归类为风险级别。
type RiskClassifier interface {
	ClassifyRisk(toolName string, argumentsInJSON string) RiskLevel
}

// Verdict 是输入过滤阶段对一次工具调用的裁决。
type Verdict struct {
	Approved  bool      `json:"approved"`
	RiskLevel RiskLevel `json:"risk_level"`
	Reason    string    `json:"reason,omitempty"`
}

// MiddlewareConfig 收拢 guardrail middleware 的可注入依赖。
type MiddlewareConfig struct {
	Policy     SafetyPolicy
	Classifier RiskClassifier
	Approver   Approver
}
