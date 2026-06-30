package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	cozeloopobs "ai-designing/observability/cozeloop"
	"ai-designing/reflection/critique"
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
	Mode                  string  `json:"mode"`
	TaskChars             int     `json:"task_chars"`
	Iterations            int     `json:"iterations"`
	FinalScore            float64 `json:"final_score"`
	Converged             bool    `json:"converged"`
	AnswerChars           int     `json:"answer_chars"`
	UsedADKCustomizedData bool    `json:"used_adk_customized_data"`
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

// main 运行一个非 coding 的活动方案/运营文案质量迭代 Agent。
func main() {
	if _, err := runAgent(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 使用内置活动需求直接跑 Eino ADK Runner，适合 GoLand 里点 main_test 验证。
func runAgent(ctx context.Context) (runOutput, error) {
	task := defaultActivityRequest()
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
	maxIterations := parsePositiveInt(firstEnv("ACTIVITY_CRITIQUE_MAX_ITERATIONS"))
	if maxIterations <= 0 {
		maxIterations = 3
	}
	qualityThreshold := parsePositiveFloat(firstEnv("ACTIVITY_CRITIQUE_QUALITY_THRESHOLD"))
	if qualityThreshold <= 0 {
		qualityThreshold = 0.9
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

	return withRunAgentTrace(ctx, len([]rune(task)), maxIterations, qualityThreshold, func(traceCtx context.Context) (runOutput, error) {
		runner, _, err := critique.NewRunner(traceCtx, critique.Config{
			Name:             "activity_critique_agent",
			Description:      "Iteratively improve an activity plan or marketing copy with generator-critic reflection.",
			GeneratorModel:   chatModel,
			ToolFn:           activityChecklistTool,
			MaxIterations:    maxIterations,
			QualityThreshold: qualityThreshold,
		})
		if err != nil {
			return runOutput{}, err
		}
		response, usedCustomized, err := queryRunner(traceCtx, runner, task)
		if err != nil {
			return runOutput{}, err
		}
		output := summarizeResponse(task, response, usedCustomized)
		fmt.Printf("model=%s\nbase_url=%s\napi_key=%s\n", modelConfig.Model, displayBaseURL(modelConfig.BaseURL), redactKey(modelConfig.APIKey))
		fmt.Printf("cozeloop=%s endpoint=%s workspace=%s\n", enabledText(cozeLoopConfig.Enabled), cozeloopobs.DisplayEndpoint(cozeLoopConfig), cozeloopobs.DisplayWorkspaceID(cozeLoopConfig))
		printAgentResult(response, output)
		return output, nil
	})
}

// defaultActivityRequest 返回 main_test 可直接点击运行的活动运营输入。
func defaultActivityRequest() string {
	return strings.Join([]string{
		"请为一场 AI 公开课设计活动方案和核心运营文案。",
		"业务目标：提升报名转化，并让销售团队后续能跟进高意向线索。",
		"目标人群：AI 产品经理、企业培训负责人、正在评估 AI 工具落地的业务负责人。",
		"活动形式：线上直播公开课，45 分钟分享 + 15 分钟答疑。",
		"预算：总预算 5000 元，主要用于海报设计、社群分发和讲师物料。",
		"请输出能被运营同学直接执行的方案，不要只写概念。",
	}, "\n")
}

// activityPlan 是工具层可稳定校验的活动方案结构，避免用全文关键词冒充质量判断。
type activityPlan struct {
	Goal     string         `json:"goal"`
	Audience string         `json:"audience"`
	Budget   activityBudget `json:"budget"`
	Channels []string       `json:"channels"`
	Timeline []string       `json:"timeline"`
	CTA      string         `json:"cta"`
	RiskPlan []string       `json:"risk_plan"`
}

// activityBudget 表示预算金额和资源项，工具只检查硬约束，不判断预算策略优劣。
type activityBudget struct {
	Amount float64  `json:"amount"`
	Items  []string `json:"items"`
}

// activityChecklistRule 定义活动方案质检中的一个维度和对应证据校验。
type activityChecklistRule struct {
	Name        string
	MissingText string
	Validate    func(activityPlan) bool
}

// activityChecklistTool 做确定性质量检查，把模型容易漏掉的运营要素反馈给 critic。
func activityChecklistTool(output string) string {
	plan, err := parseActivityPlan(output)
	if err != nil {
		return "工具检查：当前输出不是结构化活动方案 JSON，无法稳定校验预算、渠道、CTA 和风险兜底。请按 goal/audience/budget/channels/timeline/cta/risk_plan 输出。"
	}
	rules := []activityChecklistRule{
		{Name: "目标人群", MissingText: "目标人群缺少具体对象", Validate: hasSpecificAudience},
		{Name: "预算", MissingText: "预算缺少具体金额或资源口径", Validate: hasConcreteBudget},
		{Name: "渠道", MissingText: "渠道缺少具体触点", Validate: hasConcreteChannel},
		{Name: "时间节奏", MissingText: "时间节奏缺少阶段或时间点", Validate: hasConcreteTimeline},
		{Name: "CTA", MissingText: "CTA 缺少明确动作", Validate: hasActionableCTA},
		{Name: "风险兜底", MissingText: "风险兜底缺少备用动作", Validate: hasRiskFallback},
	}
	missing := make([]string, 0)
	covered := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.Validate(plan) {
			covered = append(covered, rule.Name)
		} else {
			missing = append(missing, rule.MissingText)
		}
	}
	if len(missing) == 0 {
		return "工具检查：预算、目标人群、渠道、时间节奏、CTA、风险兜底均已覆盖。"
	}
	if len(covered) == 0 {
		return "工具检查：" + strings.Join(missing, "；") + "。"
	}
	return "工具检查：已覆盖" + strings.Join(covered, "、") + "；" + strings.Join(missing, "；") + "。"
}

// queryRunner 通过 ADK Runner 调用 critique Agent，并读取 customized output 中的结构化结果。
func queryRunner(ctx context.Context, runner *adk.Runner, task string) (*critique.Response, bool, error) {
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
		if response, ok := event.Output.CustomizedOutput.(*critique.Response); ok {
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
		return nil, false, fmt.Errorf("activity critique runner finished without assistant output")
	}
	return &critique.Response{Task: task, Output: fallbackAnswer, Converged: false}, false, nil
}

// summarizeResponse 把完整响应压成稳定摘要，避免命令输出依赖大段生成文本。
func summarizeResponse(task string, response *critique.Response, usedCustomized bool) runOutput {
	output := runOutput{
		Mode:                  "agent",
		TaskChars:             len([]rune(task)),
		UsedADKCustomizedData: usedCustomized,
	}
	if response == nil {
		return output
	}
	output.Iterations = response.Iterations
	output.FinalScore = response.FinalScore
	output.Converged = response.Converged
	output.AnswerChars = len([]rune(response.Output))
	return output
}

// printAgentResult 输出 agent 的最终方案和简洁摘要。
func printAgentResult(response *critique.Response, output runOutput) {
	fmt.Println("\n=== Activity Critique Final Output ===")
	if response != nil {
		fmt.Println(response.UserMessage())
	}
	fmt.Printf("\n=== Activity Critique Summary ===\n%+v\n", output)
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

// parseActivityPlan 从模型输出中抽取结构化活动方案 JSON。
func parseActivityPlan(output string) (activityPlan, error) {
	jsonText, err := extractJSONObject(output)
	if err != nil {
		return activityPlan{}, err
	}
	var plan activityPlan
	if err := json.Unmarshal([]byte(jsonText), &plan); err != nil {
		return activityPlan{}, err
	}
	return plan, nil
}

// extractJSONObject 从模型输出中抽取第一个完整 JSON object。
func extractJSONObject(text string) (string, error) {
	trimmed := strings.TrimSpace(stripJSONFence(text))
	if trimmed == "" {
		return "", fmt.Errorf("empty activity plan")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return "", fmt.Errorf("activity plan does not contain json object")
	}
	inString := false
	escaped := false
	depth := 0
	for i := start; i < len(trimmed); i++ {
		ch := trimmed[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := strings.TrimSpace(trimmed[start : i+1])
				if !json.Valid([]byte(candidate)) {
					return "", fmt.Errorf("extracted activity plan json object is invalid")
				}
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("activity plan json object is incomplete")
}

// stripJSONFence 兼容模型把活动方案 JSON 放进 ```json 代码块的情况。
func stripJSONFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[1 : len(lines)-1]
	} else {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// hasSpecificAudience 判断方案是否给出了可定位的人群，而不是只写“目标人群”栏目名。
func hasSpecificAudience(plan activityPlan) bool {
	return isConcreteText(plan.Audience) &&
		containsAny(plan.Audience, []string{"产品经理", "培训负责人", "业务负责人", "运营", "销售", "企业客户", "学员", "用户画像", "受众", "负责人"})
}

// hasConcreteBudget 判断预算是否有金额或资源口径，避免“预算：待定”被误判通过。
func hasConcreteBudget(plan activityPlan) bool {
	return plan.Budget.Amount > 0 || hasConcreteListItem(plan.Budget.Items, []string{"海报", "社群", "讲师", "物料", "设计", "投放", "人力"})
}

// hasConcreteChannel 判断渠道是否落到具体触点，而不是只写“渠道”两个字。
func hasConcreteChannel(plan activityPlan) bool {
	return hasConcreteListItem(plan.Channels, []string{"社群", "公众号", "朋友圈", "短信", "邮件", "企微", "小红书", "直播间", "官网", "销售", "私域"})
}

// hasConcreteTimeline 判断是否给出阶段、排期或明确活动时间。
func hasConcreteTimeline(plan activityPlan) bool {
	return hasConcreteListItem(plan.Timeline, []string{"预热", "发布", "活动前", "活动中", "活动后", "复盘", "T-", "D-", "分钟", "小时", "天", "排期", "直播", "跟进"})
}

// hasActionableCTA 判断 CTA 是否包含明确动作，不能只写“提升转化”这种目标描述。
func hasActionableCTA(plan activityPlan) bool {
	return isConcreteText(plan.CTA) &&
		containsAny(plan.CTA, []string{"扫码报名", "点击报名", "立即报名", "预约咨询", "领取资料", "添加企微", "填写表单", "报名链接"})
}

// hasRiskFallback 判断风险是否有可执行兜底动作，而不是只提醒“注意风险”。
func hasRiskFallback(plan activityPlan) bool {
	return hasConcreteListItem(plan.RiskPlan, []string{"备用", "备选", "候补", "替补", "回放", "补发", "改期", "讲师备份", "直播备份"})
}

// containsAny 判断输出是否包含一组关键词中的任意一个。
func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

// hasConcreteListItem 判断列表中是否存在非空泛且命中业务关键词的项目。
func hasConcreteListItem(values []string, keywords []string) bool {
	for _, value := range values {
		if isConcreteText(value) && containsAny(value, keywords) {
			return true
		}
	}
	return false
}

// isConcreteText 过滤“待定”“注意风险”这类只有栏目壳、没有执行信息的内容。
func isConcreteText(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	vagueMarkers := []string{"待定", "注意风险", "提升转化", "暂无", "不确定", "后续补充", "待补充"}
	return !containsAny(value, vagueMarkers)
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

// parsePositiveFloat 解析正浮点数环境变量，非法时返回 0。
func parsePositiveFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
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
