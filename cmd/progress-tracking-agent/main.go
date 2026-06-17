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

	"ai-designing/cmd/internal/e2etest"
	progresstracking "ai-designing/memory/progress_tracking"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath             = ".env"
	dbEnvKey                   = "PROGRESS_TRACKING_DB"
	planIDEnvKey               = "PROGRESS_TRACKING_PLAN_ID"
	generatedPlanTriggerRounds = 3
)

// runConfig 保存 CLI 参数解析后的运行配置，避免业务流程直接读取 flags。
type runConfig struct {
	DBPath         string
	PlanID         string
	Message        string
	PrepareOnly    bool
	Items          []string
	StartIndex     int
	CompleteIndex  int
	CompleteResult string
	CompleteFiles  []string
	FailIndex      int
	FailError      string
}

// modelConfig 保存 Agent 运行所需的 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// agentQuerier 抽出主流程需要的最小 Agent 能力，便于测试触发轮次而不调用真实模型。
type agentQuerier interface {
	Query(context.Context, progresstracking.AgentRequest) (*progresstracking.AgentResponse, error)
}

// runOutput 是命令执行后的摘要，测试和人工排查都只看这个稳定结构。
type runOutput struct {
	DBPath      string `json:"db_path"`
	PlanID      string `json:"plan_id"`
	Mode        string `json:"mode"`
	Items       int    `json:"items"`
	AnswerChars int    `json:"answer_chars"`
}

// main 运行线下活动筹备 Agent，或用 prepare-only 只验证 SQLite checkpoint。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装配置、SQLite tracker 和可选 Eino ADK Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	tracker, err := progresstracking.NewProgressTracker(ctx, progresstracking.Config{
		DBPath: config.DBPath,
		PlanID: config.PlanID,
	})
	if err != nil {
		return runOutput{}, err
	}
	defer tracker.Close()

	if config.PrepareOnly {
		if err := runPrepareOnly(ctx, tracker, config); err != nil {
			return runOutput{}, err
		}
		printTrackerState(tracker)
		return runOutput{
			DBPath: tracker.Path(),
			PlanID: tracker.PlanID(),
			Mode:   "prepare-only",
			Items:  len(tracker.Items()),
		}, nil
	}

	if strings.TrimSpace(config.Message) == "" {
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

	return withRunAgentTrace(ctx, tracker.Path(), tracker.PlanID(), generatedPlanTriggerRounds, func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := openai.NewChatModel(traceCtx, &openai.ChatModelConfig{
			APIKey:  modelConfig.APIKey,
			Model:   modelConfig.Model,
			BaseURL: modelConfig.BaseURL,
		})
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		agent, err := progresstracking.NewEventPlanningAgent(traceCtx, progresstracking.AgentConfig{
			Model:         chatModel,
			Tracker:       tracker,
			MaxIterations: 10,
		})
		if err != nil {
			return runOutput{}, err
		}
		response, err := agent.Query(traceCtx, progresstracking.AgentRequest{Message: config.Message})
		if err != nil {
			return runOutput{}, err
		}

		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		fmt.Printf("\n=== 计划产生 ===\n%s\n", response.Message)
		printTrackerState(tracker)
		triggeredChars, err := triggerGeneratedPlanRounds(traceCtx, agent, tracker)
		if err != nil {
			return runOutput{}, err
		}
		return runOutput{
			DBPath:      tracker.Path(),
			PlanID:      tracker.PlanID(),
			Mode:        "agent",
			Items:       len(tracker.Items()),
			AnswerChars: len([]rune(response.Message)) + triggeredChars,
		}, nil
	})
}

// parseRunConfig 读取 flags、.env 和输入文件，所有运行参数都从外部传入。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("progress-tracking-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dbPath string
	var planID string
	var message string
	var messageFile string
	var itemsText string
	var itemsFile string
	var completeFiles string
	config := runConfig{
		StartIndex:    -1,
		CompleteIndex: -1,
		FailIndex:     -1,
	}
	fs.StringVar(&dbPath, "db", "", "SQLite path; fallback env PROGRESS_TRACKING_DB")
	fs.StringVar(&planID, "plan-id", "", "plan id; fallback env PROGRESS_TRACKING_PLAN_ID")
	fs.StringVar(&message, "message", "", "natural language event-planning message for the ADK agent")
	fs.StringVar(&messageFile, "message-file", "", "file containing the natural language message")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "only update/read SQLite tracker without calling model")
	fs.StringVar(&itemsText, "items", "", "prepare-only task list as JSON array, newline text, or semicolon-separated text")
	fs.StringVar(&itemsFile, "items-file", "", "prepare-only task list file")
	fs.IntVar(&config.StartIndex, "start-index", -1, "prepare-only 0-based task index to mark in_progress")
	fs.IntVar(&config.CompleteIndex, "complete-index", -1, "prepare-only 0-based task index to complete")
	fs.StringVar(&config.CompleteResult, "complete-result", "", "prepare-only completion result")
	fs.StringVar(&completeFiles, "complete-files", "", "prepare-only files/materials, separated by comma or semicolon")
	fs.IntVar(&config.FailIndex, "fail-index", -1, "prepare-only 0-based task index to fail")
	fs.StringVar(&config.FailError, "fail-error", "", "prepare-only failure reason")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(defaultEnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}

	config.DBPath = firstNonEmpty(dbPath, os.Getenv(dbEnvKey))
	if strings.TrimSpace(config.DBPath) == "" {
		return runConfig{}, fmt.Errorf("sqlite db path is required; pass -db or set %s", dbEnvKey)
	}
	config.PlanID = firstNonEmpty(planID, os.Getenv(planIDEnvKey))
	config.Message = strings.TrimSpace(message)
	if messageFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(messageFile))
		if err != nil {
			return runConfig{}, err
		}
		config.Message = strings.TrimSpace(string(content))
	}
	if itemsFile != "" {
		content, err := os.ReadFile(e2etest.ResolvePath(itemsFile))
		if err != nil {
			return runConfig{}, err
		}
		itemsText = string(content)
	}
	if strings.TrimSpace(itemsText) != "" {
		items, err := parseItems(itemsText)
		if err != nil {
			return runConfig{}, err
		}
		config.Items = items
	}
	config.CompleteFiles = parseStringList(completeFiles)
	return config, nil
}

// triggerGeneratedPlanRounds 查询 Agent 刚写入 SQLite 的计划项，并按计划 index 连续触发前三轮。
func triggerGeneratedPlanRounds(ctx context.Context, agent agentQuerier, tracker *progresstracking.ProgressTracker) (int, error) {
	items := tracker.Items()
	fmt.Println("\n=== 查询生成计划 ===")
	printGeneratedPlanItems(items)
	if len(items) == 0 {
		fmt.Println("没有生成计划项，跳过遍历触发。")
		return 0, nil
	}

	answerChars := 0
	triggered := 0
	for index, item := range items {
		if triggered >= generatedPlanTriggerRounds {
			break
		}
		if item.Status == progresstracking.TaskStatusCompleted {
			continue
		}
		triggered++
		fmt.Printf("\n=== 遍历第 %d 轮 ===\n", triggered)
		fmt.Printf("触发计划项[%d]: %s\n", index, item.Description)
		response, err := agent.Query(ctx, progresstracking.AgentRequest{
			Message: buildPlanTriggerMessage(index, triggered, item.Description),
		})
		if err != nil {
			return answerChars, err
		}
		answerChars += len([]rune(response.Message))
		fmt.Printf("\n=== 编辑完成 ===\n%s\n", response.Message)
		printTrackerState(tracker)
	}
	if triggered == 0 {
		fmt.Println("所有计划项已完成，本轮没有新的触发。")
	}

	fmt.Println("\n=== 看看结果 ===")
	printTrackerState(tracker)
	return answerChars, nil
}

// printGeneratedPlanItems 用稳定格式展示模型生成的任务，方便确认后续每轮触发的 index。
func printGeneratedPlanItems(items []progresstracking.TaskItem) {
	for index, item := range items {
		fmt.Printf("%d. [%s] %s\n", index, item.Status, item.Description)
	}
}

// buildPlanTriggerMessage 把主流程的一次遍历转换成自然语言输入，仍由 Agent 自己调用 progress tools。
func buildPlanTriggerMessage(index int, round int, description string) string {
	return strings.Join([]string{
		fmt.Sprintf("这是主流程第 %d 次自动触发。", round),
		fmt.Sprintf("请处理当前 SQLite 计划中的第 %d 项：%s。", index, description),
		"请先调用 progress_resumption_context 查询当前进度。",
		fmt.Sprintf("然后调用 progress_start_task 标记 index=%d。", index),
		"调用 progress_complete_task 写入 result, 默认都给齿形完成的结论(当前是调试状态)；files 可写入本轮材料名。",
		"最后用中文简要说明本轮完成了什么，以及 checkpoint 是否更新。",
	}, "\n")
}

// runPrepareOnly 执行确定性 SQLite 更新，便于不依赖模型先验证恢复路径。
func runPrepareOnly(ctx context.Context, tracker *progresstracking.ProgressTracker, config runConfig) error {
	if len(config.Items) > 0 {
		if err := tracker.CreatePlan(ctx, config.Items); err != nil {
			return err
		}
	}
	if config.StartIndex >= 0 {
		if err := tracker.Start(ctx, config.StartIndex); err != nil {
			return err
		}
	}
	if config.CompleteIndex >= 0 {
		if err := tracker.Complete(ctx, config.CompleteIndex, config.CompleteResult, config.CompleteFiles); err != nil {
			return err
		}
	}
	if config.FailIndex >= 0 {
		if err := tracker.Fail(ctx, config.FailIndex, config.FailError); err != nil {
			return err
		}
	}
	return nil
}

// printTrackerState 输出当前 SQLite 路径和恢复上下文，方便复制给下一次运行。
func printTrackerState(tracker *progresstracking.ProgressTracker) {
	fmt.Printf("sqlite=%s\nplan_id=%s\n\n", tracker.Path(), tracker.PlanID())
	fmt.Println("=== Resumption Context ===")
	fmt.Println(tracker.ResumptionContext())
}

// parseItems 支持 JSON 数组、逐行文本和分号分隔文本，避免 CLI 输入被一种格式绑死。
func parseItems(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var jsonItems []string
	if err := json.Unmarshal([]byte(text), &jsonItems); err == nil {
		items := make([]string, 0, len(jsonItems))
		for _, item := range jsonItems {
			item = strings.TrimSpace(item)
			if item != "" {
				items = append(items, item)
			}
		}
		return items, nil
	}
	items := parseStringList(strings.ReplaceAll(text, "\n", ";"))
	if len(items) == 0 {
		return nil, fmt.Errorf("items are empty")
	}
	return items, nil
}

// parseStringList 清理命令行列表参数，兼容逗号和分号。
func parseStringList(text string) []string {
	replacer := strings.NewReplacer("，", ",", "；", ";")
	text = replacer.Replace(text)
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
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

// firstNonEmpty 返回第一个非空字符串，用于 flag 覆盖 env。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
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
