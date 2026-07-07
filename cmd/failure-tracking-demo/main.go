package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"

	"ai-designing/cmd/internal/e2etest"
	failuretracking "ai-designing/memory/failure_tracking"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath = ".env"
	dbEnvKey       = "FAILURE_TRACKING_DB"
)

// modelConfig 保存 demo 运行所需的 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// embeddingRuntimeConfig 保存 failure journal 召回用的 embedding 配置。
type embeddingRuntimeConfig struct {
	Enabled      bool
	Model        string
	BaseURL      string
	EndpointPath string
	Dimension    int
}

// main 运行两轮自然语言业务会话，复现 first record -> later search 的 Agent 行为。
func main() {
	if _, err := runAgent(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 初始化 Eino model、failure journal 和酒店运营 Agent，然后跑两轮普通业务消息。
func runAgent(ctx context.Context) (runAgentTraceOutput, error) {
	if err := loadDotEnv(e2etest.ResolvePath(defaultEnvPath)); err != nil {
		return runAgentTraceOutput{}, fmt.Errorf("load env: %w", err)
	}
	modelConfig, err := loadModelConfig()
	if err != nil {
		return runAgentTraceOutput{}, err
	}
	embeddingConfig, embedder, err := loadEmbeddingConfig()
	if err != nil {
		return runAgentTraceOutput{}, err
	}
	dbPath, cleanup, err := resolveDBPath()
	if err != nil {
		return runAgentTraceOutput{}, err
	}
	defer cleanup()

	cozeLoopConfig, shutdownCozeLoop, err := cozeloopobs.InstallFromEnv(ctx)
	if err != nil {
		return runAgentTraceOutput{}, fmt.Errorf("init cozeloop: %w", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()

	return withRunAgentTrace(ctx, dbPath, len(defaultBusinessMessages()), func(traceCtx context.Context) (runAgentTraceOutput, error) {
		chatModel, err := openai.NewChatModel(traceCtx, &openai.ChatModelConfig{
			APIKey:  modelConfig.APIKey,
			Model:   modelConfig.Model,
			BaseURL: modelConfig.BaseURL,
		})
		if err != nil {
			return runAgentTraceOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		journal, err := failuretracking.NewFailureJournal(traceCtx, failuretracking.Config{
			DBPath:   dbPath,
			Embedder: embedder,
		})
		if err != nil {
			return runAgentTraceOutput{}, err
		}
		defer journal.Close()
		agent, err := failuretracking.NewHotelRecoveryAgent(traceCtx, failuretracking.AgentConfig{
			Model:         chatModel,
			Journal:       journal,
			MaxIterations: 10,
		})
		if err != nil {
			return runAgentTraceOutput{}, err
		}

		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("embedding=%s model=%s base_url=%s dim=%d\n", enabledText(embeddingConfig.Enabled), embeddingConfig.Model, displayBaseURL(embeddingConfig.BaseURL), embeddingConfig.Dimension)
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		fmt.Printf("sqlite=%s\n\n", journal.Path())

		var totalAnswerChars int
		for idx, message := range defaultBusinessMessages() {
			response, err := agent.Query(traceCtx, failuretracking.AgentRequest{Message: message})
			if err != nil {
				return runAgentTraceOutput{}, fmt.Errorf("round %d: %w", idx+1, err)
			}
			totalAnswerChars += len([]rune(response.Message))
			fmt.Printf("=== Round %d User Message ===\n%s\n\n", idx+1, message)
			fmt.Printf("=== Round %d Agent Response ===\n%s\n\n", idx+1, response.Message)
		}
		entryCount, err := journal.Count(traceCtx)
		if err != nil {
			return runAgentTraceOutput{}, err
		}
		fmt.Printf("journal_entries=%d\n", entryCount)
		return runAgentTraceOutput{
			DBPath:      journal.Path(),
			Rounds:      len(defaultBusinessMessages()),
			AnswerChars: totalAnswerChars,
			Entries:     entryCount,
		}, nil
	})
}

// defaultBusinessMessages 提供两轮普通业务消息；工具调用必须由 LLM 自己决定。
func defaultBusinessMessages() []string {
	return []string{
		strings.Join([]string{
			"值班复盘：昨晚 23:40，A 座 1208 的客人到店后等了 32 分钟。值班经理已经复核并批准这条经验进入失败日记召回库，reviewed_by=night-manager-li。",
			"失败现象是 PMS 仍显示 dirty，但 housekeeping app 已显示 clean；前台连续两次口头承诺“再等 10 分钟”，没有切换房间，客诉升级到值班经理。",
			"最终确认是 PMS 房态同步队列卡住，housekeeping 的 clean 事件没有写回 PMS。",
			"修复动作：先核对两个系统的更新时间戳；如果 5 分钟内不同步，立即安排同房型空房，同房型不足就升级高一档房型，并给早餐券或延迟退房；房费减免必须让值班经理确认。",
			"这条复盘没有真实工具调用记录或 SessionState 键；不能编造 tool 或 mechanical_keys，只能基于任务族、失败类别和自然语言事实做弱召回。",
			"请按酒店运营流程处理这条复盘。",
		}, "\n"),
		strings.Join([]string{
			"现场求助：今天 18:20，又有客人带小孩到店，PMS 仍显示 dirty，但 housekeeping app 显示 clean。",
			"同房型只剩 1 间，高一档房型还有 2 间，客人已经等了 18 分钟并要求立即入住或补偿。",
			"请给前台现在应该怎么处理。",
		}, "\n"),
	}
}

// resolveDBPath 默认使用临时空 SQLite，保证每次 demo 都能复现第一轮新经验、第二轮召回。
func resolveDBPath() (string, func(), error) {
	if path := strings.TrimSpace(os.Getenv(dbEnvKey)); path != "" {
		return path, func() {}, nil
	}
	file, err := os.CreateTemp("", "failure-tracking-*.sqlite")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", nil, err
	}
	return path, func() { _ = os.Remove(path) }, nil
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

// loadEmbeddingConfig 读取真实 embedding 配置；缺省时降级为本地 hash embedding。
func loadEmbeddingConfig() (embeddingRuntimeConfig, failuretracking.Embedder, error) {
	model := firstEnv("EMBEDDING_MODEL", "EMBED_MODEL")
	baseURL := firstEnv("EMBEDDING_BASE_URL", "EMBED_OPENAI_BASE_URL", "EMBEDDING_OPENAI_BASE_URL")
	endpointPath := firstEnv("EMBEDDING_ENDPOINT_PATH")
	dimension := parsePositiveInt(firstEnv("EMBEDDING_DIM", "EMBEDDING_DIMENSION"))
	config := embeddingRuntimeConfig{
		Enabled:      model != "" || baseURL != "",
		Model:        model,
		BaseURL:      baseURL,
		EndpointPath: endpointPath,
		Dimension:    dimension,
	}
	if !config.Enabled {
		return config, failuretracking.NewHashEmbedder(64), nil
	}
	apiKey := firstEnv("EMBEDDING_API_KEY", "EMBEDDING_KEY", "EMBED_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_API_KEY")
	embedder, err := failuretracking.NewHTTPEmbedder(failuretracking.HTTPEmbedderConfig{
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
