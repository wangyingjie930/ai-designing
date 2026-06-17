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
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"

	"ai-designing/cmd/internal/e2etest"
	hierarchical "ai-designing/memory/hierarchical"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath      = ".env"
	defaultRoundsPath   = "memory/hierarchical/examples/travel_rounds.txt"
	defaultDBPath       = "output/hierarchical-memory-agent.sqlite"
	defaultDemoBudget   = 80
	dbEnvKey            = "HIERARCHICAL_MEMORY_DB"
	scopeEnvKey         = "HIERARCHICAL_MEMORY_SCOPE"
	workingBudgetEnvKey = "HIERARCHICAL_WORKING_BUDGET"
)

// runConfig 保存 CLI 参数解析后的运行配置，主流程不直接读取 flags。
type runConfig struct {
	DBPath          string
	Scope           string
	WorkingBudget   int
	Messages        []string
	PrepareOnly     bool
	AddContent      string
	AddSource       string
	AddImportance   *float64
	RetrieveQuery   string
	RetrieveK       int
	Consolidate     bool
	InstructionText string
}

// modelConfig 保存 Agent 运行所需的 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// embeddingRuntimeConfig 保存真实 embedding 服务连接信息。
type embeddingRuntimeConfig struct {
	Model        string
	BaseURL      string
	EndpointPath string
	Dimension    int
}

// runOutput 是命令执行后的稳定摘要，便于 E2E 测试和人工排查。
type runOutput struct {
	DBPath      string `json:"db_path"`
	Scope       string `json:"scope"`
	Mode        string `json:"mode"`
	Rounds      int    `json:"rounds"`
	Working     int    `json:"working"`
	Session     int    `json:"session"`
	LongTerm    int    `json:"long_term"`
	AnswerChars int    `json:"answer_chars"`
}

// main 启动旅行规划 Agent，或用 prepare-only 验证三层记忆和 SQLite 写入。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装 .env、真实 embedding、SQLite memory 和可选 Eino ADK Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	embeddingConfig, embedder, err := loadEmbeddingConfig()
	if err != nil {
		return runOutput{}, err
	}
	memory, err := hierarchical.NewHierarchicalMemory(ctx, hierarchical.Config{
		DBPath:        config.DBPath,
		Scope:         config.Scope,
		WorkingBudget: config.WorkingBudget,
		Embedder:      embedder,
	})
	if err != nil {
		return runOutput{}, err
	}
	defer memory.Close()

	if config.PrepareOnly {
		if err := runPrepareOnly(ctx, memory, config); err != nil {
			return runOutput{}, err
		}
		printRuntimeConfig(modelConfig{}, embeddingConfig, memory, false)
		if err := printMemorySnapshot(ctx, memory); err != nil {
			return runOutput{}, err
		}
		context := memory.Context()
		longTerm, err := memory.LongTerm(ctx, 0)
		if err != nil {
			return runOutput{}, err
		}
		return runOutput{
			DBPath:   memory.Path(),
			Scope:    memory.Scope(),
			Mode:     "prepare-only",
			Rounds:   0,
			Working:  len(context.Working),
			Session:  len(context.Session),
			LongTerm: len(longTerm),
		}, nil
	}

	if len(config.Messages) == 0 {
		return runOutput{}, fmt.Errorf("message is required unless -prepare-only is set")
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

	return withRunAgentTrace(ctx, memory.Path(), memory.Scope(), len(config.Messages), memory.WorkingBudget(), func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := openai.NewChatModel(traceCtx, &openai.ChatModelConfig{
			APIKey:  modelConfig.APIKey,
			Model:   modelConfig.Model,
			BaseURL: modelConfig.BaseURL,
		})
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		agent, err := hierarchical.NewTravelPlanningAgent(traceCtx, hierarchical.AgentConfig{
			Model:         chatModel,
			Memory:        memory,
			Instruction:   config.InstructionText,
			MaxIterations: 10,
		})
		if err != nil {
			return runOutput{}, err
		}

		printRuntimeConfig(modelConfig, embeddingConfig, memory, true)
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))

		totalAnswerChars, err := runAgentRounds(traceCtx, agent, memory, config.Messages)
		if err != nil {
			return runOutput{}, err
		}
		longTerm, err := memory.LongTerm(traceCtx, 0)
		if err != nil {
			return runOutput{}, err
		}
		context := memory.Context()
		return runOutput{
			DBPath:      memory.Path(),
			Scope:       memory.Scope(),
			Mode:        "agent",
			Rounds:      len(config.Messages),
			Working:     len(context.Working),
			Session:     len(context.Session),
			LongTerm:    len(longTerm),
			AnswerChars: totalAnswerChars,
		}, nil
	})
}

// parseRunConfig 读取 flags、.env 和外部文本文件，避免业务参数写死在代码里。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("hierarchical-memory-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dbPath string
	var scope string
	var message string
	var messageFile string
	var roundsFile string
	var addImportance float64
	var instructionFile string
	config := runConfig{RetrieveK: 0}
	fs.StringVar(&dbPath, "db", "", "SQLite path; fallback env HIERARCHICAL_MEMORY_DB")
	fs.StringVar(&scope, "scope", "", "memory scope; fallback env HIERARCHICAL_MEMORY_SCOPE")
	fs.IntVar(&config.WorkingBudget, "working-budget", 0, "working memory token budget; fallback env HIERARCHICAL_WORKING_BUDGET")
	fs.StringVar(&message, "message", "", "natural language travel-planning message for the ADK agent")
	fs.StringVar(&messageFile, "message-file", "", "file containing the natural language message")
	fs.StringVar(&roundsFile, "rounds-file", "", "file containing multiple messages as JSON array or blocks separated by a line with ---")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "update/read memory without calling the chat model")
	fs.StringVar(&config.AddContent, "add", "", "prepare-only memory content to add into working tier")
	fs.StringVar(&config.AddSource, "add-source", "", "source for -add, such as user/tool/reflection/file")
	fs.Float64Var(&addImportance, "importance", -1, "importance for -add, from 0.0 to 1.0")
	fs.StringVar(&config.RetrieveQuery, "retrieve", "", "prepare-only query for long-term retrieval")
	fs.IntVar(&config.RetrieveK, "k", 0, "top-k for retrieval")
	fs.BoolVar(&config.Consolidate, "consolidate", false, "persist important working/session memories to SQLite long-term tier")
	fs.StringVar(&instructionFile, "instruction-file", "", "optional file overriding the default travel-planning instruction")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(defaultEnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}

	config.DBPath = firstNonEmpty(dbPath, os.Getenv(dbEnvKey))
	if config.DBPath == "" {
		config.DBPath = e2etest.ResolvePath(defaultDBPath)
	}
	config.Scope = firstNonEmpty(scope, os.Getenv(scopeEnvKey))
	if config.WorkingBudget <= 0 {
		config.WorkingBudget = parsePositiveInt(os.Getenv(workingBudgetEnvKey))
	}
	if config.WorkingBudget <= 0 {
		config.WorkingBudget = defaultDemoBudget
	}
	if strings.TrimSpace(message) != "" {
		config.Messages = append(config.Messages, strings.TrimSpace(message))
	}
	if messageFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(messageFile))
		if err != nil {
			return runConfig{}, err
		}
		if strings.TrimSpace(string(content)) != "" {
			config.Messages = append(config.Messages, strings.TrimSpace(string(content)))
		}
	}
	if roundsFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(roundsFile))
		if err != nil {
			return runConfig{}, err
		}
		rounds, err := parseRoundMessages(string(content))
		if err != nil {
			return runConfig{}, err
		}
		config.Messages = append(config.Messages, rounds...)
	}
	if len(config.Messages) == 0 && !config.PrepareOnly {
		rounds, err := loadRoundMessages(defaultRoundsPath)
		if err != nil {
			return runConfig{}, err
		}
		config.Messages = rounds
	}
	if addImportance >= 0 {
		config.AddImportance = &addImportance
	}
	if instructionFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(instructionFile))
		if err != nil {
			return runConfig{}, err
		}
		config.InstructionText = strings.TrimSpace(string(content))
	}
	return config, nil
}

// loadRoundMessages 读取多轮 demo 输入，GoLand 无参数运行时也能直接看到循环效果。
func loadRoundMessages(path string) ([]string, error) {
	content, err := os.ReadFile(e2etest.ResolvePath(path))
	if err != nil {
		return nil, err
	}
	return parseRoundMessages(string(content))
}

// runAgentRounds 连续多轮复用同一个 Agent 和同一个 memory，展示记忆产生和迁移过程。
func runAgentRounds(ctx context.Context, agent *hierarchical.Agent, memory *hierarchical.HierarchicalMemory, messages []string) (int, error) {
	var totalAnswerChars int
	for index, message := range messages {
		fmt.Printf("\n\n=== Round %d User Message ===\n%s\n", index+1, message)
		response, err := agent.Query(ctx, hierarchical.AgentRequest{Message: message})
		if err != nil {
			return totalAnswerChars, fmt.Errorf("round %d: %w", index+1, err)
		}
		totalAnswerChars += len([]rune(response.Message))
		fmt.Printf("\n=== Round %d Agent Response ===\n%s\n", index+1, response.Message)
		fmt.Printf("\n=== Round %d Memory State ===\n", index+1)
		if err := printMemorySnapshot(ctx, memory); err != nil {
			return totalAnswerChars, err
		}
	}
	return totalAnswerChars, nil
}

// runPrepareOnly 执行确定性的 add/retrieve/consolidate，便于先验证 SQLite 和 embedding 路径。
func runPrepareOnly(ctx context.Context, memory *hierarchical.HierarchicalMemory, config runConfig) error {
	if strings.TrimSpace(config.AddContent) != "" {
		if strings.TrimSpace(config.AddSource) == "" {
			return fmt.Errorf("-add-source is required when -add is set")
		}
		if _, err := memory.Add(ctx, hierarchical.AddRequest{
			Content:    config.AddContent,
			Source:     config.AddSource,
			Importance: config.AddImportance,
		}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(config.RetrieveQuery) != "" {
		response, err := memory.Retrieve(ctx, hierarchical.RetrieveRequest{
			Query: config.RetrieveQuery,
			K:     config.RetrieveK,
		})
		if err != nil {
			return err
		}
		fmt.Println("=== Retrieve Results ===")
		for index, result := range response.Results {
			fmt.Printf("%d. score=%.4f source=%s content=%s\n", index+1, result.Score, result.Entry.Source, result.Text)
		}
	}
	if config.Consolidate {
		response, err := memory.Consolidate(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("consolidated=%d\n", response.Persisted)
	}
	return nil
}

// printRuntimeConfig 输出本轮真实模型、embedding、SQLite 和 scope 信息，同时隐藏密钥。
func printRuntimeConfig(model modelConfig, embedding embeddingRuntimeConfig, memory *hierarchical.HierarchicalMemory, includeModel bool) {
	if includeModel {
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", model.Model, displayBaseURL(model.BaseURL), redactKey(model.APIKey))
	}
	fmt.Printf("embedding_model=%s\nembedding_base_url=%s\nembedding_endpoint_path=%s\nembedding_dim=%d\n",
		embedding.Model,
		displayBaseURL(embedding.BaseURL),
		embedding.EndpointPath,
		embedding.Dimension,
	)
	fmt.Printf("sqlite=%s\nscope=%s\nworking_budget=%d\n", memory.Path(), memory.Scope(), memory.WorkingBudget())
}

// printMemorySnapshot 输出完整三层快照，方便肉眼观察不同轮次的记忆状态。
func printMemorySnapshot(ctx context.Context, memory *hierarchical.HierarchicalMemory) error {
	longTerm, err := memory.LongTerm(ctx, 0)
	if err != nil {
		return err
	}
	printMemoryContext(memory.Context())
	fmt.Println("\n=== Long-term Memory ===")
	for index, entry := range longTerm {
		fmt.Printf("%d. source=%s importance=%.2f access=%d content=%s\n", index+1, entry.Source, entry.Importance, entry.AccessCount, entry.Content)
	}
	if len(longTerm) == 0 {
		fmt.Println("(empty)")
	}
	return nil
}

// printMemoryContext 用稳定格式展示当前 working/session tier，便于人工确认预算淘汰。
func printMemoryContext(context hierarchical.ContextResponse) {
	fmt.Println("\n=== Working Memory ===")
	for index, entry := range context.Working {
		fmt.Printf("%d. source=%s importance=%.2f tokens=%d content=%s\n", index+1, entry.Source, entry.Importance, entry.TokenCount, entry.Content)
	}
	if len(context.Working) == 0 {
		fmt.Println("(empty)")
	}
	fmt.Println("\n=== Session Memory ===")
	for index, entry := range context.Session {
		fmt.Printf("%d. source=%s importance=%.2f tokens=%d content=%s\n", index+1, entry.Source, entry.Importance, entry.TokenCount, entry.Content)
	}
	if len(context.Session) == 0 {
		fmt.Println("(empty)")
	}
	fmt.Printf("\nworking_tokens=%d/%d\n", context.WorkingTokenCount, context.WorkingBudget)
}

// parseRoundMessages 支持 JSON 字符串数组，或用单独一行 --- 分隔的多轮自然语言消息。
func parseRoundMessages(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("rounds file is empty")
	}
	var jsonMessages []string
	if err := json.Unmarshal([]byte(text), &jsonMessages); err == nil {
		return normalizeMessages(jsonMessages), nil
	}
	var rounds []string
	var current []string
	flush := func() {
		message := strings.TrimSpace(strings.Join(current, "\n"))
		if message != "" {
			rounds = append(rounds, message)
		}
		current = nil
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			flush()
			continue
		}
		current = append(current, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	rounds = normalizeMessages(rounds)
	if len(rounds) == 0 {
		return nil, fmt.Errorf("rounds file has no messages")
	}
	return rounds, nil
}

// normalizeMessages 清理空消息，保持用户给定轮次顺序。
func normalizeMessages(messages []string) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message != "" {
			out = append(out, message)
		}
	}
	return out
}

// loadModelConfig 读取 OpenAI-compatible 模型配置；模型名和密钥都必须由环境提供。
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

// loadEmbeddingConfig 读取真实 embedding 配置，缺少任一必要项都直接报错。
func loadEmbeddingConfig() (embeddingRuntimeConfig, hierarchical.Embedder, error) {
	model := firstEnv("EMBEDDING_MODEL", "EMBED_MODEL")
	baseURL := firstEnv("EMBEDDING_BASE_URL", "EMBED_OPENAI_BASE_URL", "EMBEDDING_OPENAI_BASE_URL")
	endpointPath := firstEnv("EMBEDDING_ENDPOINT_PATH")
	dimension := parsePositiveInt(firstEnv("EMBEDDING_DIM", "EMBEDDING_DIMENSION"))
	config := embeddingRuntimeConfig{
		Model:        model,
		BaseURL:      baseURL,
		EndpointPath: endpointPath,
		Dimension:    dimension,
	}
	apiKey := firstEnv("EMBEDDING_API_KEY", "EMBEDDING_KEY", "EMBED_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_API_KEY")
	embedder, err := hierarchical.NewHTTPEmbedder(hierarchical.HTTPEmbedderConfig{
		APIKey:       apiKey,
		Model:        model,
		BaseURL:      baseURL,
		EndpointPath: endpointPath,
		Dimension:    dimension,
		Timeout:      45 * time.Second,
	})
	if err != nil {
		return config, nil, err
	}
	return config, embedder, nil
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

// enabledText 把布尔开关渲染成命令行状态，避免输出 true/false 含义不直观。
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
