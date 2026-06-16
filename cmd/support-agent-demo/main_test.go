package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"

	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/perception/compaction"
)

// TestSupportAgentDemoEndToEnd 用固定 P1 支付工单跑通长会话压缩和客服回复的真实 Agent 链路。
func TestSupportAgentDemoEndToEnd(t *testing.T) {
	if !e2etest.Enabled() {
		t.Skipf("跳过 cmd 端到端测试；设置 %s=1 后会使用 .env 真实调用模型", e2etest.EnvName)
	}

	const (
		envPath     = ".env"
		message     = "现在进展如何？下一步应该怎么处理？"
		ticketID    = "T-PAY-10086"
		customerID  = "CUST-42"
		productLine = "payment"
		severity    = "P1"
		slaMinutes  = 20
		printPrompt = false
	)

	if err := loadDotEnv(e2etest.ResolvePath(envPath)); err != nil {
		t.Fatalf("load env: %v", err)
	}
	modelConfig, err := loadModelConfig()
	if err != nil {
		t.Fatalf("load model config: %v", err)
	}

	ctx := context.Background()
	cozeLoopConfig, shutdownCozeLoop, err := cozeloopobs.InstallFromEnv(ctx)
	if err != nil {
		t.Fatalf("init cozeloop: %v", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  modelConfig.APIKey,
		Model:   modelConfig.Model,
		BaseURL: modelConfig.BaseURL,
	})
	if err != nil {
		t.Fatalf("init chat model: %v", err)
	}

	supportContext := compaction.SupportContext{
		TicketID:       ticketID,
		CustomerID:     customerID,
		ProductLine:    productLine,
		Severity:       severity,
		SLADeadlineISO: time.Now().UTC().Add(time.Duration(slaMinutes) * time.Minute).Format(time.RFC3339),
	}
	agent, err := compaction.NewSupportAgent(ctx, compaction.SupportAgentConfig{
		Model: chatModel,
		CompactorConfig: compaction.Config{
			// Demo 故意用小窗口，便于一条命令稳定触发语义压缩。
			ContextBudget:                 1200,
			TargetTokens:                  260,
			PreserveRecent:                2,
			LongObservationTokenThreshold: 60,
			MaxRecentErrors:               2,
			BaseTriggerRatio:              0.65,
			MediumRiskTriggerRatio:        0.58,
			HighRiskTriggerRatio:          0.52,
		},
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatalf("init support agent: %v", err)
	}
	seedLongSupportSession(agent, supportContext)

	fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
	fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
	fmt.Printf("ticket=%s severity=%s sla_deadline=%s\n\n", supportContext.TicketID, supportContext.Severity, supportContext.SLADeadlineISO)

	response, err := agent.Query(ctx, compaction.SupportRequest{
		Context: supportContext,
		Message: message,
	})
	if err != nil {
		t.Fatalf("run support agent: %v", err)
	}

	printCompactionEvent(response.PromptView.TokenPressure, response.CompactionEvent)
	fmt.Println("\n=== Handoff Summary ===")
	fmt.Println(response.HandoffSummary.JSON())
	fmt.Println("\n=== Agent Response ===")
	fmt.Println(response.Message)
	if printPrompt {
		fmt.Println("\n=== Prompt View ===")
		fmt.Println(response.PromptView.SystemPrompt(""))
	}
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

// seedLongSupportSession 注入一段长客服历史，演示压缩如何保护错误证据。
func seedLongSupportSession(agent *compaction.SupportAgent, ctx compaction.SupportContext) {
	agent.AddTurn(ctx, compaction.NewTurn(compaction.RoleUser, compaction.TurnKindMessage,
		"客户反馈支付一直失败，订单 ORD-20260612-001 无法完成，页面偶发 502。", compaction.WithTokens(80)))
	agent.AddTurn(ctx, compaction.NewTurn(compaction.RoleAssistant, compaction.TurnKindAction,
		"查询订单 ORD-20260612-001、支付网关、最近 30 分钟错误日志。", compaction.WithTokens(60)))
	agent.AddTurn(ctx, compaction.NewTurn(compaction.RoleToolResult, compaction.TurnKindObservation,
		strings.Repeat("gateway access log status=200 latency=35ms trace=normal; ", 120),
		compaction.WithTokens(700),
		compaction.WithHandle("log://payment/gateway/access/ORD-20260612-001")))
	agent.AddTurn(ctx, compaction.NewTurn(compaction.RoleToolResult, compaction.TurnKindObservation,
		"Traceback: timeout in /app/payment/gateway.go:88; upstream status=502; queue_depth=347; retry path A failed.",
		compaction.WithTokens(80),
		compaction.WithHandle("log://payment/gateway/error/ORD-20260612-001")))
	agent.AddTurn(ctx, compaction.NewTurn(compaction.RoleAssistant, compaction.TurnKindDecision,
		"已判断不是客户本地网络问题，也不应只建议用户刷新或重复支付。", compaction.WithTokens(35)))
	agent.AddTurn(ctx, compaction.NewTurn(compaction.RoleUser, compaction.TurnKindMessage,
		"客户还在线，要求尽快恢复；如果 20 分钟内无结论，需要升级二线。", compaction.WithTokens(30)))
}

// printCompactionEvent 打印中间件测压结果和本轮压缩事件，便于确认压缩是否由 token 阈值触发。
func printCompactionEvent(pressure compaction.TokenPressure, event *compaction.CompactionEvent) {
	fmt.Println("=== Token Pressure ===")
	fmt.Printf("tokens=%d trigger=%d budget=%d trigger_ratio=%.2f target=%d exceeds_trigger=%v should_compact=%v\n",
		pressure.TotalTokens,
		pressure.TriggerTokens,
		pressure.ContextBudget,
		pressure.TriggerRatio,
		pressure.TargetTokens,
		pressure.ExceedsTrigger,
		pressure.ShouldCompact,
	)

	fmt.Println("\n=== Compaction Event ===")
	if event == nil {
		fmt.Println("no compaction triggered by token-limit middleware")
		return
	}
	fmt.Printf("level=%d turns=%d->%d tokens=%d->%d ratio=%.2f errors=%d/%d represented=%d handoff=%v\n",
		event.Level,
		event.TurnsBefore,
		event.TurnsAfter,
		event.TokensBefore,
		event.TokensAfter,
		event.CompressionRatio(),
		event.ErrorTracesOut,
		event.ErrorTracesIn,
		event.ErrorTracesRepresented,
		event.HandoffRecommended,
	)
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
