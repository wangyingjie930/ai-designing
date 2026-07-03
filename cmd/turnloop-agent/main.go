package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath           = ".env"
	defaultScenarioName      = "拟真人销售 TurnLoop 抢占合并"
	defaultFirstMessage      = "客户说预算只有三万，先别催单，帮我像真人销售一样先稳住关系。"
	defaultSecondMessage     = "补充：客户其实最关心交付周期，明天下午要给老板汇报，请把前面的预算信息也一起考虑。"
	defaultSecondDelay       = 150 * time.Millisecond
	defaultPreemptTimeout    = 300 * time.Millisecond
	defaultCompletionLimit   = 20 * time.Second
	envFirstMessage          = "TURNLOOP_FIRST_MESSAGE"
	envSecondMessage         = "TURNLOOP_SECOND_MESSAGE"
	envSecondDelayMS         = "TURNLOOP_SECOND_DELAY_MS"
	envPreemptTimeoutMS      = "TURNLOOP_PREEMPT_TIMEOUT_MS"
	envCompletionLimitMS     = "TURNLOOP_COMPLETION_LIMIT_MS"
	envTurnLoopPrepareOnly   = "TURNLOOP_PREPARE_ONLY"
	salesAgentMaxIterations  = 4
	minPositiveDurationLabel = "must be greater than zero"
)

// runConfig 保存 CLI 和 .env 归一化后的运行配置，cmd 层只做编排不承载销售策略。
type runConfig struct {
	FirstMessage    string
	SecondMessage   string
	SecondDelay     time.Duration
	PreemptTimeout  time.Duration
	CompletionLimit time.Duration
	PrepareOnly     bool
	EnvPath         string
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// chatModelFactory 允许命令测试替换真实模型，避免普通 go test 访问网络。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

// runOutput 是命令执行后的稳定摘要，trace 和测试都只看这些低敏字段。
type runOutput struct {
	Mode           string `json:"mode"`
	Scenario       string `json:"scenario"`
	InputMessages  int    `json:"input_messages"`
	MergedMessages int    `json:"merged_messages"`
	Preemptions    int    `json:"preemptions"`
	DiscardedTurns int    `json:"discarded_turns"`
	AnswerChars    int    `json:"answer_chars"`
}

// salesConversationConfig 是 TurnLoop 销售对话的运行时控制参数。
type salesConversationConfig struct {
	FirstMessage    string
	SecondMessage   string
	SecondDelay     time.Duration
	PreemptTimeout  time.Duration
	CompletionLimit time.Duration
}

// salesTurnLoopSessionConfig 控制外层调用者触发抢占时的等待策略。
type salesTurnLoopSessionConfig struct {
	PreemptTimeout time.Duration
}

// salesConversationResult 汇总 TurnLoop 对话完成后的关键状态。
type salesConversationResult struct {
	Answer         string
	MergedMessages int
	Preemptions    int
	DiscardedTurns int
}

// salesChatItem 是 TurnLoop buffer 中的最小消息单元；Sequence 用于合并时稳定去重和排序。
type salesChatItem struct {
	Sequence int
	Content  string
}

// salesTurnResult 是 OnAgentEvents 收到最终有效回答后的内部结果。
type salesTurnResult struct {
	answer   string
	consumed []salesChatItem
}

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// main 运行拟真人销售 TurnLoop demo。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装配置、trace、模型和 TurnLoop 销售 Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	if config.PrepareOnly {
		printPrepareSummary(config)
		return runOutput{
			Mode:          "prepare-only",
			Scenario:      defaultScenarioName,
			InputMessages: 2,
		}, nil
	}

	modelConfig, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	cozeLoopConfig, shutdownCozeLoop, err := cozeloopobs.InstallFromEnv(ctx)
	if err != nil {
		return runOutput{}, fmt.Errorf("init cozeloop: %w", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()

	traceInput := runAgentTraceInput{
		FirstMessageChars:  len([]rune(config.FirstMessage)),
		SecondMessageChars: len([]rune(config.SecondMessage)),
		PreemptTimeoutMS:   int(config.PreemptTimeout / time.Millisecond),
	}
	return withRunAgentTrace(ctx, traceInput, func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		agent, err := newSalesChatAgent(traceCtx, chatModel)
		if err != nil {
			return runOutput{}, err
		}
		result, err := runSalesConversation(traceCtx, agent, salesConversationConfig{
			FirstMessage:    config.FirstMessage,
			SecondMessage:   config.SecondMessage,
			SecondDelay:     config.SecondDelay,
			PreemptTimeout:  config.PreemptTimeout,
			CompletionLimit: config.CompletionLimit,
		})
		if err != nil {
			return runOutput{}, err
		}

		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		fmt.Printf("scenario=%s preemptions=%d merged_messages=%d discarded_turns=%d\n",
			defaultScenarioName, result.Preemptions, result.MergedMessages, result.DiscardedTurns)
		fmt.Printf("\n=== TurnLoop Sales Agent 回复 ===\n%s\n", result.Answer)

		return runOutput{
			Mode:           "agent",
			Scenario:       defaultScenarioName,
			InputMessages:  2,
			MergedMessages: result.MergedMessages,
			Preemptions:    result.Preemptions,
			DiscardedTurns: result.DiscardedTurns,
			AnswerChars:    len([]rune(result.Answer)),
		}, nil
	})
}

// parseRunConfig 读取命令参数；默认两句输入让 IDE/main_test 直接点击即可复现抢占。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("turnloop-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		FirstMessage:    defaultFirstMessage,
		SecondMessage:   defaultSecondMessage,
		SecondDelay:     defaultSecondDelay,
		PreemptTimeout:  defaultPreemptTimeout,
		CompletionLimit: defaultCompletionLimit,
		EnvPath:         defaultEnvPath,
	}
	fs.StringVar(&config.EnvPath, "env-file", defaultEnvPath, "dotenv file used as the source of truth")
	fs.StringVar(&config.FirstMessage, "first-message", defaultFirstMessage, "first user message")
	fs.StringVar(&config.SecondMessage, "second-message", defaultSecondMessage, "second user message that preempts the first turn")
	fs.DurationVar(&config.SecondDelay, "second-delay", defaultSecondDelay, "delay before pushing second message")
	fs.DurationVar(&config.PreemptTimeout, "preempt-timeout", defaultPreemptTimeout, "safe-point preempt timeout before immediate escalation")
	fs.DurationVar(&config.CompletionLimit, "completion-limit", defaultCompletionLimit, "max time to wait for the merged turn")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print TurnLoop sales summary without calling model")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}

	dotenv, err := loadDotEnv(e2etest.ResolvePath(config.EnvPath))
	if err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	applyDotEnvRunConfig(&config, dotenv)
	if err := validateRunConfig(config); err != nil {
		return runConfig{}, err
	}
	return config, nil
}

// applyDotEnvRunConfig 让 .env 能覆盖命令参数，方便本地 demo 一切以文件为准。
func applyDotEnvRunConfig(config *runConfig, values map[string]string) {
	if config == nil || len(values) == 0 {
		return
	}
	if first := strings.TrimSpace(values[envFirstMessage]); first != "" {
		config.FirstMessage = first
	}
	if second := strings.TrimSpace(values[envSecondMessage]); second != "" {
		config.SecondMessage = second
	}
	if delay, ok := parseMillisDuration(values[envSecondDelayMS]); ok {
		config.SecondDelay = delay
	}
	if timeout, ok := parseMillisDuration(values[envPreemptTimeoutMS]); ok {
		config.PreemptTimeout = timeout
	}
	if limit, ok := parseMillisDuration(values[envCompletionLimitMS]); ok {
		config.CompletionLimit = limit
	}
	if prepareOnly, ok := parseBoolEnv(values[envTurnLoopPrepareOnly]); ok {
		config.PrepareOnly = prepareOnly
	}
}

// validateRunConfig 在进入模型前检查必要输入，避免 TurnLoop 后台 goroutine 才暴露配置错误。
func validateRunConfig(config runConfig) error {
	if strings.TrimSpace(config.FirstMessage) == "" {
		return errors.New("first message is required")
	}
	if strings.TrimSpace(config.SecondMessage) == "" {
		return errors.New("second message is required")
	}
	if config.SecondDelay <= 0 {
		return fmt.Errorf("second delay %s", minPositiveDurationLabel)
	}
	if config.PreemptTimeout <= 0 {
		return fmt.Errorf("preempt timeout %s", minPositiveDurationLabel)
	}
	if config.CompletionLimit <= 0 {
		return fmt.Errorf("completion limit %s", minPositiveDurationLabel)
	}
	return nil
}

// newSalesChatAgent 创建拟真人销售 Agent；TurnLoop 只负责编排，销售人格放在 Agent instruction 中。
func newSalesChatAgent(ctx context.Context, chatModel model.BaseChatModel) (*adk.ChatModelAgent, error) {
	if chatModel == nil {
		return nil, errors.New("sales agent model is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "human_like_sales_agent",
		Description:   "A human-like sales agent that adapts to new customer information mid-conversation.",
		Instruction:   defaultSalesInstruction(),
		Model:         chatModel,
		MaxIterations: salesAgentMaxIterations,
	})
}

// runSalesConversation 启动 TurnLoop，并模拟第二句在第一轮生成过程中进入队列。
func runSalesConversation(ctx context.Context, agent adk.TypedAgent[*schema.Message], config salesConversationConfig) (salesConversationResult, error) {
	if agent == nil {
		return salesConversationResult{}, errors.New("sales agent is required")
	}
	if config.CompletionLimit <= 0 {
		config.CompletionLimit = defaultCompletionLimit
	}
	if config.SecondDelay <= 0 {
		config.SecondDelay = defaultSecondDelay
	}
	if config.PreemptTimeout <= 0 {
		config.PreemptTimeout = defaultPreemptTimeout
	}

	session := newSalesTurnLoopSession(ctx, agent, salesTurnLoopSessionConfig{PreemptTimeout: config.PreemptTimeout})
	defer session.Stop()
	if _, err := session.PushUserMessage(config.FirstMessage); err != nil {
		return salesConversationResult{}, err
	}
	timer := time.NewTimer(config.SecondDelay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return salesConversationResult{}, ctx.Err()
	case <-timer.C:
	}

	ack, err := session.PushUserMessage(config.SecondMessage)
	if err != nil {
		return salesConversationResult{}, err
	}
	if err := waitForPreemptAck(ctx, ack, config.CompletionLimit); err != nil {
		return salesConversationResult{}, err
	}
	return session.WaitAnswer(ctx, config.CompletionLimit)
}

// salesTurnLoopSession 是外层可控的 TurnLoop 会话；调用方可以任意时机 Push 新用户输入。
type salesTurnLoopSession struct {
	mu             sync.Mutex
	loop           *adk.TurnLoop[salesChatItem, *schema.Message]
	state          *salesTurnLoopState
	preemptTimeout time.Duration
	stopped        bool
}

// newSalesTurnLoopSession 创建并启动销售 TurnLoop，会话本身不决定何时发第二句或第 N 句。
func newSalesTurnLoopSession(ctx context.Context, agent adk.TypedAgent[*schema.Message], config salesTurnLoopSessionConfig) *salesTurnLoopSession {
	if config.PreemptTimeout <= 0 {
		config.PreemptTimeout = defaultPreemptTimeout
	}
	state := newSalesTurnLoopState()
	loop := newSalesTurnLoop(agent, state)
	loop.Run(ctx)
	return &salesTurnLoopSession{
		loop:           loop,
		state:          state,
		preemptTimeout: config.PreemptTimeout,
	}
}

// PushUserMessage 由外层调用；如果当前 turn 仍在运行，本次 Push 会请求抢占并把旧输入带到下一轮。
func (s *salesTurnLoopSession) PushUserMessage(content string) (<-chan struct{}, error) {
	if s == nil || s.loop == nil || s.state == nil {
		return nil, errors.New("sales turnloop session is not initialized")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("sales user message is required")
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, errors.New("sales turnloop session has stopped")
	}
	item := s.state.nextItem(content)
	loop := s.loop
	timeout := s.preemptTimeout
	s.mu.Unlock()

	ok, ack := loop.Push(item, preemptActiveTurn(timeout))
	if !ok {
		return nil, errors.New("push sales user message failed")
	}
	return ack, nil
}

// WaitAnswer 等待下一条未被作废的销售回复；外层可以在等待前继续 Push 来覆盖旧回复。
func (s *salesTurnLoopSession) WaitAnswer(ctx context.Context, limit time.Duration) (salesConversationResult, error) {
	if s == nil || s.state == nil {
		return salesConversationResult{}, errors.New("sales turnloop session is not initialized")
	}
	if limit <= 0 {
		limit = defaultCompletionLimit
	}
	select {
	case result := <-s.state.final:
		return salesConversationResult{
			Answer:         result.answer,
			MergedMessages: len(uniqueSalesItems(result.consumed)),
			Preemptions:    s.state.preemptionCount(),
			DiscardedTurns: s.state.discardedTurnCount(),
		}, nil
	case <-ctx.Done():
		return salesConversationResult{}, ctx.Err()
	case <-time.After(limit):
		return salesConversationResult{}, errors.New("timed out waiting for sales answer")
	}
}

// Stop 结束会话；外层不再需要回复时应调用，避免后台 TurnLoop 悬挂。
func (s *salesTurnLoopSession) Stop() {
	if s == nil || s.loop == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	loop := s.loop
	s.mu.Unlock()
	loop.Stop(adk.WithImmediate(), adk.WithSkipCheckpoint())
	loop.Wait()
}

// waitForPreemptAck 等待本次 Push 的抢占请求被 TurnLoop 接收；空闲 Push 没有 ack。
func waitForPreemptAck(ctx context.Context, ack <-chan struct{}, limit time.Duration) error {
	if ack == nil {
		return nil
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(limit):
		return errors.New("timed out waiting for preempt ack")
	}
}

// preemptActiveTurn 只在当前有 active turn 时抢占；空闲时普通入队，避免误伤下一轮。
func preemptActiveTurn(timeout time.Duration) adk.PushOption[salesChatItem, *schema.Message] {
	return adk.WithPushStrategy(func(ctx context.Context, tc *adk.TurnContext[salesChatItem, *schema.Message]) []adk.PushOption[salesChatItem, *schema.Message] {
		if tc == nil {
			return nil
		}
		return []adk.PushOption[salesChatItem, *schema.Message]{
			adk.WithPreemptTimeout[salesChatItem, *schema.Message](adk.AnySafePoint, timeout),
		}
	})
}

// newSalesTurnLoop 把 TurnLoop 的 GenInput/PrepareAgent/OnAgentEvents 三个业务回调串起来。
func newSalesTurnLoop(agent adk.TypedAgent[*schema.Message], state *salesTurnLoopState) *adk.TurnLoop[salesChatItem, *schema.Message] {
	return adk.NewTurnLoop[salesChatItem, *schema.Message](adk.TurnLoopConfig[salesChatItem, *schema.Message]{
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[salesChatItem, *schema.Message], items []salesChatItem) (*adk.GenInputResult[salesChatItem, *schema.Message], error) {
			batch := state.prepareBatch(items)
			if len(batch) == 0 {
				return nil, errors.New("turnloop received empty sales message batch")
			}
			return &adk.GenInputResult[salesChatItem, *schema.Message]{
				Input: &adk.AgentInput{
					Messages: []adk.Message{
						schema.UserMessage(buildMergedSalesMessage(batch)),
					},
					EnableStreaming: true,
				},
				Consumed:  batch,
				Remaining: nil,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, loop *adk.TurnLoop[salesChatItem, *schema.Message], consumed []salesChatItem) (adk.TypedAgent[*schema.Message], error) {
			return agent, nil
		},
		OnAgentEvents: state.handleAgentEvents,
	})
}

// salesTurnLoopState 保存 cmd 侧抢占状态；第一轮作废时，已消费输入会放回 interrupted 队列。
type salesTurnLoopState struct {
	mu             sync.Mutex
	latestSequence int
	interrupted    []salesChatItem
	preemptions    int
	discardedTurns int
	final          chan salesTurnResult
}

// newSalesTurnLoopState 创建一次销售对话的 TurnLoop 状态容器。
func newSalesTurnLoopState() *salesTurnLoopState {
	return &salesTurnLoopState{
		final: make(chan salesTurnResult, 1),
	}
}

// nextItem 分配外部输入序号，并在新输入到来时作废尚未被外层读取的旧答案。
func (s *salesTurnLoopState) nextItem(content string) salesChatItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latestSequence++
	s.discardPendingFinalLocked()
	return salesChatItem{Sequence: s.latestSequence, Content: content}
}

// discardPendingFinalLocked 把尚未交付给外层的旧答案作废，并保留其用户输入用于下一轮合并。
func (s *salesTurnLoopState) discardPendingFinalLocked() {
	select {
	case pending := <-s.final:
		s.discardedTurns++
		s.interrupted = uniqueSalesItems(append(s.interrupted, pending.consumed...))
	default:
	}
}

// prepareBatch 合并被抢占的旧输入和本轮新输入，保证下一轮模型看到完整用户意图。
func (s *salesTurnLoopState) prepareBatch(items []salesChatItem) []salesChatItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := append([]salesChatItem{}, s.interrupted...)
	s.interrupted = nil
	batch = append(batch, items...)
	return uniqueSalesItems(batch)
}

// handleAgentEvents 消费 ADK 事件；抢占时丢弃当前输出，只保留当前输入给下一轮。
func (s *salesTurnLoopState) handleAgentEvents(ctx context.Context, tc *adk.TurnContext[salesChatItem, *schema.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
	var answer string
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			if turnWasPreempted(tc) {
				s.markPreempted(tc.Consumed)
				return nil
			}
			return event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			if turnWasPreempted(tc) {
				s.markPreempted(tc.Consumed)
				return nil
			}
			return err
		}
		if message != nil && message.Role == schema.Assistant && strings.TrimSpace(message.Content) != "" {
			answer = message.Content
		}
	}
	if turnWasPreempted(tc) {
		s.markPreempted(tc.Consumed)
		return nil
	}
	s.markCompleted(tc.Consumed, answer)
	return nil
}

// markPreempted 记录被作废 turn 的输入；本轮 assistant 输出不会进入 final。
func (s *salesTurnLoopState) markPreempted(consumed []salesChatItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preemptions++
	s.discardedTurns++
	s.interrupted = uniqueSalesItems(append(s.interrupted, consumed...))
}

// markCompleted 只在最终有效 turn 完成时输出结果；若还没覆盖两句输入，继续等后续 turn。
func (s *salesTurnLoopState) markCompleted(consumed []salesChatItem, answer string) {
	consumed = uniqueSalesItems(consumed)
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxSalesSequence(consumed) < s.latestSequence {
		// 新输入已到来但当前 turn 未覆盖它，旧回复作废并把已消费输入带入下一轮。
		s.discardedTurns++
		s.interrupted = uniqueSalesItems(append(s.interrupted, consumed...))
		return
	}
	select {
	case s.final <- salesTurnResult{answer: answer, consumed: consumed}:
	default:
	}
}

// maxSalesSequence 返回一组输入中最新的外部输入序号。
func maxSalesSequence(items []salesChatItem) int {
	maxSeq := 0
	for _, item := range items {
		if item.Sequence > maxSeq {
			maxSeq = item.Sequence
		}
	}
	return maxSeq
}

// preemptionCount 返回本次会话的抢占次数。
func (s *salesTurnLoopState) preemptionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preemptions
}

// discardedTurnCount 返回被丢弃的旧 turn 数量。
func (s *salesTurnLoopState) discardedTurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discardedTurns
}

// turnWasPreempted 判断当前 turn 是否已经被 Push(...WithPreemptTimeout) 抢占。
func turnWasPreempted(tc *adk.TurnContext[salesChatItem, *schema.Message]) bool {
	if tc == nil {
		return false
	}
	select {
	case <-tc.Preempted:
		return true
	default:
		return false
	}
}

// uniqueSalesItems 按 Sequence 去重排序，避免多次抢占时重复合并同一句用户输入。
func uniqueSalesItems(items []salesChatItem) []salesChatItem {
	bySeq := make(map[int]salesChatItem, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		bySeq[item.Sequence] = salesChatItem{
			Sequence: item.Sequence,
			Content:  strings.TrimSpace(item.Content),
		}
	}
	seqs := make([]int, 0, len(bySeq))
	for seq := range bySeq {
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)
	merged := make([]salesChatItem, 0, len(seqs))
	for _, seq := range seqs {
		merged = append(merged, bySeq[seq])
	}
	return merged
}

// buildMergedSalesMessage 把多次用户输入压成一条模型可读消息，明确旧回答已作废。
func buildMergedSalesMessage(items []salesChatItem) string {
	var b strings.Builder
	b.WriteString("你正在和同一位潜在客户对话。客户刚刚补充了新信息，旧一轮回复如果尚未完成请作废，以这里合并后的完整上下文重新回答。\n\n")
	b.WriteString("## 用户输入时间线\n")
	for _, item := range uniqueSalesItems(items) {
		fmt.Fprintf(&b, "%d. %s\n", item.Sequence, item.Content)
	}
	b.WriteString("\n请像真人销售一样回答：先承接客户顾虑，再给下一步推进话术，不要暴露系统实现。")
	return b.String()
}

// defaultSalesInstruction 定义拟真人销售 Agent 的边界和风格。
func defaultSalesInstruction() string {
	return strings.TrimSpace(`
你是一名拟真人销售 Agent，服务于 B2B 线索跟进场景。
要求：
1. 先像真人销售一样承接客户情绪、预算、时间和决策压力，不要机械总结。
2. 新信息到来时，优先以最新上下文重新组织回复，不要延续已经作废的旧话术。
3. 不要强行逼单；用低压、具体、可执行的下一步推进客户。
4. 输出要自然、简洁、有温度，避免说“作为 AI”或暴露内部流程。`)
}

// printPrepareSummary 输出可人工检查的 TurnLoop demo 摘要，不触发模型或外部服务。
func printPrepareSummary(config runConfig) {
	fmt.Printf("scenario=%s\n", defaultScenarioName)
	fmt.Printf("mode=prepare-only input_messages=2\n")
	fmt.Printf("first_message_chars=%d second_message_chars=%d\n", len([]rune(config.FirstMessage)), len([]rune(config.SecondMessage)))
	fmt.Printf("second_delay=%s preempt_timeout=%s completion_limit=%s\n", config.SecondDelay, config.PreemptTimeout, config.CompletionLimit)
}

// loadModelConfig 从已加载的 .env 环境读取模型配置。
func loadModelConfig() (modelConfig, error) {
	config := modelConfig{
		APIKey:  strings.TrimSpace(firstEnv("OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY")),
		Model:   strings.TrimSpace(firstEnv("LLM_MODEL", "OPENAI_MODEL")),
		BaseURL: normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL", "BASE_URL")),
	}
	if config.APIKey == "" {
		return modelConfig{}, errors.New("OPENAI_API_KEY is required")
	}
	if config.Model == "" {
		return modelConfig{}, errors.New("LLM_MODEL or OPENAI_MODEL is required")
	}
	return config, nil
}

// loadDotEnv 读取简单 KEY=VALUE 格式；文件值会覆盖当前进程环境变量。
func loadDotEnv(path string) (map[string]string, error) {
	values := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return values, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return values, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			values[key] = value
			if err := os.Setenv(key, value); err != nil {
				return values, err
			}
		}
	}
	return values, scanner.Err()
}

// parseBoolEnv 解析 .env 中的布尔开关，无法识别时保持调用方已有配置。
func parseBoolEnv(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true, true
	case "0", "false", "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}

// parseMillisDuration 解析毫秒级 .env 配置，非法值会被忽略并保留默认值。
func parseMillisDuration(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

// firstEnv 返回第一项非空环境变量。
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// normalizeOpenAIBaseURL 兼容 go-openai 期望的 /v1 base url。
func normalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// displayBaseURL 让命令输出中的空 base url 也容易读。
func displayBaseURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return "default"
	}
	return baseURL
}

// redactKey 在命令输出中只展示 API key 的存在性。
func redactKey(key string) string {
	if strings.TrimSpace(key) == "" {
		return "empty"
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// enabledText 把布尔开关转为稳定命令输出。
func enabledText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
