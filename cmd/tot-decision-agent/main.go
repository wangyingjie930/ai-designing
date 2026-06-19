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
	"ai-designing/reasoning/tot"
)

const (
	defaultEnvPath         = ".env"
	defaultPromptPath      = "reasoning/tot/examples/event_decision_prompt.txt"
	defaultReasoningMethod = "mcts"
)

// runConfig 保存命令行参数，cmd 只负责把外部输入转成 ToT 调用。
type runConfig struct {
	EnvPath        string
	Prompt         string
	PromptFile     string
	PrepareOnly    bool
	Scope          string
	Method         string
	MaxDepth       int
	BeamSize       int
	NSim           int
	ForestSize     int
	RatingScale    int
	AnswerApproach string
	BatchGrading   bool
	PrintJSON      bool
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是 cmd 调用后的稳定摘要，测试和人工验证都看这几个字段。
type runOutput struct {
	Mode                  string     `json:"mode"`
	Method                tot.Method `json:"method"`
	PromptChars           int        `json:"prompt_chars"`
	AnswerChars           int        `json:"answer_chars"`
	RootChildren          int        `json:"root_children"`
	RootVisits            int        `json:"root_visits"`
	SFTSamples            int        `json:"sft_samples"`
	PreferencePairs       int        `json:"preference_pairs"`
	UsedADKCustomizedData bool       `json:"used_adk_customized_data"`
}

// decisionReport 是命令行可选 JSON 输出，包含最终答案和推理树快照。
type decisionReport struct {
	Output          runOutput            `json:"output"`
	Answer          string               `json:"answer,omitempty"`
	Tree            *tot.NodeSnapshot    `json:"tree,omitempty"`
	SFTSamples      []tot.SFTSample      `json:"sft_samples,omitempty"`
	PreferencePairs []tot.PreferencePair `json:"preference_pairs,omitempty"`
}

// chatModelFactory 抽出模型创建点，测试时替换成 fake model 来验证 cmd 调用链。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// main 运行一个非 coding 决策 prompt，并通过 Eino ADK Runner 调用 ToT Agent。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 加载 prompt 和模型配置，核心调用链是 tot.NewRunner -> runner.Query。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	prompt, err := loadPrompt(config)
	if err != nil {
		return runOutput{}, err
	}
	reasonConfig, err := buildReasonConfig(config)
	if err != nil {
		return runOutput{}, err
	}
	output := runOutput{
		Mode:        "prepare-only",
		Method:      reasonConfig.Method,
		PromptChars: len([]rune(prompt)),
	}
	if config.PrepareOnly {
		fmt.Println("=== ToT Decision Prompt ===")
		fmt.Println(prompt)
		return output, nil
	}
	modelConfig, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	chatModel, err := newChatModel(ctx, modelConfig)
	if err != nil {
		return runOutput{}, fmt.Errorf("init chat model: %w", err)
	}
	runner, core, err := tot.NewRunner(ctx, tot.Config{
		Name:         "tot_event_decision_agent",
		Description:  "Use Tree-of-Thought reasoning to decide a non-coding event plan.",
		Scope:        config.Scope,
		Model:        chatModel,
		ReasonConfig: reasonConfig,
	})
	if err != nil {
		return runOutput{}, err
	}
	response, usedCustomized, err := queryRunner(ctx, runner, prompt)
	if err != nil {
		return runOutput{}, err
	}
	if response == nil {
		response = &tot.Response{
			Answer: "",
			Prompt: prompt,
			Method: core.Method(),
			Root:   core.Root(),
		}
	}
	output = summarizeResponse(prompt, response, usedCustomized)
	fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
	fmt.Println("\n=== ToT Final Answer ===")
	fmt.Println(response.Answer)
	fmt.Printf("\n=== ToT Summary ===\n%+v\n", output)
	if config.PrintJSON {
		printDecisionReport(output, response)
	}
	return output, nil
}

// parseRunConfig 读取 flags 和 .env，默认用 MCTS 跑线下活动决策场景。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("tot-decision-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		EnvPath:    defaultEnvPath,
		PromptFile: defaultPromptPath,
	}
	fs.StringVar(&config.EnvPath, "env", defaultEnvPath, "env file path")
	fs.StringVar(&config.Prompt, "message", "", "decision prompt text")
	fs.StringVar(&config.PromptFile, "message-file", defaultPromptPath, "decision prompt file")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print prompt without calling model")
	fs.StringVar(&config.Scope, "scope", "", "optional task scope prepended to internal system prompts")
	fs.StringVar(&config.Method, "method", "", "tot method: beam_search, dfs, mcts, lats; default mcts")
	fs.IntVar(&config.MaxDepth, "max-depth", 0, "reasoning max depth")
	fs.IntVar(&config.BeamSize, "beam-size", 0, "beam search size")
	fs.IntVar(&config.NSim, "nsim", 0, "mcts/lats simulation count")
	fs.IntVar(&config.ForestSize, "forest-size", 0, "number of independent trees")
	fs.IntVar(&config.RatingScale, "rating-scale", 0, "grader rating scale")
	fs.StringVar(&config.AnswerApproach, "answer-approach", "", "pool or best")
	fs.BoolVar(&config.BatchGrading, "batch-grading", false, "rate sibling options in one grader call")
	fs.BoolVar(&config.PrintJSON, "json", false, "print machine-readable report")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(config.EnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.Method = firstNonEmpty(config.Method, os.Getenv("TOT_REASONING_METHOD"), defaultReasoningMethod)
	config.AnswerApproach = firstNonEmpty(config.AnswerApproach, os.Getenv("TOT_ANSWER_APPROACH"))
	if config.MaxDepth <= 0 {
		config.MaxDepth = parsePositiveInt(os.Getenv("TOT_MAX_DEPTH"))
	}
	if config.BeamSize <= 0 {
		config.BeamSize = parsePositiveInt(os.Getenv("TOT_BEAM_SIZE"))
	}
	if config.NSim <= 0 {
		config.NSim = parsePositiveInt(os.Getenv("TOT_NSIM"))
	}
	if config.ForestSize <= 0 {
		config.ForestSize = parsePositiveInt(os.Getenv("TOT_FOREST_SIZE"))
	}
	if config.RatingScale <= 0 {
		config.RatingScale = parsePositiveInt(os.Getenv("TOT_RATING_SCALE"))
	}
	config.Scope = firstNonEmpty(config.Scope, os.Getenv("TOT_SCOPE"))
	return config, nil
}

// loadPrompt 读取用户直接传入的 prompt；没有 -message 时读取默认活动决策文件。
func loadPrompt(config runConfig) (string, error) {
	if strings.TrimSpace(config.Prompt) != "" {
		return strings.TrimSpace(config.Prompt), nil
	}
	if strings.TrimSpace(config.PromptFile) == "" {
		return "", fmt.Errorf("message or message-file is required")
	}
	data, err := os.ReadFile(e2etest.ResolvePath(config.PromptFile))
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("prompt is empty")
	}
	return prompt, nil
}

// buildReasonConfig 把命令参数转成 ToT 配置，未传字段交给 tot 包默认值处理。
func buildReasonConfig(config runConfig) (tot.ReasonConfig, error) {
	reasonConfig := tot.ReasonConfig{
		Method:         tot.Method(strings.TrimSpace(config.Method)),
		MaxDepth:       config.MaxDepth,
		BeamSize:       config.BeamSize,
		NSim:           config.NSim,
		ForestSize:     config.ForestSize,
		RatingScale:    config.RatingScale,
		BatchGrading:   config.BatchGrading,
		AnswerApproach: tot.AnswerApproach(strings.TrimSpace(config.AnswerApproach)),
	}
	if reasonConfig.Method == "" {
		reasonConfig.Method = tot.MethodMCTS
	}
	if reasonConfig.AnswerApproach == "" {
		reasonConfig.AnswerApproach = tot.AnswerApproachPool
	}
	if _, _, err := tot.NewRunner(context.Background(), tot.Config{Model: noopModel{}, ReasonConfig: reasonConfig}); err != nil {
		return tot.ReasonConfig{}, err
	}
	return reasonConfig, nil
}

// queryRunner 通过 ADK Runner 调用 ToT，并取出 ADK customized output 中的 tot.Response。
func queryRunner(ctx context.Context, runner *adk.Runner, prompt string) (*tot.Response, bool, error) {
	iter := runner.Query(ctx, prompt)
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
		if response, ok := event.Output.CustomizedOutput.(*tot.Response); ok {
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
		return nil, false, fmt.Errorf("tot runner finished without assistant output")
	}
	return &tot.Response{Answer: fallbackAnswer, Prompt: prompt}, false, nil
}

// summarizeResponse 把 ToT 响应压成稳定摘要，便于 cmd 测试确认已经完成调用。
func summarizeResponse(prompt string, response *tot.Response, usedCustomized bool) runOutput {
	output := runOutput{
		Mode:                  "agent",
		Method:                response.Method,
		PromptChars:           len([]rune(prompt)),
		AnswerChars:           len([]rune(response.Answer)),
		UsedADKCustomizedData: usedCustomized,
	}
	if response.Root != nil {
		output.RootChildren = len(response.Root.Children)
		output.RootVisits = response.Root.Visits
		output.SFTSamples = len(tot.ExtractSFTDataset(response.Root))
		output.PreferencePairs = len(tot.ExtractRLHFPreferenceDataset(response.Root, 0.2))
	}
	return output
}

// printDecisionReport 输出完整树和训练样本，方便人工检查 ToT 不是普通一次性问答。
func printDecisionReport(output runOutput, response *tot.Response) {
	report := decisionReport{Output: output, Answer: response.Answer}
	if response.Root != nil {
		snapshot := response.Root.ToSnapshot()
		report.Tree = &snapshot
		report.SFTSamples = tot.ExtractSFTDataset(response.Root)
		report.PreferencePairs = tot.ExtractRLHFPreferenceDataset(response.Root, 0.2)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: marshal report: %v\n", err)
		return
	}
	fmt.Println("\n=== ToT JSON Report ===")
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

// noopModel 只用于 buildReasonConfig 的配置校验，不参与真实推理。
type noopModel struct{}

// Generate 满足 BaseChatModel 接口；如果被误用会直接给出空回复。
func (noopModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("", nil), nil
}

// Stream 满足 BaseChatModel 接口；当前命令不使用流式输出。
func (noopModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", nil)}), nil
}
