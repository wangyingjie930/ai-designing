package researchswarm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestLeaderUsesSpawnerAndMailbox 验证 leader 日常入口会启动三个外部 worker，并通过 SQLite 协作产出报告。
func TestLeaderUsesSpawnerAndMailbox(t *testing.T) {
	store := openTestStore(t)
	spawner := &fakeSpawner{}
	result, err := RunLeader(context.Background(), LeaderConfig{
		TeamName:       "team-a",
		Topic:          "AI Agent 外部搜索风险",
		Store:          store,
		SearchProvider: SearchProviderFake,
		SearchClient:   NewFakeSearchClient(),
		Spawner:        spawner,
		PollInterval:   time.Millisecond,
		Timeout:        3 * time.Second,
		CommandPath:    "research-report-agent",
	})
	if err != nil {
		t.Fatalf("RunLeader() error = %v worker_errors=%v", err, spawner.errors())
	}
	if len(spawner.commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(spawner.commands))
	}
	if len(spawner.errors()) > 0 {
		t.Fatalf("worker errors = %v", spawner.errors())
	}
	if result.TeamName != "team-a" || result.SourceCardCount == 0 || result.ReportSectionCount == 0 {
		t.Fatalf("bad result: %#v", result)
	}
	if !strings.Contains(result.FinalReport, "[S") {
		t.Fatalf("final report should include source ids: %s", result.FinalReport)
	}
	if !strings.Contains(result.FinalReport, "AI Agent 外部搜索风险") {
		t.Fatalf("final report should use runtime topic: %s", result.FinalReport)
	}
}

// TestRunLeaderSpawnsThroughDirectorToolCalls 验证 RunLeader 不在 Go 代码里显式固定调用 SpawnTeammate。
func TestRunLeaderSpawnsThroughDirectorToolCalls(t *testing.T) {
	store := openTestStore(t)
	spawner := &fakeSpawner{}
	result, err := RunLeader(context.Background(), LeaderConfig{
		TeamName:       "team-tool-driven",
		Topic:          "AI Agent 外部搜索风险",
		Store:          store,
		SearchProvider: SearchProviderFake,
		SearchClient:   NewFakeSearchClient(),
		Spawner:        spawner,
		PollInterval:   time.Millisecond,
		Timeout:        3 * time.Second,
		CommandPath:    "research-report-agent",
		DirectorModel:  &singleSpawnDirectorModel{},
	})
	requireNoError(t, err)
	if len(spawner.commands) != 1 || spawner.commands[0].AgentName != "tool-selected-worker" {
		t.Fatalf("spawned commands = %#v, want exactly the model-requested teammate", spawner.commands)
	}
	if result.TeamName != "team-tool-driven" {
		t.Fatalf("result = %#v", result)
	}
	if result.ReportSectionCount == 0 || !strings.Contains(result.FinalReport, "AI Agent 外部搜索风险") {
		t.Fatalf("result = %#v", result)
	}
}

// TestRunLeaderFeedsCompletionEventsToDirector 验证 leader 把 worker 显式结果消息作为下一轮输入交给 director。
func TestRunLeaderFeedsCompletionEventsToDirector(t *testing.T) {
	store := openTestStore(t)
	spawner := &fakeSpawner{}
	director := &eventDrivenDirectorModel{}
	result, err := RunLeader(context.Background(), LeaderConfig{
		TeamName:       "team-event-driven",
		Topic:          "AI Agent 外部搜索风险",
		Store:          store,
		SearchProvider: SearchProviderFake,
		SearchClient:   NewFakeSearchClient(),
		Spawner:        spawner,
		PollInterval:   time.Millisecond,
		Timeout:        3 * time.Second,
		CommandPath:    "research-report-agent",
		DirectorModel:  director,
	})
	if err != nil {
		t.Fatalf("RunLeader() error = %v worker_errors=%v", err, spawner.errors())
	}
	if got := spawnedAgentNames(spawner.commands); strings.Join(got, ",") != "searcher,analyst,writer" {
		t.Fatalf("spawned agents = %v", got)
	}
	if !director.sawSearcherCompletion || !director.sawAnalystCompletion || !director.sawWriterCompletion {
		t.Fatalf("director did not see completion events: %#v", director)
	}
	if !strings.Contains(result.FinalReport, "AI Agent 外部搜索风险") {
		t.Fatalf("final report should use runtime topic: %s", result.FinalReport)
	}
}

// TestCreateTeamDoesNotSpawnWorkers 验证 TeamCreate 风格的动作只创建团队上下文，不隐式固定 teammate roster。
func TestCreateTeamDoesNotSpawnWorkers(t *testing.T) {
	store := openTestStore(t)
	spawner := &fakeSpawner{}
	team, err := CreateTeam(context.Background(), TeamConfig{
		TeamName:       "team-dynamic",
		Topic:          "AI Agent 外部搜索风险",
		Store:          store,
		SearchProvider: SearchProviderFake,
		Spawner:        spawner,
	})
	requireNoError(t, err)
	if team.TeamName != "team-dynamic" {
		t.Fatalf("team name = %q", team.TeamName)
	}
	if len(spawner.commands) != 0 {
		t.Fatalf("CreateTeam spawned workers = %#v", spawner.commands)
	}
	members, err := store.ListMembers(context.Background(), "team-dynamic")
	requireNoError(t, err)
	if len(members) != 1 || members[0].Role != RoleReportDirector {
		t.Fatalf("members = %#v, want only leader", members)
	}
}

// TestSpawnTeammateAddsOneNamedWorker 验证 teammate 是通过显式 spawn 动作逐个加入团队，而不是从固定列表批量创建。
func TestSpawnTeammateAddsOneNamedWorker(t *testing.T) {
	store := openTestStore(t)
	spawner := &passiveSpawner{}
	team, err := CreateTeam(context.Background(), TeamConfig{
		TeamName:       "team-dynamic",
		Topic:          "AI Agent 外部搜索风险",
		Store:          store,
		SearchProvider: SearchProviderFake,
		Spawner:        spawner,
		CommandPath:    "research-report-agent",
	})
	requireNoError(t, err)
	teammate, err := team.SpawnTeammate(context.Background(), SpawnTeammateRequest{
		Name: "web-researcher",
		Role: RoleSearcher,
	})
	requireNoError(t, err)
	if teammate.AgentID != "web-researcher@team-dynamic" {
		t.Fatalf("agent id = %q", teammate.AgentID)
	}
	if len(spawner.commands) != 1 || spawner.commands[0].AgentName != "web-researcher" {
		t.Fatalf("spawned commands = %#v", spawner.commands)
	}
	members, err := store.ListMembers(context.Background(), "team-dynamic")
	requireNoError(t, err)
	if len(members) != 2 {
		t.Fatalf("members = %#v, want leader plus one teammate", members)
	}
}

// TestSpawnTeammateUsesPromptAndDescription 验证 spawn 入参对齐 Claude Code AgentTool 的 description/prompt。
func TestSpawnTeammateUsesPromptAndDescription(t *testing.T) {
	store := openTestStore(t)
	team, err := CreateTeam(context.Background(), TeamConfig{
		TeamName:       "team-dynamic",
		Topic:          "AI Agent 外部搜索风险",
		Store:          store,
		SearchProvider: SearchProviderFake,
		Spawner:        &passiveSpawner{},
		CommandPath:    "research-report-agent",
	})
	requireNoError(t, err)
	_, err = team.SpawnTeammate(context.Background(), SpawnTeammateRequest{
		Name:        "web-researcher",
		Role:        RoleSearcher,
		Description: "搜索资料",
		Prompt:      "围绕客服工单风险控制整理证据",
	})
	requireNoError(t, err)
	tasks, err := store.ListTasks(context.Background(), "team-dynamic")
	requireNoError(t, err)
	if len(tasks) != 1 || tasks[0].Title != "搜索资料" {
		t.Fatalf("tasks = %#v, want description as task title", tasks)
	}
	messages, err := store.ConsumeMessages(context.Background(), "team-dynamic", "web-researcher@team-dynamic", 1)
	requireNoError(t, err)
	if len(messages) != 1 || !strings.Contains(messages[0].ContentJSON, "客服工单风险控制") {
		t.Fatalf("messages = %#v, want prompt in mailbox payload", messages)
	}
}

// TestWorkerUsesTaskPromptAsSearchQuery 验证默认 fake model 消费运行时 task prompt，不把调查主题写死在模型里。
func TestWorkerUsesTaskPromptAsSearchQuery(t *testing.T) {
	store := openTestStore(t)
	requireNoError(t, store.UpsertMember(context.Background(), Member{
		TeamName: "team-topic",
		AgentID:  "report_director@team-topic",
		Name:     "report_director",
		Role:     RoleReportDirector,
		Status:   WorkerStatusRunning,
	}))
	payload, err := json.Marshal(TaskPayload{
		Topic:  "客服工单风险控制",
		Prompt: "调查知识库检索在售后工单中的误召回风险",
	})
	requireNoError(t, err)
	_, err = store.EnqueueMessage(context.Background(), MailboxMessage{
		TeamName:    "team-topic",
		FromAgent:   "report_director@team-topic",
		ToAgent:     "searcher@team-topic",
		Kind:        MessageKindTask,
		ContentJSON: string(payload),
	})
	requireNoError(t, err)

	err = RunWorker(context.Background(), WorkerConfig{
		TeamName:      "team-topic",
		AgentName:     "searcher",
		Role:          RoleSearcher,
		Store:         store,
		SearchClient:  NewFakeSearchClient(),
		PollInterval:  time.Millisecond,
		MaxIdleTicks:  1,
		MaxIterations: 8,
	})
	requireNoError(t, err)
	cards, err := store.ListSourceCards(context.Background(), "team-topic")
	requireNoError(t, err)
	if len(cards) == 0 || cards[0].Query != "调查知识库检索在售后工单中的误召回风险" {
		t.Fatalf("source cards = %#v, want runtime prompt as query", cards)
	}
}

// TestWorkerReportsCompletionToLeaderOnly 验证 worker 显式汇报结果，并由 runtime 单独上报 idle。
func TestWorkerReportsCompletionToLeaderOnly(t *testing.T) {
	store := openTestStore(t)
	requireNoError(t, store.UpsertMember(context.Background(), Member{
		TeamName: "team-topic",
		AgentID:  "report_director@team-topic",
		Name:     "report_director",
		Role:     RoleReportDirector,
		Status:   WorkerStatusRunning,
	}))
	task, err := store.CreateTask(context.Background(), ResearchTask{
		TeamName: "team-topic",
		Assignee: "searcher@team-topic",
		Title:    "搜索资料",
		Status:   TaskStatusPending,
	})
	requireNoError(t, err)
	payload, err := json.Marshal(TaskPayload{
		TaskID: task.ID,
		Topic:  "客服工单风险控制",
		Prompt: "调查知识库检索在售后工单中的误召回风险",
	})
	requireNoError(t, err)
	_, err = store.EnqueueMessage(context.Background(), MailboxMessage{
		TeamName:    "team-topic",
		FromAgent:   "report_director@team-topic",
		ToAgent:     "searcher@team-topic",
		Kind:        MessageKindTask,
		ContentJSON: string(payload),
	})
	requireNoError(t, err)

	err = RunWorker(context.Background(), WorkerConfig{
		TeamName:      "team-topic",
		AgentName:     "searcher",
		Role:          RoleSearcher,
		Store:         store,
		SearchClient:  NewFakeSearchClient(),
		PollInterval:  time.Millisecond,
		MaxIdleTicks:  1,
		MaxIterations: 8,
	})
	requireNoError(t, err)
	leaderMessages, err := store.ConsumeMessages(context.Background(), "team-topic", "report_director@team-topic", 10)
	requireNoError(t, err)
	if len(leaderMessages) != 2 || leaderMessages[0].Kind != MessageKindNotification || leaderMessages[1].Kind != MessageKindNotification {
		t.Fatalf("leader messages = %#v, want explicit result plus idle notification", leaderMessages)
	}
	if !strings.Contains(leaderMessages[0].ContentJSON, `"from":"searcher"`) || !strings.Contains(leaderMessages[0].ContentJSON, `"message":"source_cards`) {
		t.Fatalf("explicit result payload = %s", leaderMessages[0].ContentJSON)
	}
	if !strings.Contains(leaderMessages[1].ContentJSON, `"type":"idle_notification"`) || !strings.Contains(leaderMessages[1].ContentJSON, `"completed_status":"resolved"`) {
		t.Fatalf("idle payload = %s", leaderMessages[1].ContentJSON)
	}
	analystMessages, err := store.ConsumeMessages(context.Background(), "team-topic", "analyst@team-topic", 10)
	requireNoError(t, err)
	if len(analystMessages) != 0 {
		t.Fatalf("analyst should not receive default peer notification: %#v", analystMessages)
	}
}

// TestWorkerConsumesShutdown 验证 worker 收到 shutdown 控制消息后会更新状态并退出。
func TestWorkerConsumesShutdown(t *testing.T) {
	store := openTestStore(t)
	requireNoError(t, store.UpsertMember(context.Background(), Member{
		TeamName: "team-a",
		AgentID:  "searcher@team-a",
		Name:     "searcher",
		Role:     RoleSearcher,
		Status:   WorkerStatusIdle,
	}))
	_, err := store.EnqueueMessage(context.Background(), MailboxMessage{
		TeamName:    "team-a",
		FromAgent:   "leader@team-a",
		ToAgent:     "searcher@team-a",
		Kind:        MessageKindShutdown,
		ContentJSON: `{}`,
	})
	requireNoError(t, err)

	err = RunWorker(context.Background(), WorkerConfig{
		TeamName:     "team-a",
		AgentName:    "searcher",
		Role:         RoleSearcher,
		Store:        store,
		SearchClient: NewFakeSearchClient(),
		PollInterval: time.Millisecond,
		MaxIdleTicks: 3,
	})
	requireNoError(t, err)
	members, err := store.ListMembers(context.Background(), "team-a")
	requireNoError(t, err)
	if len(members) != 1 || members[0].Status != WorkerStatusStopped {
		t.Fatalf("members = %#v, want stopped worker", members)
	}
}

type fakeSpawner struct {
	mu       sync.Mutex
	commands []SpawnCommand
	errs     []string
}

func (s *fakeSpawner) Spawn(ctx context.Context, cmd SpawnCommand) (SpawnedProcess, error) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	s.mu.Unlock()
	go func() {
		store, err := OpenStore(ctx, cmd.DBPath)
		if err != nil {
			s.recordError("open store: " + err.Error())
			return
		}
		defer store.Close()
		if err := RunWorker(ctx, WorkerConfig{
			TeamName:     cmd.TeamName,
			AgentName:    cmd.AgentName,
			Role:         cmd.Role,
			Store:        store,
			SearchClient: NewFakeSearchClient(),
			PollInterval: time.Millisecond,
			MaxIdleTicks: 200,
		}); err != nil {
			s.recordError("run worker: " + err.Error())
		}
	}()
	return SpawnedProcess{PID: 1000 + len(s.commands)}, nil
}

func (s *fakeSpawner) Shutdown(ctx context.Context, process SpawnedProcess) error {
	return nil
}

func (s *fakeSpawner) recordError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *fakeSpawner) errors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.errs...)
}

type passiveSpawner struct {
	commands []SpawnCommand
}

func (s *passiveSpawner) Spawn(ctx context.Context, cmd SpawnCommand) (SpawnedProcess, error) {
	s.commands = append(s.commands, cmd)
	return SpawnedProcess{PID: 2000 + len(s.commands)}, nil
}

func (s *passiveSpawner) Shutdown(ctx context.Context, process SpawnedProcess) error {
	return nil
}

type singleSpawnDirectorModel struct {
	calls int
}

func (m *singleSpawnDirectorModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		return toolCallMessage("call_spawn_teammate", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        "tool-selected-worker",
			"role":        string(RoleWriter),
			"description": "模型选择的任务",
			"prompt":      "AI Agent 外部搜索风险",
		})), nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *singleSpawnDirectorModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

type eventDrivenDirectorModel struct {
	spawnedSearch         bool
	spawnedAnalyst        bool
	spawnedWriter         bool
	sawSearcherCompletion bool
	sawAnalystCompletion  bool
	sawWriterCompletion   bool
}

func (m *eventDrivenDirectorModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	text := joinedMessageContent(input)
	switch {
	case !m.spawnedSearch:
		m.spawnedSearch = true
		return toolCallMessage("call_spawn_searcher", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        defaultSearchAgent,
			"role":        string(RoleSearcher),
			"description": string(RoleSearcher),
			"prompt":      "AI Agent 外部搜索风险",
		})), nil
	case !m.spawnedAnalyst && strings.Contains(text, `"from":"searcher"`):
		m.sawSearcherCompletion = true
		m.spawnedAnalyst = true
		return toolCallMessage("call_spawn_analyst", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        defaultAnalystAgent,
			"role":        string(RoleAnalyst),
			"description": string(RoleAnalyst),
			"prompt":      "AI Agent 外部搜索风险",
		})), nil
	case !m.spawnedWriter && strings.Contains(text, `"from":"analyst"`):
		m.sawAnalystCompletion = true
		m.spawnedWriter = true
		return toolCallMessage("call_spawn_writer", SpawnTeammateToolName, toolArgs(map[string]any{
			"name":        defaultWriterAgent,
			"role":        string(RoleWriter),
			"description": string(RoleWriter),
			"prompt":      "AI Agent 外部搜索风险",
		})), nil
	case strings.Contains(text, `"from":"writer"`):
		m.sawWriterCompletion = true
		return schema.AssistantMessage("调查报告已完成。", nil), nil
	default:
		return schema.AssistantMessage("等待 teammate 显式结果消息。", nil), nil
	}
}

func (m *eventDrivenDirectorModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

func joinedMessageContent(messages []*schema.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		if message == nil {
			continue
		}
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func spawnedAgentNames(commands []SpawnCommand) []string {
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		out = append(out, command.AgentName)
	}
	return out
}
