package researchswarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
)

// LeaderConfig 配置 report_director 如何创建 team、启动外部 worker 并汇总报告。
type LeaderConfig struct {
	TeamName       string
	Topic          string
	DBPath         string
	Store          *Store
	SearchProvider SearchProvider
	SearchClient   SearchClient
	Spawner        ProcessSpawner
	PollInterval   time.Duration
	Timeout        time.Duration
	CommandPath    string
	CommandPrefix  []string
	DirectorModel  model.BaseChatModel
}

// TeamConfig 对应 Claude Code 的 TeamCreate：只建立团队上下文，不隐式创建 teammate。
type TeamConfig struct {
	TeamName       string
	Topic          string
	DBPath         string
	Store          *Store
	SearchProvider SearchProvider
	SearchClient   SearchClient
	Spawner        ProcessSpawner
	PollInterval   time.Duration
	Timeout        time.Duration
	CommandPath    string
	CommandPrefix  []string
}

// TeamRuntime 保存 leader 当前管理的 team 上下文和已动态 spawn 的 teammate。
type TeamRuntime struct {
	TeamName       string
	Topic          string
	LeaderID       string
	Store          *Store
	SearchProvider SearchProvider
	SearchClient   SearchClient
	Spawner        ProcessSpawner
	PollInterval   time.Duration
	Timeout        time.Duration
	CommandPath    string
	CommandPrefix  []string

	ownedStore     bool
	teammates      []TeammateHandle
	processes      []SpawnedProcess
	failedTeammate int
}

// SpawnTeammateRequest 对应 AgentTool(name + team_name) 的动态 teammate 创建请求。
type SpawnTeammateRequest struct {
	Name        string
	Role        AgentRole
	Description string
	Prompt      string
}

// TeammateHandle 是动态 spawn 后返回给 leader 的 teammate 句柄。
type TeammateHandle struct {
	Name    string
	Role    AgentRole
	AgentID string
	Process SpawnedProcess
	TaskID  int64
}

// SpawnCommand 是 leader 交给 ProcessSpawner 的外部 worker 启动契约。
type SpawnCommand struct {
	CommandPath    string
	CommandPrefix  []string
	DBPath         string
	TeamName       string
	AgentName      string
	Role           AgentRole
	SearchProvider SearchProvider
}

// SpawnedProcess 保存外部 worker 的进程句柄摘要。
type SpawnedProcess struct {
	PID int
	cmd *exec.Cmd
}

// ProcessSpawner 隔离真实 os/exec 和测试里的 fake/inline worker。
type ProcessSpawner interface {
	Spawn(ctx context.Context, cmd SpawnCommand) (SpawnedProcess, error)
	Shutdown(ctx context.Context, process SpawnedProcess) error
}

// ExecProcessSpawner 使用 os/exec 启动同一个命令的 worker 模式。
type ExecProcessSpawner struct{}

// Spawn 启动一个外部 worker 进程。
func (s *ExecProcessSpawner) Spawn(ctx context.Context, cmd SpawnCommand) (SpawnedProcess, error) {
	commandPath := firstNonEmpty(cmd.CommandPath, os.Args[0])
	args := append([]string{}, cmd.CommandPrefix...)
	args = append(args,
		"-role", "worker",
		"-team", cmd.TeamName,
		"-agent", cmd.AgentName,
		"-db", cmd.DBPath,
		"-search-provider", string(cmd.SearchProvider),
	)
	process := exec.CommandContext(ctx, commandPath, args...)
	process.Stderr = os.Stderr
	if err := process.Start(); err != nil {
		return SpawnedProcess{}, err
	}
	return SpawnedProcess{PID: process.Process.Pid, cmd: process}, nil
}

// Shutdown 等待 worker 收到 mailbox shutdown 后退出，超时则由 command context 兜底。
func (s *ExecProcessSpawner) Shutdown(ctx context.Context, process SpawnedProcess) error {
	if process.cmd == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- process.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if process.cmd.Process != nil {
			_ = process.cmd.Process.Kill()
		}
		return ctx.Err()
	}
}

// CreateTeam 创建 team lead 和共享上下文；它不会自动创建任何 worker。
func CreateTeam(ctx context.Context, config TeamConfig) (*TeamRuntime, error) {
	if strings.TrimSpace(config.Topic) == "" {
		return nil, fmt.Errorf("topic is required")
	}
	teamName := firstNonEmpty(config.TeamName, defaultTeamName)
	store := config.Store
	ownedStore := false
	if store == nil {
		opened, err := OpenStore(ctx, firstNonEmpty(config.DBPath, defaultDBPath(teamName)))
		if err != nil {
			return nil, err
		}
		store = opened
		ownedStore = true
	}
	if config.SearchProvider == "" {
		config.SearchProvider = SearchProviderFake
	}
	if config.SearchClient == nil {
		config.SearchClient = NewFakeSearchClient()
	}
	if config.Spawner == nil {
		config.Spawner = &ExecProcessSpawner{}
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 200 * time.Millisecond
	}
	if config.Timeout <= 0 {
		config.Timeout = 20 * time.Second
	}
	leaderID := AgentID(defaultLeaderName, teamName)
	if err := store.UpsertMember(ctx, Member{
		TeamName: teamName,
		AgentID:  leaderID,
		Name:     defaultLeaderName,
		Role:     RoleReportDirector,
		Status:   WorkerStatusRunning,
		PID:      os.Getpid(),
	}); err != nil {
		if ownedStore {
			_ = store.Close()
		}
		return nil, err
	}
	return &TeamRuntime{
		TeamName:       teamName,
		Topic:          config.Topic,
		LeaderID:       leaderID,
		Store:          store,
		SearchProvider: config.SearchProvider,
		SearchClient:   config.SearchClient,
		Spawner:        config.Spawner,
		PollInterval:   config.PollInterval,
		Timeout:        config.Timeout,
		CommandPath:    config.CommandPath,
		CommandPrefix:  config.CommandPrefix,
		ownedStore:     ownedStore,
	}, nil
}

// Close 释放 CreateTeam 自动打开的 store；调用方传入的 store 不会被关闭。
func (t *TeamRuntime) Close() error {
	if t == nil || !t.ownedStore || t.Store == nil {
		return nil
	}
	return t.Store.Close()
}

// SpawnTeammate 动态创建一个 teammate，并可选投递初始任务。
func (t *TeamRuntime) SpawnTeammate(ctx context.Context, req SpawnTeammateRequest) (TeammateHandle, error) {
	if t == nil || t.Store == nil {
		return TeammateHandle{}, fmt.Errorf("team runtime is required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return TeammateHandle{}, fmt.Errorf("teammate name is required")
	}
	role := req.Role
	if role == "" {
		role = roleFromAgentName(name)
	}
	agentID := AgentID(name, t.TeamName)
	if err := t.Store.UpsertMember(ctx, Member{
		TeamName: t.TeamName,
		AgentID:  agentID,
		Name:     name,
		Role:     role,
		Status:   WorkerStatusStarting,
	}); err != nil {
		return TeammateHandle{}, err
	}
	process, err := t.Spawner.Spawn(ctx, SpawnCommand{
		CommandPath:    firstNonEmpty(t.CommandPath, os.Args[0]),
		CommandPrefix:  t.CommandPrefix,
		DBPath:         t.Store.Path(),
		TeamName:       t.TeamName,
		AgentName:      name,
		Role:           role,
		SearchProvider: t.SearchProvider,
	})
	if err != nil {
		t.failedTeammate++
		_ = t.Store.UpsertMember(ctx, Member{TeamName: t.TeamName, AgentID: agentID, Name: name, Role: role, Status: WorkerStatusFailed})
		return TeammateHandle{}, err
	}
	_ = t.Store.UpsertMember(ctx, Member{
		TeamName: t.TeamName,
		AgentID:  agentID,
		Name:     name,
		Role:     role,
		Status:   WorkerStatusIdle,
		PID:      process.PID,
	})
	handle := TeammateHandle{Name: name, Role: role, AgentID: agentID, Process: process}
	t.teammates = append(t.teammates, handle)
	t.processes = append(t.processes, process)
	if strings.TrimSpace(req.Prompt) != "" {
		taskID, err := t.DispatchTask(ctx, name, firstNonEmpty(req.Description, "teammate task"), req.Prompt)
		if err != nil {
			return handle, err
		}
		handle.TaskID = taskID
		t.teammates[len(t.teammates)-1] = handle
	}
	return handle, nil
}

// DispatchTask 向已存在 teammate 投递后续 mailbox 任务。
func (t *TeamRuntime) DispatchTask(ctx context.Context, agentName string, title string, prompt string) (int64, error) {
	return dispatchTask(ctx, t.Store, t.TeamName, t.LeaderID, agentName, title, t.Topic, prompt)
}

// ShutdownTeammates 给所有动态创建的 teammate 发送 shutdown 并等待退出。
func (t *TeamRuntime) ShutdownTeammates(ctx context.Context) int {
	if t == nil {
		return 0
	}
	for _, teammate := range t.teammates {
		_, _ = t.Store.EnqueueMessage(ctx, MailboxMessage{
			TeamName:    t.TeamName,
			FromAgent:   t.LeaderID,
			ToAgent:     teammate.AgentID,
			Kind:        MessageKindShutdown,
			ContentJSON: `{}`,
		})
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	failed := 0
	for _, process := range t.processes {
		if err := t.Spawner.Shutdown(shutdownCtx, process); err != nil && shutdownCtx.Err() == nil {
			failed++
		}
	}
	_ = t.Store.UpsertMember(ctx, Member{TeamName: t.TeamName, AgentID: t.LeaderID, Name: defaultLeaderName, Role: RoleReportDirector, Status: WorkerStatusStopped, PID: os.Getpid()})
	return failed
}

// RunLeader 执行一条命令入口背后的完整调查报告 swarm。
func RunLeader(ctx context.Context, config LeaderConfig) (*LeaderResult, error) {
	store := config.Store
	if store == nil {
		opened, err := OpenStore(ctx, firstNonEmpty(config.DBPath, defaultDBPath(firstNonEmpty(config.TeamName, defaultTeamName))))
		if err != nil {
			return nil, err
		}
		defer opened.Close()
		store = opened
	}
	team, err := CreateTeam(ctx, TeamConfig{
		TeamName:       config.TeamName,
		Topic:          config.Topic,
		Store:          store,
		SearchProvider: config.SearchProvider,
		SearchClient:   config.SearchClient,
		Spawner:        config.Spawner,
		PollInterval:   config.PollInterval,
		Timeout:        config.Timeout,
		CommandPath:    config.CommandPath,
		CommandPrefix:  config.CommandPrefix,
	})
	if err != nil {
		return nil, err
	}
	teamName := team.TeamName
	leaderCtx, cancel := context.WithTimeout(ctx, team.Timeout)
	defer cancel()
	directorModel := config.DirectorModel
	if directorModel == nil {
		directorModel = &deterministicDirectorModel{}
	}
	err = runLeaderLifecycle(leaderCtx, team, directorModel)
	result := partialLeaderResult(ctx, store, teamName, config.Topic, team.failedTeammate)
	result.FailedWorkerCount = team.failedTeammate + team.ShutdownTeammates(ctx)
	if err != nil {
		return result, err
	}
	return result, nil
}

// runLeaderLifecycle 采用 Claude Code 风格的事件推进：director 每轮只响应 leader 输入或 teammate 完成事件。
func runLeaderLifecycle(ctx context.Context, team *TeamRuntime, directorModel model.BaseChatModel) error {
	if err := runLeaderAgent(ctx, team, directorModel, LeaderDirectorInput{
		Type:     "start",
		TeamName: team.TeamName,
		Topic:    team.Topic,
	}); err != nil {
		return err
	}
	deadline := time.Now().Add(team.Timeout)
	finalReportEventSeen := false
	for {
		messages, err := team.Store.ConsumeMessages(ctx, team.TeamName, team.LeaderID, 10)
		if err != nil {
			if isSQLiteLocked(err) {
				time.Sleep(team.PollInterval)
				continue
			}
			return fmt.Errorf("consume leader mailbox: %w", err)
		}
		for _, message := range messages {
			input := leaderInputFromMailbox(team, message)
			if input.Event != nil && input.Event.Artifact == "final_report" {
				finalReportEventSeen = true
			}
			if err := runLeaderAgent(ctx, team, directorModel, input); err != nil {
				return err
			}
		}
		done, err := hasSection(ctx, team.Store, team.TeamName, "最终报告")
		if err != nil {
			return err
		}
		if done && finalReportEventSeen {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for research swarm")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(team.PollInterval):
		}
	}
}

func leaderInputFromMailbox(team *TeamRuntime, message MailboxMessage) LeaderDirectorInput {
	input := LeaderDirectorInput{
		Type:     string(message.Kind),
		TeamName: team.TeamName,
		Topic:    team.Topic,
	}
	var event TaskCompletionEvent
	if err := json.Unmarshal([]byte(message.ContentJSON), &event); err == nil && strings.TrimSpace(event.Type) != "" {
		input.Event = &event
	}
	return input
}

func dispatchTask(ctx context.Context, store *Store, teamName string, leaderID string, agentName string, title string, topic string, prompt string) (int64, error) {
	agentID := AgentID(agentName, teamName)
	task, err := store.CreateTask(ctx, ResearchTask{TeamName: teamName, Assignee: agentID, Title: title, Status: TaskStatusPending})
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(TaskPayload{TaskID: task.ID, Topic: topic, Prompt: prompt})
	if err != nil {
		return 0, err
	}
	_, err = store.EnqueueMessage(ctx, MailboxMessage{
		TeamName:    teamName,
		FromAgent:   leaderID,
		ToAgent:     agentID,
		Kind:        MessageKindTask,
		ContentJSON: string(payload),
	})
	return task.ID, err
}

func hasSection(ctx context.Context, store *Store, teamName string, sectionName string) (bool, error) {
	sections, err := store.ListReportSections(ctx, teamName)
	if err != nil {
		return false, err
	}
	for _, section := range sections {
		if section.Section == sectionName {
			return true, nil
		}
	}
	return false, nil
}

func partialLeaderResult(ctx context.Context, store *Store, teamName string, topic string, failedWorkers int) *LeaderResult {
	cards, _ := store.ListSourceCards(ctx, teamName)
	sections, _ := store.ListReportSections(ctx, teamName)
	finalReport := buildFinalReport(sections)
	return &LeaderResult{
		TeamName:           teamName,
		Topic:              topic,
		SourceCardCount:    len(cards),
		ReportSectionCount: len(sections),
		FailedWorkerCount:  failedWorkers,
		FinalReport:        finalReport,
		SourceCards:        cards,
		ReportSections:     sections,
	}
}

func buildFinalReport(sections []ReportSection) string {
	for _, section := range sections {
		if section.Section == "最终报告" {
			return section.Content
		}
	}
	var builder strings.Builder
	for _, section := range sections {
		if strings.TrimSpace(section.Content) == "" {
			continue
		}
		builder.WriteString("## ")
		builder.WriteString(section.Section)
		builder.WriteString("\n")
		builder.WriteString(section.Content)
		builder.WriteString("\n\n")
	}
	return strings.TrimSpace(builder.String())
}

func defaultDBPath(teamName string) string {
	return os.TempDir() + string(os.PathSeparator) + teamName + "-research-swarm.sqlite"
}
