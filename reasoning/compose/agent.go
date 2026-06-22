package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai-designing/reasoning/cot"
	"ai-designing/reasoning/hypothesis"
	"ai-designing/reasoning/tot"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Agent 负责把复杂度路由、直接回答、CoT、ToT 和假设检验串成一条自适应主流程。
type Agent struct {
	name                  string
	description           string
	scenario              string
	routerModel           model.BaseChatModel
	directModel           model.BaseChatModel
	confirmationModel     model.BaseChatModel
	cotAgent              *cot.ReasoningAgent
	totAgent              *tot.ReasoningAgent
	hypothesisAgent       *hypothesis.Agent
	routerMaxTokens       int
	directMaxTokens       int
	confirmationMaxTokens int
	confidenceThreshold   float64
	bestPathThreshold     float64
}

// NewAgent 创建组合 Agent，并把未单独配置的子模型回退到主模型。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("compose agent model is required")
	}
	name := firstNonEmpty(config.Name, defaultAgentName)
	description := firstNonEmpty(config.Description, defaultAgentDescription)
	routerModel := fallbackModel(config.RouterModel, config.Model)
	directModel := fallbackModel(config.DirectModel, config.Model)
	confirmationModel := fallbackModel(config.ConfirmationModel, config.Model)

	cotAgent, err := newCOTAgent(ctx, config)
	if err != nil {
		return nil, err
	}
	totAgent, err := newTOTAgent(ctx, config)
	if err != nil {
		return nil, err
	}
	hypothesisAgent, err := newHypothesisAgent(ctx, config)
	if err != nil {
		return nil, err
	}

	return &Agent{
		name:                  name,
		description:           description,
		scenario:              strings.TrimSpace(config.Scenario),
		routerModel:           routerModel,
		directModel:           directModel,
		confirmationModel:     confirmationModel,
		cotAgent:              cotAgent,
		totAgent:              totAgent,
		hypothesisAgent:       hypothesisAgent,
		routerMaxTokens:       defaultPositive(config.RouterMaxTokens, defaultRouterMaxTokens),
		directMaxTokens:       defaultPositive(config.DirectMaxTokens, defaultDirectMaxTokens),
		confirmationMaxTokens: defaultPositive(config.ConfirmationMaxTokens, defaultConfirmationMaxTokens),
		confidenceThreshold:   defaultProbability(config.ConfidenceThreshold, defaultConfidenceThreshold),
		bestPathThreshold:     defaultProbability(config.BestPathThreshold, defaultBestPathThreshold),
	}, nil
}

// Name 返回 ADK 和 trace 展示用的 agent 名称。
func (a *Agent) Name() string {
	if a == nil {
		return ""
	}
	return a.name
}

// Description 返回 ADK agent 描述。
func (a *Agent) Description() string {
	if a == nil {
		return ""
	}
	return a.description
}

// Solve 按“复杂度路由 -> 对应推理路径 -> 必要时假设检验 -> 最终响应/人工升级”执行完整流程。
func (a *Agent) Solve(ctx context.Context, req Request) (*Response, error) {
	if a == nil {
		return nil, errors.New("compose agent is nil")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		query = queryFromMessages(req.Messages)
	}
	if query == "" {
		return nil, errors.New("query is required")
	}
	decision, err := a.route(ctx, query)
	if err != nil {
		return nil, err
	}
	response := &Response{
		Query:    query,
		Scenario: a.scenario,
		Decision: decision,
		Path:     []PathStep{},
	}
	switch decision.Complexity {
	case ComplexitySimple:
		return a.solveSimple(ctx, response)
	case ComplexityModerate:
		return a.solveModerate(ctx, response)
	case ComplexityComplex:
		return a.solveComplex(ctx, response)
	default:
		return nil, fmt.Errorf("unsupported complexity %q", decision.Complexity)
	}
}

// solveSimple 执行图里的 Direct Response/System 1 路径。
func (a *Agent) solveSimple(ctx context.Context, response *Response) (*Response, error) {
	answer, err := callRoleModel(ctx, directModelRole, a.directModel, DefaultDirectSystemPrompt(), buildDirectPrompt(response.Query, a.scenario), a.directMaxTokens)
	if err != nil {
		return nil, err
	}
	response.DirectAnswer = answer
	response.FinalAnswer = answer
	response.addStep(PathDirect, "completed", "simple query answered directly")
	return response, nil
}

// solveModerate 执行单路径 CoT，并在低置信度或校验失败时进入假设检验。
func (a *Agent) solveModerate(ctx context.Context, response *Response) (*Response, error) {
	cotResp, err := a.cotAgent.Solve(ctx, cot.Request{Question: response.Query})
	if err != nil {
		return nil, err
	}
	response.COT = cotResp
	confidence := cotConfidence(cotResp)
	response.addStep(PathCOT, "completed", fmt.Sprintf("verified=%t confidence=%.2f", cotResp.Verified, confidence))
	if cotResp.Verified && confidence >= a.confidenceThreshold {
		response.FinalAnswer = cotResp.FinalAnswer
		return response, nil
	}
	return a.verifyWithHypothesis(ctx, response, cotResp.FinalAnswer, PathCOT, false)
}

// solveComplex 执行多路径 ToT，再把候选答案送入假设检验循环。
func (a *Agent) solveComplex(ctx context.Context, response *Response) (*Response, error) {
	totResp, err := a.totAgent.Generate(ctx, tot.Request{Prompt: response.Query})
	if err != nil {
		return nil, err
	}
	response.TOT = totResp
	response.addStep(PathTOT, "completed", fmt.Sprintf("method=%s answer_chars=%d", totResp.Method, len([]rune(totResp.Answer))))

	bestPath, err := a.confirmBestPath(ctx, response.Query, totResp.Answer)
	if err != nil {
		return nil, err
	}
	response.BestPath = &bestPath
	status := "not_confirmed"
	if bestPath.Confirmed && bestPath.Confidence >= a.bestPathThreshold {
		status = "confirmed"
	}
	response.addStep(PathTOT, status, fmt.Sprintf("best_path_confidence=%.2f", bestPath.Confidence))
	return a.verifyWithHypothesis(ctx, response, totResp.Answer, PathTOT, status == "confirmed")
}

// verifyWithHypothesis 执行图里的 Hypothesis Testing/Converged 分支，并决定是否升级给人。
func (a *Agent) verifyWithHypothesis(ctx context.Context, response *Response, candidate string, source PathKind, probeEnv bool) (*Response, error) {
	problem := buildHypothesisProblem(response.Query, a.scenario, candidate, source, probeEnv)
	hypothesisResp, err := a.hypothesisAgent.Diagnose(ctx, hypothesis.Request{Problem: problem})
	if err != nil {
		return nil, err
	}
	response.Hypothesis = hypothesisResp
	response.UsedHypothesis = true
	response.addStep(PathHypothesis, "completed", fmt.Sprintf("converged=%t needs_hitl=%t", hypothesisResp.Outcome.Converged, hypothesisResp.Outcome.NeedsHITL))
	if hypothesisResp.Outcome.NeedsHITL {
		response.Escalated = true
		response.EscalationReason = hypothesisResp.Outcome.Reason
		response.FinalAnswer = "需要人工接管：" + hypothesisResp.FinalAnswer
		response.addStep(PathEscalate, "completed", hypothesisResp.Outcome.Reason)
		return response, nil
	}
	response.FinalAnswer = composeVerifiedAnswer(candidate, hypothesisResp)
	return response, nil
}

// route 调用复杂度路由模型；JSON 解析失败时保守回退到启发式路由。
func (a *Agent) route(ctx context.Context, query string) (RouteDecision, error) {
	reply, err := callRoleModel(ctx, routerModelRole, a.routerModel, DefaultRouterSystemPrompt(), buildRouterPrompt(query, a.scenario), a.routerMaxTokens)
	if err != nil {
		return RouteDecision{}, err
	}
	decision, err := ParseRouteDecision(reply)
	if err == nil {
		return decision, nil
	}
	return heuristicRouteDecision(query, "router parse fallback: "+err.Error()), nil
}

// confirmBestPath 判断 ToT 候选答案是否已经形成可验证的最佳路径。
func (a *Agent) confirmBestPath(ctx context.Context, query string, candidate string) (BestPathDecision, error) {
	reply, err := callRoleModel(ctx, confirmationModelRole, a.confirmationModel, DefaultBestPathSystemPrompt(), buildBestPathPrompt(query, a.scenario, candidate), a.confirmationMaxTokens)
	if err != nil {
		return BestPathDecision{}, err
	}
	decision, err := ParseBestPathDecision(reply)
	if err != nil {
		return BestPathDecision{}, err
	}
	return decision, nil
}

// addStep 追加一条路径审计记录，方便命令行和 JSON 看清实际经过的节点。
func (r *Response) addStep(kind PathKind, status string, summary string) {
	if r == nil {
		return
	}
	r.Path = append(r.Path, PathStep{
		Kind:    kind,
		Status:  strings.TrimSpace(status),
		Summary: strings.TrimSpace(summary),
	})
}

// UserMessage 把组合响应转成人可读的 ADK assistant 文本，结构化明细保留在 customized output。
func (r *Response) UserMessage() string {
	if r == nil {
		return ""
	}
	lines := []string{strings.TrimSpace(r.FinalAnswer)}
	lines = append(lines, "", fmt.Sprintf("路由：%s -> %s，confidence=%.2f", r.Decision.Complexity, r.Decision.RecommendedPath, r.Decision.Confidence))
	if r.UsedHypothesis {
		lines = append(lines, "验证：已进入假设检验。")
	}
	if r.Escalated {
		lines = append(lines, "状态：需要人工接管。")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// newCOTAgent 按组合配置创建 CoT 子 Agent，并继承场景和主模型。
func newCOTAgent(ctx context.Context, config Config) (*cot.ReasoningAgent, error) {
	cotConfig := config.COTConfig
	cotConfig.Name = firstNonEmpty(cotConfig.Name, "compose_cot_agent")
	cotConfig.Description = firstNonEmpty(cotConfig.Description, "Single-path verifier used by the adaptive compose agent.")
	cotConfig.Scenario = firstNonEmpty(cotConfig.Scenario, config.Scenario)
	cotConfig.Model = fallbackModel(cotConfig.Model, config.Model)
	cotConfig.VerifierModel = fallbackModel(cotConfig.VerifierModel, cotConfig.Model)
	return cot.NewReasoningAgent(ctx, cotConfig)
}

// newTOTAgent 按组合配置创建 ToT 子 Agent，并使用适合 demo 的快速默认搜索参数。
func newTOTAgent(ctx context.Context, config Config) (*tot.ReasoningAgent, error) {
	totConfig := config.TOTConfig
	totConfig.Name = firstNonEmpty(totConfig.Name, "compose_tot_agent")
	totConfig.Description = firstNonEmpty(totConfig.Description, "Parallel exploration agent used by the adaptive compose agent.")
	totConfig.Scope = firstNonEmpty(totConfig.Scope, config.Scenario)
	totConfig.Model = fallbackModel(totConfig.Model, config.Model)
	totConfig.GraderModel = fallbackModel(totConfig.GraderModel, totConfig.Model)
	totConfig.ReasonConfig = defaultComposeReasonConfig(totConfig.ReasonConfig)
	return tot.NewReasoningAgent(ctx, totConfig)
}

// newHypothesisAgent 按组合配置创建 hypothesis 子 Agent，并设置保守的默认预算。
func newHypothesisAgent(ctx context.Context, config Config) (*hypothesis.Agent, error) {
	hypothesisConfig := config.HypothesisConfig
	hypothesisConfig.Name = firstNonEmpty(hypothesisConfig.Name, "compose_hypothesis_agent")
	hypothesisConfig.Description = firstNonEmpty(hypothesisConfig.Description, "Hypothesis testing verifier used by the adaptive compose agent.")
	hypothesisConfig.Scenario = firstNonEmpty(hypothesisConfig.Scenario, config.Scenario)
	hypothesisConfig.Model = fallbackModel(hypothesisConfig.Model, config.Model)
	hypothesisConfig.PlannerModel = fallbackModel(hypothesisConfig.PlannerModel, hypothesisConfig.Model)
	hypothesisConfig.GeneratorModel = fallbackModel(hypothesisConfig.GeneratorModel, hypothesisConfig.Model)
	hypothesisConfig.EvaluatorModel = fallbackModel(hypothesisConfig.EvaluatorModel, hypothesisConfig.Model)
	if hypothesisConfig.MaxIterations <= 0 {
		hypothesisConfig.MaxIterations = defaultHypothesisMaxIteration
	}
	return hypothesis.NewAgent(ctx, hypothesisConfig)
}

// defaultComposeReasonConfig 给复杂路径设置短预算默认值，避免普通 demo 误触发深搜索。
func defaultComposeReasonConfig(config tot.ReasonConfig) tot.ReasonConfig {
	if config.Method == "" {
		config.Method = tot.MethodMCTS
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = 1
	}
	if config.NSim <= 0 {
		config.NSim = 1
	}
	if config.ForestSize <= 0 {
		config.ForestSize = 1
	}
	if config.AnswerApproach == "" {
		config.AnswerApproach = tot.AnswerApproachPool
	}
	return config
}

// cotConfidence 用最低步骤置信度表示整条单路径推理的薄弱点。
func cotConfidence(response *cot.Response) float64 {
	if response == nil || response.WeakestStep == nil {
		return 0
	}
	return response.WeakestStep.Confidence
}

// composeVerifiedAnswer 把候选答案和 hypothesis 收敛结论合成最终给用户的答复。
func composeVerifiedAnswer(candidate string, response *hypothesis.Response) string {
	if response == nil {
		return strings.TrimSpace(candidate)
	}
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return response.FinalAnswer
	}
	return strings.TrimSpace(candidate + "\n\n验证结论：" + response.FinalAnswer)
}

// queryFromMessages 把 ADK 多消息输入压成当前问题，只提取文本部分。
func queryFromMessages(messages []*schema.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := messageText(message)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n")
}

// messageText 读取普通文本消息；多模态消息只提取 text part。
func messageText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	parts := make([]string, 0, len(message.UserInputMultiContent))
	for _, part := range message.UserInputMultiContent {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// firstNonEmpty 返回第一项非空字符串，用于配置覆盖。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// defaultPositive 返回正整数配置，否则使用默认值。
func defaultPositive(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// defaultProbability 返回有效概率配置，否则使用默认值。
func defaultProbability(value float64, fallback float64) float64 {
	if value > 0 {
		return normalizeProbability(value)
	}
	return fallback
}

// fallbackModel 返回显式模型或主模型，集中处理子角色模型默认值。
func fallbackModel(candidate model.BaseChatModel, fallback model.BaseChatModel) model.BaseChatModel {
	if candidate != nil {
		return candidate
	}
	return fallback
}
