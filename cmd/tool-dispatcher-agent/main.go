package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	tooldispatcher "ai-designing/action/tool_dispatcher"
	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath        = ".env"
	defaultScenarioName   = "客户成功续费风险工具分诊"
	defaultMessage        = "客户 ACME-42 本月要续费，但最近核心功能用量下降，还卡着发票问题。请给客户成功经理一份处理建议。"
	envDispatcherMessage  = "TOOL_DISPATCHER_MESSAGE"
	envDispatcherPrepOnly = "TOOL_DISPATCHER_PREPARE_ONLY"
)

// runConfig 保存 CLI 参数解析后的运行配置，cmd 层不直接承载业务规则。
type runConfig struct {
	Message     string
	PrepareOnly bool
	EnvPath     string
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// chatModelFactory 允许命令测试替换真实模型，避免普通 go test 访问网络。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

// runOutput 是命令执行后的稳定摘要，trace 和测试只依赖这些低敏字段。
type runOutput struct {
	Mode         string `json:"mode"`
	Scenario     string `json:"scenario"`
	QueryChars   int    `json:"query_chars"`
	AnswerChars  int    `json:"answer_chars"`
	DynamicTools int    `json:"dynamic_tools"`
	LoadedTools  int    `json:"loaded_tools"`
}

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// main 运行客户成功续费风险工具分诊 demo。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装配置、trace、模型和 tool dispatcher ADK Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	dynamicTools, err := tooldispatcher.NewRenewalRiskTools()
	if err != nil {
		return runOutput{}, err
	}
	loadedTools := len(tooldispatcher.DefaultRenewalToolSelection())
	if config.PrepareOnly {
		printPrepareSummary(config, len(dynamicTools), loadedTools)
		return runOutput{
			Mode:         "prepare-only",
			Scenario:     defaultScenarioName,
			QueryChars:   len([]rune(config.Message)),
			DynamicTools: len(dynamicTools),
			LoadedTools:  loadedTools,
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

	return withRunAgentTrace(ctx, len([]rune(config.Message)), len(dynamicTools), func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		agent, err := tooldispatcher.NewRenewalRiskAgent(traceCtx, tooldispatcher.RenewalRiskAgentConfig{
			Model:        chatModel,
			DynamicTools: dynamicTools,
		})
		if err != nil {
			return runOutput{}, err
		}
		response, err := agent.Query(traceCtx, tooldispatcher.RenewalRiskRequest{Message: config.Message})
		if err != nil {
			return runOutput{}, err
		}
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		fmt.Printf("scenario=%s dynamic_tools=%d loaded_tools=%d\n", defaultScenarioName, len(dynamicTools), loadedTools)
		fmt.Printf("\n=== Tool Dispatcher Agent 回复 ===\n%s\n", response.Message)
		return runOutput{
			Mode:         "agent",
			Scenario:     defaultScenarioName,
			QueryChars:   len([]rune(config.Message)),
			AnswerChars:  len([]rune(response.Message)),
			DynamicTools: len(dynamicTools),
			LoadedTools:  loadedTools,
		}, nil
	})
}

// parseRunConfig 读取命令参数；默认消息让 main_test 或 IDE 可以直接点击运行。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("tool-dispatcher-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{Message: defaultMessage, EnvPath: defaultEnvPath}
	var messageFile string
	fs.StringVar(&config.EnvPath, "env-file", defaultEnvPath, "dotenv file used as the source of truth")
	fs.StringVar(&config.Message, "message", defaultMessage, "natural language renewal-risk request")
	fs.StringVar(&messageFile, "message-file", "", "file containing the natural language request")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print tool dispatcher summary without calling model")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	dotenv, err := loadDotEnv(e2etest.ResolvePath(config.EnvPath))
	if err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	if messageFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(messageFile))
		if err != nil {
			return runConfig{}, err
		}
		config.Message = string(content)
	}
	applyDotEnvRunConfig(&config, dotenv)
	config.Message = strings.TrimSpace(config.Message)
	if config.Message == "" {
		return runConfig{}, fmt.Errorf("message is required")
	}
	return config, nil
}

// printPrepareSummary 输出可人工检查的工具分诊摘要，不触发模型或外部服务。
func printPrepareSummary(config runConfig, dynamicTools int, loadedTools int) {
	fmt.Printf("scenario=%s\n", defaultScenarioName)
	fmt.Printf("mode=prepare-only dynamic_tools=%d default_loaded_tools=%d\n", dynamicTools, loadedTools)
	fmt.Printf("default_selection=%s\n", strings.Join(tooldispatcher.DefaultRenewalToolSelection(), ","))
	fmt.Printf("default_message_chars=%d\n", len([]rune(config.Message)))
}

// loadModelConfig 从已加载的 .env 环境读取模型配置。
func loadModelConfig() (modelConfig, error) {
	config := modelConfig{
		APIKey:  strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Model:   strings.TrimSpace(firstEnv("LLM_MODEL", "OPENAI_MODEL")),
		BaseURL: normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL", "BASE_URL")),
	}
	if config.APIKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is required")
	}
	if config.Model == "" {
		return modelConfig{}, fmt.Errorf("LLM_MODEL or OPENAI_MODEL is required")
	}
	return config, nil
}

// applyDotEnvRunConfig 让 .env 中的运行配置覆盖命令行参数，保持 demo 输入以文件为准。
func applyDotEnvRunConfig(config *runConfig, values map[string]string) {
	if config == nil || len(values) == 0 {
		return
	}
	if message := strings.TrimSpace(values[envDispatcherMessage]); message != "" {
		config.Message = message
	}
	if prepareOnly, ok := parseBoolEnv(values[envDispatcherPrepOnly]); ok {
		config.PrepareOnly = prepareOnly
	}
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
		return "missing"
	}
	if len(key) <= 6 {
		return "***"
	}
	return key[:3] + "***" + key[len(key)-3:]
}

// enabledText 把 bool 转成稳定的命令行文字。
func enabledText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
