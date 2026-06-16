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
	localmem0 "ai-designing/memory/mem0"
	cozeloopobs "ai-designing/observability/cozeloop"
)

// TestMem0AgentDemoEndToEnd 用固定记忆样例跑通 LLM、Eino ADK、SQLite 写入和 search 召回链路。
func TestMem0AgentDemoEndToEnd(t *testing.T) {
	const (
		envPath     = ".env"
		dbPath      = "memory/mem0/mem0.sqlite"
		message     = "你知道我不喜欢吃什么吗"
		userID      = "mem0-demo-user"
		agentID     = "mem0-demo-agent"
		runID       = "mem0-demo-run"
		printSearch = true
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
	resolvedDBPath := e2etest.ResolvePath(dbPath)
	memory, err := localmem0.NewMemory(ctx, localmem0.Config{
		DBPath:   resolvedDBPath,
		Model:    chatModel,
		Embedder: localmem0.NewFakeEmbedder(64),
	})
	if err != nil {
		t.Fatalf("init mem0 memory: %v", err)
	}
	defer memory.Close()

	agent, err := localmem0.NewAgent(ctx, localmem0.AgentConfig{
		Model:         chatModel,
		Memory:        memory,
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("init mem0 agent: %v", err)
	}

	fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
	fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
	fmt.Printf("sqlite=%s\nuser=%s agent=%s run=%s\n\n", resolvedDBPath, userID, agentID, runID)

	resp, err := agent.Query(ctx, localmem0.AgentRequest{
		UserID:  userID,
		AgentID: agentID,
		RunID:   runID,
		Message: message,
	})
	if err != nil {
		t.Fatalf("run mem0 agent: %v", err)
	}
	fmt.Println("=== Agent Response ===")
	fmt.Println(resp.Message)

	if printSearch {
		printPersistedSearch(ctx, memory, message, userID, agentID, runID)
	}
}

// printPersistedSearch 在 Agent 跑完后直接查询 SQLite，确认工具写入可以被 search 召回。
func printPersistedSearch(ctx context.Context, memory *localmem0.Memory, message, userID, agentID, runID string) {
	threshold := 0.0
	search, err := memory.Search(ctx, localmem0.SearchRequest{
		Query: message,
		Filters: map[string]any{
			"user_id":  userID,
			"agent_id": agentID,
			"run_id":   runID,
		},
		TopK:      5,
		Threshold: &threshold,
		Explain:   true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: search persisted memory: %v\n", err)
		return
	}
	fmt.Println("\n=== Persisted Memory Search ===")
	if len(search.Results) == 0 {
		fmt.Println("no persisted memories matched")
		return
	}
	for idx, item := range search.Results {
		fmt.Printf("%d. score=%.3f id=%s memory=%s\n", idx+1, item.Score, item.ID, item.Memory)
	}
}

// modelConfig 保存 demo 运行所需的 OpenAI-compatible 模型连接信息。
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
