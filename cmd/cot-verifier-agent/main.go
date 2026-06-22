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
	"ai-designing/reasoning/cot"
)

const (
	defaultEnvPath      = ".env"
	defaultQuestionPath = "reasoning/cot/examples/family_care_prompt.txt"
	defaultScenario     = "家庭照护排班"
)

// runConfig 保存命令行参数，cmd 只负责把外部输入转成 CoT Agent 调用。
type runConfig struct {
	EnvPath         string
	Question        string
	QuestionFile    string
	Scenario        string
	PrepareOnly     bool
	PrintJSON       bool
	MaxTokens       int
	VerifyMaxTokens int
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
	QuestionChars         int    `json:"question_chars"`
	Steps                 int    `json:"steps"`
	Issues                int    `json:"issues"`
	WeakestStep           int    `json:"weakest_step"`
	AnswerChars           int    `json:"answer_chars"`
	Verified              bool   `json:"verified"`
	UsedADKCustomizedData bool   `json:"used_adk_customized_data"`
}

// cotReport 是可选 JSON 输出，保留完整链路方便人工检查。
type cotReport struct {
	Output runOutput     `json:"output"`
	Result *cot.Response `json:"result,omitempty"`
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

// main 运行一个非 coding 的家庭照护排班 CoT verifier Agent。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 加载输入、模型配置和 CozeLoop，并通过 Eino ADK Runner 调用 CoT Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	question, err := loadQuestion(config)
	if err != nil {
		return runOutput{}, err
	}
	output := runOutput{
		Mode:          "prepare-only",
		Scenario:      config.Scenario,
		QuestionChars: len([]rune(question)),
	}
	if config.PrepareOnly {
		fmt.Println("=== CoT Verifier Question ===")
		fmt.Println(question)
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

	return withRunAgentTrace(ctx, config.Scenario, len([]rune(question)), func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		runner, _, err := cot.NewRunner(traceCtx, cot.Config{
			Name:            "cot_family_care_agent",
			Description:     "Use Chain-of-Thought verification to solve a non-coding family-care scheduling problem.",
			Scenario:        config.Scenario,
			Model:           chatModel,
			MaxTokens:       config.MaxTokens,
			VerifyMaxTokens: config.VerifyMaxTokens,
		})
		if err != nil {
			return runOutput{}, err
		}
		response, usedCustomized, err := queryRunner(traceCtx, runner, question)
		if err != nil {
			return runOutput{}, err
		}
		output := summarizeResponse(config.Scenario, question, response, usedCustomized)
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		if config.PrintJSON {
			printCOTReport(output, response)
		}
		return output, nil
	})
}

// parseRunConfig 读取 flags 和 .env，默认使用家庭照护排班这个非 coding 场景。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("cot-verifier-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		EnvPath:      defaultEnvPath,
		QuestionFile: defaultQuestionPath,
	}
	fs.StringVar(&config.EnvPath, "env", defaultEnvPath, "env file path")
	fs.StringVar(&config.Question, "message", "", "question text for the CoT verifier agent")
	fs.StringVar(&config.QuestionFile, "message-file", defaultQuestionPath, "file containing the question")
	fs.StringVar(&config.Scenario, "scenario", "", "non-coding scenario name")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print question without calling model")
	fs.BoolVar(&config.PrintJSON, "json", false, "print machine-readable report")
	fs.IntVar(&config.MaxTokens, "max-tokens", 0, "max tokens for chain generation")
	fs.IntVar(&config.VerifyMaxTokens, "verify-max-tokens", 0, "max tokens for each verification call")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(config.EnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.Scenario = firstNonEmpty(config.Scenario, os.Getenv("COT_SCENARIO"), defaultScenario)
	if config.MaxTokens <= 0 {
		config.MaxTokens = parsePositiveInt(os.Getenv("COT_MAX_TOKENS"))
	}
	if config.VerifyMaxTokens <= 0 {
		config.VerifyMaxTokens = parsePositiveInt(os.Getenv("COT_VERIFY_MAX_TOKENS"))
	}
	return config, nil
}

// loadQuestion 读取用户直接传入的问题；没有 -message 时读取默认场景文件。
func loadQuestion(config runConfig) (string, error) {
	if strings.TrimSpace(config.Question) != "" {
		return strings.TrimSpace(config.Question), nil
	}
	if strings.TrimSpace(config.QuestionFile) == "" {
		return "", fmt.Errorf("message or message-file is required")
	}
	data, err := os.ReadFile(e2etest.ResolvePath(config.QuestionFile))
	if err != nil {
		return "", err
	}
	question := strings.TrimSpace(string(data))
	if question == "" {
		return "", fmt.Errorf("question is empty")
	}
	return question, nil
}

// queryRunner 通过 ADK Runner 调用 CoT Agent，并读取 customized output 中的 cot.Response。
func queryRunner(ctx context.Context, runner *adk.Runner, question string) (*cot.Response, bool, error) {
	iter := runner.Query(ctx, question)
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
		if response, ok := event.Output.CustomizedOutput.(*cot.Response); ok {
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
		return nil, false, fmt.Errorf("cot runner finished without assistant output")
	}
	return &cot.Response{FinalAnswer: fallbackAnswer, Verified: false}, false, nil
}

// summarizeResponse 把 CoT 响应压成稳定摘要，避免 trace 记录完整用户问题或推理内容。
func summarizeResponse(scenario string, question string, response *cot.Response, usedCustomized bool) runOutput {
	output := runOutput{
		Mode:                  "agent",
		Scenario:              scenario,
		QuestionChars:         len([]rune(question)),
		UsedADKCustomizedData: usedCustomized,
	}
	if response == nil {
		return output
	}
	output.Steps = len(response.Chain.Steps)
	output.Issues = len(response.Issues)
	output.AnswerChars = len([]rune(response.FinalAnswer))
	output.Verified = response.Verified
	if response.WeakestStep != nil {
		output.WeakestStep = response.WeakestStep.StepNumber
	}
	return output
}

// printAgentResult 输出 agent 的最终答案和简洁摘要，完整结构只在 -json 时输出。
func printAgentResult(response *cot.Response, output runOutput) {
	fmt.Println("\n=== CoT Final Answer ===")
	if response != nil {
		fmt.Println(response.UserMessage())
	}
	fmt.Printf("\n=== CoT Summary ===\n%+v\n", output)
}

// printCOTReport 输出完整推理链，方便人工验证 CoT 和 verifier 都跑过。
func printCOTReport(output runOutput, response *cot.Response) {
	report := cotReport{Output: output, Result: response}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: marshal report: %v\n", err)
		return
	}
	fmt.Println("\n=== CoT JSON Report ===")
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
