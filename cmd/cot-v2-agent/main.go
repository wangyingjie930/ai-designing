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

	jsonschema "github.com/eino-contrib/jsonschema"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"

	"ai-designing/cmd/internal/e2etest"
	cotv2 "ai-designing/reasoning/cot_v2"
)

const (
	defaultEnvPath      = ".env"
	defaultQuestionPath = "reasoning/cot_v2/examples/payroll_anomaly_prompt.txt"
	defaultScenario     = "薪酬异常审核"
)

// runConfig 保存命令行参数；正常运行必须通过模型和工程侧证据源完成，不再内置 Step 或证据。
type runConfig struct {
	EnvPath      string
	Question     string
	QuestionFile string
	Scenario     string
	PrepareOnly  bool
	PrintJSON    bool
	MaxTokens    int
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，避免默认输出塞满完整业务证据。
type runOutput struct {
	Mode          string `json:"mode"`
	Scenario      string `json:"scenario"`
	QuestionChars int    `json:"question_chars"`
	Drafts        int    `json:"drafts"`
	Steps         int    `json:"steps"`
	StopReason    string `json:"stop_reason"`
	FinalDecision string `json:"final_decision"`
	Verified      bool   `json:"verified"`
}

type cotV2Report struct {
	Output runOutput           `json:"output"`
	Drafts cotv2.StepDraftList `json:"drafts"`
	Trace  cotv2.ClaimTrace    `json:"trace"`
}

type chatModelFactory func(context.Context, modelConfig, *jsonschema.Schema) (model.BaseChatModel, error)

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig, js *jsonschema.Schema) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:        "cot_v2_step_drafts",
				Description: "Candidate verifiable claim drafts for cot_v2.",
				Strict:      true,
				JSONSchema:  js,
			},
		},
	})
}

func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

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
		fmt.Println("=== CoT v2 Question ===")
		fmt.Println(question)
		return output, nil
	}

	modelConfig, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	draftSchema := cotv2.StepDraftListJSONSchema()
	chatModel, err := newChatModel(ctx, modelConfig, draftSchema)
	if err != nil {
		return runOutput{}, fmt.Errorf("init structured chat model: %w", err)
	}
	drafts, err := generateStepDrafts(ctx, chatModel, config, question)
	if err != nil {
		return runOutput{}, err
	}
	collector := newEvidenceCollector(question)
	trace, err := buildTraceWithEvidence(ctx, "cot-v2-agent", drafts, collector)
	if err != nil {
		return runOutput{}, err
	}
	trace = cotv2.NewTraceRunner(newValidatorRegistry()).Run(trace)
	output = summarizeTrace(config.Scenario, question, drafts, trace)

	fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
	printAgentResult(output, trace)
	if config.PrintJSON {
		printCOTV2Report(output, drafts, trace)
	}
	return output, nil
}

func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("cot-v2-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		EnvPath:      defaultEnvPath,
		QuestionFile: defaultQuestionPath,
	}
	fs.StringVar(&config.EnvPath, "env", defaultEnvPath, "env file path")
	fs.StringVar(&config.Question, "message", "", "question text for the CoT v2 agent")
	fs.StringVar(&config.QuestionFile, "message-file", defaultQuestionPath, "file containing the question")
	fs.StringVar(&config.Scenario, "scenario", "", "non-coding scenario name")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print question without calling model")
	fs.BoolVar(&config.PrintJSON, "json", false, "print machine-readable report")
	fs.IntVar(&config.MaxTokens, "max-tokens", 0, "max tokens for draft generation")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(config.EnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.Scenario = firstNonEmpty(config.Scenario, os.Getenv("COT_V2_SCENARIO"), defaultScenario)
	if config.MaxTokens <= 0 {
		config.MaxTokens = parsePositiveInt(os.Getenv("COT_V2_MAX_TOKENS"))
	}
	return config, nil
}

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

func generateStepDrafts(ctx context.Context, chatModel model.BaseChatModel, config runConfig, question string) (cotv2.StepDraftList, error) {
	if chatModel == nil {
		return cotv2.StepDraftList{}, fmt.Errorf("chat model is required")
	}
	messages := []*einoschema.Message{
		einoschema.SystemMessage(defaultSystemPrompt()),
		einoschema.UserMessage(buildDraftPrompt(config.Scenario, question)),
	}
	opts := make([]model.Option, 0, 1)
	if config.MaxTokens > 0 {
		opts = append(opts, model.WithMaxTokens(config.MaxTokens))
	}
	resp, err := chatModel.Generate(ctx, messages, opts...)
	if err != nil {
		return cotv2.StepDraftList{}, err
	}
	if resp == nil {
		return cotv2.StepDraftList{}, fmt.Errorf("model returned nil response")
	}
	return cotv2.ParseStepDraftList(resp.Content)
}

func defaultSystemPrompt() string {
	return strings.Join([]string{
		"你是一个生产版 CoT 命题草稿生成器。",
		"只输出符合 JSON Schema 的 StepDraftList，不要输出 Markdown 或额外解释。",
		"每个 step 只表达一个可验证命题；证据是否存在、验证器是否通过，由程序侧决定。",
		"不要声称自己已经验证了数据库、政策或审批单；只能写 suggested_evidence_query。",
	}, "\n")
}

func buildDraftPrompt(scenario string, question string) string {
	return strings.Join([]string{
		"请把下面任务拆成候选 StepDraft。",
		"要求覆盖 observe、derive、verify、decide 等必要步骤；每一步都要给 suggested_subject 和 suggested_predicate。",
		"如果某一步需要计算，请把 suggested_evidence_query 写成工程工具可执行的查询意图。",
		"",
		"场景：" + strings.TrimSpace(scenario),
		"任务：",
		strings.TrimSpace(question),
	}, "\n")
}

func summarizeTrace(scenario string, question string, drafts cotv2.StepDraftList, trace cotv2.ClaimTrace) runOutput {
	return runOutput{
		Mode:          "agent",
		Scenario:      scenario,
		QuestionChars: len([]rune(question)),
		Drafts:        len(drafts.Steps),
		Steps:         len(trace.Steps),
		StopReason:    trace.StopReason,
		FinalDecision: trace.FinalDecision,
		Verified:      trace.StopReason == "all_required_claims_verified",
	}
}

func printAgentResult(output runOutput, trace cotv2.ClaimTrace) {
	fmt.Println("\n=== CoT v2 Claim Trace Summary ===")
	fmt.Printf("steps=%d verified=%t stop_reason=%s final_decision=%s\n", output.Steps, output.Verified, output.StopReason, output.FinalDecision)
	for _, step := range trace.Steps {
		fmt.Printf("- %s [%s/%s] %s\n", step.StepID, step.Kind, step.Status, step.ClaimText)
	}
}

func printCOTV2Report(output runOutput, drafts cotv2.StepDraftList, trace cotv2.ClaimTrace) {
	report := cotV2Report{Output: output, Drafts: drafts, Trace: trace}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: marshal report: %v\n", err)
		return
	}
	fmt.Println("\n=== CoT v2 JSON Report ===")
	fmt.Println(string(data))
}

func loadModelConfig() (modelConfig, error) {
	apiKey := firstEnv("OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY")
	if apiKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is empty; set it in .env or environment")
	}
	modelName := firstEnv("LLM_MODEL", "OPENAI_MODEL")
	if modelName == "" {
		return modelConfig{}, fmt.Errorf("LLM_MODEL is empty; set it in .env or environment")
	}
	baseURL := normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL", "BASE_URL"))
	return modelConfig{APIKey: apiKey, Model: modelName, BaseURL: baseURL}, nil
}

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

func firstEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parsePositiveInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func normalizeOpenAIBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" || strings.HasSuffix(value, "/v1") {
		return value
	}
	return value + "/v1"
}

func displayBaseURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<default>"
	}
	return value
}

func redactKey(value string) string {
	if value == "" {
		return "<empty>"
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
