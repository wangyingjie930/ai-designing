package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/reflection/replay"
)

const defaultEnvPath = ".env"

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，测试和人工输出都只依赖这些字段。
type runOutput struct {
	Mode                  string `json:"mode"`
	RecordedTraces        int    `json:"recorded_traces"`
	FailureCount          int    `json:"failure_count"`
	ReflectionCount       int    `json:"reflection_count"`
	LessonCount           int    `json:"lesson_count"`
	AdviceChars           int    `json:"advice_chars"`
	UsedADKCustomizedData bool   `json:"used_adk_customized_data"`
}

// chatModelFactory 抽出模型创建点，测试时替换成 fake model。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  config.APIKey,
		Model:   config.Model,
		BaseURL: config.BaseURL,
	})
}

// main 运行一个非 coding 的活动运营经验回放 Agent。
func main() {
	if _, err := runAgent(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 组装默认历史轨迹、Eino ADK Runner 和命令级 trace。
func runAgent(ctx context.Context) (runOutput, error) {
	traces := defaultOperationalTraces()
	task := defaultRetrospectiveTask()
	if err := loadDotEnv(defaultEnvPath); err != nil {
		return runOutput{}, fmt.Errorf("load env: %w", err)
	}
	modelConfig, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	chatModel, err := newChatModel(ctx, modelConfig)
	if err != nil {
		return runOutput{}, fmt.Errorf("init chat model: %w", err)
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

	failureCount := countFailures(traces)
	return withRunAgentTrace(ctx, len(traces), failureCount, 2, len([]rune(task)), func(traceCtx context.Context) (runOutput, error) {
		runner, core, err := replay.NewRunner(traceCtx, replay.Config{
			Name:        "replay_retrospective_agent",
			Description: "Operational retrospective agent that reuses lessons from failed activity executions.",
			Model:       chatModel,
			Batch:       2,
			Window:      2,
			Spike:       1.5,
			MaxTokens:   1024,
		})
		if err != nil {
			return runOutput{}, err
		}
		if err := seedExperience(traceCtx, core, traces); err != nil {
			return runOutput{}, err
		}
		response, usedCustomized, err := queryRunner(traceCtx, runner, task)
		if err != nil {
			return runOutput{}, err
		}
		output := summarizeResponse(response, failureCount, usedCustomized)
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		return output, nil
	})
}

// defaultOperationalTraces 返回非 coding 的活动运营历史轨迹，刻意包含近期失败 spike。
func defaultOperationalTraces() []replay.ExecutionTrace {
	return []replay.ExecutionTrace{
		{
			Task:    "第一场 AI 公开课报名转化活动",
			Outcome: replay.OutcomeSuccess,
			Steps: []replay.Step{
				{Action: "D-2 发布海报并确认报名表单"},
				{Action: "直播前同步销售跟进口径"},
			},
		},
		{
			Task:    "第二场企业培训负责人社群预热",
			Outcome: replay.OutcomeSuccess,
			Steps: []replay.Step{
				{Action: "提前检查社群二维码和直播间链接"},
				{Action: "活动后 30 分钟分发高意向名单"},
			},
		},
		{
			Task:    "第三场 AI 工具落地公开课",
			Outcome: replay.OutcomeFailure,
			Error:   "报名链接过期，直播前 2 小时才发现，导致社群转化中断",
			Steps: []replay.Step{
				{Action: "提前三天发送海报", Observation: "海报里的报名链接没有二次校验"},
				{Action: "直播当天提醒报名", Observation: "用户反馈链接打不开"},
			},
		},
		{
			Task:    "第四场业务负责人直播答疑",
			Outcome: replay.OutcomeFailure,
			Error:   "直播后高意向名单没有及时给销售，跟进超过 24 小时才开始",
			Steps: []replay.Step{
				{Action: "直播结束后人工整理问答", Observation: "没有预设名单分发责任人"},
				{Action: "第二天补发名单", Observation: "销售反馈线索已明显变冷"},
			},
		},
	}
}

// defaultRetrospectiveTask 返回 agent 的默认新任务，模拟运营同学要准备下一场活动。
func defaultRetrospectiveTask() string {
	return strings.Join([]string{
		"请基于过往 AI 公开课和社群活动复盘，为下一场公开课报名转化准备执行前提醒。",
		"目标：避免报名链路、企微承接、直播后高意向名单分发再次出问题。",
		"输出要给运营和值班销售直接照着检查。",
	}, "\n")
}

// seedExperience 把历史轨迹灌入 replay 核心，让 L1/L2 经验在 ADK 查询前形成。
func seedExperience(ctx context.Context, core *replay.ExperienceReplay, traces []replay.ExecutionTrace) error {
	if core == nil {
		return fmt.Errorf("experience replay core is nil")
	}
	for _, trace := range traces {
		if _, err := core.RecordTrace(ctx, trace); err != nil {
			return err
		}
	}
	return nil
}

// queryRunner 通过 ADK Runner 调用 replay Agent，并读取 customized output 中的结构化结果。
func queryRunner(ctx context.Context, runner *adk.Runner, task string) (*replay.Response, bool, error) {
	iter := runner.Query(ctx, task)
	var fallbackAnswer string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, false, event.Err
		}
		if event.Output == nil {
			continue
		}
		if response, ok := event.Output.CustomizedOutput.(*replay.Response); ok {
			return response, true, nil
		}
		if event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, false, err
		}
		if message != nil && message.Role == schema.Assistant {
			fallbackAnswer = strings.TrimSpace(message.Content)
		}
	}
	if fallbackAnswer == "" {
		return nil, false, fmt.Errorf("replay runner finished without assistant output")
	}
	return &replay.Response{Task: task, Advice: fallbackAnswer}, false, nil
}

// summarizeResponse 把完整 replay 响应压成稳定摘要，避免命令输出依赖大段生成文本。
func summarizeResponse(response *replay.Response, failureCount int, usedCustomized bool) runOutput {
	output := runOutput{
		Mode:                  "agent",
		FailureCount:          failureCount,
		UsedADKCustomizedData: usedCustomized,
	}
	if response == nil {
		return output
	}
	output.RecordedTraces = response.RecordedTraces
	output.ReflectionCount = response.ReflectionCount
	output.LessonCount = response.LessonCount
	output.AdviceChars = len([]rune(response.Advice))
	return output
}

// printAgentResult 输出 agent 的最终提醒和简洁摘要。
func printAgentResult(response *replay.Response, output runOutput) {
	fmt.Println("\n=== Replay Retrospective Advice ===")
	if response != nil {
		fmt.Println(response.UserMessage())
	}
	fmt.Printf("\n=== Replay Retrospective Summary ===\n%+v\n", output)
}

// countFailures 统计默认轨迹里的失败数量，供 trace 和 runOutput 使用。
func countFailures(traces []replay.ExecutionTrace) int {
	failures := 0
	for _, trace := range traces {
		if trace.Outcome == replay.OutcomeFailure {
			failures++
		}
	}
	return failures
}

// loadModelConfig 读取 OpenAI-compatible 模型配置，普通测试通过 fake model 绕过真实调用。
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

// loadDotEnv 加载简单 KEY=VALUE 格式配置，缺少 .env 时保持当前环境变量。
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

// displayBaseURL 让默认地址在输出里更明确。
func displayBaseURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return "default OpenAI endpoint"
	}
	return baseURL
}

// redactKey 打印配置时隐藏密钥。
func redactKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// enabledText 统一命令行展示里的布尔开关文字。
func enabledText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
