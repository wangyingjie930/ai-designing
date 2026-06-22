package hypothesis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Agent 用 Eino ChatModel 组装 planner、generator、evaluator 三个角色。
type Agent struct {
	name          string
	description   string
	scenario      string
	planner       PlannerFunc
	generator     GeneratorFunc
	evaluator     EvaluatorFunc
	maxIterations int
	maxHypotheses int
	maxEvidence   int
}

// NewAgent 创建迭代假设检验 Agent，并把缺省角色模型回退到主模型。
func NewAgent(_ context.Context, config Config) (*Agent, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultAgentName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = defaultAgentDescription
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}
	maxHypotheses := defaultPositive(config.MaxHypotheses, defaultMaxHypotheses)
	maxEvidence := defaultPositive(config.MaxEvidence, defaultMaxEvidence)
	plannerMaxTokens := defaultPositive(config.PlannerMaxTokens, defaultPlannerMaxTokens)
	evidenceMaxTokens := defaultPositive(config.EvidenceMaxTokens, defaultEvidenceMaxTokens)
	evaluatorMaxTokens := defaultPositive(config.EvaluatorMaxTokens, defaultEvaluatorMaxTokens)

	planner := config.Planner
	generator := config.Generator
	evaluator := config.Evaluator
	if planner == nil || generator == nil || evaluator == nil {
		models, err := normalizeRoleModels(config)
		if err != nil {
			return nil, err
		}
		scenario := strings.TrimSpace(config.Scenario)
		if planner == nil {
			planner = buildModelPlanner(models.planner, scenario, maxHypotheses, plannerMaxTokens)
		}
		if generator == nil {
			generator = buildModelGenerator(models.generator, scenario, maxEvidence, evidenceMaxTokens)
		}
		if evaluator == nil {
			evaluator = buildModelEvaluator(models.evaluator, scenario, evaluatorMaxTokens)
		}
	}
	planner = limitPlanner(planner, maxHypotheses)
	generator = limitGenerator(generator, maxEvidence)
	if _, err := NewIterativeHypothesisLoop(planner, generator, evaluator, maxIterations); err != nil {
		return nil, err
	}
	return &Agent{
		name:          name,
		description:   description,
		scenario:      strings.TrimSpace(config.Scenario),
		planner:       planner,
		generator:     generator,
		evaluator:     evaluator,
		maxIterations: maxIterations,
		maxHypotheses: maxHypotheses,
		maxEvidence:   maxEvidence,
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

// Diagnose 运行完整假设检验主流程，并生成面向用户的结论摘要。
func (a *Agent) Diagnose(ctx context.Context, req Request) (*Response, error) {
	if a == nil {
		return nil, errors.New("hypothesis agent is nil")
	}
	problem := strings.TrimSpace(req.Problem)
	if problem == "" {
		problem = problemFromMessages(req.Messages)
	}
	if problem == "" {
		return nil, errors.New("problem is required")
	}
	ctx = withProblemContext(ctx, problem)
	loop, err := NewIterativeHypothesisLoop(a.planner, a.generator, a.evaluator, a.maxIterations)
	if err != nil {
		return nil, err
	}
	tree, outcome, err := loop.Run(ctx, problem)
	if err != nil {
		return nil, err
	}
	response := &Response{
		Problem:     problem,
		Scenario:    a.scenario,
		Tree:        tree.Snapshot(),
		Outcome:     outcome,
		FinalAnswer: buildFinalAnswer(tree, outcome),
	}
	return response, nil
}

// UserMessage 把结构化响应转成人可读的 ADK assistant 文本。
func (r *Response) UserMessage() string {
	if r == nil {
		return ""
	}
	lines := []string{strings.TrimSpace(r.FinalAnswer)}
	lines = append(lines, "", fmt.Sprintf("循环结果：iterations=%d converged=%t needs_hitl=%t", r.Outcome.IterationsUsed, r.Outcome.Converged, r.Outcome.NeedsHITL))
	lines = append(lines, fmt.Sprintf("假设统计：total=%d survivors=%d confirmed=%d", len(r.Tree.Hypotheses), r.Tree.SurvivorCount, r.Tree.ConfirmedCount))
	for _, h := range r.Tree.Hypotheses {
		lines = append(lines, fmt.Sprintf("- %s | %s | posterior=%.2f | status=%s", h.ID, h.Description, h.Posterior, h.Status))
		if h.FalsifiedBy != "" {
			lines = append(lines, fmt.Sprintf("  falsified_by: %s", h.FalsifiedBy))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// roleModels 保存三类模型角色，方便缺省值处理集中在一个地方。
type roleModels struct {
	planner   model.BaseChatModel
	generator model.BaseChatModel
	evaluator model.BaseChatModel
}

// normalizeRoleModels 把未单独配置的角色模型回退到主模型。
func normalizeRoleModels(config Config) (roleModels, error) {
	base := config.Model
	planner := config.PlannerModel
	if planner == nil {
		planner = base
	}
	generator := config.GeneratorModel
	if generator == nil {
		generator = base
	}
	evaluator := config.EvaluatorModel
	if evaluator == nil {
		evaluator = base
	}
	if planner == nil || generator == nil || evaluator == nil {
		return roleModels{}, errors.New("model is required when planner/generator/evaluator funcs are not all provided")
	}
	return roleModels{planner: planner, generator: generator, evaluator: evaluator}, nil
}

// buildModelPlanner 把 ChatModel 封装成 PlannerFunc。
func buildModelPlanner(chatModel model.BaseChatModel, scenario string, maxHypotheses int, maxTokens int) PlannerFunc {
	return func(ctx context.Context, problem string, existing []Hypothesis, iteration int) ([]Proposal, error) {
		reply, err := callRoleModel(ctx, plannerModelRole, chatModel, DefaultPlannerSystemPrompt(), buildPlannerPrompt(problem, scenario, existing, iteration, maxHypotheses), maxTokens)
		if err != nil {
			return nil, err
		}
		return ParsePlannerResponse(reply)
	}
}

// buildModelGenerator 把 ChatModel 封装成 GeneratorFunc。
func buildModelGenerator(chatModel model.BaseChatModel, scenario string, maxEvidence int, maxTokens int) GeneratorFunc {
	return func(ctx context.Context, h Hypothesis) ([]EvidenceCandidate, error) {
		reply, err := callRoleModel(ctx, generatorModelRole, chatModel, DefaultGeneratorSystemPrompt(), buildGeneratorPrompt(currentProblem(ctx), scenario, h, maxEvidence), maxTokens)
		if err != nil {
			return nil, err
		}
		return ParseGeneratorResponse(reply)
	}
}

// buildModelEvaluator 把 ChatModel 封装成 EvaluatorFunc。
func buildModelEvaluator(chatModel model.BaseChatModel, scenario string, maxTokens int) EvaluatorFunc {
	return func(ctx context.Context, h Hypothesis, evidence EvidenceCandidate) (Evaluation, error) {
		reply, err := callRoleModel(ctx, evaluatorModelRole, chatModel, DefaultEvaluatorSystemPrompt(), buildEvaluatorPrompt(currentProblem(ctx), scenario, h, evidence), maxTokens)
		if err != nil {
			return Evaluation{}, err
		}
		return ParseEvaluatorResponse(reply)
	}
}

// limitPlanner 对 planner 输出做硬截断，避免真实模型一次吐太多假设导致运行时间失控。
func limitPlanner(planner PlannerFunc, maxHypotheses int) PlannerFunc {
	return func(ctx context.Context, problem string, existing []Hypothesis, iteration int) ([]Proposal, error) {
		proposals, err := planner(ctx, problem, existing, iteration)
		if err != nil {
			return nil, err
		}
		proposals = filterNovelProposals(proposals, existing)
		if maxHypotheses > 0 && len(proposals) > maxHypotheses {
			return proposals[:maxHypotheses], nil
		}
		return proposals, nil
	}
}

// filterNovelProposals 过滤已经进入假设树的候选，避免 planner 后续轮次把旧 candidate 当新候选返回。
func filterNovelProposals(proposals []Proposal, existing []Hypothesis) []Proposal {
	if len(proposals) == 0 || len(existing) == 0 {
		return proposals
	}
	seen := make(map[string]struct{}, len(existing))
	for _, h := range existing {
		if strings.TrimSpace(h.Description) == "" {
			continue
		}
		seen[hypothesisID(h.Description)] = struct{}{}
	}
	filtered := make([]Proposal, 0, len(proposals))
	for _, proposal := range proposals {
		if strings.TrimSpace(proposal.Description) == "" {
			continue
		}
		if _, exists := seen[hypothesisID(proposal.Description)]; exists {
			continue
		}
		filtered = append(filtered, proposal)
	}
	return filtered
}

// limitGenerator 对每个假设的证据数做硬截断，控制 evaluator 调用次数。
func limitGenerator(generator GeneratorFunc, maxEvidence int) GeneratorFunc {
	return func(ctx context.Context, h Hypothesis) ([]EvidenceCandidate, error) {
		candidates, err := generator(ctx, h)
		if err != nil {
			return nil, err
		}
		if maxEvidence > 0 && len(candidates) > maxEvidence {
			return candidates[:maxEvidence], nil
		}
		return candidates, nil
	}
}

type problemContextKey struct{}

// withProblemContext 把完整问题放进 context，供 generator/evaluator prompt 使用。
func withProblemContext(ctx context.Context, problem string) context.Context {
	return context.WithValue(ctx, problemContextKey{}, strings.TrimSpace(problem))
}

// currentProblem 从 context 读取完整问题，缺失时返回空字符串。
func currentProblem(ctx context.Context) string {
	if value, ok := ctx.Value(problemContextKey{}).(string); ok {
		return value
	}
	return ""
}

// problemFromMessages 把 ADK 多消息输入压成当前问题，只提取文本部分。
func problemFromMessages(messages []*schema.Message) string {
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

// buildFinalAnswer 根据幸存假设生成简短业务结论，不把完整证据内容塞进 trace。
func buildFinalAnswer(tree *HypothesisTree, outcome LoopOutcome) string {
	if tree == nil {
		return "没有生成可用假设。"
	}
	if outcome.ConfirmedID != "" {
		if h := tree.ByID(outcome.ConfirmedID); h != nil {
			prefix := "最可能解释"
			if outcome.Converged {
				prefix = "已收敛解释"
			}
			return fmt.Sprintf("%s：%s。退出原因：%s。", prefix, h.Description, outcome.Reason)
		}
	}
	survivors := tree.Survivors()
	if len(survivors) == 0 {
		return "当前候选假设都已被反证，建议人工补充新事实后重新规划假设。"
	}
	descriptions := make([]string, 0, len(survivors))
	for _, h := range survivors {
		descriptions = append(descriptions, h.Description)
	}
	return fmt.Sprintf("尚未收敛，仍需人工复核的假设：%s。", strings.Join(descriptions, "；"))
}

// defaultPositive 返回正整数配置，否则使用默认值。
func defaultPositive(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
