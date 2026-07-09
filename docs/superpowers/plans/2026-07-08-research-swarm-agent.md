# Research Swarm Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a runnable Go + Eino ADK external-process research report swarm where one leader command creates a team, runs a report_director ADK agent, dynamically spawns teammate worker processes through a model-visible `spawn_teammate` tool, coordinates them through SQLite mailbox storage, uses fake or HTTP search, and emits a sourced investigation report.

**Architecture:** `action/research_swarm` owns the reusable domain model, SQLite store, search clients, ADK tools, `CreateTeam` / `TeamRuntime.SpawnTeammate` boundaries, report_director tools, leader runner, and worker loop. `cmd/research-report-agent` stays thin: parse CLI/env, build config, run leader or worker mode, and print text/JSON output. Tests use fake search, fake process spawning, and deterministic fake models by default; real multi-process command execution is guarded by `CMD_E2E=1`.

**Tech Stack:** Go 1.25.8, `github.com/cloudwego/eino v0.9.6`, Eino ADK `ChatModelAgent`, SQLite via `github.com/mattn/go-sqlite3`, standard `net/http` for HTTP JSON search.

## Global Constraints

- Preserve existing repository changes outside this feature, especially current `reasoning/cot_v2` and `cmd/cot-v2-agent` work.
- Keep package code under `action/research_swarm/` and command code under `cmd/research-report-agent/`.
- Add concise Chinese comments before new exported structs, exported functions, and non-obvious lifecycle logic.
- Default local execution must not require network search or real model credentials.
- Default search provider is fake; HTTP JSON search is enabled through `SEARCH_PROVIDER=http_json`, `SEARCH_API_URL`, and optional `SEARCH_API_KEY`.
- Worker identity format is `<agent_name>@<team_name>`.
- Team creation must not hardcode or implicitly create a teammate roster; teammates join through report_director model calls to `spawn_teammate`.
- Cross-agent communication must go through SQLite mailbox APIs and `send_message`, not shared in-memory state.
- Root trace/command summary must not include full search payloads or full report body.

---

## File Structure

- Create `action/research_swarm/types.go`: shared enums and DTOs.
- Create `action/research_swarm/store.go`: SQLite schema and persistence methods.
- Create `action/research_swarm/search.go`: fake and HTTP JSON search clients.
- Create `action/research_swarm/tools.go`: Eino ADK tool constructors for mailbox, task, source, and section operations.
- Create `action/research_swarm/agent.go`: role-aware ADK agent construction plus deterministic fake model helpers for tests/demo.
- Create `action/research_swarm/leader.go`: `CreateTeam`, `TeamRuntime.SpawnTeammate`, task dispatch, report aggregation, and shutdown.
- Create `action/research_swarm/director.go`: report_director ADK agent plus model-visible `spawn_teammate` tool.
- Create `action/research_swarm/worker.go`: mailbox polling worker loop.
- Create `action/research_swarm/*_test.go`: focused tests for store/search/tools/leader/worker behavior.
- Create `cmd/research-report-agent/main.go`: CLI entrypoint.
- Create `cmd/research-report-agent/trace.go`: compact command summary helpers.
- Create `cmd/research-report-agent/main_test.go`: command config, prepare-only, JSON summary, and guarded E2E tests.

---

### Task 1: Core Contracts And SQLite Store

**Files:**
- Create: `action/research_swarm/types.go`
- Create: `action/research_swarm/store.go`
- Test: `action/research_swarm/store_test.go`

**Interfaces:**
- Produces `OpenStore(ctx context.Context, path string) (*Store, error)`.
- Produces store methods: `UpsertMember`, `ListMembers`, `EnqueueMessage`, `ConsumeMessages`, `CreateTask`, `UpdateTask`, `ListTasks`, `SaveSourceCard`, `ListSourceCards`, `SaveReportSection`, `ListReportSections`.

- [ ] **Step 1: Write failing store tests**

```go
func TestStoreMailboxConsumesEachMessageOnce(t *testing.T) {
    store := openTestStore(t)
    msg, err := store.EnqueueMessage(context.Background(), MailboxMessage{TeamName: "team-a", FromAgent: "leader@team-a", ToAgent: "searcher@team-a", Kind: MessageKindTask, ContentJSON: `{"topic":"agent search"}`})
    requireNoError(t, err)
    got, err := store.ConsumeMessages(context.Background(), "team-a", "searcher@team-a", 10)
    requireNoError(t, err)
    if len(got) != 1 || got[0].ID != msg.ID {
        t.Fatalf("got messages = %#v, want id %d", got, msg.ID)
    }
    again, err := store.ConsumeMessages(context.Background(), "team-a", "searcher@team-a", 10)
    requireNoError(t, err)
    if len(again) != 0 {
        t.Fatalf("message consumed twice: %#v", again)
    }
}
```

- [ ] **Step 2: Run failing test**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run TestStoreMailboxConsumesEachMessageOnce -count=1`
Expected: fail because package/files do not exist yet.

- [ ] **Step 3: Implement contracts and SQLite store**

Implement the schema exactly from the design doc and keep SQL behind `Store` methods.

- [ ] **Step 4: Run store tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run 'TestStore' -count=1`
Expected: pass.

---

### Task 2: Search Clients And ADK Tools

**Files:**
- Create: `action/research_swarm/search.go`
- Create: `action/research_swarm/tools.go`
- Test: `action/research_swarm/search_test.go`
- Test: `action/research_swarm/tools_test.go`

**Interfaces:**
- Consumes `Store`, `SourceCard`, `ReportSection`.
- Produces `SearchClient`, `FakeSearchClient`, `HTTPJSONSearchClient`.
- Produces `Toolset` and `NewRoleTools(ctx, ToolConfig) ([]tool.BaseTool, error)`.

- [ ] **Step 1: Write failing search/tool tests**

```go
func TestFakeSearchClientReturnsStableResults(t *testing.T) {
    client := NewFakeSearchClient()
    resp, err := client.Search(context.Background(), SearchRequest{Query: "AI Agent search risk", TopK: 2, Language: "zh"})
    requireNoError(t, err)
    if len(resp.Results) != 2 {
        t.Fatalf("results = %d, want 2", len(resp.Results))
    }
    if resp.Results[0].URL == "" || resp.Results[0].Title == "" {
        t.Fatalf("first result missing citation fields: %#v", resp.Results[0])
    }
}
```

- [ ] **Step 2: Run failing tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run 'TestFakeSearchClient|TestRoleTools' -count=1`
Expected: fail because clients/tools are missing.

- [ ] **Step 3: Implement search clients and tool constructors**

Use `toolutils.InferTool` with `jsonschema` tags on request structs. Searcher gets search/source tools; analyst and writer get list/save section tools; all roles get message/task tools.

- [ ] **Step 4: Run search/tool tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run 'TestFakeSearchClient|TestHTTPJSONSearchClient|TestRoleTools' -count=1`
Expected: pass.

---

### Task 3: Role Agent, Worker Loop, And Leader Orchestration

**Files:**
- Create: `action/research_swarm/agent.go`
- Create: `action/research_swarm/worker.go`
- Create: `action/research_swarm/leader.go`
- Test: `action/research_swarm/worker_test.go`
- Test: `action/research_swarm/leader_test.go`

**Interfaces:**
- Consumes store/search/tools from Tasks 1-2.
- Produces `NewRoleAgent(ctx, RoleAgentConfig) (adk.Agent, error)`.
- Produces `RunWorker(ctx, WorkerConfig) error`.
- Produces `RunLeader(ctx, LeaderConfig) (*LeaderResult, error)`.
- Produces `ProcessSpawner` with `ExecProcessSpawner` and fake test implementation.

- [ ] **Step 1: Write failing leader/worker tests**

```go
func TestLeaderUsesSpawnerAndMailbox(t *testing.T) {
    store := openTestStore(t)
    spawner := &fakeSpawner{}
    result, err := RunLeader(context.Background(), LeaderConfig{TeamName: "team-a", Topic: "AI Agent 外部搜索风险", Store: store, SearchProvider: SearchProviderFake, Spawner: spawner, PollInterval: time.Millisecond, Timeout: 3 * time.Second, CommandPath: "research-report-agent"})
    requireNoError(t, err)
    if len(spawner.commands) != 3 {
        t.Fatalf("commands = %d, want 3", len(spawner.commands))
    }
    if result.TeamName != "team-a" || result.Topic == "" {
        t.Fatalf("bad result: %#v", result)
    }
}
```

- [ ] **Step 2: Run failing tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run 'TestLeader|TestWorker' -count=1`
Expected: fail because orchestration is missing.

- [ ] **Step 3: Implement deterministic worker behavior and ADK wrapper**

Worker mode must build an Eino ADK agent. For local fake-model execution, use deterministic role model behavior so E2E does not require credentials while still exercising `adk.Runner.Query`.

- [ ] **Step 4: Implement leader orchestration**

Leader first creates only the team context. The default one-command investigation flow then runs report_director with a `start` input; the director model calls `spawn_teammate` for searcher, then waits for worker completion to come back through leader mailbox. Worker runtime sends `task_completed` messages with `type:"artifact_ready"` after source cards or report sections are persisted. Leader feeds those events into the next director turn, then the director decides whether to spawn analyst or writer. When the final report section exists, leader sends shutdown and aggregates the report.

- [ ] **Step 5: Run leader/worker tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -count=1`
Expected: pass.

---

### Task 4: Command Entrypoint And End-To-End Verification

**Files:**
- Create: `cmd/research-report-agent/main.go`
- Create: `cmd/research-report-agent/trace.go`
- Test: `cmd/research-report-agent/main_test.go`

**Interfaces:**
- Consumes `researchswarm.RunLeader`, `researchswarm.RunWorker`, `researchswarm.NewSearchClientFromEnv`.
- Produces user-facing commands:
  - `go run ./cmd/research-report-agent -prepare-only`
  - `go run ./cmd/research-report-agent -role leader -topic "..."`
  - `go run ./cmd/research-report-agent -role leader -topic "..." -json`

- [ ] **Step 1: Write failing command tests**

```go
func TestRunPrepareOnlyDoesNotRequireModelOrWorkers(t *testing.T) {
    out, err := run(context.Background(), []string{"-prepare-only"})
    requireNoError(t, err)
    if out.Mode != "prepare" || out.WorkerCount != 3 {
        t.Fatalf("bad prepare output: %#v", out)
    }
}
```

- [ ] **Step 2: Run failing command tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/research-report-agent -count=1`
Expected: fail because command package is missing.

- [ ] **Step 3: Implement CLI**

Parse flags, load env-driven search config, use default temporary DB path, run leader or worker, and print compact text or JSON summaries.

- [ ] **Step 4: Run command tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/research-report-agent -count=1`
Expected: pass.

- [ ] **Step 5: Run local end-to-end command**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/research-report-agent -role leader -topic "调查 AI Agent 外部搜索在客服工单中的风险控制价值" -json`
Expected: command exits 0 and JSON output includes `team_name`, `source_card_count`, `report_section_count`, `final_report`, and `failed_worker_count: 0`.

---

### Task 5: Final Audit

**Files:**
- Modify only files created by this plan if verification shows gaps.

**Interfaces:**
- Consumes all prior task outputs.
- Produces fresh evidence that design requirements are satisfied.

- [ ] **Step 1: Format all new Go files**

Run: `gofmt -w action/research_swarm cmd/research-report-agent`

- [ ] **Step 2: Run package tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm ./cmd/research-report-agent -count=1`
Expected: pass.

- [ ] **Step 3: Run command E2E**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/research-report-agent -role leader -topic "调查 AI Agent 外部搜索在客服工单中的风险控制价值" -json`
Expected: pass with non-empty sourced report.

- [ ] **Step 4: Design coverage audit**

Check the design doc sections against implemented files:
`rg -n "SearchProviderFake|HTTPJSONSearchClient|ProcessSpawner|send_message|source_cards|report_sections|RunLeader|RunWorker" action/research_swarm cmd/research-report-agent`

- [ ] **Step 5: Report exact evidence**

Final response must list changed files, test commands run, and the local E2E command result.
