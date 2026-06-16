package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"

	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/perception/triage"
)

// TestTriageAgentEndToEnd 用固定 SaaS 工单样例跑通上下文分诊 Agent 的真实模型链路。
func TestTriageAgentEndToEnd(t *testing.T) {
	const (
		envPath     = ".env"
		message     = ""
		tenantID    = "acme"
		userID      = "u-finance-admin"
		sessionID   = "sess-20260613-acme-001"
		ticketID    = "T-BILLING-1024"
		productLine = "customer-success-suite"
		plan        = "enterprise"
		budget      = 1200
	)

	if err := loadDotEnv(e2etest.ResolvePath(envPath)); err != nil {
		t.Fatalf("load env: %v", err)
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
		t.Fatalf("init cozeloop: %v", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()

	modelConfig, err := loadModelConfig()
	if err != nil {
		t.Fatalf("load model config: %v", err)
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  modelConfig.APIKey,
		Model:   modelConfig.Model,
		BaseURL: modelConfig.BaseURL,
	})
	if err != nil {
		t.Fatalf("init chat model: %v", err)
	}
	agent, err := triage.NewSaaSTriageAgent(ctx, chatModel, triage.AgentConfig{
		TriageConfig:  triage.TriageConfig{Budget: budget},
		ResourceStore: triage.NewInMemoryResourceStore(docs...),
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("init triage agent: %v", err)
	}

	fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
	fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
	fmt.Printf("tenant=%s user=%s session=%s ticket=%s budget=%d\n\n", request.Runtime.TenantID, request.Runtime.UserID, request.Runtime.SessionID, request.Runtime.TicketID, budget)
	answer, err := triage.RunSaaSTriageWithAgent(ctx, agent, request)
	if err != nil {
		t.Fatalf("run triage agent: %v", err)
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
