package tot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ReasoningAgent 用 Eino ChatModel 复刻 AG2 ReasoningAgent 的 thinker/grader/executor/rewriter 角色。
type ReasoningAgent struct {
	name          string
	description   string
	scope         string
	systemMessage string
	treeMessage   string
	executorMsg   string
	model         model.BaseChatModel
	graderModel   model.BaseChatModel
	config        ReasonConfig
	root          *ThinkNode
	latsContext   string
	codeExecutor  CodeExecutor
	random        *rand.Rand
}

// NewReasoningAgent 创建 Tree-of-Thought 推理代理，并完成默认值、scope 和代码执行边界校验。
func NewReasoningAgent(_ context.Context, config Config) (*ReasoningAgent, error) {
	if config.Model == nil {
		return nil, errors.New("reasoning agent model is required")
	}
	reasonConfig, err := normalizeReasonConfig(config.ReasonConfig)
	if err != nil {
		return nil, err
	}
	if config.CodeExecutor != nil && !reasonConfig.InterimExecution {
		return nil, errors.New("code executor requires interim_execution=true")
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultReasoningName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = defaultReasoningDescription
	}
	systemMessage := strings.TrimSpace(config.SystemMessage)
	if systemMessage == "" {
		systemMessage = DefaultReasoningAgentMessage()
	}
	graderModel := config.GraderModel
	if graderModel == nil {
		graderModel = config.Model
	}
	treeMessage := DefaultTreeOfThoughtMessage()
	if config.CodeExecutor == nil {
		treeMessage = withoutPythonInstructions(treeMessage)
	}
	randomSource := config.Rand
	if randomSource == nil {
		randomSource = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	agent := &ReasoningAgent{
		name:          name,
		description:   description,
		scope:         strings.TrimSpace(config.Scope),
		systemMessage: systemMessage,
		treeMessage:   treeMessage,
		executorMsg:   DefaultExecutorMessage(),
		model:         config.Model,
		graderModel:   graderModel,
		config:        reasonConfig,
		codeExecutor:  config.CodeExecutor,
		random:        randomSource,
	}
	return agent, nil
}

// Name 返回 ADK 和本地调试都使用的 agent 名称。
func (a *ReasoningAgent) Name() string {
	if a == nil {
		return ""
	}
	return a.name
}

// Description 返回 ADK agent 描述。
func (a *ReasoningAgent) Description() string {
	if a == nil {
		return ""
	}
	return a.description
}

// Method 返回当前搜索策略。
func (a *ReasoningAgent) Method() Method {
	if a == nil {
		return ""
	}
	return a.config.Method
}

// Root 返回最近一次推理生成的树根节点。
func (a *ReasoningAgent) Root() *ThinkNode {
	if a == nil {
		return nil
	}
	return a.root
}

// GenerateText 用单条自然语言 prompt 运行推理代理。
func (a *ReasoningAgent) GenerateText(ctx context.Context, prompt string) (*Response, error) {
	return a.Generate(ctx, Request{Prompt: prompt})
}

// Generate 处理 prompt 或消息列表，并通过森林搜索生成最终答案。
func (a *ReasoningAgent) Generate(ctx context.Context, req Request) (*Response, error) {
	if a == nil {
		return nil, errors.New("reasoning agent is nil")
	}
	prompt, groundTruth, err := a.processPrompt(ctx, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" {
		return &Response{Answer: "TERMINATE", Method: a.config.Method}, nil
	}
	answers := make([]string, 0, a.config.ForestSize)
	var lastRoot *ThinkNode
	for i := 0; i < a.config.ForestSize; i++ {
		var answer string
		switch a.config.Method {
		case MethodBeamSearch, MethodDFS:
			answer, err = a.beamReply(ctx, prompt, groundTruth)
		case MethodMCTS, MethodLATS:
			answer, err = a.mctsReply(ctx, prompt, groundTruth)
		default:
			err = fmt.Errorf("invalid reasoning method: %s", a.config.Method)
		}
		if err != nil {
			return nil, err
		}
		answers = append(answers, answer)
		lastRoot = a.root
	}
	finalAnswer := answers[0]
	if len(answers) > 1 {
		finalAnswer, err = a.mergeForestAnswers(ctx, prompt, answers)
		if err != nil {
			return nil, err
		}
	}
	return &Response{
		Answer:        finalAnswer,
		Prompt:        prompt,
		GroundTruth:   groundTruth,
		Method:        a.config.Method,
		Root:          lastRoot,
		ForestAnswers: answers,
	}, nil
}

// processPrompt 对齐 AG2 _process_prompt：单轮直接使用，多轮先让 rewriter 压缩，GROUND_TRUTH 单独拆出。
func (a *ReasoningAgent) processPrompt(ctx context.Context, req Request) (string, string, error) {
	if strings.TrimSpace(req.Prompt) != "" {
		prompt, groundTruth := splitGroundTruth(req.Prompt)
		return prompt, groundTruth, nil
	}
	messages := req.Messages
	if len(messages) == 0 {
		return "", "", nil
	}
	copied := make([]*schema.Message, 0, len(messages))
	groundTruth := ""
	for _, message := range messages {
		if message == nil {
			continue
		}
		cloned := *message
		content, gt := splitGroundTruth(messageText(&cloned))
		if gt != "" && groundTruth == "" {
			groundTruth = gt
			cloned.Content = content
		}
		copied = append(copied, &cloned)
	}
	if len(copied) == 0 {
		return "", groundTruth, nil
	}
	if len(copied) == 1 {
		return strings.TrimSpace(messageText(copied[0])), groundTruth, nil
	}
	prompt, err := a.callModel(ctx, a.model, "", buildRewritePrompt(messagesDebugText(copied)))
	if err != nil {
		return "", "", fmt.Errorf("rewrite prompt: %w", err)
	}
	return strings.TrimSpace(prompt), groundTruth, nil
}

// beamReply 实现 beam_search 和 dfs，dfs 等价于 beam_size=1。
func (a *ReasoningAgent) beamReply(ctx context.Context, prompt string, groundTruth string) (string, error) {
	root := NewThinkNode(prompt, nil)
	a.root = root
	prevLeaves := []*ThinkNode{root}
	finalSet := map[*ThinkNode]struct{}{}
	var finalAnswers []*ThinkNode

	for len(prevLeaves) > 0 && len(finalAnswers) < a.config.BeamSize {
		var newLeaves []*ThinkNode
		var newLeavesPerBeam [][]*ThinkNode
		for _, node := range prevLeaves {
			if a.isTerminal(node) {
				if node.RatingDetails == "" {
					reward, err := a.rateNode(ctx, node, groundTruth, false)
					if err != nil {
						return "", err
					}
					node.Value = reward
				}
				if _, exists := finalSet[node]; !exists {
					finalSet[node] = struct{}{}
					finalAnswers = append(finalAnswers, node)
				}
				continue
			}
			expansionLeaves, err := a.expand(ctx, node)
			if err != nil {
				return "", err
			}
			newLeaves = append(newLeaves, expansionLeaves...)
			newLeavesPerBeam = append(newLeavesPerBeam, expansionLeaves)
		}
		prevLeaves = newLeaves
		if len(prevLeaves)+len(finalAnswers) > a.config.BeamSize {
			if len(finalAnswers) >= a.config.BeamSize {
				prevLeaves = nil
				break
			}
			if a.config.BatchGrading {
				for _, beamNodes := range newLeavesPerBeam {
					if err := a.rateBatchNodes(ctx, beamNodes, groundTruth); err != nil {
						return "", err
					}
				}
			} else {
				for _, node := range prevLeaves {
					reward, err := a.rateNode(ctx, node, groundTruth, false)
					if err != nil {
						return "", err
					}
					node.Value = reward
				}
			}
			sort.SliceStable(prevLeaves, func(i, j int) bool {
				return prevLeaves[i].Value > prevLeaves[j].Value
			})
			keep := a.config.BeamSize - len(finalAnswers)
			if keep < len(prevLeaves) {
				prevLeaves = prevLeaves[:keep]
			}
			if a.config.InterimExecution {
				for _, node := range prevLeaves {
					output, err := a.executeNode(ctx, node)
					if err != nil {
						return "", err
					}
					if output != "" {
						node.Output = &output
					}
				}
			}
		}
	}
	if len(finalAnswers) == 0 {
		return "", errors.New("no final answers found")
	}
	return a.completeFromFinalLeaves(ctx, prompt, finalAnswers)
}

// mctsReply 实现 MCTS 和 LATS；LATS 会把历史尝试和反思注入后续 expansion/rating。
func (a *ReasoningAgent) mctsReply(ctx context.Context, prompt string, groundTruth string) (string, error) {
	root := NewThinkNode(prompt, nil)
	a.root = root
	answerNodes := make([]*ThinkNode, 0, a.config.NSim)
	a.latsContext = "## Previous trajectories and reflections\n\n"

	for i := 0; i < a.config.NSim; i++ {
		node := root
		for !a.isTerminal(node) && len(node.Children) > 0 {
			node = a.bestUCTChild(node)
			if a.config.InterimExecution {
				output, err := a.executeNode(ctx, node)
				if err != nil {
					return "", err
				}
				if output != "" {
					node.Output = &output
				}
			}
		}
		for !a.isTerminal(node) {
			if len(node.Children) == 0 {
				if _, err := a.expand(ctx, node); err != nil {
					return "", err
				}
			}
			if len(node.Children) == 0 {
				node.Content += "\nTERMINATE"
				break
			}
			node = node.Children[a.random.Intn(len(node.Children))]
			if a.config.InterimExecution {
				output, err := a.executeNode(ctx, node)
				if err != nil {
					return "", err
				}
				if output != "" {
					node.Output = &output
				}
			}
		}
		answer, err := a.completeFromTrajectory(ctx, prompt, node)
		if err != nil {
			return "", err
		}
		answerNode := NewThinkNode(answer, node)
		reward, err := a.rateNode(ctx, answerNode, groundTruth, true)
		if err != nil {
			return "", err
		}
		answerNode.Value = reward
		answerNodes = append(answerNodes, answerNode)
		a.latsContext += fmt.Sprintf("### Previous Try:\n%s\n\nRating:%s\n\n", node.Trajectory(), answerNode.RatingDetails)
		node.Backpropagate(reward)
	}
	if len(answerNodes) == 0 {
		return "", errors.New("no mcts answer nodes found")
	}
	best := answerNodes[0]
	for _, node := range answerNodes[1:] {
		if node.Value > best.Value {
			best = node
		}
	}
	return best.Content, nil
}

// rateNode 调用 grader 评价单条轨迹或最终答案，并把评分详情写回节点。
func (a *ReasoningAgent) rateNode(ctx context.Context, node *ThinkNode, groundTruth string, isOutcome bool) (float64, error) {
	if node == nil {
		return 0, nil
	}
	if node.Value > 0 && node.RatingDetails != "" {
		return node.Value, nil
	}
	system := buildProcessRatingMessage(a.config.RatingScale, groundTruth)
	if isOutcome {
		system = buildOutcomeRatingMessage(a.config.RatingScale, groundTruth)
	}
	prompt := "Rate:\n" + node.Trajectory()
	if a.config.Method == MethodLATS {
		prompt = a.latsContext + "\n\n---\n\n" + prompt
	}
	rating, err := a.callModel(ctx, a.graderModel, a.addScope(system), prompt)
	if err != nil {
		return 0, fmt.Errorf("rate node: %w", err)
	}
	node.RatingDetails = rating
	return parseReward(rating, a.config.RatingScale), nil
}

// rateBatchNodes 调用 grader 一次评价同一父节点下的多个候选节点。
func (a *ReasoningAgent) rateBatchNodes(ctx context.Context, nodes []*ThinkNode, groundTruth string) error {
	if len(nodes) == 0 {
		return nil
	}
	parent := nodes[0].Parent
	if parent == nil {
		return errors.New("batch rating requires a non-root parent")
	}
	for _, node := range nodes {
		if node.Parent != parent {
			return errors.New("batch rating nodes must share the same parent")
		}
	}
	system := a.addScope(buildBatchRatingMessage(a.config.RatingScale, groundTruth))
	prompt := ""
	if a.config.Method == MethodLATS {
		prompt += a.latsContext + "\n\n---\n\n"
	}
	prompt += "Trajectory:\n" + parent.Trajectory() + "\n\n---\n\nOptions:\n"
	for i, node := range nodes {
		prompt += fmt.Sprintf("\nOption %d:\n%s", i+1, node.Content)
	}
	rating, err := a.callModel(ctx, a.graderModel, system, prompt)
	if err != nil {
		return fmt.Errorf("rate batch nodes: %w", err)
	}
	rewards, details, ok := parseBatchRewards(rating, len(nodes), a.config.RatingScale)
	if !ok {
		for _, node := range nodes {
			node.Value = 0
		}
		return nil
	}
	for i, node := range nodes {
		if node.Value > 0 && node.RatingDetails != "" {
			continue
		}
		node.RatingDetails = details[i]
		node.Value = rewards[i]
	}
	return nil
}

// executeNode 执行中间思考步骤；Python 代码交给可选 CodeExecutor，其余内容交给 executor 模型。
func (a *ReasoningAgent) executeNode(ctx context.Context, node *ThinkNode) (string, error) {
	if node == nil || node.Output != nil {
		if node != nil && node.Output != nil {
			return *node.Output, nil
		}
		return "", nil
	}
	if strings.Contains(node.Content, "TERMINATE") {
		return "", nil
	}
	if strings.Contains(node.Content, "```python") {
		if a.codeExecutor == nil {
			return "Python code execution is disabled. Follow a different approach.", nil
		}
		output, err := a.codeExecutor.ExecutePython(ctx, node.Content)
		if err != nil {
			return "", fmt.Errorf("execute python: %w", err)
		}
		return strings.TrimSpace(output), nil
	}
	prompt := ""
	if a.config.Method == MethodLATS {
		prompt += a.latsContext + "\n\n---\n\n"
	}
	prompt += "Trajectory:\n" + node.Trajectory() + "\nOutput:"
	output, err := a.callModel(ctx, a.model, a.addScope(a.executorMsg), prompt)
	if err != nil {
		return "", fmt.Errorf("execute node: %w", err)
	}
	if strings.Contains(output, "```python") {
		return "To execute Python code please provide the exact snippet in a fenced block like ```python ... ```.", nil
	}
	return output, nil
}

// expand 调用 thinker 为当前节点生成下一步选项，并把选项挂成子节点。
func (a *ReasoningAgent) expand(ctx context.Context, node *ThinkNode) ([]*ThinkNode, error) {
	if node == nil {
		return nil, nil
	}
	prompt := node.Trajectory() + "\n---\nHow should the thinking process continue?"
	if a.config.Method == MethodLATS {
		prompt = a.latsContext + "\n\n---\n\n" + prompt
	}
	reply, err := a.callModel(ctx, a.model, a.addScope(a.treeMessage), prompt)
	if err != nil {
		return nil, fmt.Errorf("expand node: %w", err)
	}
	if reflection := parseReflection(reply); reflection != "" {
		node.Reflection += reflection
	}
	options := parseOptions(reply)
	children := make([]*ThinkNode, 0, len(options))
	for _, option := range options {
		children = append(children, NewThinkNode(strings.TrimSpace(option), node))
	}
	return children, nil
}

// completeFromFinalLeaves 根据 pool/best 策略把 beam search 的最终叶子合成完整回答。
func (a *ReasoningAgent) completeFromFinalLeaves(ctx context.Context, prompt string, leaves []*ThinkNode) (string, error) {
	switch a.config.AnswerApproach {
	case AnswerApproachBest:
		best := leaves[0]
		for _, leaf := range leaves[1:] {
			if leaf.Value > best.Value {
				best = leaf
			}
		}
		return a.completeFromTrajectory(ctx, prompt, best)
	case AnswerApproachPool:
		var thoughts []string
		for i, node := range leaves {
			thoughts = append(thoughts, fmt.Sprintf("--- Possibility %d ---\n%s\n", i+1, node.Trajectory()))
		}
		message := strings.Join([]string{
			"给定一组思考过程，请为用户问题生成一个完整答复。",
			"Question:",
			prompt,
			"",
			"Thinking processes:",
			strings.Join(thoughts, "\n\n"),
			"",
			"Final Answer:",
		}, "\n")
		return a.callModel(ctx, a.model, a.addScope(a.systemMessage), message)
	default:
		return "", fmt.Errorf("invalid answer approach: %s", a.config.AnswerApproach)
	}
}

// completeFromTrajectory 根据单条思考轨迹生成最终回答。
func (a *ReasoningAgent) completeFromTrajectory(ctx context.Context, prompt string, node *ThinkNode) (string, error) {
	message := strings.Join([]string{
		"给定一条思考过程，请为用户问题生成一个完整答复。",
		"Question:",
		prompt,
		"",
		"Thinking process:",
		node.Trajectory(),
		"",
		"Final Answer:",
	}, "\n")
	return a.callModel(ctx, a.model, a.addScope(a.systemMessage), message)
}

// mergeForestAnswers 把多个独立推理树的答案合并成单个最终答复。
func (a *ReasoningAgent) mergeForestAnswers(ctx context.Context, prompt string, answers []string) (string, error) {
	joined := "-" + strings.Join(answers, "\n-")
	message := strings.Join([]string{
		"给定多个不同答案，请为用户问题生成一个完整答复。",
		"Question:",
		prompt,
		"",
		"Answers:",
		joined,
		"",
		"Final Answer:",
	}, "\n")
	return a.callModel(ctx, a.model, a.addScope(a.systemMessage), message)
}

// bestUCTChild 按 UCT 权重选择 MCTS 下一步节点。
func (a *ReasoningAgent) bestUCTChild(node *ThinkNode) *ThinkNode {
	best := node.Children[0]
	bestWeight := math.Inf(-1)
	parentVisits := math.Max(float64(node.Visits), 1)
	for _, child := range node.Children {
		exploitation := child.Value / (float64(child.Visits) + epsilon)
		exploration := a.config.ExplorationConstant * math.Sqrt(2*math.Log(parentVisits+epsilon)/(float64(child.Visits)+epsilon))
		weight := exploitation + exploration
		if weight > bestWeight {
			bestWeight = weight
			best = child
		}
	}
	return best
}

// isTerminal 判断节点是否达到最大深度或显式包含 TERMINATE。
func (a *ReasoningAgent) isTerminal(node *ThinkNode) bool {
	if node == nil {
		return true
	}
	return node.Depth >= a.config.MaxDepth || strings.Contains(node.Content, "TERMINATE")
}

// callModel 统一封装 Eino BaseChatModel.Generate，保持四个内部角色的调用形态一致。
func (a *ReasoningAgent) callModel(ctx context.Context, chatModel model.BaseChatModel, systemPrompt string, userPrompt string) (string, error) {
	if chatModel == nil {
		return "", errors.New("chat model is required")
	}
	messages := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, schema.SystemMessage(systemPrompt))
	}
	messages = append(messages, schema.UserMessage(userPrompt))
	resp, err := chatModel.Generate(ctx, messages)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return strings.TrimSpace(resp.Content), nil
}

// addScope 把任务 scope 加到内部角色系统提示词前面。
func (a *ReasoningAgent) addScope(systemPrompt string) string {
	if a == nil || strings.TrimSpace(a.scope) == "" {
		return systemPrompt
	}
	return "Task Scope: " + a.scope + "\n\n" + systemPrompt
}

// normalizeReasonConfig 复刻 AG2 默认参数，并校验策略枚举。
func normalizeReasonConfig(config ReasonConfig) (ReasonConfig, error) {
	if config.Method == "" {
		config.Method = MethodBeamSearch
	}
	switch config.Method {
	case MethodBeamSearch, MethodMCTS, MethodLATS, MethodDFS:
	default:
		return ReasonConfig{}, fmt.Errorf("invalid reasoning method: %s", config.Method)
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = 4
	}
	if config.BeamSize <= 0 {
		config.BeamSize = 3
	}
	if config.Method == MethodDFS {
		config.BeamSize = 1
	}
	if config.AnswerApproach == "" {
		config.AnswerApproach = AnswerApproachPool
	}
	if config.AnswerApproach != AnswerApproachPool && config.AnswerApproach != AnswerApproachBest {
		return ReasonConfig{}, fmt.Errorf("invalid answer approach: %s", config.AnswerApproach)
	}
	if config.NSim <= 0 {
		config.NSim = 3
	}
	if config.ExplorationConstant <= 0 {
		config.ExplorationConstant = 1.41
	}
	if config.ForestSize <= 0 {
		config.ForestSize = 1
	}
	if config.RatingScale <= 1 {
		config.RatingScale = 10
	}
	return config, nil
}
