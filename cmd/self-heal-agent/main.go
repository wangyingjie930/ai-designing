package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/reflection/selfheal"
)

const defaultEnvPath = ".env"

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，避免测试和 trace 依赖大段模型正文。
type runOutput struct {
	Scenario              string `json:"scenario"`
	Status                string `json:"status"`
	Iterations            int    `json:"iterations"`
	Commits               int    `json:"commits"`
	AnswerChars           int    `json:"answer_chars"`
	UsedADKCustomizedData bool   `json:"used_adk_customized_data"`
	PolicyVersion         int    `json:"policy_version"`
	RefundWindowHours     int    `json:"refund_window_hours"`
	EscalationEnabled     bool   `json:"escalation_enabled"`
	CompensationLimit     int    `json:"compensation_limit"`
}

// policySummary 是客服 SOP 策略的最小可观察状态，用于证明非 coding 配置真的被修复。
type policySummary struct {
	PolicyVersion      int  `json:"policy_version"`
	RefundWindowHours  int  `json:"refund_window_hours"`
	EscalationEnabled  bool `json:"escalation_enabled"`
	CompensationLimit  int  `json:"compensation_limit"`
	AuditRequired      bool `json:"audit_required"`
	KnownRollbackCount int  `json:"known_rollback_count"`
}

// supportPolicy 表示客服补偿 SOP 的业务配置状态，示例里用内存模拟配置中心版本。
type supportPolicy struct {
	Version           int
	RefundWindowHours int
	EscalationEnabled bool
	CompensationLimit int
	AuditRequired     bool
	AuditTrail        []string
}

// supportSOPScenario 封装非 coding 场景的 apply/verify/rollback 副作用边界。
type supportSOPScenario struct {
	policy    supportPolicy
	snapshots map[string]supportPolicy
}

// chatModelFactory 抽出模型创建点，测试时替换成 fake model。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// main 运行一个非 coding 的客服补偿 SOP 自愈 Agent。
func main() {
	if _, err := runAgent(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 使用内置客服 SOP 失败信号跑 Eino ADK Runner，适合 GoLand 里点 main_test 验证。
func runAgent(ctx context.Context) (runOutput, error) {
	initialFailure := defaultSupportSOPFailure()
	if err := loadDotEnv(defaultEnvPath); err != nil {
		return runOutput{}, fmt.Errorf("load env: %w", err)
	}
	modelConfig, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	chatModel, err := newChatModel(ctx, modelConfig)
	if err != nil {
		return runOutput{}, fmt.Errorf("init chat model: %w", err)
	}
	maxIterations := parsePositiveInt(firstEnv("SELF_HEAL_MAX_ITERATIONS"))
	if maxIterations <= 0 {
		maxIterations = 3
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
		Scenario:        "support_sop_self_heal",
		FailureKind:     initialFailure.Kind,
		AffectedFiles:   len(initialFailure.AffectedFiles),
		MaxIterations:   maxIterations,
		FailureSeverity: initialFailure.Severity,
	}
	return withRunAgentTrace(ctx, traceInput, func(traceCtx context.Context) (runOutput, error) {
		scenario := newSupportSOPScenario()
		diagnoser := selfheal.ModelDiagnoser{Model: chatModel, MaxTokens: 1024}
		fixGenerator := selfheal.ModelFixGenerator{Model: chatModel, MaxTokens: 1024}
		critic := selfheal.ModelCritic{Model: chatModel, MaxTokens: 512}
		runner, _, err := selfheal.NewRunner(traceCtx, selfheal.Config{
			Name:          "support_sop_self_heal_agent",
			Description:   "Self-heal customer support SOP gaps with model diagnosis and controlled business configuration tools.",
			MaxIterations: maxIterations,
			Diagnoser:     diagnoser.Diagnose,
			FixGenerator:  fixGenerator.GenerateFix,
			Critic:        critic.Review,
			Applier:       scenario.Apply,
			Verifier:      scenario.Verify,
			Rollback:      scenario.Rollback,
		})
		if err != nil {
			return runOutput{}, err
		}
		response, usedCustomized, err := queryRunner(traceCtx, runner, initialFailure)
		if err != nil {
			return runOutput{}, err
		}
		output := summarizeResponse(response, usedCustomized, scenario.PolicySummary())
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		return output, nil
	})
}

// defaultSupportSOPFailure 返回默认非 coding 痛点：客服 SOP 缺口导致一线反复升级。
func defaultSupportSOPFailure() selfheal.FailureSignal {
	return selfheal.FailureSignal{
		Kind:     "support_sop_gap",
		Severity: 2,
		ErrorText: strings.Join([]string{
			"客服补偿 SOP 缺少升级边界和补偿窗口。",
			"一线客服遇到航班延误、入住取消、客户二次投诉时，只能反复询问主管。",
			"痛点：响应慢、口径不一致、主管被重复打断，且没有稳定配置可以回放验证。",
		}, "\n"),
		AffectedFiles: []string{"support/sop/compensation_policy"},
	}
}

// newSupportSOPScenario 创建一份有缺口的客服补偿 SOP 初始配置。
func newSupportSOPScenario() *supportSOPScenario {
	return &supportSOPScenario{
		policy: supportPolicy{
			Version:           1,
			RefundWindowHours: 0,
			EscalationEnabled: false,
			CompensationLimit: 0,
			AuditRequired:     true,
			AuditTrail:        []string{"v1: 缺少补偿窗口和升级边界"},
		},
		snapshots: map[string]supportPolicy{},
	}
}

// Apply 把模型生成的业务补丁应用到内存 SOP 配置，并返回可回滚的版本 ID。
func (s *supportSOPScenario) Apply(_ context.Context, proposal selfheal.FixProposal) (string, error) {
	if s == nil {
		return "", fmt.Errorf("support SOP scenario is nil")
	}
	diff := strings.ToLower(strings.TrimSpace(proposal.FixDiff))
	if diff == "" {
		return "", fmt.Errorf("support SOP fix diff is empty")
	}
	before := clonePolicy(s.policy)
	targetVersion := before.Version + 1
	commitID := fmt.Sprintf("support-sop-v%d", targetVersion)
	s.snapshots[commitID] = before

	recognized := false
	if containsAny(diff, []string{"refund_window_hours=24", "refund_window_hours:24", "补偿窗口"}) {
		s.policy.RefundWindowHours = 24
		recognized = true
	}
	if containsAny(diff, []string{"escalation_enabled=true", "escalation:true", "升级边界"}) {
		s.policy.EscalationEnabled = true
		recognized = true
	}
	switch {
	case containsAny(diff, []string{"compensation_limit=500", "compensation_limit:500", "auto_refund_no_review=true"}):
		s.policy.CompensationLimit = 500
		s.policy.AuditRequired = false
		recognized = true
	case containsAny(diff, []string{"compensation_limit=200", "compensation_limit:200", "补偿上限"}):
		s.policy.CompensationLimit = 200
		s.policy.AuditRequired = true
		recognized = true
	}
	if !recognized {
		return "", fmt.Errorf("support SOP fix diff has no recognizable business change")
	}
	s.policy.Version = targetVersion
	s.policy.AuditTrail = append(s.policy.AuditTrail, fmt.Sprintf("v%d: %s", targetVersion, strings.TrimSpace(proposal.Summary)))
	return commitID, nil
}

// Verify 用业务回放规则检查 SOP 是否真的解决痛点，并拦截过度补偿回归。
func (s *supportSOPScenario) Verify(context.Context, selfheal.FixProposal) (*selfheal.FailureSignal, error) {
	if s == nil {
		return nil, fmt.Errorf("support SOP scenario is nil")
	}
	if s.policy.CompensationLimit > 300 || !s.policy.AuditRequired {
		return &selfheal.FailureSignal{
			Kind:     "support_sop_regression",
			Severity: 4,
			ErrorText: strings.Join([]string{
				"补偿 SOP 新版本允许过高补偿或无审核赔付。",
				"这会把一线处理效率问题扩大成财务和合规风险。",
			}, "\n"),
			AffectedFiles: []string{"support/sop/compensation_policy", "finance/refund_policy", "risk/audit_rule"},
		}, nil
	}
	if s.policy.RefundWindowHours < 24 || !s.policy.EscalationEnabled || s.policy.CompensationLimit <= 0 {
		return &selfheal.FailureSignal{
			Kind:          "support_sop_gap",
			Severity:      2,
			ErrorText:     "客服补偿 SOP 仍缺少补偿窗口、升级开关或补偿上限，回放用例未通过。",
			AffectedFiles: []string{"support/sop/compensation_policy"},
		}, nil
	}
	return nil, nil
}

// Rollback 按 commit ID 恢复旧 SOP 策略，模拟配置中心版本回退。
func (s *supportSOPScenario) Rollback(_ context.Context, commitID string) error {
	if s == nil {
		return fmt.Errorf("support SOP scenario is nil")
	}
	snapshot, ok := s.snapshots[strings.TrimSpace(commitID)]
	if !ok {
		return fmt.Errorf("rollback snapshot not found: %s", commitID)
	}
	s.policy = clonePolicy(snapshot)
	return nil
}

// PolicySummary 返回当前 SOP 策略摘要，供测试、输出和 trace 使用。
func (s *supportSOPScenario) PolicySummary() policySummary {
	if s == nil {
		return policySummary{}
	}
	return policySummary{
		PolicyVersion:      s.policy.Version,
		RefundWindowHours:  s.policy.RefundWindowHours,
		EscalationEnabled:  s.policy.EscalationEnabled,
		CompensationLimit:  s.policy.CompensationLimit,
		AuditRequired:      s.policy.AuditRequired,
		KnownRollbackCount: len(s.snapshots),
	}
}

// unsafeFixProposal 返回一个会扩大业务风险的补丁，用于验证回滚分支。
func unsafeFixProposal() selfheal.FixProposal {
	return selfheal.FixProposal{
		Summary: "错误地放开无审核自动赔付",
		FixDiff: "refund_window_hours=24; escalation_enabled=true; compensation_limit=500; auto_refund_no_review=true",
	}
}

// queryRunner 通过 ADK Runner 调用 self-heal Agent，并读取 customized output 中的结构化结果。
func queryRunner(ctx context.Context, runner *adk.Runner, failure selfheal.FailureSignal) (*selfheal.Response, bool, error) {
	payload, err := json.Marshal(failure)
	if err != nil {
		return nil, false, err
	}
	iter := runner.Query(ctx, string(payload))
	var fallbackAnswer string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, false, event.Err
		}
		if event.Output == nil {
			continue
		}
		if response, ok := event.Output.CustomizedOutput.(*selfheal.Response); ok {
			return response, true, nil
		}
		if event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, false, err
		}
		if message != nil && message.Role == schema.Assistant {
			fallbackAnswer = strings.TrimSpace(message.Content)
		}
	}
	if fallbackAnswer == "" {
		return nil, false, fmt.Errorf("self-heal runner finished without assistant output")
	}
	return &selfheal.Response{Status: selfheal.StatusHumanHandoff, Iterations: 0}, false, nil
}

// summarizeResponse 把完整响应和策略状态压成命令稳定摘要。
func summarizeResponse(response *selfheal.Response, usedCustomized bool, summary policySummary) runOutput {
	output := runOutput{
		Scenario:              "support_sop_self_heal",
		UsedADKCustomizedData: usedCustomized,
		PolicyVersion:         summary.PolicyVersion,
		RefundWindowHours:     summary.RefundWindowHours,
		EscalationEnabled:     summary.EscalationEnabled,
		CompensationLimit:     summary.CompensationLimit,
	}
	if response == nil {
		return output
	}
	output.Status = string(response.Status)
	output.Iterations = response.Iterations
	output.Commits = len(response.Commits)
	output.AnswerChars = len([]rune(response.UserMessage()))
	return output
}

// printAgentResult 输出自愈结果和业务配置摘要。
func printAgentResult(response *selfheal.Response, output runOutput) {
	fmt.Println("\n=== Self-Heal Final Output ===")
	if response != nil {
		fmt.Println(response.UserMessage())
	}
	fmt.Printf("\n=== Support SOP Summary ===\n%+v\n", output)
}

// loadModelConfig 读取 OpenAI-compatible 模型配置，普通测试通过 fake model 绕过真实调用。
func loadModelConfig() (modelConfig, error) {
	apiKey := firstEnv("OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY")
	if apiKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is empty; set it in .env or environment")
	}
	modelName := firstEnv("LLM_MODEL", "OPENAI_MODEL")
	if modelName == "" {
		return modelConfig{}, fmt.Errorf("LLM_MODEL is empty; set it in .env or environment")
	}
	baseURL := normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL"))
	return modelConfig{APIKey: apiKey, Model: modelName, BaseURL: baseURL}, nil
}

// loadDotEnv 加载简单 KEY=VALUE 格式配置，缺少 .env 时保持当前环境变量。
func loadDotEnv(path string) error {
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
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
		if key == "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// clonePolicy 深拷贝策略里的切片字段，避免快照被后续审计记录污染。
func clonePolicy(policy supportPolicy) supportPolicy {
	cloned := policy
	cloned.AuditTrail = append([]string(nil), policy.AuditTrail...)
	return cloned
}

// containsAny 判断输出是否包含一组关键词中的任意一个。
func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

// firstEnv 返回第一项非空环境变量。
func firstEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
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

// parsePositiveInt 解析正整数环境变量，非法时返回 0。
func parsePositiveInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

// displayBaseURL 让默认地址在输出里更明确。
func displayBaseURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return "default OpenAI endpoint"
	}
	return baseURL
}

// redactKey 打印配置时隐藏密钥。
func redactKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// enabledText 统一命令行展示里的布尔开关文字。
func enabledText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
