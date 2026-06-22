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
	"ai-designing/reasoning/compose"
	"ai-designing/reasoning/cot"
	"ai-designing/reasoning/hypothesis"
	"ai-designing/reasoning/tot"
)

const (
	defaultEnvPath         = ".env"
	defaultQueryPath       = "reasoning/compose/examples/student_support_prompt.txt"
	defaultScenario        = "学生学习支持分流"
	defaultReasoningMethod = "mcts"
	defaultMaxDepth        = 1
	defaultNSim            = 1
	defaultMaxIterations   = 2
	defaultMaxHypotheses   = 2
	defaultMaxEvidence     = 1
)

// runConfig 保存命令行参数，cmd 只负责把外部输入转成 compose Agent 调用。
type runConfig struct {
	EnvPath               string
	Query                 string
	QueryFile             string
	Scenario              string
	PrepareOnly           bool
	PrintJSON             bool
	Method                string
	MaxDepth              int
	NSim                  int
	ForestSize            int
	MaxIterations         int
	MaxHypotheses         int
	MaxEvidence           int
	ConfidenceThreshold   float64
	BestPathThreshold     float64
	RouterMaxTokens       int
	DirectMaxTokens       int
	ConfirmationMaxTokens int
	COTMaxTokens          int
	COTVerifyMaxTokens    int
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，trace 和测试都只看这些字段。
type runOutput struct {
	Mode                  string             `json:"mode"`
	Scenario              string             `json:"scenario"`
	QueryChars            int                `json:"query_chars"`
	Complexity            compose.Complexity `json:"complexity"`
	RecommendedPath       compose.PathKind   `json:"recommended_path"`
	UsedHypothesis        bool               `json:"used_hypothesis"`
	Escalated             bool               `json:"escalated"`
	AnswerChars           int                `json:"answer_chars"`
	COTSteps              int                `json:"cot_steps"`
	TOTRootVisits         int                `json:"tot_root_visits"`
	Hypotheses            int                `json:"hypotheses"`
	HypothesisConverged   bool               `json:"hypothesis_converged"`
	UsedADKCustomizedData bool               `json:"used_adk_customized_data"`
}

// composeReport 是可选 JSON 输出，保留完整组合结果方便人工检查路由路径。
type composeReport struct {
	Output runOutput         `json:"output"`
	Result *compose.Response `json:"result,omitempty"`
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

// main 运行一个非 coding 的自适应复杂度路由 Agent。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 加载输入、模型配置和 CozeLoop，并通过 Eino ADK Runner 调用 compose Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	query, err := loadQuery(config)
	if err != nil {
		return runOutput{}, err
	}
	output := runOutput{
		Mode:       "prepare-only",
		Scenario:   config.Scenario,
		QueryChars: len([]rune(query)),
	}
	if config.PrepareOnly {
		fmt.Println("=== Compose Router Query ===")
		fmt.Println(query)
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

	return withRunAgentTrace(ctx, config.Scenario, len([]rune(query)), func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		runner, _, err := compose.NewRunner(traceCtx, buildComposeConfig(config, chatModel))
		if err != nil {
			return runOutput{}, err
		}
		response, usedCustomized, err := queryRunner(traceCtx, runner, query)
		if err != nil {
			return runOutput{}, err
		}
		output := summarizeResponse(config, query, response, usedCustomized)
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		if config.PrintJSON {
			printComposeReport(output, response)
		}
		return output, nil
	})
}

// parseRunConfig 读取 flags 和 .env，默认使用学生学习支持这个非 coding 场景。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("compose-router-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		EnvPath:   defaultEnvPath,
		QueryFile: defaultQueryPath,
	}
	fs.StringVar(&config.EnvPath, "env", defaultEnvPath, "env file path")
	fs.StringVar(&config.Query, "message", "", "query text for the compose router agent")
	fs.StringVar(&config.QueryFile, "message-file", defaultQueryPath, "file containing the query")
	fs.StringVar(&config.Scenario, "scenario", "", "non-coding scenario name")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print query without calling model")
	fs.BoolVar(&config.PrintJSON, "json", false, "print machine-readable report")
	fs.StringVar(&config.Method, "method", "", "complex-path ToT method: beam_search, dfs, mcts, lats; default mcts")
	fs.IntVar(&config.MaxDepth, "max-depth", 0, "complex-path ToT max depth")
	fs.IntVar(&config.NSim, "nsim", 0, "complex-path MCTS/LATS simulation count")
	fs.IntVar(&config.ForestSize, "forest-size", 0, "complex-path forest size")
	fs.IntVar(&config.MaxIterations, "max-iterations", 0, "hypothesis loop iterations")
	fs.IntVar(&config.MaxHypotheses, "max-hypotheses", 0, "max hypotheses per iteration")
	fs.IntVar(&config.MaxEvidence, "max-evidence", 0, "max evidence items per hypothesis")
	fs.Float64Var(&config.ConfidenceThreshold, "confidence-threshold", 0, "CoT confidence threshold")
	fs.Float64Var(&config.BestPathThreshold, "best-path-threshold", 0, "ToT best-path confirmation threshold")
	fs.IntVar(&config.RouterMaxTokens, "router-max-tokens", 0, "max tokens for complexity router")
	fs.IntVar(&config.DirectMaxTokens, "direct-max-tokens", 0, "max tokens for direct response")
	fs.IntVar(&config.ConfirmationMaxTokens, "confirmation-max-tokens", 0, "max tokens for best-path confirmation")
	fs.IntVar(&config.COTMaxTokens, "cot-max-tokens", 0, "max tokens for CoT generation")
	fs.IntVar(&config.COTVerifyMaxTokens, "cot-verify-max-tokens", 0, "max tokens for each CoT verification call")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(config.EnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.Scenario = firstNonEmpty(config.Scenario, os.Getenv("COMPOSE_SCENARIO"), defaultScenario)
	config.Method = firstNonEmpty(config.Method, os.Getenv("COMPOSE_TOT_METHOD"), defaultReasoningMethod)
	config.MaxDepth = firstPositive(config.MaxDepth, parsePositiveInt(os.Getenv("COMPOSE_TOT_MAX_DEPTH")), defaultMaxDepth)
	config.NSim = firstPositive(config.NSim, parsePositiveInt(os.Getenv("COMPOSE_TOT_NSIM")), defaultNSim)
	config.ForestSize = firstPositive(config.ForestSize, parsePositiveInt(os.Getenv("COMPOSE_TOT_FOREST_SIZE")))
	config.MaxIterations = firstPositive(config.MaxIterations, parsePositiveInt(os.Getenv("COMPOSE_HYPOTHESIS_MAX_ITERATIONS")), defaultMaxIterations)
	config.MaxHypotheses = firstPositive(config.MaxHypotheses, parsePositiveInt(os.Getenv("COMPOSE_HYPOTHESIS_MAX_HYPOTHESES")), defaultMaxHypotheses)
	config.MaxEvidence = firstPositive(config.MaxEvidence, parsePositiveInt(os.Getenv("COMPOSE_HYPOTHESIS_MAX_EVIDENCE")), defaultMaxEvidence)
	config.ConfidenceThreshold = firstPositiveFloat(config.ConfidenceThreshold, parsePositiveFloat(os.Getenv("COMPOSE_CONFIDENCE_THRESHOLD")))
	config.BestPathThreshold = firstPositiveFloat(config.BestPathThreshold, parsePositiveFloat(os.Getenv("COMPOSE_BEST_PATH_THRESHOLD")))
	return config, nil
}

// loadQuery 读取用户直接传入的问题；没有 -message 时读取默认场景文件。
func loadQuery(config runConfig) (string, error) {
	if strings.TrimSpace(config.Query) != "" {
		return strings.TrimSpace(config.Query), nil
	}
	if strings.TrimSpace(config.QueryFile) == "" {
		return "", fmt.Errorf("message or message-file is required")
	}
	data, err := os.ReadFile(e2etest.ResolvePath(config.QueryFile))
	if err != nil {
		return "", err
	}
	query := strings.TrimSpace(string(data))
	if query == "" {
		return "", fmt.Errorf("query is empty")
	}
	return query, nil
}

// buildComposeConfig 把命令行参数转换成组合 Agent 配置。
func buildComposeConfig(config runConfig, chatModel model.BaseChatModel) compose.Config {
	return compose.Config{
		Name:                  "compose_student_support_agent",
		Description:           "Use adaptive routing to solve a non-coding student support triage problem.",
		Scenario:              config.Scenario,
		Model:                 chatModel,
		RouterMaxTokens:       config.RouterMaxTokens,
		DirectMaxTokens:       config.DirectMaxTokens,
		ConfirmationMaxTokens: config.ConfirmationMaxTokens,
		ConfidenceThreshold:   config.ConfidenceThreshold,
		BestPathThreshold:     config.BestPathThreshold,
		COTConfig: cot.Config{
			MaxTokens:       config.COTMaxTokens,
			VerifyMaxTokens: config.COTVerifyMaxTokens,
		},
		TOTConfig: tot.Config{
			ReasonConfig: tot.ReasonConfig{
				Method:         tot.Method(strings.TrimSpace(config.Method)),
				MaxDepth:       config.MaxDepth,
				NSim:           config.NSim,
				ForestSize:     config.ForestSize,
				AnswerApproach: tot.AnswerApproachPool,
			},
		},
		HypothesisConfig: hypothesis.Config{
			MaxIterations: config.MaxIterations,
			MaxHypotheses: config.MaxHypotheses,
			MaxEvidence:   config.MaxEvidence,
		},
	}
}

// queryRunner 通过 ADK Runner 调用 Compose Agent，并读取 customized output 中的 compose.Response。
func queryRunner(ctx context.Context, runner *adk.Runner, query string) (*compose.Response, bool, error) {
	iter := runner.Query(ctx, query)
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
		if response, ok := event.Output.CustomizedOutput.(*compose.Response); ok {
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
		return nil, false, fmt.Errorf("compose runner finished without assistant output")
	}
	return &compose.Response{Query: query, FinalAnswer: fallbackAnswer}, false, nil
}

// summarizeResponse 把组合响应压成稳定摘要，避免 trace 记录完整问题或推理内容。
func summarizeResponse(config runConfig, query string, response *compose.Response, usedCustomized bool) runOutput {
	output := runOutput{
		Mode:                  "agent",
		Scenario:              config.Scenario,
		QueryChars:            len([]rune(query)),
		UsedADKCustomizedData: usedCustomized,
	}
	if response == nil {
		return output
	}
	output.Complexity = response.Decision.Complexity
	output.RecommendedPath = response.Decision.RecommendedPath
	output.UsedHypothesis = response.UsedHypothesis
	output.Escalated = response.Escalated
	output.AnswerChars = len([]rune(response.FinalAnswer))
	if response.COT != nil {
		output.COTSteps = len(response.COT.Chain.Steps)
	}
	if response.TOT != nil && response.TOT.Root != nil {
		output.TOTRootVisits = response.TOT.Root.Visits
	}
	if response.Hypothesis != nil {
		output.Hypotheses = len(response.Hypothesis.Tree.Hypotheses)
		output.HypothesisConverged = response.Hypothesis.Outcome.Converged
	}
	return output
}

// printAgentResult 输出 agent 的最终答案和简洁摘要，完整结构只在 -json 时输出。
func printAgentResult(response *compose.Response, output runOutput) {
	fmt.Println("\n=== Compose Final Answer ===")
	if response != nil {
		fmt.Println(response.UserMessage())
	}
	fmt.Printf("\n=== Compose Summary ===\n%+v\n", output)
}

// printComposeReport 输出完整路由结果，方便人工验证不是单次问答。
func printComposeReport(output runOutput, response *compose.Response) {
	report := composeReport{Output: output, Result: response}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: marshal report: %v\n", err)
		return
	}
	fmt.Println("\n=== Compose JSON Report ===")
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

// firstPositiveFloat 返回第一项正浮点数，用于 flag、env、默认值顺序覆盖。
func firstPositiveFloat(values ...float64) float64 {
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

// parsePositiveFloat 解析正浮点数环境变量，非法时返回 0。
func parsePositiveFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
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
