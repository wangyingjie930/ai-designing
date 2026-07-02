package guardrail

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	LookupServiceCaseToolName   = "lookup_service_case"
	DraftVisitPlanToolName      = "draft_visit_plan"
	SendCustomerNoticeToolName  = "send_customer_notice"
	CancelServiceContractMarker = "cancel_service_contract"
)

// HomeServiceAgentConfig 配置家装售后排期场景的 Guardrail ADK Agent。
type HomeServiceAgentConfig struct {
	Model         model.BaseChatModel
	Policy        SafetyPolicy
	Approver      Approver
	ExtraTools    []tool.BaseTool
	MaxIterations int
}

// HomeServiceAgent 用 Eino ADK 编排售后排期工具，并统一套上 guardrail middleware。
type HomeServiceAgent struct {
	runner *adk.Runner
}

// HomeServiceRequest 是家装售后排期 Agent 的自然语言输入。
type HomeServiceRequest struct {
	Message string `json:"message"`
}

// HomeServiceResponse 是家装售后排期 Agent 的最终回复。
type HomeServiceResponse struct {
	Message string `json:"message"`
}

// LookupServiceCaseRequest 表示查询售后工单的工具参数。
type LookupServiceCaseRequest struct {
	CaseID string `json:"case_id" jsonschema:"description=售后工单编号"`
}

// ServiceCase 是售后工单工具返回给模型的结构化上下文。
type ServiceCase struct {
	CaseID        string `json:"case_id"`
	CustomerName  string `json:"customer_name"`
	ContactEmail  string `json:"contact_email"`
	AddressArea   string `json:"address_area"`
	Issue         string `json:"issue"`
	PreferredTime string `json:"preferred_time"`
	InternalToken string `json:"internal_token"`
}

// DraftVisitPlanRequest 表示生成上门排期建议的工具参数。
type DraftVisitPlanRequest struct {
	CaseID        string `json:"case_id" jsonschema:"description=售后工单编号"`
	PreferredTime string `json:"preferred_time" jsonschema:"description=客户期望上门时间"`
}

// VisitPlan 是排期建议工具返回的低风险行动计划。
type VisitPlan struct {
	CaseID      string   `json:"case_id"`
	TimeWindow  string   `json:"time_window"`
	Technician  string   `json:"technician"`
	Checklist   []string `json:"checklist"`
	CustomerTip string   `json:"customer_tip"`
}

// SendCustomerNoticeRequest 表示真正外发客户通知的高风险工具参数。
type SendCustomerNoticeRequest struct {
	CaseID  string `json:"case_id" jsonschema:"description=售后工单编号"`
	Channel string `json:"channel" jsonschema:"description=通知渠道，如 sms 或 wecom"`
	Message string `json:"message" jsonschema:"description=将发送给客户的通知内容"`
}

// SendCustomerNoticeResponse 表示外发通知工具的执行结果。
type SendCustomerNoticeResponse struct {
	Sent    bool   `json:"sent"`
	CaseID  string `json:"case_id"`
	Channel string `json:"channel"`
}

// HomeServiceToolset 承载家装售后排期 demo 的确定性工具实现。
type HomeServiceToolset struct{}

// DefaultHomeServicePolicy 返回家装售后场景的默认安全策略。
func DefaultHomeServicePolicy() SafetyPolicy {
	policy := DefaultSafetyPolicy()
	policy.AllowedTools = []string{
		LookupServiceCaseToolName,
		DraftVisitPlanToolName,
		SendCustomerNoticeToolName,
	}
	policy.BlockedPatterns = []string{
		`(?i)` + CancelServiceContractMarker,
		`(?i)直接取消合同`,
	}
	return policy
}

// NewHomeServiceAgent 创建使用 GuardrailSandwich 保护的非 coding 场景 Agent。
func NewHomeServiceAgent(ctx context.Context, config HomeServiceAgentConfig) (*HomeServiceAgent, error) {
	if config.Model == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	tools, err := NewHomeServiceTools()
	if err != nil {
		return nil, err
	}
	tools = append(tools, config.ExtraTools...)
	policy := config.Policy
	if isZeroPolicy(policy) {
		policy = DefaultHomeServicePolicy()
	}
	middleware, err := NewMiddleware(MiddlewareConfig{
		Policy:   policy,
		Approver: config.Approver,
	})
	if err != nil {
		return nil, err
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 6
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "home_service_guardrail_agent",
		Description:   "家装售后排期助理，能查询工单、生成排期建议，并在外发通知前执行 guardrail。",
		Instruction:   DefaultHomeServiceInstruction(),
		Model:         config.Model,
		MaxIterations: maxIterations,
		Handlers:      []adk.ChatModelAgentMiddleware{middleware},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return nil, err
	}
	return &HomeServiceAgent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// NewHomeServiceTools 创建家装售后排期 demo 的工具集合。
func NewHomeServiceTools() ([]tool.BaseTool, error) {
	toolset := HomeServiceToolset{}
	lookupTool, err := toolutils.InferTool[LookupServiceCaseRequest, *ServiceCase](
		LookupServiceCaseToolName,
		"查询家装售后工单，返回问题、区域、期望上门时间和内部上下文。",
		toolset.LookupServiceCase,
	)
	if err != nil {
		return nil, err
	}
	planTool, err := toolutils.InferTool[DraftVisitPlanRequest, *VisitPlan](
		DraftVisitPlanToolName,
		"根据工单和客户期望时间生成上门排期建议，不直接联系客户。",
		toolset.DraftVisitPlan,
	)
	if err != nil {
		return nil, err
	}
	noticeTool, err := toolutils.InferTool[SendCustomerNoticeRequest, *SendCustomerNoticeResponse](
		SendCustomerNoticeToolName,
		"向客户外发通知。该动作会触达客户，必须通过 guardrail 审批。",
		toolset.SendCustomerNotice,
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{lookupTool, planTool, noticeTool}, nil
}

// DefaultHomeServiceInstruction 返回家装售后排期 Agent 的业务提示词。
func DefaultHomeServiceInstruction() string {
	return strings.Join([]string{
		"你是家装售后排期助理，目标是帮客服安全处理客户上门维修请求。",
		"必须先调用 lookup_service_case 获取工单上下文，再用 draft_visit_plan 形成排期建议。",
		"send_customer_notice 是真实外发动作，只有 guardrail 批准后才视为已发送；如果被拦截，要明确提示需要人工确认。",
		"不要在最终回复里暴露邮箱、token、密钥或其他内部敏感字段。",
	}, "\n")
}

// Query 处理一条自然语言售后排期请求，并返回最终模型回复。
func (a *HomeServiceAgent) Query(ctx context.Context, req HomeServiceRequest) (*HomeServiceResponse, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("agent is not initialized")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	iter := a.runner.Query(ctx, req.Message)
	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, err
		}
		if message != nil && strings.TrimSpace(message.Content) != "" {
			final = message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		return nil, fmt.Errorf("agent finished without assistant response")
	}
	return &HomeServiceResponse{Message: final}, nil
}

// LookupServiceCase 查询家装售后工单，故意带有敏感字段来演示输出脱敏。
func (HomeServiceToolset) LookupServiceCase(ctx context.Context, req LookupServiceCaseRequest) (*ServiceCase, error) {
	caseID := strings.TrimSpace(req.CaseID)
	if caseID == "" {
		caseID = "HS-1001"
	}
	return &ServiceCase{
		CaseID:        caseID,
		CustomerName:  "林女士",
		ContactEmail:  "owner@example.com",
		AddressArea:   "浦东新区",
		Issue:         "厨房水槽下方持续渗水，客户担心影响橱柜。",
		PreferredTime: "今天 18:00 前",
		InternalToken: "token=svc-secret",
	}, nil
}

// DraftVisitPlan 生成低风险排期建议，只返回可给客服参考的行动计划。
func (HomeServiceToolset) DraftVisitPlan(ctx context.Context, req DraftVisitPlanRequest) (*VisitPlan, error) {
	caseID := strings.TrimSpace(req.CaseID)
	if caseID == "" {
		caseID = "HS-1001"
	}
	preferredTime := strings.TrimSpace(req.PreferredTime)
	if preferredTime == "" {
		preferredTime = "今天 18:00 前"
	}
	return &VisitPlan{
		CaseID:     caseID,
		TimeWindow: preferredTime,
		Technician: "张师傅",
		Checklist: []string{
			"携带备用下水管和密封胶",
			"上门前 30 分钟由客服确认客户在家",
			"维修后拍照回传工单",
		},
		CustomerTip: "请客户提前清空水槽下方物品。",
	}, nil
}

// SendCustomerNotice 代表真实外发动作，默认会被 guardrail 在执行前拦截。
func (HomeServiceToolset) SendCustomerNotice(ctx context.Context, req SendCustomerNoticeRequest) (*SendCustomerNoticeResponse, error) {
	return &SendCustomerNoticeResponse{
		Sent:    true,
		CaseID:  req.CaseID,
		Channel: req.Channel,
	}, nil
}

// isZeroPolicy 判断调用方是否完全没有传策略，从而使用场景默认值。
func isZeroPolicy(policy SafetyPolicy) bool {
	return len(policy.AllowedTools) == 0 &&
		len(policy.BlockedPatterns) == 0 &&
		len(policy.AutoApprove) == 0 &&
		len(policy.RequireHuman) == 0 &&
		len(policy.SensitivePatterns) == 0
}
