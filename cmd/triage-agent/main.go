package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"

	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/perception/triage"
)

// main 提供可 prepare-only 验证、也可用 .env API key 实跑的上下文分诊客服 Agent。
func main() {
	var envPath string
	var message string
	var tenantID string
	var userID string
	var sessionID string
	var ticketID string
	var productLine string
	var plan string
	var budget int
	var prepareOnly bool
	var printContext bool

	flag.StringVar(&envPath, "env", ".env", "Env file path.")
	flag.StringVar(&message, "message", "", "Current customer message.")
	flag.StringVar(&tenantID, "tenant", "acme", "Tenant id.")
	flag.StringVar(&userID, "user", "u-finance-admin", "User id.")
	flag.StringVar(&sessionID, "session", "sess-20260613-acme-001", "Support session id.")
	flag.StringVar(&ticketID, "ticket", "T-BILLING-1024", "Support ticket id.")
	flag.StringVar(&productLine, "product-line", "customer-success-suite", "Product line.")
	flag.StringVar(&plan, "plan", "enterprise", "Tenant subscription plan.")
	flag.IntVar(&budget, "budget", 1200, "Context triage token budget for demo.")
	flag.BoolVar(&prepareOnly, "prepare-only", false, "Only run deterministic triage and print JSON; no model call.")
	flag.BoolVar(&printContext, "print-context", false, "Print prepared triage context before the final agent answer.")
	flag.Parse()

	if err := loadDotEnv(envPath); err != nil {
		exitf("load env: %v", err)
	}

	ctx := context.Background()
	runtime := triage.RuntimeContext{
		TenantID:    tenantID,
		UserID:      userID,
		SessionID:   sessionID,
		TicketID:    ticketID,
		ProductLine: productLine,
		Plan:        plan,
	}
	request, docs := triage.DemoScenario(runtime, message, budget)

	cozeLoopConfig, shutdownCozeLoop, err := cozeloopobs.InstallFromEnv(ctx)
	if err != nil {
		exitf("init cozeloop: %v", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()

	modelConfig, err := loadModelConfig()
	if err != nil {
		exitf("load model config: %v", err)
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  modelConfig.APIKey,
		Model:   modelConfig.Model,
		BaseURL: modelConfig.BaseURL,
	})
	if err != nil {
		exitf("init chat model: %v", err)
	}
	agent, err := triage.NewSaaSTriageAgent(ctx, chatModel, triage.AgentConfig{
		TriageConfig:  triage.TriageConfig{Budget: budget},
		ResourceStore: triage.NewInMemoryResourceStore(docs...),
		MaxIterations: 8,
	})
	if err != nil {
		exitf("init triage agent: %v", err)
	}

	fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
	fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
	fmt.Printf("tenant=%s user=%s session=%s ticket=%s budget=%d\n\n", request.Runtime.TenantID, request.Runtime.UserID, request.Runtime.SessionID, request.Runtime.TicketID, budget)
	answer, err := triage.RunSaaSTriageWithAgent(ctx, agent, request)
	if err != nil {
		exitf("run triage agent: %v", err)
	}
	fmt.Println("=== Agent Response ===")
	fmt.Println(answer)
}

type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// loadModelConfig 读取 OpenAI-compatible 模型配置。
func loadModelConfig() (modelConfig, error) {
	apiKey := firstEnv("OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY")
	if apiKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is empty; set it in .env or environment")
	}
	modelName := firstEnv("LLM_MODEL", "OPENAI_MODEL")
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	baseURL := normalizeOpenAIBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL"))
	return modelConfig{APIKey: apiKey, Model: modelName, BaseURL: baseURL}, nil
}

// loadDotEnv 加载简单 KEY=VALUE 格式的 .env，并让文件配置覆盖外部环境。
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

// normalizeOpenAIBaseURL 兼容 go-openai 期望的 /v1 base url。
func normalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// displayBaseURL 让默认官方地址在输出里更明确。
func displayBaseURL(baseURL string) string {
	if baseURL == "" {
		return "default OpenAI endpoint"
	}
	return baseURL
}

// enabledText 把布尔开关渲染成更易读的命令行状态。
func enabledText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// redactKey 打印配置时隐藏密钥。
func redactKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// printJSON 用稳定格式输出 prepare-only 的结构化 trace。
func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		exitf("encode json: %v", err)
	}
}

// exitf 输出命令行错误并返回非零退出码。
func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
