package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/reasoning/hypothesis"
)

const (
	defaultEnvPath       = ".env"
	defaultProblemPath   = "reasoning/hypothesis/examples/community_signup_prompt.txt"
	defaultScenario      = "社区活动报名下滑诊断"
	defaultMaxIterations = 2
	defaultMaxHypotheses = 2
	defaultMaxEvidence   = 1
)

// runConfig 保存命令行参数，cmd 只负责把外部输入转成 Hypothesis Agent 调用。
type runConfig struct {
	EnvPath            string
	Problem            string
	ProblemFile        string
	Scenario           string
	PrepareOnly        bool
	PrintJSON          bool
	MaxIterations      int
	MaxHypotheses      int
	MaxEvidence        int
	PlannerMaxTokens   int
	EvidenceMaxTokens  int
	EvaluatorMaxTokens int
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，trace 和测试都只看这些字段。
type runOutput struct {
	Mode                  string `json:"mode"`
	Scenario              string `json:"scenario"`
	ProblemChars          int    `json:"problem_chars"`
	MaxIterations         int    `json:"max_iterations"`
	MaxHypotheses         int    `json:"max_hypotheses"`
	MaxEvidence           int    `json:"max_evidence"`
	IterationsUsed        int    `json:"iterations_used"`
	Hypotheses            int    `json:"hypotheses"`
	Survivors             int    `json:"survivors"`
	Confirmed             int    `json:"confirmed"`
	Converged             bool   `json:"converged"`
	NeedsHITL             bool   `json:"needs_hitl"`
	AnswerChars           int    `json:"answer_chars"`
	UsedADKCustomizedData bool   `json:"used_adk_customized_data"`
}

// hypothesisReport 是可选 JSON 输出，保留完整假设树方便人工检查。
type hypothesisReport struct {
	Output runOutput            `json:"output"`
	Result *hypothesis.Response `json:"result,omitempty"`
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

// main 运行一个非 coding 的迭代假设检验 Agent。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 加载输入、模型配置和 CozeLoop，并通过 Eino ADK Runner 调用 Hypothesis Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	problem, err := loadProblem(config)
	if err != nil {
		return runOutput{}, err
	}
	output := runOutput{
		Mode:          "prepare-only",
		Scenario:      config.Scenario,
		ProblemChars:  len([]rune(problem)),
		MaxIterations: config.MaxIterations,
		MaxHypotheses: config.MaxHypotheses,
		MaxEvidence:   config.MaxEvidence,
	}
	if config.PrepareOnly {
		fmt.Println("=== Hypothesis Problem ===")
		fmt.Println(problem)
		return output, nil
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

	return withRunAgentTrace(ctx, config.Scenario, len([]rune(problem)), config.MaxIterations, config.MaxHypotheses, config.MaxEvidence, func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		runner, _, err := hypothesis.NewRunner(traceCtx, hypothesis.Config{
			Name:               "hypothesis_community_signup_agent",
			Description:        "Use iterative hypothesis testing to diagnose a non-coding community activity problem.",
			Scenario:           config.Scenario,
			Model:              chatModel,
			MaxIterations:      config.MaxIterations,
			MaxHypotheses:      config.MaxHypotheses,
			MaxEvidence:        config.MaxEvidence,
			PlannerMaxTokens:   config.PlannerMaxTokens,
			EvidenceMaxTokens:  config.EvidenceMaxTokens,
			EvaluatorMaxTokens: config.EvaluatorMaxTokens,
		})
		if err != nil {
			return runOutput{}, err
		}
		response, usedCustomized, err := queryRunner(traceCtx, runner, problem)
		if err != nil {
			return runOutput{}, err
		}
		output := summarizeResponse(config, problem, response, usedCustomized)
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		if config.PrintJSON {
			printHypothesisReport(output, response)
		}
		return output, nil
	})
}

// parseRunConfig 读取 flags 和 .env，默认使用社区活动报名下滑这个非 coding 场景。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("hypothesis-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		EnvPath:     defaultEnvPath,
		ProblemFile: defaultProblemPath,
	}
	fs.StringVar(&config.EnvPath, "env", defaultEnvPath, "env file path")
	fs.StringVar(&config.Problem, "message", "", "problem text for the hypothesis agent")
	fs.StringVar(&config.ProblemFile, "message-file", defaultProblemPath, "file containing the problem")
	fs.StringVar(&config.Scenario, "scenario", "", "non-coding scenario name")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print problem without calling model")
	fs.BoolVar(&config.PrintJSON, "json", false, "print machine-readable report")
	fs.IntVar(&config.MaxIterations, "max-iterations", 0, "max hypothesis loop iterations")
	fs.IntVar(&config.MaxHypotheses, "max-hypotheses", 0, "max new hypotheses per iteration")
	fs.IntVar(&config.MaxEvidence, "max-evidence", 0, "max evidence items per hypothesis per iteration")
	fs.IntVar(&config.PlannerMaxTokens, "planner-max-tokens", 0, "max tokens for hypothesis planning")
	fs.IntVar(&config.EvidenceMaxTokens, "evidence-max-tokens", 0, "max tokens for evidence generation")
	fs.IntVar(&config.EvaluatorMaxTokens, "evaluator-max-tokens", 0, "max tokens for evidence evaluation")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(config.EnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.Scenario = firstNonEmpty(config.Scenario, os.Getenv("HYPOTHESIS_SCENARIO"), defaultScenario)
	config.MaxIterations = firstPositive(config.MaxIterations, parsePositiveInt(os.Getenv("HYPOTHESIS_MAX_ITERATIONS")), defaultMaxIterations)
	config.MaxHypotheses = firstPositive(config.MaxHypotheses, parsePositiveInt(os.Getenv("HYPOTHESIS_MAX_HYPOTHESES")), defaultMaxHypotheses)
	config.MaxEvidence = firstPositive(config.MaxEvidence, parsePositiveInt(os.Getenv("HYPOTHESIS_MAX_EVIDENCE")), defaultMaxEvidence)
	config.PlannerMaxTokens = firstPositive(config.PlannerMaxTokens, parsePositiveInt(os.Getenv("HYPOTHESIS_PLANNER_MAX_TOKENS")))
	config.EvidenceMaxTokens = firstPositive(config.EvidenceMaxTokens, parsePositiveInt(os.Getenv("HYPOTHESIS_EVIDENCE_MAX_TOKENS")))
	config.EvaluatorMaxTokens = firstPositive(config.EvaluatorMaxTokens, parsePositiveInt(os.Getenv("HYPOTHESIS_EVALUATOR_MAX_TOKENS")))
	return config, nil
}

// loadProblem 读取用户直接传入的问题；没有 -message 时读取默认场景文件。
func loadProblem(config runConfig) (string, error) {
	if strings.TrimSpace(config.Problem) != "" {
		return strings.TrimSpace(config.Problem), nil
	}
	if strings.TrimSpace(config.ProblemFile) == "" {
		return "", fmt.Errorf("message or message-file is required")
	}
	data, err := os.ReadFile(e2etest.ResolvePath(config.ProblemFile))
	if err != nil {
		return "", err
	}
	problem := strings.TrimSpace(string(data))
	if problem == "" {
		return "", fmt.Errorf("problem is empty")
	}
	return problem, nil
}

// queryRunner 通过 ADK Runner 调用 Hypothesis Agent，并读取 customized output 中的 response。
func queryRunner(ctx context.Context, runner *adk.Runner, problem string) (*hypothesis.Response, bool, error) {
	iter := runner.Query(ctx, problem)
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
		if response, ok := event.Output.CustomizedOutput.(*hypothesis.Response); ok {
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
		return nil, false, fmt.Errorf("hypothesis runner finished without assistant output")
	}
	return &hypothesis.Response{Problem: problem, FinalAnswer: fallbackAnswer}, false, nil
}

// summarizeResponse 把 Hypothesis 响应压成稳定摘要，避免 trace 记录完整问题或证据明细。
func summarizeResponse(config runConfig, problem string, response *hypothesis.Response, usedCustomized bool) runOutput {
	output := runOutput{
		Mode:                  "agent",
		Scenario:              config.Scenario,
		ProblemChars:          len([]rune(problem)),
		MaxIterations:         config.MaxIterations,
		MaxHypotheses:         config.MaxHypotheses,
		MaxEvidence:           config.MaxEvidence,
		UsedADKCustomizedData: usedCustomized,
	}
	if response == nil {
		return output
	}
	output.IterationsUsed = response.Outcome.IterationsUsed
	output.Hypotheses = len(response.Tree.Hypotheses)
	output.Survivors = response.Tree.SurvivorCount
	output.Confirmed = response.Tree.ConfirmedCount
	output.Converged = response.Outcome.Converged
	output.NeedsHITL = response.Outcome.NeedsHITL
	output.AnswerChars = len([]rune(response.FinalAnswer))
	return output
}

// printAgentResult 输出 agent 的最终答案和简洁摘要，完整结构只在 -json 时输出。
func printAgentResult(response *hypothesis.Response, output runOutput) {
	fmt.Println("\n=== Hypothesis Final Answer ===")
	if response != nil {
		fmt.Println(response.UserMessage())
	}
	fmt.Printf("\n=== Hypothesis Summary ===\n%+v\n", output)
}

// printHypothesisReport 输出完整假设树，方便人工验证不是单次问答。
func printHypothesisReport(output runOutput, response *hypothesis.Response) {
	report := hypothesisReport{Output: output, Result: response}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: marshal report: %v\n", err)
		return
	}
	fmt.Println("\n=== Hypothesis JSON Report ===")
	fmt.Println(string(data))
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

// loadDotEnv 加载简单 KEY=VALUE 格式配置，和仓库其他 cmd 保持一致。
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

// firstNonEmpty 返回第一项非空字符串，用于 flag 覆盖 env。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// firstPositive 返回第一项正整数，用于 flag、env、默认值顺序覆盖。
func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
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
