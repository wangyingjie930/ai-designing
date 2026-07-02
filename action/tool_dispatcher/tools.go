package tooldispatcher

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	LoadAccountSnapshotToolName        = "load_account_snapshot"
	CheckRenewalContractToolName       = "check_renewal_contract"
	DraftRetentionPlaybookToolName     = "draft_retention_playbook"
	EstimateExpansionPotentialToolName = "estimate_expansion_potential"
	BookOnsiteWorkshopToolName         = "book_onsite_workshop"
)

// AccountSnapshotRequest 表示查询客户健康快照的工具参数。
type AccountSnapshotRequest struct {
	AccountID string `json:"account_id" jsonschema:"description=客户或账号编号，例如 ACME-42"`
}

// AccountSnapshot 是客户健康工具返回给模型的结构化上下文。
type AccountSnapshot struct {
	AccountID       string   `json:"account_id"`
	CustomerName    string   `json:"customer_name"`
	Segment         string   `json:"segment"`
	HealthScore     int      `json:"health_score"`
	RiskSignals     []string `json:"risk_signals"`
	OpenBlockers    []string `json:"open_blockers"`
	LastCSMActivity string   `json:"last_csm_activity"`
}

// RenewalContractRequest 表示查询续费合同的工具参数。
type RenewalContractRequest struct {
	AccountID string `json:"account_id" jsonschema:"description=客户或账号编号，例如 ACME-42"`
}

// RenewalContract 是续费合同工具返回的商业约束。
type RenewalContract struct {
	AccountID     string `json:"account_id"`
	Plan          string `json:"plan"`
	DaysToRenewal int    `json:"days_to_renewal"`
	ARR           int    `json:"arr"`
	DecisionMaker string `json:"decision_maker"`
	InvoiceStatus string `json:"invoice_status"`
}

// RetentionPlaybookRequest 表示生成续费挽留方案的工具参数。
type RetentionPlaybookRequest struct {
	AccountID     string `json:"account_id" jsonschema:"description=客户或账号编号，例如 ACME-42"`
	RiskSignal    string `json:"risk_signal" jsonschema:"description=需要处理的核心风险信号"`
	DaysToRenewal int    `json:"days_to_renewal" jsonschema:"description=距离续费的天数"`
}

// RetentionPlaybook 是给客户成功经理执行的续费挽留方案。
type RetentionPlaybook struct {
	AccountID       string   `json:"account_id"`
	PlaybookName    string   `json:"playbook_name"`
	PriorityActions []string `json:"priority_actions"`
	CSMMessage      string   `json:"csm_message"`
	Escalation      string   `json:"escalation"`
}

// ExpansionPotentialRequest 表示评估扩容机会的工具参数。
type ExpansionPotentialRequest struct {
	AccountID string `json:"account_id" jsonschema:"description=客户或账号编号"`
}

// ExpansionPotential 是一个无关但真实的商业工具输出，用来模拟大工具库。
type ExpansionPotential struct {
	AccountID string `json:"account_id"`
	Potential string `json:"potential"`
	Reason    string `json:"reason"`
}

// WorkshopBookingRequest 表示预约线下工作坊的工具参数。
type WorkshopBookingRequest struct {
	AccountID string `json:"account_id" jsonschema:"description=客户或账号编号"`
	City      string `json:"city" jsonschema:"description=预约城市"`
}

// WorkshopBooking 是线下工作坊预约工具的结果。
type WorkshopBooking struct {
	AccountID string `json:"account_id"`
	Booked    bool   `json:"booked"`
	Summary   string `json:"summary"`
}

// RenewalRiskToolset 承载续费风险分诊 demo 的确定性工具实现。
type RenewalRiskToolset struct{}

// NewRenewalRiskTools 创建续费风险分诊 demo 的动态工具库。
func NewRenewalRiskTools() ([]tool.BaseTool, error) {
	toolset := RenewalRiskToolset{}
	builders := []func() (tool.BaseTool, error){
		func() (tool.BaseTool, error) {
			return toolutils.InferTool[AccountSnapshotRequest, *AccountSnapshot](
				LoadAccountSnapshotToolName,
				"加载客户健康快照，包含用量、阻塞问题、风险信号和客户成功最近动作。",
				toolset.LoadAccountSnapshot,
			)
		},
		func() (tool.BaseTool, error) {
			return toolutils.InferTool[RenewalContractRequest, *RenewalContract](
				CheckRenewalContractToolName,
				"查询续费合同约束，包含套餐、ARR、距离续费天数、决策人和发票状态。",
				toolset.CheckRenewalContract,
			)
		},
		func() (tool.BaseTool, error) {
			return toolutils.InferTool[RetentionPlaybookRequest, *RetentionPlaybook](
				DraftRetentionPlaybookToolName,
				"根据客户风险信号和续费窗口生成客户成功经理可执行的续费挽留方案。",
				toolset.DraftRetentionPlaybook,
			)
		},
		func() (tool.BaseTool, error) {
			return toolutils.InferTool[ExpansionPotentialRequest, *ExpansionPotential](
				EstimateExpansionPotentialToolName,
				"评估客户是否存在扩容机会；适合增长场景，不是续费风险分诊的首选工具。",
				toolset.EstimateExpansionPotential,
			)
		},
		func() (tool.BaseTool, error) {
			return toolutils.InferTool[WorkshopBookingRequest, *WorkshopBooking](
				BookOnsiteWorkshopToolName,
				"预约线下成功工作坊；适合高触达活动，不是普通续费风险分诊的首选工具。",
				toolset.BookOnsiteWorkshop,
			)
		},
	}
	tools := make([]tool.BaseTool, 0, len(builders))
	for _, build := range builders {
		candidate, err := build()
		if err != nil {
			return nil, err
		}
		tools = append(tools, candidate)
	}
	return tools, nil
}

// DefaultRenewalToolSelection 返回 demo 默认希望通过 tool_search 加载的工具名。
func DefaultRenewalToolSelection() []string {
	return []string{
		LoadAccountSnapshotToolName,
		CheckRenewalContractToolName,
		DraftRetentionPlaybookToolName,
	}
}

// LoadAccountSnapshot 查询客户健康快照，展示续费风险判断需要的基础事实。
func (RenewalRiskToolset) LoadAccountSnapshot(ctx context.Context, req AccountSnapshotRequest) (*AccountSnapshot, error) {
	accountID := normalizeAccountID(req.AccountID)
	return &AccountSnapshot{
		AccountID:       accountID,
		CustomerName:    "ACME 在线教育",
		Segment:         "enterprise",
		HealthScore:     58,
		RiskSignals:     []string{"近 14 天核心功能用量下降 37%", "管理员连续两次反馈发票问题", "关键讲师账号未完成迁移"},
		OpenBlockers:    []string{"发票抬头待财务确认", "续费报价需要补充 SLA 条款"},
		LastCSMActivity: "3 天前发送过续费提醒，但没有形成明确下一步会议。",
	}, nil
}

// CheckRenewalContract 查询合同约束，补齐续费窗口和商业上下文。
func (RenewalRiskToolset) CheckRenewalContract(ctx context.Context, req RenewalContractRequest) (*RenewalContract, error) {
	accountID := normalizeAccountID(req.AccountID)
	return &RenewalContract{
		AccountID:     accountID,
		Plan:          "enterprise",
		DaysToRenewal: 27,
		ARR:           128000,
		DecisionMaker: "教研运营负责人",
		InvoiceStatus: "发票信息未闭环，可能阻塞采购流程",
	}, nil
}

// DraftRetentionPlaybook 生成客户成功经理可执行的续费挽留方案。
func (RenewalRiskToolset) DraftRetentionPlaybook(ctx context.Context, req RetentionPlaybookRequest) (*RetentionPlaybook, error) {
	accountID := normalizeAccountID(req.AccountID)
	riskSignal := strings.TrimSpace(req.RiskSignal)
	if riskSignal == "" {
		riskSignal = "用量下降且关键阻塞未闭环"
	}
	days := req.DaysToRenewal
	if days <= 0 {
		days = 27
	}
	return &RetentionPlaybook{
		AccountID:    accountID,
		PlaybookName: "90 天续费挽留方案",
		PriorityActions: []string{
			fmt.Sprintf("T+1 前关闭发票阻塞，并把处理结果同步给 %s 的采购和业务负责人", accountID),
			fmt.Sprintf("围绕“%s”安排一次 30 分钟业务复盘，确认是否影响续费范围", riskSignal),
			fmt.Sprintf("距离续费还有 %d 天，先锁定决策人会议，再讨论折扣或 SLA 条款", days),
		},
		CSMMessage: "先解决发票和迁移阻塞，再用业务复盘把续费价值重新对齐。",
		Escalation: "如果 48 小时内没有决策人响应，升级给客户成功负责人协助推进。",
	}, nil
}

// EstimateExpansionPotential 模拟扩容场景工具，用来证明动态检索会筛掉无关能力。
func (RenewalRiskToolset) EstimateExpansionPotential(ctx context.Context, req ExpansionPotentialRequest) (*ExpansionPotential, error) {
	return &ExpansionPotential{
		AccountID: normalizeAccountID(req.AccountID),
		Potential: "medium",
		Reason:    "讲师账号仍有增长空间，但当前首要问题是续费风险闭环。",
	}, nil
}

// BookOnsiteWorkshop 模拟线下活动预约工具，用来证明工具库可以包含非当前任务能力。
func (RenewalRiskToolset) BookOnsiteWorkshop(ctx context.Context, req WorkshopBookingRequest) (*WorkshopBooking, error) {
	city := strings.TrimSpace(req.City)
	if city == "" {
		city = "上海"
	}
	return &WorkshopBooking{
		AccountID: normalizeAccountID(req.AccountID),
		Booked:    false,
		Summary:   "未直接预约；需要客户成功经理先确认续费风险是否已经降级。城市：" + city,
	}, nil
}

// normalizeAccountID 统一默认客户编号，避免 demo 输入为空时工具链断掉。
func normalizeAccountID(accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "ACME-42"
	}
	return accountID
}
