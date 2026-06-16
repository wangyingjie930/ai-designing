package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"

	cozeloopobs "ai-designing/observability/cozeloop"
	multimodalfusion "ai-designing/perception/multimodal-fusion"
)

type repeatedFlag []string

// String implements flag.Value for repeated string flags.
func (r *repeatedFlag) String() string {
	return strings.Join(*r, ",")
}

// Set implements flag.Value for repeated string flags.
func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func main() {
	var files repeatedFlag
	var kinds repeatedFlag
	var hints repeatedFlag
	var imageProcessing repeatedFlag
	var task string
	var reportType string
	var industry string
	var audience string
	var goal string
	var includeTrace bool
	var envPath string
	var printConfig bool

	flag.Var(&files, "file", "File path to analyze. Can be repeated.")
	flag.Var(&kinds, "kind", "Optional modality for the matching -file: text, table, log, pdf, image, audio, sql_result. Can be repeated.")
	flag.Var(&hints, "hint", "Optional business hint for the matching -file. Can be repeated.")
	flag.Var(&imageProcessing, "image-processing", "Optional image handling mode for the matching -file: auto, vision, or ocr. Can be repeated.")
	flag.StringVar(&task, "task", "输出核心摘要、数字事实核查、行动要点和风险缺口", "Analysis task.")
	flag.StringVar(&reportType, "report-type", "report", "Report type, for example industry, company, contract, audit, operations.")
	flag.StringVar(&industry, "industry", "", "Industry or domain.")
	flag.StringVar(&audience, "audience", "", "Target audience.")
	flag.StringVar(&goal, "goal", "", "Business goal.")
	flag.BoolVar(&includeTrace, "trace", true, "Include fusion trace in the tool context.")
	flag.StringVar(&envPath, "env", ".env", "Env file path.")
	flag.BoolVar(&printConfig, "print-config", false, "Print effective model config and exit.")
	flag.Parse()

	if err := loadDotEnv(envPath); err != nil {
		exitf("load env: %v", err)
	}
	if len(files) == 0 {
		exitf("missing -file. Example: go run ./cmd/report-agent -file ./report.pdf -hint 行业研报")
	}

	ctx := context.Background()
	modelConfig, err := loadModelConfig()
	if err != nil {
		exitf("load model config: %v", err)
	}
	cozeLoopConfig := cozeloopobs.ConfigFromEnv()
	if printConfig {
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, modelConfig.BaseURL, redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		return
	}

	request := multimodalfusion.ReportAnalysisRequest{
		Files:        buildFileInputs(files, kinds, hints, imageProcessing),
		Task:         task,
		IncludeTrace: includeTrace,
		Report: multimodalfusion.ReportProfile{
			ReportType:     reportType,
			Industry:       industry,
			TargetAudience: audience,
			Goal:           goal,
		},
	}

	cozeLoopConfig, shutdownCozeLoop, err := cozeloopobs.InstallFromEnv(ctx)
	if err != nil {
		exitf("init cozeloop: %v", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()
	fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))

	chatModel, err := newOpenAIChatModel(ctx, modelConfig)
	if err != nil {
		exitf("init chat model: %v", err)
	}

	agent, err := multimodalfusion.NewReportAnalysisAgent(ctx, chatModel, multimodalfusion.AgentConfig{})
	if err != nil {
		exitf("init report agent: %v", err)
	}

	result, err := multimodalfusion.AnalyzeWithAgent(ctx, agent, request)
	if err != nil {
		exitf("run agent: %v", err)
	}

	fmt.Println(result)
}

type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// loadModelConfig reads OpenAI-compatible model settings from env aliases.
func loadModelConfig() (modelConfig, error) {
	apiKey := firstEnv("OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY")
	if apiKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is empty")
	}
	model := firstEnv("LLM_MODEL", "OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}
	baseURL := normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL"))

	return modelConfig{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
	}, nil
}

// newOpenAIChatModel initializes Eino's OpenAI-compatible chat model from config.
func newOpenAIChatModel(ctx context.Context, config modelConfig) (*openai.ChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// buildFileInputs 按索引对齐重复的文件、类型、提示和图片处理参数。
func buildFileInputs(files []string, kinds []string, hints []string, imageProcessing []string) []multimodalfusion.FileInput {
	inputs := make([]multimodalfusion.FileInput, 0, len(files))
	for i, path := range files {
		var kind multimodalfusion.ModalityType
		if i < len(kinds) {
			kind = multimodalfusion.ModalityType(strings.TrimSpace(kinds[i]))
		}
		hint := ""
		if i < len(hints) {
			hint = hints[i]
		}
		var imageMode multimodalfusion.ImageProcessingMode
		if i < len(imageProcessing) {
			imageMode = multimodalfusion.ImageProcessingMode(strings.TrimSpace(imageProcessing[i]))
		}
		inputs = append(inputs, multimodalfusion.FileInput{
			Path:            path,
			Kind:            kind,
			Hint:            hint,
			ImageProcessing: imageMode,
		})
	}
	return inputs
}

// loadDotEnv loads simple KEY=VALUE lines and lets the file override process env.
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

// firstEnv returns the first non-empty value across compatible env names.
func firstEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}

// normalizeOpenAIBaseURL makes local OpenAI-compatible proxies work with go-openai clients.
func normalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// enabledText renders booleans as command-line status text.
func enabledText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// redactKey keeps config debugging useful without printing secrets.
func redactKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// exitf prints a command-line error and exits non-zero.
func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "report-agent: "+format+"\n", args...)
	os.Exit(1)
}
