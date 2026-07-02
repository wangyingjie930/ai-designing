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

	guardrail "ai-designing/action/guardrail"
	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath      = ".env"
	defaultScenarioName = "家装售后排期外发防护"
	defaultMessage      = "客户售后工单 HS-1001 反馈厨房水槽漏水，希望今天安排师傅上门，并短信通知客户预计到达时间。"
)

// runConfig 保存 CLI 参数解析后的运行配置，避免业务流程直接读取 flags。
type runConfig struct {
	Message         string
	PrepareOnly     bool
	ApproveExternal bool
}

// modelConfig 保存 Agent 运行所需的 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// chatModelFactory 允许命令测试替换真实模型，避免普通 go test 访问网络。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

// runOutput 是命令执行后的摘要，trace 和测试都只看这个稳定结构。
type runOutput struct {
	Mode            string `json:"mode"`
	Scenario        string `json:"scenario"`
	QueryChars      int    `json:"query_chars"`
	AnswerChars     int    `json:"answer_chars"`
	AllowedTools    int    `json:"allowed_tools"`
	ApproveExternal bool   `json:"approve_external"`
}

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// main 运行家装售后排期 guardrail demo。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装配置、trace、模型和 Guardrail ADK Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	policy := guardrail.DefaultHomeServicePolicy()
	if config.PrepareOnly {
		printPolicySummary(policy, config)
		return runOutput{
			Mode:            "prepare-only",
			Scenario:        defaultScenarioName,
			QueryChars:      len([]rune(config.Message)),
			AllowedTools:    len(policy.AllowedTools),
			ApproveExternal: config.ApproveExternal,
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

	return withRunAgentTrace(ctx, len([]rune(config.Message)), config.ApproveExternal, func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		agent, err := guardrail.NewHomeServiceAgent(traceCtx, guardrail.HomeServiceAgentConfig{
			Model:    chatModel,
			Policy:   policy,
			Approver: buildApprover(config.ApproveExternal),
		})
		if err != nil {
			return runOutput{}, err
		}
		response, err := agent.Query(traceCtx, guardrail.HomeServiceRequest{Message: config.Message})
		if err != nil {
			return runOutput{}, err
		}
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		fmt.Printf("scenario=%s approve_external=%v\n", defaultScenarioName, config.ApproveExternal)
		fmt.Printf("\n=== Guardrail Agent 回复 ===\n%s\n", response.Message)
		return runOutput{
			Mode:            "agent",
			Scenario:        defaultScenarioName,
			QueryChars:      len([]rune(config.Message)),
			AnswerChars:     len([]rune(response.Message)),
			AllowedTools:    len(policy.AllowedTools),
			ApproveExternal: config.ApproveExternal,
		}, nil
	})
}

// parseRunConfig 读取命令参数；默认消息让 main_test 或 IDE 可以直接点击运行。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("guardrail-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{Message: defaultMessage}
	var messageFile string
	fs.StringVar(&config.Message, "message", defaultMessage, "natural language home-service request")
	fs.StringVar(&messageFile, "message-file", "", "file containing the natural language request")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print guardrail policy summary without calling model")
	fs.BoolVar(&config.ApproveExternal, "approve-external", false, "demo switch: approve high-risk external notification")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(defaultEnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	if messageFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(messageFile))
		if err != nil {
			return runConfig{}, err
		}
		config.Message = string(content)
	}
	config.Message = strings.TrimSpace(config.Message)
	if config.Message == "" {
		return runConfig{}, fmt.Errorf("message is required")
	}
	return config, nil
}

// buildApprover 根据 demo 开关创建审批器；默认不批准真实外发动作。
func buildApprover(approveExternal bool) guardrail.Approver {
	return guardrail.ApproverFunc(func(ctx context.Context, req guardrail.ApprovalRequest) (guardrail.ApprovalDecision, error) {
		if approveExternal {
			return guardrail.ApprovalDecision{Approved: true, Reason: "demo approval granted"}, nil
		}
		return guardrail.ApprovalDecision{Approved: false, Reason: "demo approval denied; require customer-service confirmation"}, nil
	})
}

// printPolicySummary 输出可人工检查的策略摘要，不触发模型或外部服务。
func printPolicySummary(policy guardrail.SafetyPolicy, config runConfig) {
	fmt.Printf("scenario=%s\n", defaultScenarioName)
	fmt.Printf("mode=prepare-only approve_external=%v\n", config.ApproveExternal)
	fmt.Printf("allowed_tools=%s\n", strings.Join(policy.AllowedTools, ","))
	fmt.Printf("blocked_patterns=%d sensitive_patterns=%d\n", len(policy.BlockedPatterns), len(policy.SensitivePatterns))
	fmt.Printf("default_message_chars=%d\n", len([]rune(config.Message)))
}

// loadModelConfig 从 .env 或当前环境读取模型配置。
func loadModelConfig() (modelConfig, error) {
	config := modelConfig{
		APIKey:  strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Model:   strings.TrimSpace(firstEnv("LLM_MODEL", "OPENAI_MODEL")),
		BaseURL: normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL")),
	}
	if config.APIKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is required")
	}
	if config.Model == "" {
		return modelConfig{}, fmt.Errorf("LLM_MODEL or OPENAI_MODEL is required")
	}
	return config, nil
}

// loadDotEnv 读取简单 KEY=VALUE 格式；本地 demo 一切以 .env 为准，覆盖当前进程同名变量。
func loadDotEnv(path string) error {
	if strings.TrimSpace(path) == "" {
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
		if key != "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
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
