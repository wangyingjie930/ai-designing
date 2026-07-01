package main

import (
	skillmode2 "ai-designing/reflection/skillmode"
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/model"

	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultEnvPath                 = ".env"
	skillBackendLocal              = "local"
	skillBackendRegistry           = "registry"
	defaultRegistrySnapshotVersion = "repo-snapshot-v1"
)

// runConfig 保存命令行参数，cmd 只负责把外部输入转成 Skill mode agent 调用。
type runConfig struct {
	EnvPath       string
	Mode          skillmode2.Mode
	Message       string
	MessageFile   string
	PrepareOnly   bool
	PrintJSON     bool
	MaxIterations int
	SkillBackend  string
}

// modelConfig 保存 OpenAI-compatible 模型连接信息。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令执行后的稳定摘要，trace 和测试都只看这些字段。
type runOutput struct {
	Mode         string          `json:"mode"`
	SkillMode    skillmode2.Mode `json:"skill_mode"`
	Scenario     string          `json:"scenario"`
	SkillName    string          `json:"skill_name"`
	SkillBackend string          `json:"skill_backend"`
	QueryChars   int             `json:"query_chars"`
	AnswerChars  int             `json:"answer_chars"`
}

// skillModeReport 是可选 JSON 输出，保留最终答复方便人工检查。
type skillModeReport struct {
	Output runOutput            `json:"output"`
	Result *skillmode2.Response `json:"result,omitempty"`
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

// main 运行一个非 coding 的 Eino ADK Skill Middleware 模式示例 Agent。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 加载输入、模型配置和 CozeLoop，并通过 Eino ADK Runner 调用 Skill mode Agent。
func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	scenario, err := skillmode2.ScenarioForMode(config.Mode)
	if err != nil {
		return runOutput{}, err
	}
	query, err := loadMessage(config, scenario)
	if err != nil {
		return runOutput{}, err
	}
	output := runOutput{
		Mode:         "prepare-only",
		SkillMode:    scenario.Mode,
		Scenario:     scenario.Title,
		SkillName:    scenario.SkillName,
		SkillBackend: config.SkillBackend,
		QueryChars:   len([]rune(query)),
	}
	if config.PrepareOnly {
		fmt.Println("=== Skill Mode Agent Query ===")
		fmt.Printf("mode=%s\nskill=%s\n", scenario.Mode, scenario.SkillName)
		fmt.Println(query)
		return output, nil
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

	return withRunAgentTrace(ctx, scenario, len([]rune(query)), func(traceCtx context.Context) (runOutput, error) {
		chatModel, err := newChatModel(traceCtx, modelConfig)
		if err != nil {
			return runOutput{}, fmt.Errorf("init chat model: %w", err)
		}
		skillBackend, err := buildCommandSkillBackend(traceCtx, config)
		if err != nil {
			return runOutput{}, err
		}
		runner, err := skillmode2.NewRunner(traceCtx, skillmode2.Config{
			Mode:          scenario.Mode,
			Model:         chatModel,
			SubAgentModel: chatModel,
			MaxIterations: config.MaxIterations,
			SkillBackend:  skillBackend,
		})
		if err != nil {
			return runOutput{}, err
		}
		rawResponse, err := skillmode2.QueryRunner(traceCtx, runner, query)
		if err != nil {
			return runOutput{}, err
		}
		response := skillmode2.BuildResponse(scenario, query, rawResponse.Message)
		output := summarizeResponse(response)
		output.SkillBackend = config.SkillBackend
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\nskill_backend=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey), config.SkillBackend)
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		if config.PrintJSON {
			printSkillModeReport(output, response)
		}
		return output, nil
	})
}

// parseRunConfig 读取 flags 和 .env，并填充命令入口默认值。
func parseRunConfig(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("skill-mode-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	config := runConfig{
		EnvPath: defaultEnvPath,
	}
	modeValue := ""
	backendValue := ""
	fs.StringVar(&config.EnvPath, "env", defaultEnvPath, "env file path")
	fs.StringVar(&modeValue, "mode", "fork_with_context", "skill mode: inline, fork_with_context, fork")
	fs.StringVar(&backendValue, "skill-backend", "", "skill backend: registry or local")
	fs.StringVar(&config.Message, "message", "", "customer-support request")
	fs.StringVar(&config.MessageFile, "message-file", "", "file containing the request")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print request without calling model")
	fs.BoolVar(&config.PrintJSON, "json", false, "print machine-readable report")
	fs.IntVar(&config.MaxIterations, "max-iterations", 0, "max chat model iterations")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(config.EnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	mode, err := parseMode(firstNonEmpty(modeValue, os.Getenv("SKILL_MODE_AGENT_MODE"), string(skillmode2.ModeInline)))
	if err != nil {
		return runConfig{}, err
	}
	backend, err := parseSkillBackend(firstNonEmpty(backendValue, os.Getenv("SKILL_MODE_AGENT_BACKEND"), skillBackendRegistry))
	if err != nil {
		return runConfig{}, err
	}
	config.Mode = mode
	config.SkillBackend = backend
	config.MaxIterations = firstPositive(config.MaxIterations, parsePositiveInt(os.Getenv("SKILL_MODE_AGENT_MAX_ITERATIONS")), 6)
	return config, nil
}

// buildCommandSkillBackend 为 main 路径构造实际传入 Runner 的 Skill backend。
func buildCommandSkillBackend(ctx context.Context, config runConfig) (adkskill.Backend, error) {
	switch config.SkillBackend {
	case skillBackendLocal:
		return nil, nil
	case skillBackendRegistry:
		manifest, err := buildDemoSkillReleaseManifest(ctx, skillmode2.DefaultScenarios())
		if err != nil {
			return nil, err
		}
		return skillmode2.NewRegistryBackend(manifest, skillmode2.RegistryBackendOptions{Channel: "prod"})
	default:
		return nil, fmt.Errorf("unsupported skill backend %q", config.SkillBackend)
	}
}

// buildDemoSkillReleaseManifest 显式构造发布 manifest；local backend 只属于 local 运行路径。
func buildDemoSkillReleaseManifest(_ context.Context, scenarios map[skillmode2.Mode]skillmode2.Scenario) (skillmode2.SkillReleaseManifest, error) {
	ordered := make([]skillmode2.Scenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		ordered = append(ordered, scenario)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].SkillName < ordered[j].SkillName
	})

	artifacts := make([]skillmode2.SkillArtifact, 0, len(ordered))
	aliases := make([]skillmode2.SkillAlias, 0, len(ordered))
	for _, scenario := range ordered {
		frontMatter, err := frontMatterFromScenario(scenario)
		if err != nil {
			return skillmode2.SkillReleaseManifest{}, err
		}
		content, err := readDemoSkillBody(scenario.SkillName)
		if err != nil {
			return skillmode2.SkillReleaseManifest{}, err
		}
		artifacts = append(artifacts, skillmode2.SkillArtifact{
			FrontMatter: frontMatter,
			Version:     defaultRegistrySnapshotVersion,
			Owner:       "skillmode-demo",
			SourceSHA:   "local-repo-snapshot",
			Content:     content,
		})
		aliases = append(aliases, skillmode2.SkillAlias{
			SkillName: frontMatter.Name,
			Channel:   "prod",
			Version:   defaultRegistrySnapshotVersion,
		})
	}
	return skillmode2.SkillReleaseManifest{
		Artifacts: artifacts,
		Aliases:   aliases,
	}, nil
}

// frontMatterFromScenario 把发布侧场景声明转换成 Skill manifest 元数据，避免依赖 SKILL.md frontmatter。
func frontMatterFromScenario(scenario skillmode2.Scenario) (adkskill.FrontMatter, error) {
	name := strings.TrimSpace(scenario.SkillName)
	if name == "" {
		return adkskill.FrontMatter{}, fmt.Errorf("scenario skill name is required")
	}
	description := strings.TrimSpace(scenario.Rationale)
	if description == "" {
		description = strings.TrimSpace(scenario.Title)
	}
	if description == "" {
		return adkskill.FrontMatter{}, fmt.Errorf("scenario description is required: %s", name)
	}
	return adkskill.FrontMatter{
		Name:        name,
		Description: description,
		Context:     scenario.ContextMode,
		Agent:       strings.TrimSpace(scenario.AgentName),
	}, nil
}

// readDemoSkillBody 只读取 Skill 指令正文；发布元数据由 Scenario 显式提供。
func readDemoSkillBody(skillName string) (string, error) {
	path := e2etest.ResolvePath(filepath.Join(skillmode2.DefaultSkillsDir, strings.TrimSpace(skillName), "SKILL.md"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return extractSkillBody(string(data))
}

// extractSkillBody 去掉 SKILL.md frontmatter，避免 manifest 构建时复用本地 backend 的解析结果。
func extractSkillBody(content string) (string, error) {
	const delimiter = "---"
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, delimiter) {
		return "", fmt.Errorf("skill frontmatter delimiter is required")
	}
	rest := strings.TrimPrefix(trimmed, delimiter)
	_, body, ok := strings.Cut(rest, delimiter)
	if !ok {
		return "", fmt.Errorf("skill closing frontmatter delimiter is required")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("skill body is empty")
	}
	return body, nil
}

// loadMessage 读取用户传入消息；未传时使用场景默认客服输入。
func loadMessage(config runConfig, scenario skillmode2.Scenario) (string, error) {
	if strings.TrimSpace(config.Message) != "" {
		return strings.TrimSpace(config.Message), nil
	}
	if strings.TrimSpace(config.MessageFile) != "" {
		data, err := os.ReadFile(e2etest.ResolvePath(config.MessageFile))
		if err != nil {
			return "", err
		}
		message := strings.TrimSpace(string(data))
		if message == "" {
			return "", fmt.Errorf("message file is empty")
		}
		return message, nil
	}
	if strings.TrimSpace(scenario.DefaultQuery) == "" {
		return "", fmt.Errorf("default query is empty")
	}
	return scenario.DefaultQuery, nil
}

// summarizeResponse 把 Skill mode 响应压成稳定摘要，避免 trace 记录完整客诉内容。
func summarizeResponse(response *skillmode2.Response) runOutput {
	output := runOutput{Mode: "agent"}
	if response == nil {
		return output
	}
	output.SkillMode = response.Mode
	output.Scenario = response.Scenario
	output.SkillName = response.SkillName
	output.QueryChars = response.QueryChars
	output.AnswerChars = response.AnswerChars
	return output
}

// printAgentResult 输出 agent 的最终答案和简洁摘要。
func printAgentResult(response *skillmode2.Response, output runOutput) {
	fmt.Println("=== Skill Mode Agent Result ===")
	fmt.Printf("mode=%s\nskill=%s\nquery_chars=%d\nanswer_chars=%d\n", output.SkillMode, output.SkillName, output.QueryChars, output.AnswerChars)
	if response != nil {
		fmt.Printf("scenario=%s\nreason=%s\n\n%s\n", response.Scenario, response.Rationale, response.Message)
	}
}

// printSkillModeReport 输出机器可读 JSON。
func printSkillModeReport(output runOutput, response *skillmode2.Response) {
	data, err := json.MarshalIndent(skillModeReport{Output: output, Result: response}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: marshal json: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// parseMode 校验用户输入的 skill 模式。
func parseMode(value string) (skillmode2.Mode, error) {
	mode := skillmode2.Mode(strings.TrimSpace(value))
	switch mode {
	case skillmode2.ModeInline, skillmode2.ModeForkWithContext, skillmode2.ModeFork:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported mode %q; use inline, fork_with_context, or fork", value)
	}
}

// parseSkillBackend 校验命令入口支持的 Skill backend 类型。
func parseSkillBackend(value string) (string, error) {
	backend := strings.TrimSpace(value)
	switch backend {
	case "", skillBackendRegistry:
		return skillBackendRegistry, nil
	case skillBackendLocal:
		return skillBackendLocal, nil
	default:
		return "", fmt.Errorf("unsupported skill backend %q; use registry or local", value)
	}
}

// loadModelConfig 从环境变量读取 OpenAI-compatible 模型配置。
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

// firstPositive 返回第一项正整数，用于 flag、env、默认值顺序覆盖。
func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// parsePositiveInt 解析正整数环境变量，非法时返回 0。
func parsePositiveInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
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
