package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	claudepermissions "ai-designing/action/claude_permissions"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

const defaultMessage = "先检查 TENANT-42，再把 invoice_v2 功能开关关闭，并生成一条变更说明。"

// runConfig 保存终端入口的权限模式和用户请求。
type runConfig struct {
	Message     string
	Mode        claudepermissions.PermissionMode
	PrepareOnly bool
	ApproveAll  bool
}

// modelConfig 保存 OpenAI-compatible 模型连接参数。
type modelConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// runOutput 是命令测试和调用方可稳定检查的运行摘要。
type runOutput struct {
	Mode        claudepermissions.PermissionMode
	ToolCount   int
	AnswerChars int
}

// chatModelFactory 允许命令测试替换真实模型。
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)

var newChatModel chatModelFactory = func(ctx context.Context, config modelConfig) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: config.APIKey, Model: config.Model, BaseURL: config.BaseURL,
	})
}

// main 启动 Claude Code 风格的 SaaS 权限 Agent。
func main() {
	if _, err := runAgent(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runAgent 加载 .env、创建 Agent，并并行运行终端审批界面。
func runAgent(ctx context.Context, args []string, input io.Reader, output io.Writer) (runOutput, error) {
	if err := loadDotEnv(".env"); err != nil {
		return runOutput{}, fmt.Errorf("load .env: %w", err)
	}
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	policies := claudepermissions.DefaultSaaSToolPolicies()
	if config.PrepareOnly {
		printPolicySummary(output, config.Mode, policies)
		return runOutput{Mode: config.Mode, ToolCount: len(policies)}, nil
	}

	modelSettings, err := loadModelConfig()
	if err != nil {
		return runOutput{}, err
	}
	chatModel, err := newChatModel(ctx, modelSettings)
	if err != nil {
		return runOutput{}, fmt.Errorf("init chat model: %w", err)
	}
	broker := claudepermissions.NewPermissionBroker(8)
	agent, err := claudepermissions.NewSaaSAgent(ctx, claudepermissions.SaaSAgentConfig{
		Model: chatModel, Mode: config.Mode, Broker: broker,
		Executor: claudepermissions.NewInMemorySaaSExecutor(),
	})
	if err != nil {
		return runOutput{}, err
	}

	approvalCtx, stopApprovals := context.WithCancel(ctx)
	defer stopApprovals()
	go serveApprovals(approvalCtx, broker, input, output, config.ApproveAll)

	response, err := agent.Query(ctx, claudepermissions.SaaSRequest{Message: config.Message})
	if err != nil {
		return runOutput{}, err
	}
	fmt.Fprintf(output, "\n=== Agent 回复 ===\n%s\n", response.Message)
	return runOutput{
		Mode: config.Mode, ToolCount: len(policies), AnswerChars: len([]rune(response.Message)),
	}, nil
}

// parseRunConfig 解析命令参数，.env 中的 CLAUDE_PERMISSION_MODE 可作为默认模式。
func parseRunConfig(args []string) (runConfig, error) {
	defaultMode := strings.TrimSpace(os.Getenv("CLAUDE_PERMISSION_MODE"))
	if defaultMode == "" {
		defaultMode = string(claudepermissions.PermissionModeDefault)
	}
	defaultInput := strings.TrimSpace(os.Getenv("CLAUDE_PERMISSION_MESSAGE"))
	if defaultInput == "" {
		defaultInput = defaultMessage
	}
	fs := flag.NewFlagSet("claude-permission-agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := runConfig{Message: defaultInput}
	var mode string
	fs.StringVar(&config.Message, "message", defaultInput, "natural language SaaS change request")
	fs.StringVar(&mode, "mode", defaultMode, "default|plan|acceptEdits|dontAsk|bypassPermissions")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print permission configuration without calling the model")
	fs.BoolVar(&config.ApproveAll, "approve-all", false, "automatically approve prompts; demo only")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	config.Message = strings.TrimSpace(config.Message)
	config.Mode = claudepermissions.PermissionMode(strings.TrimSpace(mode))
	if config.Message == "" {
		return runConfig{}, fmt.Errorf("message is required")
	}
	if !validMode(config.Mode) {
		return runConfig{}, fmt.Errorf("unsupported permission mode %q", config.Mode)
	}
	return config, nil
}

// serveApprovals 消费待审批请求；Query 保持暂停，直到这里提交 allow 或 deny。
func serveApprovals(ctx context.Context, broker *claudepermissions.PermissionBroker, input io.Reader, output io.Writer, approveAll bool) {
	scanner := bufio.NewScanner(input)
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-broker.Requests():
			if !broker.IsPending(request.ID) {
				continue
			}
			var response claudepermissions.PermissionResponse
			if approveAll {
				fmt.Fprintf(output, "\n[permission] auto allow %s %s\n", request.ToolName, request.ArgumentsJSON)
				response = claudepermissions.PermissionResponse{RequestID: request.ID, Behavior: claudepermissions.PermissionAllow, Reason: "approved by -approve-all"}
			} else {
				response = readApprovalFromScanner(scanner, output, request)
			}
			broker.Resolve(response)
		}
	}
}

// readApproval 为测试或单次调用创建审批输入扫描器。
func readApproval(input io.Reader, output io.Writer, request claudepermissions.PermissionRequest) claudepermissions.PermissionResponse {
	return readApprovalFromScanner(bufio.NewScanner(input), output, request)
}

// readApprovalFromScanner 展示结构化请求，并支持允许、拒绝或修改 JSON 后允许。
func readApprovalFromScanner(scanner *bufio.Scanner, output io.Writer, request claudepermissions.PermissionRequest) claudepermissions.PermissionResponse {
	fmt.Fprintf(output, "\nPermission required\ntool: %s\narguments: %s\nreason: %s\n", request.ToolName, request.ArgumentsJSON, request.Reason)
	fmt.Fprint(output, "[y] allow  [n] deny  [e] edit JSON and allow: ")
	if !scanner.Scan() {
		return claudepermissions.PermissionResponse{RequestID: request.ID, Behavior: claudepermissions.PermissionDeny, Reason: "approval input closed"}
	}
	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes":
		return claudepermissions.PermissionResponse{RequestID: request.ID, Behavior: claudepermissions.PermissionAllow, Reason: "approved in terminal"}
	case "e", "edit":
		fmt.Fprint(output, "updated JSON: ")
		if scanner.Scan() {
			updated := strings.TrimSpace(scanner.Text())
			if json.Valid([]byte(updated)) {
				return claudepermissions.PermissionResponse{RequestID: request.ID, Behavior: claudepermissions.PermissionAllow, UpdatedArgumentsJSON: updated, Reason: "edited and approved in terminal"}
			}
		}
		return claudepermissions.PermissionResponse{RequestID: request.ID, Behavior: claudepermissions.PermissionDeny, Reason: "updated arguments are not valid JSON"}
	default:
		return claudepermissions.PermissionResponse{RequestID: request.ID, Behavior: claudepermissions.PermissionDeny, Reason: "denied in terminal"}
	}
}

// printPolicySummary 输出工具分类和整工具 deny 规则，便于不调用模型时检查配置。
func printPolicySummary(output io.Writer, mode claudepermissions.PermissionMode, policies []claudepermissions.ToolPolicy) {
	fmt.Fprintf(output, "mode=%s\n", mode)
	for _, policy := range policies {
		fmt.Fprintf(output, "%s=%s\n", policy.Name, policy.Kind)
	}
	fmt.Fprintln(output, "blanket_deny=delete_tenant")
}

// loadModelConfig 从 .env 读取 OpenAI-compatible 模型配置。
func loadModelConfig() (modelConfig, error) {
	config := modelConfig{
		APIKey:  strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Model:   strings.TrimSpace(firstEnv("LLM_MODEL", "OPENAI_MODEL")),
		BaseURL: normalizeBaseURL(firstEnv("LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE_URL")),
	}
	if config.APIKey == "" {
		return modelConfig{}, fmt.Errorf("OPENAI_API_KEY is required")
	}
	if config.Model == "" {
		return modelConfig{}, fmt.Errorf("LLM_MODEL or OPENAI_MODEL is required")
	}
	return config, nil
}

// loadDotEnv 加载简单 KEY=VALUE 文件，并以仓库 .env 覆盖当前进程同名配置。
func loadDotEnv(path string) error {
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
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if err := os.Setenv(strings.TrimSpace(key), value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// firstEnv 返回首个非空环境变量。
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// normalizeBaseURL 为本地 OpenAI-compatible 根地址补齐 /v1。
func normalizeBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" || strings.HasSuffix(value, "/v1") {
		return value
	}
	return value + "/v1"
}

// validMode 校验命令行权限模式。
func validMode(mode claudepermissions.PermissionMode) bool {
	switch mode {
	case claudepermissions.PermissionModeDefault,
		claudepermissions.PermissionModePlan,
		claudepermissions.PermissionModeAcceptEdits,
		claudepermissions.PermissionModeDontAsk,
		claudepermissions.PermissionModeBypassPermissions:
		return true
	default:
		return false
	}
}
