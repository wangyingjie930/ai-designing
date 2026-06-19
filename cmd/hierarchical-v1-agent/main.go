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

	"ai-designing/cmd/internal/e2etest"
	hierarchicalv1 "ai-designing/memory/hierarchical_v1"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath    = ".env"
	defaultRoundsPath = "memory/hierarchical_v1/examples/meal_rounds.txt"
	defaultDBPath     = "output/hierarchical-v1-memory.sqlite"
	dbEnvKey          = "HIERARCHICAL_V1_MEMORY_DB"
)

// runConfig 保存 CLI 参数解析后的运行配置，主流程不直接读取 flags。
type runConfig struct {
	DBPath          string
	Messages        []string
	PrepareOnly     bool
	WriteLayer      hierarchicalv1.MemoryLayer
	WriteSource     hierarchicalv1.MemorySource
	WriteKey        string
	WriteValue      map[string]any
	EvidenceRefs    []string
	Confidence      *float64
	TokenEstimate   int
	ValidUntil      string
	ReadKey         string
	PromoteKey      string
	PromoteLayer    hierarchicalv1.MemoryLayer
	InstructionText string
}

// modelConfig 保存 Agent 运行所需的 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，便于测试和人工排查。
type runOutput struct {
	DBPath       string         `json:"db_path"`
	Mode         string         `json:"mode"`
	Rounds       int            `json:"rounds"`
	AnswerChars  int            `json:"answer_chars"`
	ContextChars int            `json:"context_chars"`
	LayerItems   map[string]int `json:"layer_items"`
}

// main 启动家庭膳食规划 Agent，或用 prepare-only 验证五层记忆策略。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装 .env、五层 memory 和可选 Eino ADK Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	memory, err := hierarchicalv1.NewHierarchicalMemory(hierarchicalv1.Config{DBPath: config.DBPath})
	if err != nil {
		return runOutput{}, err
	}
	defer memory.Close()
	if config.PrepareOnly {
		return withRunAgentTrace(ctx, memory.DBPath(), 0, "prepare-only", func(traceCtx context.Context) (runOutput, error) {
			if err := runPrepareOnly(traceCtx, memory, config); err != nil {
				return runOutput{}, err
			}
			if err := printMemorySnapshot(memory); err != nil {
				return runOutput{}, err
			}
			return buildRunOutput("prepare-only", memory, 0, 0)
		})
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

	return withRunAgentTrace(ctx, memory.DBPath(), len(config.Messages), "agent", func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := openai.NewChatModel(traceCtx, &openai.ChatModelConfig{
			APIKey:  modelConfig.APIKey,
			Model:   modelConfig.Model,
			BaseURL: modelConfig.BaseURL,
		})
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		agent, err := hierarchicalv1.NewMealPlanningAgent(traceCtx, hierarchicalv1.AgentConfig{
			Model:         chatModel,
			Memory:        memory,
			Instruction:   config.InstructionText,
			MaxIterations: 10,
		})
		if err != nil {
			return runOutput{}, err
		}
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		totalAnswerChars, err := runAgentRounds(traceCtx, agent, memory, config.Messages)
		if err != nil {
			return runOutput{}, err
		}
		return buildRunOutput("agent", memory, len(config.Messages), totalAnswerChars)
	})
}

// parseRunConfig 读取 flags、.env 和外部文本文件，避免业务参数写死在代码里。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("hierarchical-v1-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var message string
	var messageFile string
	var roundsFile string
	var writeLayer string
	var writeSource string
	var writeValue string
	var evidence string
	var confidence float64
	var promoteLayer string
	var instructionFile string
	var dbPath string
	config := runConfig{WriteSource: hierarchicalv1.MemorySourceHuman}
	fs.StringVar(&dbPath, "db", "", "SQLite path for non-scratchpad memories; fallback env HIERARCHICAL_V1_MEMORY_DB")
	fs.StringVar(&message, "message", "", "natural language meal-planning message for the ADK agent")
	fs.StringVar(&messageFile, "message-file", "", "file containing one natural language message")
	fs.StringVar(&roundsFile, "rounds-file", "", "file containing multiple messages as JSON array or blocks separated by ---")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "run memory operations without calling the chat model")
	fs.StringVar(&writeLayer, "write-layer", "", "prepare-only write target layer: policy/project/user/task/scratchpad")
	fs.StringVar(&writeSource, "write-source", string(config.WriteSource), "prepare-only source: human/tool/agent_inference/verified_trace/failure_review")
	fs.StringVar(&config.WriteKey, "write-key", "", "prepare-only key to write")
	fs.StringVar(&writeValue, "write-value", "", "prepare-only value as JSON object or plain text")
	fs.StringVar(&evidence, "evidence", "", "comma-separated evidence refs for prepare-only write")
	fs.Float64Var(&confidence, "confidence", -1, "confidence for prepare-only write; default 1.0")
	fs.IntVar(&config.TokenEstimate, "token-estimate", 0, "token estimate for layer budget selection")
	fs.StringVar(&config.ValidUntil, "valid-until", "", "optional ISO time when the memory expires")
	fs.StringVar(&config.ReadKey, "read-key", "", "prepare-only key to inspect")
	fs.StringVar(&config.PromoteKey, "promote-key", "", "scratchpad key to propose into target layer")
	fs.StringVar(&promoteLayer, "promote-layer", "", "target layer for -promote-key")
	fs.StringVar(&instructionFile, "instruction-file", "", "optional file overriding the default meal-planning instruction")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(defaultEnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.DBPath = firstNonEmpty(dbPath, os.Getenv(dbEnvKey))
	if strings.TrimSpace(config.DBPath) == "" {
		config.DBPath = e2etest.ResolvePath(defaultDBPath)
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
		rounds, err := loadRoundMessages(roundsFile)
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
	if strings.TrimSpace(writeLayer) != "" {
		config.WriteLayer = hierarchicalv1.MemoryLayer(strings.TrimSpace(writeLayer))
	}
	if strings.TrimSpace(writeSource) != "" {
		config.WriteSource = hierarchicalv1.MemorySource(strings.TrimSpace(writeSource))
	}
	if strings.TrimSpace(writeValue) != "" {
		value, err := parseWriteValue(writeValue)
		if err != nil {
			return runConfig{}, err
		}
		config.WriteValue = value
	}
	config.EvidenceRefs = parseCSV(evidence)
	if confidence >= 0 {
		config.Confidence = &confidence
	}
	if strings.TrimSpace(promoteLayer) != "" {
		config.PromoteLayer = hierarchicalv1.MemoryLayer(strings.TrimSpace(promoteLayer))
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

// runPrepareOnly 执行确定性的 write/read/promote/assemble，便于先验证附件里的策略语义。
func runPrepareOnly(ctx context.Context, memory *hierarchicalv1.HierarchicalMemory, config runConfig) error {
	if strings.TrimSpace(config.WriteKey) != "" || len(config.WriteValue) > 0 || config.WriteLayer != "" {
		if !config.WriteLayer.Valid() {
			return fmt.Errorf("-write-layer must be one of policy/project/user/task/scratchpad")
		}
		if !config.WriteSource.Valid() {
			return fmt.Errorf("-write-source must be one of human/tool/agent_inference/verified_trace/failure_review")
		}
		if strings.TrimSpace(config.WriteKey) == "" {
			return fmt.Errorf("-write-key is required when writing")
		}
		if len(config.WriteValue) == 0 {
			return fmt.Errorf("-write-value is required when writing")
		}
		response, err := memory.Write(ctx, hierarchicalv1.WriteRequest{
			Key:           config.WriteKey,
			Value:         config.WriteValue,
			Layer:         config.WriteLayer,
			Source:        config.WriteSource,
			EvidenceRefs:  config.EvidenceRefs,
			Confidence:    config.Confidence,
			TokenEstimate: config.TokenEstimate,
			ValidUntil:    config.ValidUntil,
		})
		if err != nil {
			return err
		}
		fmt.Printf("wrote layer=%s source=%s key=%s\n", response.Entry.Layer, response.Entry.Source, response.Entry.Key)
	}
	if strings.TrimSpace(config.PromoteKey) != "" {
		if !config.PromoteLayer.Valid() {
			return fmt.Errorf("-promote-layer must be one of policy/project/user/task/scratchpad")
		}
		response, err := memory.ProposeScratchpadByKey(ctx, hierarchicalv1.ProposeScratchpadRequest{
			Key:         config.PromoteKey,
			TargetLayer: config.PromoteLayer,
		})
		if err != nil {
			return err
		}
		fmt.Printf("proposed key=%s target_layer=%s source=%s\n", response.Entry.Key, response.Entry.Layer, response.Entry.Source)
	}
	if strings.TrimSpace(config.ReadKey) != "" {
		entry, ok := memory.EntryByKey(config.ReadKey)
		fmt.Printf("read key=%s found=%v entry=%+v\n", config.ReadKey, ok, entry)
	}
	return nil
}

// runAgentRounds 连续多轮复用同一个 Agent 和同一个 memory，展示记忆产生和上下文选择过程。
func runAgentRounds(ctx context.Context, agent *hierarchicalv1.Agent, memory *hierarchicalv1.HierarchicalMemory, messages []string) (int, error) {
	var totalAnswerChars int
	for index, message := range messages {
		fmt.Printf("\n\n=== Round %d User Message ===\n%s\n", index+1, message)
		response, err := agent.Query(ctx, hierarchicalv1.AgentRequest{Message: message})
		if err != nil {
			return totalAnswerChars, fmt.Errorf("round %d: %w", index+1, err)
		}
		totalAnswerChars += len([]rune(response.Message))
		fmt.Printf("\n=== Round %d Agent Response ===\n%s\n", index+1, response.Message)
		fmt.Printf("\n=== Round %d Memory State ===\n", index+1)
		if err := printMemorySnapshot(memory); err != nil {
			return totalAnswerChars, err
		}
	}
	return totalAnswerChars, nil
}

// printMemorySnapshot 输出已选上下文和每层策略状态，方便人工确认预算与证据规则。
func printMemorySnapshot(memory *hierarchicalv1.HierarchicalMemory) error {
	contextResponse, err := memory.AssemblePromptContext()
	if err != nil {
		return err
	}
	fmt.Println("\n=== Selected Context ===")
	if strings.TrimSpace(contextResponse.Context) == "" {
		fmt.Println("(empty)")
	} else {
		fmt.Println(contextResponse.Context)
	}
	report := memory.HealthReport()
	fmt.Println("\n=== Layer Health ===")
	for _, layer := range hierarchicalv1.AssembleOrder() {
		health := report.Layers[layer.String()]
		fmt.Printf("%s items=%d backend=%s ttl=%s budget=%d allow_agent_write=%v require_evidence=%v\n",
			layer,
			health.EntryCount,
			health.Backend,
			formatTTL(health.TTLSeconds),
			health.TokenBudget,
			health.AllowAgentWrite,
			health.RequireEvidence,
		)
	}
	return nil
}

// buildRunOutput 生成稳定摘要，避免测试依赖完整命令行文本。
func buildRunOutput(mode string, memory *hierarchicalv1.HierarchicalMemory, rounds int, answerChars int) (runOutput, error) {
	contextResponse, err := memory.AssemblePromptContext()
	if err != nil {
		return runOutput{}, err
	}
	report := memory.HealthReport()
	items := map[string]int{}
	for layer, health := range report.Layers {
		items[layer] = health.EntryCount
	}
	return runOutput{
		DBPath:       memory.DBPath(),
		Mode:         mode,
		Rounds:       rounds,
		AnswerChars:  answerChars,
		ContextChars: len([]rune(contextResponse.Context)),
		LayerItems:   items,
	}, nil
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

// loadRoundMessages 读取多轮 demo 输入，GoLand 无参数运行时也能直接看到循环效果。
func loadRoundMessages(path string) ([]string, error) {
	content, err := os.ReadFile(e2etest.ResolvePath(path))
	if err != nil {
		return nil, err
	}
	return parseRoundMessages(string(content))
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

// parseWriteValue 把 CLI 文本转成 tool 友好的 JSON object。
func parseWriteValue(text string) (map[string]any, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("write value is empty")
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(text), &object); err == nil && len(object) > 0 {
		return object, nil
	}
	return map[string]any{"text": text}, nil
}

// parseCSV 解析逗号分隔的证据引用，空项会被忽略。
func parseCSV(text string) []string {
	parts := strings.Split(text, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
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

// formatTTL 把 TTL 秒数渲染成命令行易读文本。
func formatTTL(ttl *float64) string {
	if ttl == nil {
		return "permanent"
	}
	return strconv.FormatFloat(*ttl, 'f', -1, 64) + "s"
}
