# Muse Sales Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a runnable Eino ADK sales-conversation reference agent under `muse/` that preserves silicon-sage's Planner/Thinker/Writer business flow while adding conditional review, direct but idempotent Profile/TODO tools, durable recovery, typed finalization, and reliable Outbox delivery.

**Architecture:** A custom deterministic `muse.Agent` implements Eino ADK `adk.ResumableAgent` and owns the stage machine. Planner and Thinker use controlled Eino ChatModelAgent tool loops; policy routers, validation, ownership, persistence, recovery, finalization, and delivery remain deterministic Go components. One repository implementation owns SQLite transactions so direct Planner writes, Tool Ledger, Run checkpoints, terminal states, and Outbox records have explicit consistency boundaries.

**Tech Stack:** Go 1.25.8, `github.com/cloudwego/eino v0.9.6`, Eino OpenAI-compatible model adapter, SQLite via `github.com/mattn/go-sqlite3`, CozeLoop callbacks, standard `testing` package.

## Global Constraints

- Implement only under `muse/`, `cmd/muse-agent/`, and the two Muse documents; preserve every unrelated existing or untracked file.
- Do not import silicon-sage packages or connect to its database and external services.
- Planner is the only model role allowed to call `update_profile` and `update_todo`; Thinker is the only role allowed to call `search_knowledge`; both may use the read-only Eino `skill` meta-tool against the frozen Run backend.
- Profile/TODO tools write immediately after deterministic validation and do not roll back when a later Reviewer, Thinker, or Writer stage fails.
- Every Planner write uses service-side role authorization, CAS, MessageID-scoped idempotency, Tool Ledger audit, and one database transaction.
- Ordinary low-risk requests skip PlanPolicyGate and FinalReviewer; risk routes cannot fail open.
- No model role may create arbitrary actions or call `RunFinalizer`; finalization accepts only `reply`, `block`, `transfer`, and `stop_contact`.
- Persist Eino interrupt checkpoints and business-stage checkpoints separately; never claim Eino automatically checkpoints every ordinary stage.
- Do not store or expose Chain-of-Thought. Tests and root traces must use enumerations, lengths, hashes, and redacted summaries instead of full private text.
- Default tests and `-prepare-only` must not require network, a real model, or credentials.
- Add concise Chinese comments for transaction boundaries, idempotency, stage recovery, and authorization decisions; avoid comments that only restate syntax.
- Use one model retry for invalid Planner/Thinker/Writer output, one rewrite maximum, and fail closed for invalid PlanPolicyGate/FinalReviewer output.
- Run focused tests after each task and run `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/... ./cmd/muse-agent -count=1` before completion.

---

## File Structure

- Create `muse/domain/profile.go`: Profile fields, evidence, patch contract, and pure validation.
- Create `muse/domain/todo.go`: TODO state, transition contract, workflow graph, and pure transition validation.
- Create `muse/domain/run.go`: snapshots, plan/draft/bubble/verdict contracts, run stages, final outcomes, checkpoints, and Outbox records.
- Create `muse/repository/store.go`: narrow persistence interfaces and shared repository errors.
- Create `muse/repository/memory.go`: deterministic in-memory repository used by unit tests.
- Create `muse/repository/sqlite.go`: SQLite schema and transaction implementation for all durable records.
- Create `muse/tools/gateway.go`: deny-by-default role authorization and audit boundary.
- Create `muse/tools/update_profile.go`: Eino `update_profile` tool binding.
- Create `muse/tools/update_todo.go`: Eino `update_todo` tool binding.
- Create `muse/tools/knowledge.go`: read-only knowledge search tool.
- Create `muse/tools/tool_ledger.go`: stable idempotency-key helpers.
- Create `muse/skills/manifest.go`: immutable skill artifact and checksum types.
- Create `muse/skills/registry.go`: alias resolution and frozen default skill registry.
- Create `muse/policy/input_guard.go`: deterministic raw-input guard.
- Create `muse/policy/review_router.go`: deterministic Plan review routing.
- Create `muse/policy/output_router.go`: deterministic FinalReviewer routing.
- Create `muse/policy/output_validator.go`: deterministic Bubble and leakage validation.
- Create `muse/policy/plan_gate.go`: fail-closed semantic gate adapter.
- Create `muse/roles/planner.go`: Eino Planner tool loop and strict Plan parsing.
- Create `muse/roles/thinker.go`: Eino read-only tool loop and Draft parsing.
- Create `muse/roles/writer.go`: Bubble generation and strict parsing.
- Create `muse/roles/reviewer.go`: PlanPolicyGate and FinalReviewer model adapters.
- Create `muse/contracts.go`: root orchestration dependency interfaces.
- Create `muse/state.go`: legal stage transitions and immutable state updates.
- Create `muse/orchestrator.go`: deterministic pipeline and conditional review/rewrite logic.
- Create `muse/agent.go`: Eino ADK Agent/ResumableAgent wrapper.
- Create `muse/checkpoint/adk_store.go`: Eino `CheckPointStore` adapter backed by the repository.
- Create `muse/checkpoint/recovery.go`: stale-run claim and business-stage recovery helper.
- Create `muse/run/finalizer.go`: typed, atomic Run finalization.
- Create `muse/run/worker.go`: process-owned Run worker and explicit cancellation.
- Create `muse/run/service.go`: start/query/resume/preempt service API.
- Create `muse/delivery/outbox.go`: pending-message claim, stable delivery, retry, and sent marking.
- Create `cmd/muse-agent/main.go`: thin CLI and real/fake model wiring.
- Create `cmd/muse-agent/trace.go`: low-sensitive root callback payloads.
- Create colocated `*_test.go` files for every package boundary above.

---

### Task 1: Domain Contracts And Pure Validation

**Files:**
- Create: `muse/domain/profile.go`
- Create: `muse/domain/todo.go`
- Create: `muse/domain/run.go`
- Test: `muse/domain/profile_test.go`
- Test: `muse/domain/todo_test.go`
- Test: `muse/domain/run_test.go`

**Interfaces:**
- Produces `ValidateProfilePatch(current Profile, patch ProfilePatch) error`.
- Produces `NewTodoWorkflow(edges []TodoEdge) (*TodoWorkflow, error)` and `ValidateTransition(current TodoState, transition TodoTransition) error`.
- Produces shared `Snapshot`, `Plan`, `Draft`, `Bubble`, `Verdict`, `FinalOutcome`, `RunRecord`, `RunCheckpoint`, and `OutboxMessage` types.

- [ ] **Step 1: Write failing domain tests**

```go
func TestValidateProfilePatchRequiresEvidenceForFacts(t *testing.T) {
	patch := ProfilePatch{ExpectedVersion: 3, Changes: []ProfileChange{{
		Field: ProfileFieldCurrentSKU, Operation: PatchSet, Value: "premium",
		Source: EvidenceModelInference, Confidence: 0.92,
	}}}
	if err := ValidateProfilePatch(Profile{Version: 3}, patch); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("ValidateProfilePatch() error = %v, want evidence rejection", err)
	}
}

func TestTodoWorkflowRejectsCrossStageJump(t *testing.T) {
	workflow, err := NewTodoWorkflow([]TodoEdge{{From: TodoNode{Phase: "lead", Stage: "discover"}, To: TodoNode{Phase: "lead", Stage: "qualify"}}})
	if err != nil { t.Fatal(err) }
	transition := TodoTransition{ExpectedVersion: 2, From: TodoNode{Phase: "lead", Stage: "discover"}, To: TodoNode{Phase: "sale", Stage: "paid"}, Status: TodoInProgress}
	if err := workflow.ValidateTransition(TodoState{Version: 2, Node: transition.From}, transition); err == nil {
		t.Fatal("cross-stage jump should be rejected")
	}
}

func TestRunStageAllowsOnlyDeclaredNextStage(t *testing.T) {
	if !CanAdvance(StagePlanReady, StagePlanReviewSkipped) || CanAdvance(StagePlanReady, StageOutputReady) {
		t.Fatal("run stage graph does not enforce declared edges")
	}
}
```

- [ ] **Step 2: Run tests to verify the package is missing**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/domain -count=1`

Expected: FAIL because `muse/domain` and the declared contracts do not exist.

- [ ] **Step 3: Implement the exact shared contracts and pure validators**

```go
type ProfileField string
const (
	ProfileFieldNote ProfileField = "note"
	ProfileFieldProfession ProfileField = "profession"
	ProfileFieldIdentityTraits ProfileField = "identity_traits"
	ProfileFieldMainScenario ProfileField = "main_scenario"
	ProfileFieldSubScenario ProfileField = "sub_scenario"
	ProfileFieldPersonalGoal ProfileField = "personal_goal"
	ProfileFieldPainPoints ProfileField = "pain_points"
	ProfileFieldUrgencyLevel ProfileField = "urgency_level"
	ProfileFieldDeadline ProfileField = "deadline"
	ProfileFieldCurrentLevel ProfileField = "current_level"
	ProfileFieldWeakness ProfileField = "weakness"
	ProfileFieldTargetLevel ProfileField = "target_level"
	ProfileFieldExamHistory ProfileField = "exam_history"
	ProfileFieldDailyBudget ProfileField = "daily_budget"
	ProfileFieldPreferredTime ProfileField = "preferred_time"
	ProfileFieldDeviceSupport ProfileField = "device_support"
	ProfileFieldCurrentSKU ProfileField = "current_sku"
	ProfileFieldLeadSource ProfileField = "lead_source"
	ProfileFieldJoinGroup ProfileField = "join_group"
)

type Profile struct { Version int64; Fields map[ProfileField]string }
type EvidenceRef struct { ID string `json:"id"`; Quote string `json:"quote,omitempty"` }
type EvidenceSource string
const ( EvidenceExplicitUser EvidenceSource = "explicit_user"; EvidenceVerifiedContext EvidenceSource = "verified_context"; EvidenceModelInference EvidenceSource = "model_inference" )
type PatchOperation string
const ( PatchSet PatchOperation = "set"; PatchClear PatchOperation = "clear" )
type ProfileChange struct { Field ProfileField `json:"field"`; Operation PatchOperation `json:"operation"`; Value string `json:"value,omitempty"`; EvidenceRefs []EvidenceRef `json:"evidence_refs"`; Source EvidenceSource `json:"source"`; Confidence float64 `json:"confidence"` }
type ProfilePatch struct { ExpectedVersion int64 `json:"expected_version"`; Changes []ProfileChange `json:"changes"` }

type TodoNode struct { Phase string `json:"phase"`; Stage string `json:"stage"`; Task string `json:"task"`; Step string `json:"step"` }
type TodoStatus string
const ( TodoPending TodoStatus = "pending"; TodoInProgress TodoStatus = "in_progress"; TodoCompleted TodoStatus = "completed" )
type TodoState struct { Version int64 `json:"version"`; Node TodoNode `json:"node"`; Status TodoStatus `json:"status"` }
type TodoTransition struct { ExpectedVersion int64 `json:"expected_version"`; From TodoNode `json:"from"`; To TodoNode `json:"to"`; Status TodoStatus `json:"status"`; Reason string `json:"reason"`; EvidenceRefs []EvidenceRef `json:"evidence_refs"` }
type TodoEdge struct { From TodoNode; To TodoNode }

type FinalOutcomeType string
const ( OutcomeReply FinalOutcomeType = "reply"; OutcomeBlock FinalOutcomeType = "block"; OutcomeTransfer FinalOutcomeType = "transfer"; OutcomeStopContact FinalOutcomeType = "stop_contact" )
type FinalOutcome struct { Type FinalOutcomeType; ReasonCode string; Bubbles []Bubble; ExpectedOwnership string; ProfileVersion int64; TodoVersion int64 }

type RiskLevel string
const ( RiskLow RiskLevel = "low"; RiskMedium RiskLevel = "medium"; RiskHigh RiskLevel = "high" )
type ContactStatus string
const ( ContactActive ContactStatus = "active"; ContactDoNotContact ContactStatus = "do_not_contact"; ContactHumanOwned ContactStatus = "human_owned" )
type AttachmentRef struct { ID string `json:"id"`; MediaType string `json:"media_type"`; URI string `json:"uri"`; SHA256 string `json:"sha256"` }
type Snapshot struct { RunID string; SessionID string; MessageID string; OwnershipToken string; RawInput string; Attachments []AttachmentRef; Profile Profile; Todo TodoState; ContactStatus ContactStatus; Blacklisted bool; Frozen bool; ShortMemory []string; LongMemorySummary string; StartedAt time.Time; Manifest RunManifest }
type RunManifest struct { SchemaVersion int; CodeVersion string; Models map[string]string; Prompts map[string]string; Skills map[string]string; PolicyVersion string; ToolPolicyVersion string; Checksum string }
type StageBudgets struct { Overall time.Duration; Planner time.Duration; PlanReview time.Duration; Thinker time.Duration; Writer time.Duration; FinalReview time.Duration; Tool time.Duration; Finalize time.Duration }
func DefaultStageBudgets() StageBudgets { return StageBudgets{Overall: 60*time.Second, Planner: 12*time.Second, PlanReview: 8*time.Second, Thinker: 15*time.Second, Writer: 10*time.Second, FinalReview: 8*time.Second, Tool: 5*time.Second, Finalize: 3*time.Second} }
type Plan struct { Intent string `json:"intent"`; Facts []string `json:"facts"`; Guidance []string `json:"guidance"`; KnowledgeQueries []string `json:"knowledge_queries"`; ResponseGoal string `json:"response_goal"`; RiskLevel RiskLevel `json:"risk_level"`; RiskHints []string `json:"risk_hints"`; Uncertainties []string `json:"uncertainties"`; SkipReason string `json:"skip_reason"` }
type Draft struct { Facts []string `json:"facts"`; Advice []string `json:"advice"`; Content string `json:"content"`; Citations []string `json:"citations"` }
type BubbleType string
const ( BubbleText BubbleType = "text"; BubbleCard BubbleType = "card" )
type Bubble struct { Type BubbleType `json:"type"`; Content string `json:"content"` }
type ArtifactDraft struct { Type string `json:"type"`; Title string `json:"title"`; Content string `json:"content"` }
type WriterOutput struct { Bubbles []Bubble `json:"bubbles"`; Artifact *ArtifactDraft `json:"artifact,omitempty"` }
type VerdictDecision string
const ( VerdictApprove VerdictDecision = "approve"; VerdictRewrite VerdictDecision = "rewrite"; VerdictBlock VerdictDecision = "block"; VerdictTransfer VerdictDecision = "transfer" )
type Verdict struct { Decision VerdictDecision `json:"decision"`; ReasonCode string `json:"reason_code"`; RewriteGuidance string `json:"rewrite_guidance"` }

type GuardDecisionType string
const ( GuardAllow GuardDecisionType = "allow"; GuardSafeReply GuardDecisionType = "safe_reply"; GuardTransfer GuardDecisionType = "transfer"; GuardBlock GuardDecisionType = "block"; GuardStopContact GuardDecisionType = "stop_contact" )
type GuardDecision struct { Type GuardDecisionType; ReasonCode string; SafeReply []Bubble }
type ReviewRouteDecision struct { NeedReview bool; ReasonCode string; Risk RiskLevel }
type ReviewRouteInput struct { RawInput string; Plan Plan; ToolAudits []ToolAudit }
type OutputRouteInput struct { Plan Plan; Output WriterOutput; ValidationRisk bool; PlanWasReviewed bool }

type RunStage string
const (
	StageCreated RunStage = "created"; StageSnapshotReady RunStage = "snapshot_ready"; StageInputAllowed RunStage = "input_allowed"
	StagePlannerRunning RunStage = "planner_running"; StagePlannerWritesCommitted RunStage = "planner_tool_writes_committed"; StagePlanReady RunStage = "plan_ready"
	StagePlanApproved RunStage = "plan_approved"; StagePlanReviewSkipped RunStage = "plan_review_skipped"; StageDraftReady RunStage = "draft_ready"
	StageOutputReady RunStage = "output_ready"; StageFinalApproved RunStage = "final_approved"; StageFinalReviewSkipped RunStage = "final_review_skipped"
	StageFinalizing RunStage = "finalizing"; StageCompleted RunStage = "completed"; StageBlocked RunStage = "blocked"; StageTransferred RunStage = "transferred"
	StageStopped RunStage = "stopped"; StagePreempted RunStage = "preempted"; StageFailed RunStage = "failed"
)
type RunStatus string
const ( RunRunning RunStatus = "running"; RunCompleted RunStatus = "completed"; RunBlocked RunStatus = "blocked"; RunTransferred RunStatus = "transferred"; RunStopped RunStatus = "stopped"; RunPreempted RunStatus = "preempted"; RunFailed RunStatus = "failed" )
type RunRecord struct { ID string; SessionID string; MessageID string; OwnershipToken string; Stage RunStage; Status RunStatus; Manifest RunManifest; HeartbeatAt time.Time; LeaseOwner string; LeaseUntil time.Time }
type RunCheckpoint struct { SchemaVersion int; RunID string; Stage RunStage; InitialSnapshot Snapshot; EffectiveSnapshot Snapshot; ToolResultRefs []string; ToolAudits []ToolAudit; Plan Plan; PlanVerdict *Verdict; Draft Draft; Output WriterOutput; FinalVerdict *Verdict; Outcome *FinalOutcome; RewriteCount int; Manifest RunManifest; HeartbeatAt time.Time }
type RunRequest struct { RunID string; SessionID string; MessageID string; Input string; Attachments []AttachmentRef }
type ExecutionResult struct { RunID string; Stage RunStage; Status RunStatus; Output WriterOutput; Outcome FinalOutcome; Interrupt *ResumeState }
type ResumeState struct { RunID string; Outcome FinalOutcome }
type FinalizeRequest struct { RunID string; SessionID string; OwnershipToken string; Outcome FinalOutcome }

type ToolAudit struct { RunID string; MessageID string; Role string; Tool string; Allowed bool; ResultCode string; Duration time.Duration }
type ProfileWriteResult struct { ProfileVersion int64 `json:"profile_version"`; Replayed bool `json:"replayed"` }
type TodoWriteResult struct { TodoVersion int64 `json:"todo_version"`; Replayed bool `json:"replayed"`; RuleTaskMessageID string `json:"rule_task_message_id,omitempty"` }
type OutboxStatus string
const ( OutboxPending OutboxStatus = "pending"; OutboxSending OutboxStatus = "sending"; OutboxSent OutboxStatus = "sent"; OutboxFailed OutboxStatus = "failed" )
type OutboxPayload struct { Type string `json:"type"`; Bubbles []Bubble `json:"bubbles,omitempty"`; RunID string `json:"run_id"`; ReasonCode string `json:"reason_code,omitempty"` }
type OutboxMessage struct { MessageID string; RunID string; Type string; Payload OutboxPayload; Status OutboxStatus; Attempts int; NextAttemptAt time.Time; LeaseUntil time.Time; LastError string }

type PlannerInput struct { Snapshot Snapshot; SkillNames []string }
type ThinkerInput struct { Snapshot Snapshot; Plan Plan; RewriteGuidance string }
type WriterInput struct { Snapshot Snapshot; Plan Plan; Draft Draft; RewriteGuidance string }
type PlanReviewInput struct { RawInput string; Snapshot Snapshot; Plan Plan; ToolAudits []ToolAudit }
type FinalReviewInput struct { RawInput string; Snapshot Snapshot; Plan Plan; Output WriterOutput }
```

Validation must reject unknown fields, duplicate fields, implicit empty-string clears, confidence outside `[0,1]`, missing evidence for factual fields, version mismatch, unknown TODO nodes, undeclared edges, invalid statuses, and retained old step values when moving tasks.

- [ ] **Step 4: Run and pass domain tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/domain -count=1`

Expected: PASS with tests for all accepted and rejected field/transition/stage cases.

- [ ] **Step 5: Commit the domain slice**

```bash
git add muse/domain
git commit -m "feat: add muse domain contracts"
```

---

### Task 2: Unified Memory And SQLite Repository

**Files:**
- Create: `muse/repository/store.go`
- Create: `muse/repository/memory.go`
- Create: `muse/repository/sqlite.go`
- Test: `muse/repository/memory_test.go`
- Test: `muse/repository/sqlite_test.go`

**Interfaces:**
- Consumes domain contracts from Task 1.
- Produces narrow interfaces `SnapshotReader`, `PlannerWriter`, `RunRepository`, `FinalizationRepository`, `OutboxRepository`, and `CheckpointBlobRepository`.
- Produces `NewMemoryStore(now func() time.Time) *MemoryStore` and `OpenSQLite(ctx context.Context, path string, now func() time.Time) (*SQLiteStore, error)`.

- [ ] **Step 1: Write failing repository conformance and transaction tests**

```go
func TestSQLitePlannerWriteAndLedgerAreAtomicAndIdempotent(t *testing.T) {
	store := openSQLiteTestStore(t)
	seedSession(t, store, "session-1")
	scope := PlannerWriteScope{RunID: "run-1", SessionID: "session-1", MessageID: "message-1", Role: "planner", ToolCallID: "call-1"}
	patch := domain.ProfilePatch{ExpectedVersion: 1, Changes: []domain.ProfileChange{{Field: domain.ProfileFieldProfession, Operation: domain.PatchSet, Value: "教师", Source: domain.EvidenceExplicitUser, EvidenceRefs: []domain.EvidenceRef{{ID: "input:1"}}, Confidence: 1}}}
	first, err := store.ApplyProfilePatch(context.Background(), scope, patch)
	if err != nil { t.Fatal(err) }
	second, err := store.ApplyProfilePatch(context.Background(), scope, patch)
	if err != nil { t.Fatal(err) }
	if first.ProfileVersion != second.ProfileVersion || second.Replayed != true {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestSQLitePersistsRunCheckpointAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "muse.sqlite")
	store, err := OpenSQLite(context.Background(), path, time.Now)
	if err != nil { t.Fatal(err) }
	checkpoint := domain.RunCheckpoint{RunID: "run-1", Stage: domain.StagePlanReady, SchemaVersion: 1}
	if err := store.SaveRunCheckpoint(context.Background(), checkpoint); err != nil { t.Fatal(err) }
	if err := store.Close(); err != nil { t.Fatal(err) }
	reopened, err := OpenSQLite(context.Background(), path, time.Now)
	if err != nil { t.Fatal(err) }
	got, err := reopened.LoadRunCheckpoint(context.Background(), "run-1")
	if err != nil || got.Stage != domain.StagePlanReady { t.Fatalf("got=%+v err=%v", got, err) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/repository -count=1`

Expected: FAIL because repository interfaces and implementations are absent.

- [ ] **Step 3: Implement narrow interfaces and both stores**

```go
type SnapshotReader interface {
	LoadSnapshot(context.Context, string) (domain.Snapshot, error)
}
type PlannerWriter interface {
	ApplyProfilePatch(context.Context, PlannerWriteScope, domain.ProfilePatch) (domain.ProfileWriteResult, error)
	ApplyTodoTransition(context.Context, PlannerWriteScope, domain.TodoTransition) (domain.TodoWriteResult, error)
}
type RunRepository interface {
	CreateRun(context.Context, domain.RunRecord) error
	SaveRunCheckpoint(context.Context, domain.RunCheckpoint) error
	LoadRunCheckpoint(context.Context, string) (domain.RunCheckpoint, error)
	ClaimRecoverable(context.Context, time.Time, time.Duration, int) ([]domain.RunRecord, error)
	PreemptSession(context.Context, string, string) error
}
type FinalizationRepository interface {
	FinalizeRun(context.Context, domain.FinalizeRequest) (domain.RunRecord, error)
}
type OutboxRepository interface {
	ClaimPendingOutbox(context.Context, int, time.Time) ([]domain.OutboxMessage, error)
	MarkOutboxSent(context.Context, string, time.Time) error
	MarkOutboxFailed(context.Context, string, string, time.Time) error
}
type CheckpointBlobRepository interface {
	GetCheckpointBlob(context.Context, string) ([]byte, bool, error)
	SetCheckpointBlob(context.Context, string, []byte) error
	DeleteCheckpointBlob(context.Context, string) error
}
type Store interface { SnapshotReader; PlannerWriter; RunRepository; FinalizationRepository; OutboxRepository; CheckpointBlobRepository }
```

SQLite must enable `PRAGMA foreign_keys=ON`, use WAL for file databases, and create explicit tables for sessions, profiles, todos, todo_logs, tool_ledger, runs, run_checkpoints, run_transitions, outbox, and adk_checkpoints. `ApplyProfilePatch`, `ApplyTodoTransition`, and `FinalizeRun` each use one `sql.Tx`; unique constraints enforce profile key `message_id + field`, TODO key `message_id`, finalization key `run_id`, and Outbox `message_id`. A successful TODO transition inserts its TodoLog, Tool Ledger row, and deterministic `todo_rule_task` Outbox row in that same transaction instead of starting a detached goroutine.

- [ ] **Step 4: Run repository tests including Memory/SQLite behavior parity**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/repository -count=1`

Expected: PASS; both implementations satisfy the same CAS, replay, conflict, terminal-state, and Outbox tests.

- [ ] **Step 5: Commit the persistence slice**

```bash
git add muse/repository
git commit -m "feat: add muse durable repository"
```

---

### Task 3: Planner Write Tools, Tool Ledger, And Role Authorization

**Files:**
- Create: `muse/tools/gateway.go`
- Create: `muse/tools/tool_ledger.go`
- Create: `muse/tools/update_profile.go`
- Create: `muse/tools/update_todo.go`
- Create: `muse/tools/knowledge.go`
- Test: `muse/tools/gateway_test.go`
- Test: `muse/tools/update_profile_test.go`
- Test: `muse/tools/update_todo_test.go`
- Test: `muse/tools/tool_schema_test.go`

**Interfaces:**
- Consumes `repository.PlannerWriter`, domain validators, and `domain.TodoWorkflow`.
- Produces `NewGateway(GatewayConfig) (*Gateway, error)`.
- Produces `BuildPlannerTools(scope ExecutionScope, gateway *Gateway) ([]tool.BaseTool, error)` and `BuildThinkerTools(search KnowledgeSearcher) ([]tool.BaseTool, error)`.

- [ ] **Step 1: Write failing authorization, schema, and replay tests**

```go
func TestGatewayRejectsUpdateProfileOutsidePlanner(t *testing.T) {
	gateway := newTestGateway(t)
	_, err := gateway.UpdateProfile(context.Background(), ExecutionScope{Role: RoleThinker, RunID: "run-1", SessionID: "session-1", MessageID: "message-1"}, validProfilePatch())
	if err == nil || !errors.Is(err, ErrToolDenied) { t.Fatalf("error=%v", err) }
}

func TestUpdateTodoReturnsLedgerResultOnPlannerRetry(t *testing.T) {
	gateway := newTestGateway(t)
	scope := ExecutionScope{Role: RolePlanner, RunID: "run-1", SessionID: "session-1", MessageID: "message-1", ToolCallID: "call-1"}
	first, err := gateway.UpdateTodo(context.Background(), scope, validTodoTransition())
	if err != nil { t.Fatal(err) }
	second, err := gateway.UpdateTodo(context.Background(), scope, validTodoTransition())
	if err != nil || !second.Replayed || first.TodoVersion != second.TodoVersion { t.Fatalf("first=%+v second=%+v err=%v", first, second, err) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/tools -count=1`

Expected: FAIL because the gateway and Eino tools do not exist.

- [ ] **Step 3: Implement deny-by-default execution and Eino bindings**

```go
type Role string
const ( RolePlanner Role = "planner"; RoleThinker Role = "thinker"; RoleWriter Role = "writer"; RoleReviewer Role = "reviewer" )
type ExecutionScope struct { Role Role; RunID string; SessionID string; MessageID string; ToolCallID string }
type GatewayConfig struct { Writer repository.PlannerWriter; Workflow *domain.TodoWorkflow; Audit func(domain.ToolAudit) }

func BuildPlannerTools(scope ExecutionScope, gateway *Gateway) ([]tool.BaseTool, error) {
	profileTool, err := toolutils.InferTool[domain.ProfilePatch, *domain.ProfileWriteResult]("update_profile", "按字段证据更新客户画像；执行端会做权限、CAS 和幂等校验。", func(ctx context.Context, patch domain.ProfilePatch) (*domain.ProfileWriteResult, error) {
		result, err := gateway.UpdateProfile(ctx, scope, patch)
		return &result, err
	})
	if err != nil { return nil, err }
	todoTool, err := toolutils.InferTool[domain.TodoTransition, *domain.TodoWriteResult]("update_todo", "按冻结 SOP 的合法边更新销售进度；不能覆盖或跳阶段。", func(ctx context.Context, transition domain.TodoTransition) (*domain.TodoWriteResult, error) {
		result, err := gateway.UpdateTodo(ctx, scope, transition)
		return &result, err
	})
	if err != nil { return nil, err }
	return []tool.BaseTool{profileTool, todoTool}, nil
}
```

The gateway must authorize before validation, validate before persistence, emit a redacted audit after execution, and never retry a write internally. Tool schemas must mark required versions, evidence, `from`, and `to` fields.

- [ ] **Step 4: Run tool tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/tools -count=1`

Expected: PASS, including direct tool invocation, schema assertions, denied roles, conflicting replay, and exact replay.

- [ ] **Step 5: Commit the tool slice**

```bash
git add muse/tools
git commit -m "feat: add guarded muse planner tools"
```

---

### Task 4: Versioned Skills And Deterministic Policy Components

**Files:**
- Create: `muse/skills/manifest.go`
- Create: `muse/skills/registry.go`
- Create: `muse/policy/input_guard.go`
- Create: `muse/policy/review_router.go`
- Create: `muse/policy/output_router.go`
- Create: `muse/policy/output_validator.go`
- Create: `muse/policy/plan_gate.go`
- Test: `muse/skills/registry_test.go`
- Test: `muse/policy/input_guard_test.go`
- Test: `muse/policy/review_router_test.go`
- Test: `muse/policy/output_validator_test.go`

**Interfaces:**
- Produces `Registry.Resolve(alias string, role string) (Artifact, error)`, `Registry.Freeze(aliases []string, role string) (*FrozenBackend, []Artifact, error)`, and frozen checksums for `RunManifest`.
- Produces `FrozenBackend`, which implements Eino ADK Skill Middleware `skill.Backend` and exposes only the artifacts frozen for this Run.
- Produces `RawInputGuard.Evaluate(domain.Snapshot, string) domain.GuardDecision`.
- Produces `ReviewRouter.Route(domain.ReviewRouteInput) domain.ReviewRouteDecision` and `OutputRouter.Route(domain.OutputRouteInput) domain.ReviewRouteDecision`.
- Produces `OutputValidator.Validate(domain.WriterOutput) error`.

- [ ] **Step 1: Write failing routing and registry tests**

```go
func TestReviewRouterSkipsOrdinaryInformationRequest(t *testing.T) {
	router := NewReviewRouter()
	decision := router.Route(domain.ReviewRouteInput{RawInput: "课程几点开始？", Plan: domain.Plan{Intent: "course_info", RiskLevel: domain.RiskLow}})
	if decision.NeedReview { t.Fatalf("decision=%+v", decision) }
}

func TestReviewRouterRequiresReviewForSoftRefusalAndContinuedMarketing(t *testing.T) {
	router := NewReviewRouter()
	decision := router.Route(domain.ReviewRouteInput{RawInput: "我暂时不考虑了", Plan: domain.Plan{Intent: "continue_marketing", RiskLevel: domain.RiskMedium}})
	if !decision.NeedReview || decision.ReasonCode == "" { t.Fatalf("decision=%+v", decision) }
}

func TestRegistryFreezesAliasToImmutableChecksum(t *testing.T) {
	registry := DefaultRegistry()
	first, err := registry.Resolve("planner/default", "planner")
	if err != nil { t.Fatal(err) }
	second, err := registry.Resolve("planner/default", "planner")
	if err != nil || first.Version != second.Version || first.Checksum != second.Checksum { t.Fatalf("first=%+v second=%+v err=%v", first, second, err) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/skills ./muse/policy -count=1`

Expected: FAIL because skill and policy packages do not exist.

- [ ] **Step 3: Implement immutable artifacts and deterministic routing tables**

```go
type Artifact struct { Name string; Version string; Role string; Description string; Instruction string; RequestedTools []string; Checksum string }
type Registry struct { aliases map[string]string; artifacts map[string]Artifact }
type FrozenBackend struct { artifacts map[string]Artifact }

func (b *FrozenBackend) List(context.Context) ([]adkskill.FrontMatter, error) {
	items := make([]adkskill.FrontMatter, 0, len(b.artifacts))
	for _, artifact := range b.artifacts {
		items = append(items, adkskill.FrontMatter{Name: artifact.Name, Description: artifact.Description})
	}
	slices.SortFunc(items, func(a, c adkskill.FrontMatter) int { return strings.Compare(a.Name, c.Name) })
	return items, nil
}
func (b *FrozenBackend) Get(_ context.Context, name string) (adkskill.Skill, error) {
	artifact, ok := b.artifacts[name]
	if !ok { return adkskill.Skill{}, fmt.Errorf("skill %q is not frozen for this run", name) }
	return adkskill.Skill{FrontMatter: adkskill.FrontMatter{Name: artifact.Name, Description: artifact.Description}, Content: artifact.Instruction}, nil
}

type RawInputGuard struct{}
func (RawInputGuard) Evaluate(snapshot domain.Snapshot, input string) domain.GuardDecision {
	normalized := strings.ToLower(strings.TrimSpace(input))
	switch {
	case snapshot.Blacklisted || snapshot.Frozen:
		return domain.GuardDecision{Type: domain.GuardBlock, ReasonCode: "session_blocked"}
	case snapshot.ContactStatus == domain.ContactHumanOwned:
		return domain.GuardDecision{Type: domain.GuardTransfer, ReasonCode: "human_already_owns"}
	case snapshot.ContactStatus == domain.ContactDoNotContact:
		return domain.GuardDecision{Type: domain.GuardBlock, ReasonCode: "contact_stopped"}
	case strings.Contains(normalized, "不要再联系") || strings.Contains(normalized, "别再给我发"):
		return domain.GuardDecision{Type: domain.GuardStopContact, ReasonCode: "explicit_stop"}
	case strings.Contains(normalized, "转人工") || strings.Contains(normalized, "找客服"):
		return domain.GuardDecision{Type: domain.GuardTransfer, ReasonCode: "explicit_handoff"}
	case normalized == "":
		return domain.GuardDecision{Type: domain.GuardBlock, ReasonCode: "empty_input"}
	case len([]rune(normalized)) > 8000:
		return domain.GuardDecision{Type: domain.GuardBlock, ReasonCode: "input_too_large"}
	case strings.Contains(normalized, "ignore previous instructions") || strings.Contains(normalized, "忽略之前的指令"):
		return domain.GuardDecision{Type: domain.GuardBlock, ReasonCode: "prompt_injection"}
	default:
		return domain.GuardDecision{Type: domain.GuardAllow}
	}
}
```

`Artifact` also contains `Description`; checksum is SHA-256 over canonical name/version/role/instruction/requested-tools JSON. `Freeze` resolves aliases once, rejects requested tools outside the role allowlist, and returns a backend containing no other versions. Review reasons must be stable enums: rejected tool attempt, pricing/availability/identity promise, ambiguous stop/handoff signal, refusal plus continued marketing, non-low risk, uncertainty, and contract conflict. Output validation must reject empty Bubble lists, unknown types, over-limit content, tool syntax, internal prompt markers, and sensitive-key patterns.

- [ ] **Step 4: Run skill and policy tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/skills ./muse/policy -count=1`

Expected: PASS with low-risk skip, risk trigger, explicit stop, explicit handoff, leakage rejection, and checksum stability cases.

- [ ] **Step 5: Commit the policy slice**

```bash
git add muse/skills muse/policy
git commit -m "feat: add muse skills and risk routing"
```

---

### Task 5: Controlled Planner, Thinker, Writer, And Reviewer Roles

**Files:**
- Create: `muse/roles/planner.go`
- Create: `muse/roles/thinker.go`
- Create: `muse/roles/writer.go`
- Create: `muse/roles/reviewer.go`
- Test: `muse/roles/planner_test.go`
- Test: `muse/roles/thinker_test.go`
- Test: `muse/roles/writer_test.go`
- Test: `muse/roles/reviewer_test.go`

**Interfaces:**
- Consumes Eino `model.BaseChatModel`, skill artifacts, and Task 3 tool builders.
- Produces `Planner.Plan`, `Thinker.Think`, `Writer.Write`, `Reviewer.ReviewPlan`, and `Reviewer.ReviewFinal`.
- Produces a `RoleSet` consumed by the root Orchestrator.

- [ ] **Step 1: Write failing Eino role tests with scripted models**

```go
func TestPlannerUsesEinoToolLoopAndRetriesOnlyFinalJSON(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "profile-1", Type: "function", Function: schema.FunctionCall{Name: "update_profile", Arguments: validProfilePatchJSON()}}}),
		schema.AssistantMessage("not-json", nil),
		schema.AssistantMessage(`{"intent":"course_recommendation","facts":["用户是教师"],"guidance":["先确认目标"],"knowledge_queries":[],"response_goal":"回答课程匹配","risk_level":"low","risk_hints":[],"uncertainties":[],"skip_reason":""}`, nil),
	}}
	planner := newPlannerForTest(t, model)
	plan, err := planner.Plan(context.Background(), validPlannerInput())
	if err != nil { t.Fatal(err) }
	if plan.Intent != "course_recommendation" || model.ToolCallCount("update_profile") != 1 { t.Fatalf("plan=%+v calls=%+v", plan, model.calls) }
}

func TestFinalReviewerCannotReceiveTools(t *testing.T) {
	model := &capturingModel{reply: `{"decision":"approve","reason_code":"safe","rewrite_guidance":""}`}
	reviewer := NewReviewer(model)
	_, err := reviewer.ReviewFinal(context.Background(), validFinalReviewInput())
	if err != nil { t.Fatal(err) }
	if len(model.boundTools) != 0 { t.Fatalf("reviewer tools=%+v", model.boundTools) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/roles -count=1`

Expected: FAIL because role adapters do not exist.

- [ ] **Step 3: Implement role interfaces and strict JSON execution**

```go
type Planner interface { Plan(context.Context, domain.PlannerInput) (domain.Plan, error) }
type Thinker interface { Think(context.Context, domain.ThinkerInput) (domain.Draft, error) }
type Writer interface { Write(context.Context, domain.WriterInput) (domain.WriterOutput, error) }
type Reviewer interface {
	ReviewPlan(context.Context, domain.PlanReviewInput) (domain.Verdict, error)
	ReviewFinal(context.Context, domain.FinalReviewInput) (domain.Verdict, error)
}
type RoleSet struct { Planner Planner; Thinker Thinker; Writer Writer; Reviewer Reviewer }

func newFrozenSkillMiddleware(ctx context.Context, backend *skills.FrozenBackend) (adk.ChatModelAgentMiddleware, error) {
	name := "skill"
	return adkskill.NewMiddleware(ctx, &adkskill.Config{
		Backend: backend,
		SkillToolName: &name,
		CustomSystemPrompt: func(context.Context, string) string { return "需要业务 SOP 时调用 skill；只能选择本 Run 已冻结的技能。" },
	})
}
```

Planner must create an Eino `adk.ChatModelAgent` per Run with the frozen Eino Skill Middleware plus the two bound Planner tools and `MaxIterations: 6`; Thinker creates one with the frozen Skill Middleware plus `search_knowledge`. Writer and Reviewer call their model once without tools. Parse fenced or plain JSON, reject unknown enums and empty required fields, and retry only the role's final invalid model output once. Do not rerun a completed Planner tool effect; the repository ledger returns its prior result.

- [ ] **Step 4: Run role tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/roles -count=1`

Expected: PASS; tests prove role-specific tools, strict parsing, one retry, no Reviewer tools, and Thinker read-only behavior.

- [ ] **Step 5: Commit the role slice**

```bash
git add muse/roles
git commit -m "feat: add controlled muse role agents"
```

---

### Task 6: Typed RunFinalizer And Transactional Outbox Delivery

**Files:**
- Create: `muse/run/finalizer.go`
- Create: `muse/delivery/outbox.go`
- Test: `muse/run/finalizer_test.go`
- Test: `muse/delivery/outbox_test.go`

**Interfaces:**
- Consumes `repository.FinalizationRepository` and `repository.OutboxRepository`.
- Produces `Finalizer.Finalize(ctx context.Context, req domain.FinalizeRequest) (domain.RunRecord, error)`.
- Produces `DeliveryWorker.DrainOnce(ctx context.Context, limit int) (DeliverySummary, error)`.

- [ ] **Step 1: Write failing atomic finalization and retry tests**

```go
func TestFinalizerCompletesReplyOnceAndCreatesOneOutbox(t *testing.T) {
	store := seededFinalizerStore(t)
	finalizer := NewFinalizer(store)
	req := domain.FinalizeRequest{RunID: "run-1", SessionID: "session-1", OwnershipToken: "owner-1", Outcome: domain.FinalOutcome{Type: domain.OutcomeReply, Bubbles: []domain.Bubble{{Type: domain.BubbleText, Content: "你好"}}, ProfileVersion: 2, TodoVersion: 3}}
	first, err := finalizer.Finalize(context.Background(), req)
	if err != nil { t.Fatal(err) }
	second, err := finalizer.Finalize(context.Background(), req)
	if err != nil { t.Fatal(err) }
	if first.Status != domain.RunCompleted || second.Status != domain.RunCompleted || store.OutboxCount() != 1 { t.Fatalf("first=%+v second=%+v outbox=%d", first, second, store.OutboxCount()) }
}

func TestDeliveryRetryDoesNotRerunAgent(t *testing.T) {
	store := seededPendingOutboxStore(t)
	channel := &fakeChannel{failuresBeforeSuccess: 1}
	worker := NewDeliveryWorker(store, channel, time.Now)
	if _, err := worker.DrainOnce(context.Background(), 10); err != nil { t.Fatal(err) }
	if _, err := worker.DrainOnce(context.Background(), 10); err != nil { t.Fatal(err) }
	if channel.calls != 2 || channel.messageIDs[0] != channel.messageIDs[1] { t.Fatalf("channel=%+v", channel) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/run ./muse/delivery -count=1`

Expected: FAIL because finalization and delivery components are absent.

- [ ] **Step 3: Implement fixed final outcomes and reliable delivery**

```go
type Finalizer struct { store repository.FinalizationRepository }
func (f *Finalizer) Finalize(ctx context.Context, req domain.FinalizeRequest) (domain.RunRecord, error) {
	switch req.Outcome.Type {
	case domain.OutcomeReply, domain.OutcomeBlock, domain.OutcomeTransfer, domain.OutcomeStopContact:
		return f.store.FinalizeRun(ctx, req)
	default:
		return domain.RunRecord{}, fmt.Errorf("unsupported final outcome %q", req.Outcome.Type)
	}
}

type Channel interface { Send(context.Context, string, domain.OutboxPayload) error }
type DeliveryWorker struct { store repository.OutboxRepository; channel Channel; now func() time.Time }
```

`FinalizeRun` must verify ownership and expected Profile/TODO versions, update the terminal Run status, append a transition record, update only the fixed session/contact control fields required by transfer/stop, and insert the stable Outbox row in one transaction. Delivery claims rows with a lease, sends with stable MessageID, marks sent on success, and schedules bounded exponential retry on failure.

- [ ] **Step 4: Run finalizer and delivery tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/run ./muse/delivery -count=1`

Expected: PASS for reply, block, transfer, stop-contact, ownership conflict, version conflict, duplicate finalize, send retry, and stable MessageID.

- [ ] **Step 5: Commit the terminal-state slice**

```bash
git add muse/run/finalizer.go muse/run/finalizer_test.go muse/delivery
git commit -m "feat: add muse finalization and outbox"
```

---

### Task 7: Deterministic Orchestrator And Conditional Review Loop

**Files:**
- Create: `muse/contracts.go`
- Create: `muse/state.go`
- Create: `muse/orchestrator.go`
- Test: `muse/state_test.go`
- Test: `muse/orchestrator_test.go`

**Interfaces:**
- Consumes `repository.SnapshotReader`, `repository.RunRepository`, `roles.RoleSet`, policy components, and a narrow `Finalizer` interface.
- Produces `NewOrchestrator(OrchestratorConfig) (*Orchestrator, error)` and `Execute(ctx context.Context, req domain.RunRequest) (domain.ExecutionResult, error)`.
- Produces `Resume(ctx context.Context, state domain.ResumeState) (domain.ExecutionResult, error)`.

- [ ] **Step 1: Write failing pipeline-routing tests**

```go
func TestOrchestratorLowRiskSkipsBothReviewers(t *testing.T) {
	fixture := newOrchestratorFixture(t)
	result, err := fixture.orchestrator.Execute(context.Background(), domain.RunRequest{RunID: "run-1", SessionID: "session-1", MessageID: "message-1", Input: "课程几点开始？"})
	if err != nil { t.Fatal(err) }
	if fixture.reviewer.planCalls != 0 || fixture.reviewer.finalCalls != 0 { t.Fatalf("review calls=%+v", fixture.reviewer) }
	if result.Outcome.Type != domain.OutcomeReply || result.Stage != domain.StageCompleted { t.Fatalf("result=%+v", result) }
}

func TestOrchestratorReloadsProfileAndTodoAfterPlannerTools(t *testing.T) {
	fixture := newOrchestratorFixture(t)
	fixture.planner.writeProfileAndTodo = true
	_, err := fixture.orchestrator.Execute(context.Background(), domain.RunRequest{RunID: "run-2", SessionID: "session-1", MessageID: "message-2", Input: "我是教师，想开始体验课"})
	if err != nil { t.Fatal(err) }
	if fixture.thinker.input.Snapshot.Profile.Version != 2 || fixture.thinker.input.Snapshot.Todo.Version != 2 { t.Fatalf("snapshot=%+v", fixture.thinker.input.Snapshot) }
}

func TestFinalRewriteDoesNotRerunPlanner(t *testing.T) {
	fixture := newOrchestratorFixture(t)
	fixture.reviewer.finalVerdicts = []domain.Verdict{{Decision: domain.VerdictRewrite}, {Decision: domain.VerdictApprove}}
	_, err := fixture.orchestrator.Execute(context.Background(), validHighRiskRunRequest())
	if err != nil { t.Fatal(err) }
	if fixture.planner.calls != 1 || fixture.writer.calls != 2 || fixture.reviewer.finalCalls != 2 { t.Fatalf("planner=%d writer=%d final=%d", fixture.planner.calls, fixture.writer.calls, fixture.reviewer.finalCalls) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse -run 'TestOrchestrator|TestRunState' -count=1`

Expected: FAIL because the root state machine does not exist.

- [ ] **Step 3: Implement the stage machine as a readable named sequence**

```go
type Finalizer interface { Finalize(context.Context, domain.FinalizeRequest) (domain.RunRecord, error) }
type OrchestratorConfig struct {
	Snapshots repository.SnapshotReader
	Runs repository.RunRepository
	Roles roles.RoleSet
	InputGuard policy.RawInputGuard
	ReviewRouter policy.ReviewRouter
	OutputRouter policy.OutputRouter
	OutputValidator policy.OutputValidator
	Finalizer Finalizer
	Manifest domain.RunManifest
	Budgets domain.StageBudgets
}

func (o *Orchestrator) Execute(ctx context.Context, req domain.RunRequest) (domain.ExecutionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, o.budgets.Overall)
	defer cancel()
	state, err := o.prepareSnapshot(ctx, req)
	if err != nil { return domain.ExecutionResult{}, err }
	if result, done, err := o.applyInputGuard(ctx, state); done || err != nil { return result, err }
	if err := o.runPlannerAndReload(ctx, state); err != nil { return domain.ExecutionResult{}, err }
	if err := o.reviewPlanWhenRequired(ctx, state); err != nil { return domain.ExecutionResult{}, err }
	if err := o.runThinkerWriterAndValidation(ctx, state); err != nil { return domain.ExecutionResult{}, err }
	if err := o.reviewFinalWhenRequired(ctx, state); err != nil { return domain.ExecutionResult{}, err }
	return o.finalize(ctx, state)
}
```

Each model/tool/finalize call derives a child context from its exact `StageBudgets` field. Every named step saves a `RunCheckpoint` only after its stage succeeds. A Planner JSON retry may rerun Planner but Tool Ledger prevents duplicate writes. A FinalReviewer rewrite reruns only Thinker, Writer, validation, and FinalReviewer once. Block/transfer/stop map to one typed FinalOutcome; no role may bypass Ownership/Finalizer.

- [ ] **Step 4: Run orchestrator tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse -run 'TestOrchestrator|TestRunState' -count=1`

Expected: PASS for low risk, PlanPolicyGate risk, final review, rewrite cap, block, transfer, stop, snapshot reload, stage failure, and no rollback of committed Planner writes.

- [ ] **Step 5: Commit the orchestration slice**

```bash
git add muse/contracts.go muse/state.go muse/orchestrator.go muse/*_test.go
git commit -m "feat: add muse deterministic orchestrator"
```

---

### Task 8: Eino ADK Resumable Agent And Dual Checkpoints

**Files:**
- Create: `muse/agent.go`
- Create: `muse/checkpoint/adk_store.go`
- Create: `muse/checkpoint/recovery.go`
- Test: `muse/agent_test.go`
- Test: `muse/checkpoint/adk_store_test.go`
- Test: `muse/checkpoint/recovery_test.go`

**Interfaces:**
- Consumes root `Orchestrator` and repository checkpoint/run interfaces.
- Produces an Eino `adk.ResumableAgent` and `adk.CheckPointStore`.
- Produces `NewRunner(ctx context.Context, orchestrator *Orchestrator, store adk.CheckPointStore) (*adk.Runner, error)`.

- [ ] **Step 1: Write failing interrupt/resume and durable-checkpoint tests**

```go
func TestRunnerPersistsTransferInterruptAndResumesWithoutRerunningPlanner(t *testing.T) {
	fixture := newADKFixture(t)
	runner, err := NewRunner(context.Background(), fixture.orchestrator, fixture.adkStore)
	if err != nil { t.Fatal(err) }
	events := drainEvents(t, runner.Query(context.Background(), fixture.transferInput, adk.WithCheckPointID("cp-1")))
	if !hasInterrupt(events) { t.Fatalf("events=%+v", events) }
	rebuilt, err := NewRunner(context.Background(), fixture.rebuiltOrchestrator(), fixture.adkStore)
	if err != nil { t.Fatal(err) }
	resumed, err := rebuilt.Resume(context.Background(), "cp-1")
	if err != nil { t.Fatal(err) }
	result := customizedResult(t, drainEvents(t, resumed))
	if result.Outcome.Type != domain.OutcomeTransfer || fixture.planner.calls != 1 { t.Fatalf("result=%+v planner=%d", result, fixture.planner.calls) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse ./muse/checkpoint -run 'TestRunner|TestADKStore|TestRecovery' -count=1`

Expected: FAIL because the ADK wrapper and adapters do not exist.

- [ ] **Step 3: Implement `adk.Agent`, `adk.ResumableAgent`, and repository adapter**

```go
type Agent struct { orchestrator *Orchestrator }
func (a *Agent) Name(context.Context) string { return "muse_sales_agent" }
func (a *Agent) Description(context.Context) string { return "Deterministic sales conversation orchestrator with guarded role agents." }
func (a *Agent) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.run(ctx, requestFromMessages(input.Messages), nil)
}
func (a *Agent) Resume(ctx context.Context, info *adk.ResumeInfo, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	state, ok := info.InterruptState.(domain.ResumeState)
	if !ok { return adkErrorIterator(fmt.Errorf("invalid muse resume state %T", info.InterruptState)) }
	return a.run(ctx, domain.RunRequest{}, &state)
}

type ADKStore struct { repository repository.CheckpointBlobRepository }
func (s *ADKStore) Get(ctx context.Context, id string) ([]byte, bool, error) { return s.repository.GetCheckpointBlob(ctx, id) }
func (s *ADKStore) Set(ctx context.Context, id string, value []byte) error { return s.repository.SetCheckpointBlob(ctx, id, value) }
func (s *ADKStore) Delete(ctx context.Context, id string) error { return s.repository.DeleteCheckpointBlob(ctx, id) }
```

When Orchestrator returns an approval-required transfer, emit `adk.StatefulInterrupt(ctx, info, domain.ResumeState{RunID, Outcome})`. Ordinary stage checkpoints remain repository `RunCheckpoint` rows and are not delegated to Eino.

- [ ] **Step 4: Run checkpoint and resume tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse ./muse/checkpoint -run 'TestRunner|TestADKStore|TestRecovery' -count=1`

Expected: PASS for interrupt persistence, reconstructed Runner resume, current-stage-only recovery, checksum mismatch rejection, and lease-exclusive recovery claims.

- [ ] **Step 5: Commit the ADK/checkpoint slice**

```bash
git add muse/agent.go muse/agent_test.go muse/checkpoint
git commit -m "feat: add muse adk checkpoint recovery"
```

---

### Task 9: RunService, Worker Ownership, Preemption, And Recovery

**Files:**
- Create: `muse/run/worker.go`
- Create: `muse/run/service.go`
- Test: `muse/run/worker_test.go`
- Test: `muse/run/service_test.go`

**Interfaces:**
- Consumes Eino Runner factory, repository Run APIs, and recovery helper.
- Produces `Service.Start`, `Service.Get`, `Service.Wait`, `Service.Cancel`, `Service.Resume`, and `Service.Recover`.

- [ ] **Step 1: Write failing disconnect/preemption/recovery tests**

```go
func TestServiceCallerCancellationDoesNotCancelOwnedWorker(t *testing.T) {
	service := newServiceFixture(t)
	callerCtx, cancel := context.WithCancel(context.Background())
	runID, err := service.Start(callerCtx, StartRequest{SessionID: "session-1", MessageID: "message-1", Input: "课程几点开始？"})
	if err != nil { t.Fatal(err) }
	cancel()
	result, err := service.Wait(context.Background(), runID)
	if err != nil || result.Status != domain.RunCompleted { t.Fatalf("result=%+v err=%v", result, err) }
}

func TestNewMessagePreemptsOldRunBeforeFinalization(t *testing.T) {
	service := newBlockingServiceFixture(t)
	oldID, err := service.Start(context.Background(), StartRequest{SessionID: "session-1", MessageID: "message-1", Input: "旧消息"})
	if err != nil { t.Fatal(err) }
	newID, err := service.Start(context.Background(), StartRequest{SessionID: "session-1", MessageID: "message-2", Input: "新消息"})
	if err != nil { t.Fatal(err) }
	service.release()
	oldRun, _ := service.Get(context.Background(), oldID)
	newRun, _ := service.Get(context.Background(), newID)
	if oldRun.Status != domain.RunPreempted || newRun.Status != domain.RunCompleted { t.Fatalf("old=%+v new=%+v", oldRun, newRun) }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/run -run 'TestService|TestWorker' -count=1`

Expected: FAIL because RunService and worker ownership are absent.

- [ ] **Step 3: Implement service-owned workers and explicit lifecycle APIs**

```go
type Service struct {
	lifecycle context.Context
	store repository.Store
	runnerFactory RunnerFactory
	mu sync.Mutex
	workers map[string]*Worker
}
type StartRequest struct { SessionID string; MessageID string; Input string }
type RunnerFactory interface { New(context.Context) (*adk.Runner, error) }

func (s *Service) Start(_ context.Context, req StartRequest) (string, error) {
	run := newRunRecord(req)
	if err := s.store.PreemptSession(s.lifecycle, req.SessionID, run.ID); err != nil { return "", err }
	if err := s.store.CreateRun(s.lifecycle, run); err != nil { return "", err }
	worker := NewWorker(s.lifecycle, run, s.runnerFactory, s.store)
	s.track(worker)
	worker.Start()
	return run.ID, nil
}
```

Worker contexts derive from service lifecycle, never caller HTTP/CLI contexts. `Cancel` is explicit. Recovery claims only expired heartbeat leases, validates Manifest checksums, and restarts from the saved stage. Ownership token and final CAS make late old workers converge to PREEMPTED without sending an Outbox reply.

- [ ] **Step 4: Run service tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/run -run 'TestService|TestWorker' -count=1`

Expected: PASS for caller disconnect, explicit cancel, same-session preemption, stale-worker CAS, heartbeat lease, and process-style SQLite recovery.

- [ ] **Step 5: Commit the RunService slice**

```bash
git add muse/run/service.go muse/run/service_test.go muse/run/worker.go muse/run/worker_test.go
git commit -m "feat: add muse run lifecycle service"
```

---

### Task 10: CLI, CozeLoop-Safe Trace, Fake Model, And End-To-End Acceptance

**Files:**
- Create: `cmd/muse-agent/main.go`
- Create: `cmd/muse-agent/trace.go`
- Create: `cmd/muse-agent/main_test.go`
- Create: `muse/testmodel_test.go`
- Modify: `docs/superpowers/specs/2026-07-19-muse-sales-agent-architecture-design.md`

**Interfaces:**
- Consumes the complete Muse packages from Tasks 1-9.
- Produces `-prepare-only`, `-message`, `-resume-run`, `-list-recoverable`, `-db`, and `-json` CLI flags.
- Produces compact trace input/output containing no raw input, Profile, TODO, prompt, skill body, or final response.

- [ ] **Step 1: Write failing command and acceptance tests**

```go
func TestPrepareOnlyDoesNotRequireCredentialsOrModel(t *testing.T) {
	clearMuseEnv(t)
	output, err := runAgent(context.Background(), []string{"-prepare-only", "-db", filepath.Join(t.TempDir(), "muse.sqlite")})
	if err != nil { t.Fatal(err) }
	if output.Mode != "prepare-only" || output.AgentName != "muse_sales_agent" || len(output.Roles) != 5 { t.Fatalf("output=%+v", output) }
}

func TestFakeModelRunsProfileTodoAndLowRiskReplyEndToEnd(t *testing.T) {
	oldFactory := newChatModel
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) { return newMuseFakeModel(), nil }
	t.Cleanup(func() { newChatModel = oldFactory })
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	output, err := runAgent(context.Background(), []string{"-message", "我是教师，想了解体验课", "-db", filepath.Join(t.TempDir(), "muse.sqlite")})
	if err != nil { t.Fatal(err) }
	if output.Status != "completed" || output.ProfileVersion != 2 || output.TodoVersion != 2 || output.PlanReviewed || output.FinalReviewed { t.Fatalf("output=%+v", output) }
}

func TestRootTraceDoesNotContainSensitiveText(t *testing.T) {
	input := runTraceInput{RunID: "run-1", InputChars: 21, InputHash: "sha256:abc", Stage: "created"}
	raw, err := json.Marshal(input)
	if err != nil { t.Fatal(err) }
	for _, secret := range []string{"我是教师", "premium", "system prompt"} {
		if bytes.Contains(raw, []byte(secret)) { t.Fatalf("trace leaked %q: %s", secret, raw) }
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/muse-agent ./muse/... -count=1`

Expected: FAIL because the CLI and full acceptance fixture do not exist.

- [ ] **Step 3: Implement thin command wiring and deterministic fake-model paths**

```go
type runConfig struct { PrepareOnly bool; Message string; ResumeRun string; ListRecoverable bool; DBPath string; PrintJSON bool }
type runOutput struct {
	Mode string `json:"mode"`; AgentName string `json:"agent_name"`; RunID string `json:"run_id,omitempty"`; Status string `json:"status,omitempty"`
	Roles []string `json:"roles"`; ProfileVersion int64 `json:"profile_version,omitempty"`; TodoVersion int64 `json:"todo_version,omitempty"`
	PlanReviewed bool `json:"plan_reviewed"`; FinalReviewed bool `json:"final_reviewed"`; OutboxStatus string `json:"outbox_status,omitempty"`
}
type runTraceInput struct { RunID string `json:"run_id"`; InputChars int `json:"input_chars"`; InputHash string `json:"input_hash"`; Stage string `json:"stage"` }
```

`-prepare-only` prints frozen role/skill/tool-policy/checkpoint/budget metadata and returns before model creation. Normal mode opens SQLite, installs existing CozeLoop callbacks, builds the OpenAI-compatible Eino model, starts or resumes the Run, drains the local simulated Outbox, and prints Bubbles plus a compact summary. The fake model must use real Eino tool calls and deterministic JSON responses; no test branch may bypass the Eino Runner.

- [ ] **Step 4: Run all acceptance tests and formatting checks**

Run: `gofmt -w muse cmd/muse-agent`

Expected: all created Go files are formatted.

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./muse/... ./cmd/muse-agent -count=1`

Expected: PASS, including the 20 acceptance cases from the architecture spec.

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./... -count=1`

Expected: PASS for the full repository with no regressions.

- [ ] **Step 5: Run manual smoke commands and update only factual verification notes**

```bash
go run ./cmd/muse-agent -prepare-only -db /private/tmp/muse-prepare.sqlite
go run ./cmd/muse-agent -message "课程几点开始？" -db /private/tmp/muse-smoke.sqlite -json
go run ./cmd/muse-agent -list-recoverable -db /private/tmp/muse-smoke.sqlite
```

Expected: prepare-only shows the frozen architecture without credentials; the real-model command either returns a completed Run when configured or a clear missing-model configuration error; list-recoverable returns a stable summary without raw user text.

- [ ] **Step 6: Commit the runnable reference implementation**

```bash
git add muse cmd/muse-agent docs/superpowers/specs/2026-07-19-muse-sales-agent-architecture-design.md
git commit -m "feat: add resumable muse sales agent"
```

---

## Final Verification Checklist

- [ ] `git diff --check` reports no whitespace errors.
- [ ] `go test ./muse/... ./cmd/muse-agent -count=1` passes with fresh output.
- [ ] `go test ./... -count=1` passes with fresh output.
- [ ] Planner direct Profile/TODO writes are visible to Thinker in the same Run.
- [ ] Planner retry and crash recovery reuse Tool Ledger results instead of repeating writes.
- [ ] Low-risk requests skip both semantic Reviewers.
- [ ] Risk Reviewers cannot execute tools or override deterministic validation.
- [ ] FinalReviewer rewrite never reruns Planner.
- [ ] Eino interrupt checkpoint survives Runner reconstruction and Resume.
- [ ] SQLite business checkpoint resumes from the last completed stage.
- [ ] New-message preemption prevents stale replies without rolling back committed Profile/TODO writes.
- [ ] Run finalization and Outbox insert are atomic and idempotent.
- [ ] Delivery retry never reruns a model stage and keeps the same MessageID.
- [ ] Root traces do not contain raw user text, Profile/TODO values, prompts, skill bodies, or final replies.
- [ ] `git status --short` contains no unexpected modified or staged files.
