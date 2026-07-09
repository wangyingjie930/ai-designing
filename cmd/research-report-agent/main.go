package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	researchswarm "ai-designing/action/research_swarm"
	"ai-designing/cmd/internal/e2etest"
	cozeloopobs "ai-designing/observability/cozeloop"
)

const (
	defaultTopic   = "调查 AI Agent 外部搜索在客服工单中的风险控制价值"
	defaultEnvPath = ".env"
)

// runOutput 是命令对外打印的紧凑摘要，不包含完整 mailbox 或原始搜索 payload。
type runOutput struct {
	Mode               string `json:"mode"`
	TeamName           string `json:"team_name"`
	Topic              string `json:"topic"`
	SearchProvider     string `json:"search_provider"`
	WorkerCount        int    `json:"worker_count"`
	SourceCardCount    int    `json:"source_card_count"`
	ReportSectionCount int    `json:"report_section_count"`
	FailedWorkerCount  int    `json:"failed_worker_count"`
	FinalReport        string `json:"final_report,omitempty"`
	DBPath             string `json:"db_path,omitempty"`
}

type runConfig struct {
	Role           string
	Topic          string
	TeamName       string
	AgentName      string
	DBPath         string
	SearchProvider string
	PrepareOnly    bool
	JSON           bool
	Timeout        time.Duration
}

func main() {
	output, err := runAgent(context.Background(), os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if output.Mode == "worker" {
		return
	}
	if shouldPrintJSON(os.Args[1:]) {
		_ = json.NewEncoder(os.Stdout).Encode(output)
		return
	}
	printTextOutput(output)
}

func runAgent(ctx context.Context, args []string) (runOutput, error) {
	config, err := parseRunConfig(args)
	if err != nil {
		return runOutput{}, err
	}
	provider, searchClient, err := buildSearchClient(config.SearchProvider)
	if err != nil {
		return runOutput{}, err
	}
	if config.PrepareOnly {
		return runOutput{
			Mode:           "prepare",
			TeamName:       config.TeamName,
			Topic:          config.Topic,
			SearchProvider: string(provider),
			WorkerCount:    3,
			DBPath:         config.DBPath,
		}, nil
	}
	if !shouldInstallRunTrace(config) {
		return runAgentRuntime(ctx, config, provider, searchClient)
	}
	_, shutdownCozeLoop, err := cozeloopobs.InstallFromEnv(ctx)
	if err != nil {
		return runOutput{}, fmt.Errorf("init cozeloop: %w", err)
	}
	defer func() {
		if err := shutdownCozeLoop(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: shutdown cozeloop: %v\n", err)
		}
	}()
	return withRunAgentTrace(ctx, buildRunTraceInput(config, string(provider)), func(traceCtx context.Context) (runOutput, error) {
		return runAgentRuntime(traceCtx, config, provider, searchClient)
	})
}

// shouldInstallRunTrace 限定命令边界 trace 的接入角色，worker 子进程也需要独立上报。
func shouldInstallRunTrace(config runConfig) bool {
	if config.PrepareOnly {
		return false
	}
	switch config.Role {
	case "leader", "worker":
		return true
	default:
		return false
	}
}

func runAgentRuntime(ctx context.Context, config runConfig, provider researchswarm.SearchProvider, searchClient researchswarm.SearchClient) (runOutput, error) {
	switch config.Role {
	case "leader":
		commandPath, commandPrefix := workerCommandInvocation()
		result, err := researchswarm.RunLeader(ctx, researchswarm.LeaderConfig{
			TeamName:       config.TeamName,
			Topic:          config.Topic,
			DBPath:         config.DBPath,
			SearchProvider: provider,
			SearchClient:   searchClient,
			Spawner:        &researchswarm.ExecProcessSpawner{},
			PollInterval:   100 * time.Millisecond,
			Timeout:        config.Timeout,
			CommandPath:    commandPath,
			CommandPrefix:  commandPrefix,
		})
		if err != nil {
			return runOutput{}, err
		}
		return outputFromLeader(config, provider, result), nil
	case "worker":
		store, err := researchswarm.OpenStore(ctx, config.DBPath)
		if err != nil {
			return runOutput{}, err
		}
		defer store.Close()
		if err := researchswarm.RunWorker(ctx, researchswarm.WorkerConfig{
			TeamName:     config.TeamName,
			AgentName:    config.AgentName,
			Role:         roleFromAgentName(config.AgentName),
			Store:        store,
			SearchClient: searchClient,
			PollInterval: 100 * time.Millisecond,
		}); err != nil {
			return runOutput{}, err
		}
		return runOutput{Mode: "worker", TeamName: config.TeamName, SearchProvider: string(provider), DBPath: config.DBPath}, nil
	default:
		return runOutput{}, fmt.Errorf("unsupported role %q", config.Role)
	}
}

func parseRunConfig(args []string) (runConfig, error) {
	var config runConfig
	fs := flag.NewFlagSet("research-report-agent", flag.ContinueOnError)
	fs.StringVar(&config.Role, "role", "leader", "agent role: leader or worker")
	fs.StringVar(&config.Topic, "topic", defaultTopic, "research report topic")
	fs.StringVar(&config.TeamName, "team", "research-demo", "team name")
	fs.StringVar(&config.AgentName, "agent", "", "worker agent name")
	fs.StringVar(&config.DBPath, "db", "", "SQLite mailbox path")
	fs.StringVar(&config.SearchProvider, "search-provider", "", "search provider: fake or http_json")
	fs.BoolVar(&config.PrepareOnly, "prepare-only", false, "print configuration summary without running workers")
	fs.BoolVar(&config.JSON, "json", false, "print JSON output")
	fs.DurationVar(&config.Timeout, "timeout", 20*time.Second, "leader timeout")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, err
	}
	if err := loadDotEnv(e2etest.ResolvePath(defaultEnvPath)); err != nil {
		return runConfig{}, fmt.Errorf("load env: %w", err)
	}
	config.Role = strings.TrimSpace(config.Role)
	config.Topic = strings.TrimSpace(config.Topic)
	config.TeamName = strings.TrimSpace(config.TeamName)
	config.AgentName = strings.TrimSpace(config.AgentName)
	config.SearchProvider = strings.TrimSpace(config.SearchProvider)
	if config.TeamName == "" {
		config.TeamName = "research-demo"
	}
	if config.Role == "worker" && config.AgentName == "" {
		return runConfig{}, fmt.Errorf("-agent is required in worker mode")
	}
	if config.Topic == "" {
		return runConfig{}, fmt.Errorf("topic is required")
	}
	if config.DBPath == "" {
		config.DBPath = fmt.Sprintf("/Users/wangyingjie/Documents/code/ai-designing/output/%s-%d.sqlite", config.TeamName, time.Now().UnixNano())
	}
	return config, nil
}

func buildSearchClient(providerFlag string) (researchswarm.SearchProvider, researchswarm.SearchClient, error) {
	if strings.TrimSpace(providerFlag) == "" {
		return researchswarm.NewSearchClientFromEnv()
	}
	provider := researchswarm.SearchProvider(providerFlag)
	switch provider {
	case researchswarm.SearchProviderFake:
		return provider, researchswarm.NewFakeSearchClient(), nil
	case researchswarm.SearchProviderHTTPJSON:
		url := strings.TrimSpace(os.Getenv("SEARCH_API_URL"))
		if url == "" {
			return "", nil, fmt.Errorf("SEARCH_API_URL is required when -search-provider=http_json")
		}
		return provider, researchswarm.NewHTTPJSONSearchClient(url, os.Getenv("SEARCH_API_KEY")), nil
	default:
		return "", nil, fmt.Errorf("unsupported search provider %q", providerFlag)
	}
}

func outputFromLeader(config runConfig, provider researchswarm.SearchProvider, result *researchswarm.LeaderResult) runOutput {
	return runOutput{
		Mode:               "leader",
		TeamName:           result.TeamName,
		Topic:              result.Topic,
		SearchProvider:     string(provider),
		WorkerCount:        3,
		SourceCardCount:    result.SourceCardCount,
		ReportSectionCount: result.ReportSectionCount,
		FailedWorkerCount:  result.FailedWorkerCount,
		FinalReport:        result.FinalReport,
		DBPath:             config.DBPath,
	}
}

func roleFromAgentName(agentName string) researchswarm.AgentRole {
	switch agentName {
	case "searcher":
		return researchswarm.RoleSearcher
	case "analyst":
		return researchswarm.RoleAnalyst
	case "writer":
		return researchswarm.RoleWriter
	default:
		return researchswarm.AgentRole(agentName)
	}
}

func workerCommandInvocation() (string, []string) {
	if strings.HasSuffix(filepath.Base(os.Args[0]), ".test") {
		return "go", []string{"run", "."}
	}
	return os.Args[0], nil
}

func shouldPrintJSON(args []string) bool {
	for _, arg := range args {
		if arg == "-json" || arg == "--json" {
			return true
		}
	}
	return false
}

func printTextOutput(output runOutput) {
	fmt.Printf("mode=%s\n", output.Mode)
	fmt.Printf("team=%s\n", output.TeamName)
	fmt.Printf("search_provider=%s\n", output.SearchProvider)
	fmt.Printf("source_cards=%d\n", output.SourceCardCount)
	fmt.Printf("report_sections=%d\n", output.ReportSectionCount)
	fmt.Printf("failed_workers=%d\n", output.FailedWorkerCount)
	if strings.TrimSpace(output.FinalReport) != "" {
		fmt.Println()
		fmt.Println(output.FinalReport)
	}
}

// loadDotEnv 读取简单 KEY=VALUE 格式；本地 demo 一切以 .env 为准，覆盖当前进程同名变量。
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
