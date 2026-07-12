package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/google/uuid"

	"ai-designing/cmd/internal/e2etest"
	claudeautomemory "ai-designing/memory/claude_auto_memory"
)

const (
	defaultEnvPath         = ".env"
	defaultMemoryDir       = "output/claude-auto-memory"
	defaultRoundsPath      = "memory/claude_auto_memory/examples/interview_rounds.txt"
	extractionDrainTimeout = 60 * time.Second
)

// runConfig 保存自动记忆面试命令的文件路径和运行模式。
type runConfig struct {
	EnvPath       string
	MemoryDir     string
	RoundsFile    string
	PrepareOnly   bool
	SessionID     string
	Resume        bool
	SessionConfig claudeautomemory.SessionMemoryConfig
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的低敏摘要，测试和 trace 不依赖完整业务文本。
type runOutput struct {
	Mode           string `json:"mode"`
	MemoryDir      string `json:"memory_dir"`
	SessionID      string `json:"session_id"`
	Resumed        bool   `json:"resumed"`
	Rounds         int    `json:"rounds"`
	Recalled       int    `json:"recalled"`
	Written        int    `json:"written"`
	SessionUpdates int    `json:"session_updates"`
	Compactions    int    `json:"compactions"`
	Warnings       int    `json:"warnings"`
	AnswerChars    int    `json:"answer_chars"`
}

// chatModelFactory 允许命令测试替换真实模型而不访问网络。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: config.APIKey, Model: config.Model, BaseURL: config.BaseURL,
	})
}

// main 启动 Claude Code 风格自动记忆三轮面试演示。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装 Auto/Session Store、四个隔离模型角色和固定顺序 Runner。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	store, err := claudeautomemory.NewStore(config.MemoryDir)
	if err != nil {
		return runOutput{}, fmt.Errorf("init memory store: %w", err)
	}
	transcriptStore, err := claudeautomemory.NewTranscriptStore(config.MemoryDir, config.SessionID)
	if err != nil {
		return runOutput{}, fmt.Errorf("init transcript store: %w", err)
	}
	sessionStore, err := claudeautomemory.NewSessionStore(config.MemoryDir, config.SessionID)
	if err != nil {
		return runOutput{}, fmt.Errorf("init session memory store: %w", err)
	}
	existingTranscript, err := transcriptStore.Load(ctx)
	if err != nil {
		return runOutput{}, fmt.Errorf("load transcript: %w", err)
	}
	if config.Resume && len(existingTranscript) == 0 {
		return runOutput{}, errors.New("cannot resume an empty session")
	}
	if !config.Resume && len(existingTranscript) > 0 {
		return runOutput{}, errors.New("session already exists; pass -resume to continue it")
	}
	if config.PrepareOnly {
		output := runOutput{Mode: "prepare-only", MemoryDir: config.MemoryDir, SessionID: config.SessionID}
		fmt.Printf("mode=%s memory_dir=%s session_id=%s\n", output.Mode, output.MemoryDir, output.SessionID)
		fmt.Println("indexes=private/MEMORY.md,team/MEMORY.md")
		fmt.Printf("session_summary=%s\n", sessionStore.SummaryPath())
		return output, nil
	}

	rounds, err := loadRoundMessages(config.RoundsFile)
	if err != nil {
		return runOutput{}, err
	}
	connection, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	chatModel, err := newChatModel(ctx, connection)
	if err != nil {
		return runOutput{}, fmt.Errorf("init chat model: %w", err)
	}
	runner, err := buildRunner(ctx, store, transcriptStore, sessionStore, config, chatModel)
	if err != nil {
		return runOutput{}, err
	}
	fmt.Printf("model=%s base_url=%s api_key=%s\n", connection.Model, displayBaseURL(connection.BaseURL), redactKey(connection.APIKey))
	fmt.Printf("memory_dir=%s session_id=%s resumed=%t rounds=%d\n", store.Root(), config.SessionID, config.Resume, len(rounds))
	return runRounds(ctx, config.MemoryDir, runner, rounds, config.Resume)
}

// buildRunner 让同一个底层模型通过隔离 Prompt 承担四个互不串线的角色。
func buildRunner(ctx context.Context, store *claudeautomemory.Store, transcriptStore *claudeautomemory.TranscriptStore, sessionStore *claudeautomemory.SessionStore, config runConfig, chatModel model.BaseChatModel) (*claudeautomemory.Runner, error) {
	extractorModel, err := claudeautomemory.NewLLMExtractor(chatModel)
	if err != nil {
		return nil, err
	}
	selector, err := claudeautomemory.NewLLMSelector(chatModel)
	if err != nil {
		return nil, err
	}
	chatAgent, err := claudeautomemory.NewLLMChatAgent(chatModel)
	if err != nil {
		return nil, err
	}
	extractor, err := claudeautomemory.NewExtractor(store, extractorModel)
	if err != nil {
		return nil, err
	}
	recaller, err := claudeautomemory.NewRecaller(store, selector)
	if err != nil {
		return nil, err
	}
	sessionSummarizer, err := claudeautomemory.NewLLMSessionSummarizer(chatModel)
	if err != nil {
		return nil, err
	}
	estimator := claudeautomemory.RoughTokenEstimator{}
	updater, err := claudeautomemory.NewSessionMemoryUpdater(sessionStore, sessionSummarizer, estimator, config.SessionConfig)
	if err != nil {
		return nil, err
	}
	sessionScheduler, err := claudeautomemory.NewSessionScheduler(updater)
	if err != nil {
		return nil, err
	}
	compactor, err := claudeautomemory.NewSessionCompactor(sessionStore, sessionScheduler, estimator, config.SessionConfig)
	if err != nil {
		return nil, err
	}
	return claudeautomemory.NewRunnerWithSession(ctx, recaller, chatAgent, extractor, claudeautomemory.RunnerSessionConfig{
		SessionID: config.SessionID, TranscriptStore: transcriptStore,
		Scheduler: sessionScheduler, Compactor: compactor, Resume: config.Resume,
	})
}

// runRounds 顺序执行多轮对话，并只输出可面试解释的记忆边界 trace。
func runRounds(ctx context.Context, memoryDir string, runner *claudeautomemory.Runner, rounds []string, resumed bool) (runOutput, error) {
	output := runOutput{Mode: "agent", MemoryDir: memoryDir, SessionID: runner.SessionID(), Resumed: resumed, Rounds: len(rounds)}
	for index, message := range rounds {
		result, err := runner.RunTurn(ctx, message)
		if err != nil {
			return runOutput{}, fmt.Errorf("run round %d: %w", index+1, err)
		}
		fmt.Printf("\n=== Round %d ===\nuser: %s\nassistant: %s\n", index+1, message, result.Answer)
		fmt.Printf("recalled=%s\n", formatRecords(result.Recalled))
		fmt.Printf("compacted=%t\n", result.Compacted)
		for _, warning := range result.Warnings {
			fmt.Printf("memory_warning=%v\n", warning)
		}
		// 主回答已经写到 stdout 后再等待后台抽取；等待只影响脚本进入下一轮，不计入回答延迟。
		drainCtx, cancelDrain := context.WithTimeout(ctx, extractionDrainTimeout)
		drained, drainErr := runner.Drain(drainCtx)
		cancelDrain()
		if drainErr != nil {
			fmt.Printf("written=none\nmemory_warning=drain extraction: %v\n", drainErr)
			output.Warnings++
		} else {
			fmt.Printf("written=%s\n", formatRecords(drained.Written))
			for _, warning := range drained.Warnings {
				fmt.Printf("memory_warning=%v\n", warning)
			}
			output.Written += len(drained.Written)
			output.Warnings += len(drained.Warnings)
		}
		// Session Summary 与 Auto Memory 独立等待和计数，避免把两种生命周期混成一个结果。
		sessionCtx, cancelSession := context.WithTimeout(ctx, extractionDrainTimeout)
		sessionDrained, sessionErr := runner.WaitSession(sessionCtx)
		cancelSession()
		if sessionErr != nil {
			fmt.Printf("session_updated=false\nmemory_warning=wait session memory: %v\n", sessionErr)
			output.Warnings++
		} else {
			fmt.Printf("session_updated=%t summarized_through=%s\n", sessionDrained.Updates > 0, shortMessageID(sessionDrained.SummarizedThrough))
			for _, warning := range sessionDrained.Warnings {
				fmt.Printf("memory_warning=%v\n", warning)
			}
			output.SessionUpdates += sessionDrained.Updates
			output.Warnings += len(sessionDrained.Warnings)
		}
		output.Recalled += len(result.Recalled)
		if result.Compacted {
			output.Compactions++
		}
		output.Warnings += len(result.Warnings)
		output.AnswerChars += len([]rune(result.Answer))
	}
	return output, nil
}

// parseRunConfig 读取 flags 并先加载 .env，使模型配置以文件为本地事实源。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("claude-auto-memory-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{SessionConfig: claudeautomemory.DefaultSessionMemoryConfig()}
	fs.StringVar(&config.EnvPath, "env-file", defaultEnvPath, "dotenv file containing model config")
	fs.StringVar(&config.MemoryDir, "memory-dir", defaultMemoryDir, "private/team Markdown memory root")
	fs.StringVar(&config.RoundsFile, "rounds-file", defaultRoundsPath, "multi-round interview scenario")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "create and inspect memory storage without calling a model")
	fs.StringVar(&config.SessionID, "session-id", "", "stable session identifier; generated when omitted")
	fs.BoolVar(&config.Resume, "resume", false, "resume an existing session transcript and summary")
	fs.IntVar(&config.SessionConfig.MinimumTokensToInit, "session-init-tokens", config.SessionConfig.MinimumTokensToInit, "tokens required for the first session summary")
	fs.IntVar(&config.SessionConfig.MinimumTokensBetweenUpdates, "session-update-tokens", config.SessionConfig.MinimumTokensBetweenUpdates, "token growth required between session updates")
	fs.IntVar(&config.SessionConfig.CompactTokens, "compact-tokens", config.SessionConfig.CompactTokens, "context tokens that trigger session compaction")
	fs.IntVar(&config.SessionConfig.MinimumRecentMessages, "session-recent-messages", config.SessionConfig.MinimumRecentMessages, "minimum recent messages preserved after compaction")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	config.EnvPath = e2etest.ResolvePath(config.EnvPath)
	config.MemoryDir = e2etest.ResolvePath(config.MemoryDir)
	config.RoundsFile = e2etest.ResolvePath(config.RoundsFile)
	config.SessionID = strings.TrimSpace(config.SessionID)
	if config.SessionID == "" {
		config.SessionID = uuid.NewString()
	}
	if err := loadDotEnv(config.EnvPath); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	return config, nil
}

// shortMessageID 只在 trace 中暴露 UUID 前八位，完整边界仍保存在 state.json。
func shortMessageID(messageID string) string {
	if len(messageID) <= 8 {
		return messageID
	}
	return messageID[:8]
}

// loadRoundMessages 读取默认或用户指定的多轮面试输入。
func loadRoundMessages(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rounds file: %w", err)
	}
	return parseRoundMessages(string(content))
}

// parseRoundMessages 支持 JSON 字符串数组或单独一行 --- 分隔的自然语言轮次。
func parseRoundMessages(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("rounds file is empty")
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
		if strings.TrimSpace(scanner.Text()) == "---" {
			flush()
			continue
		}
		current = append(current, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	if len(rounds) == 0 {
		return nil, errors.New("rounds file has no messages")
	}
	return rounds, nil
}

// normalizeMessages 清理 JSON 数组里的空消息并保持原顺序。
func normalizeMessages(messages []string) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		if message = strings.TrimSpace(message); message != "" {
			out = append(out, message)
		}
	}
	return out
}

// loadModelConfig 从已加载环境读取仓库通用的 OpenAI-compatible 配置。
func loadModelConfig() (modelConfig, error) {
	config := modelConfig{
		APIKey: firstEnv("OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY"),
		Model:  firstEnv("LLM_MODEL", "OPENAI_MODEL"),
		BaseURL: normalizeOpenAIBaseURL(firstEnv(
			"LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL", "BASE_URL",
		)),
	}
	if config.APIKey == "" {
		return modelConfig{}, errors.New("OPENAI_API_KEY is required")
	}
	if config.Model == "" {
		return modelConfig{}, errors.New("LLM_MODEL or OPENAI_MODEL is required")
	}
	return config, nil
}

// loadDotEnv 加载简单 KEY=VALUE 文件，并让文件值覆盖当前进程环境。
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

// normalizeOpenAIBaseURL 兼容 go-openai 需要的 /v1 根路径。
func normalizeOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// displayBaseURL 让默认模型端点在 trace 中可读。
func displayBaseURL(baseURL string) string {
	if baseURL == "" {
		return "default"
	}
	return baseURL
}

// redactKey 隐藏模型密钥，只保留排查配置所需的首尾字符。
func redactKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// formatRecords 只打印作用域和主题，不泄露完整记忆正文。
func formatRecords(records []claudeautomemory.MemoryRecord) string {
	if len(records) == 0 {
		return "none"
	}
	items := make([]string, 0, len(records))
	for _, record := range records {
		items = append(items, fmt.Sprintf("%s/%s", record.Ref.Scope, record.Ref.Topic))
	}
	return strings.Join(items, ",")
}
