# Claude Code Auto Memory Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an interview-ready Claude Code style loop that extracts durable memories after a turn, lets the model choose type and private/team scope, stores Markdown topics plus dual indexes, and recalls relevant topics before the next turn.

**Architecture:** A filesystem `Store` owns deterministic persistence and security; small interfaces own semantic extraction, selection, and answering. `Runner` enforces `Recall -> Main Agent -> Extract`; one OpenAI-compatible model can back all three roles through isolated prompts.

**Tech Stack:** Go 1.25.8, CloudWeGo Eino `model.BaseChatModel`, standard-library JSON/Markdown/file APIs, table-driven Go tests.

## Global Constraints

- Implement only the local core loop; do not add HTTP sync, ETag, watcher, SQLite, embeddings, retention, compaction, or `hierarchical_v1` dependencies.
- Memory types are exactly `user`, `feedback`, `project`, and `reference`; scopes are exactly `private` and `team`.
- The model chooses both type and scope; engineering code validates the choice and only forces `user` to remain private.
- Private topics and `MEMORY.md` live at the configured root; team topics and `MEMORY.md` live under `<root>/team/`.
- Recall selects at most 5 manifest entries and only reads paths present in that manifest.
- Extraction happens after the main answer and advances its cursor only after the batch has been processed.
- Preserve existing comments. Every new function and struct gets a Chinese purpose comment. Core Agent-flow comments explain business role and boundary. Codex-authored prompts remain Chinese.
- Tests never call external models. Real model calls are reserved for opt-in E2E or manual CLI verification.

## File Map

| File | Responsibility |
|---|---|
| `memory/claude_auto_memory/types.go` | Closed enums, messages, candidates, records, manifest, results, and model interfaces |
| `memory/claude_auto_memory/frontmatter.go` | Deterministic topic frontmatter and index encoding |
| `memory/claude_auto_memory/security.go` | Slugging, containment, symlink, and team-secret guards |
| `memory/claude_auto_memory/store.go` | Private/team routing, upsert, dual indexes, manifest load, and safe reads |
| `memory/claude_auto_memory/extractor.go` | Incremental cursor, candidate writes, and partial success |
| `memory/claude_auto_memory/recall.go` | Selection, deduplication, five-item cap, and context rendering |
| `memory/claude_auto_memory/runner.go` | `Recall -> Main Agent -> Extract` orchestration and history |
| `memory/claude_auto_memory/prompts.go` | Chinese extraction, selection, and answer prompts |
| `memory/claude_auto_memory/llm.go` | Eino adapters and fenced-JSON parsing |
| `memory/claude_auto_memory/README.md` | Source mapping, interview explanation, and commands |
| `memory/claude_auto_memory/examples/interview_rounds.txt` | Stable three-round scenario |
| `cmd/claude-auto-memory-agent/main.go` | `.env`, model assembly, prepare-only, rounds, and trace |
| `cmd/claude-auto-memory-agent/main_test.go` | Configuration and fake-model full-loop tests |

---

### Task 1: Markdown Store, Dual Indexes, and Security

**Files:**
- Create: `memory/claude_auto_memory/types.go`
- Create: `memory/claude_auto_memory/frontmatter.go`
- Create: `memory/claude_auto_memory/security.go`
- Create: `memory/claude_auto_memory/store.go`
- Test: `memory/claude_auto_memory/store_test.go`

**Interfaces:**
- Produces: `NewStore(root string) (*Store, error)`
- Produces: `(*Store).Upsert(context.Context, MemoryCandidate) (MemoryRecord, error)`
- Produces: `(*Store).LoadManifest(context.Context) (MemoryManifest, error)`
- Produces: `(*Store).Read(context.Context, MemoryRef) (MemoryRecord, error)`

- [ ] **Step 1: Write failing persistence and rejection tests**

```go
func TestStoreUpsertMaintainsPrivateAndTeamIndexes(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil { t.Fatal(err) }
	_, err = store.Upsert(context.Background(), MemoryCandidate{
		Type: MemoryTypeUser, Scope: ScopePrivate, Topic: "中文注释偏好",
		Description: "用户希望新增代码使用中文用途注释", Content: "新增函数和结构体前写中文用途注释。",
	})
	if err != nil { t.Fatal(err) }
	_, err = store.Upsert(context.Background(), MemoryCandidate{
		Type: MemoryTypeProject, Scope: ScopeTeam, Topic: "tool-schema-convention",
		Description: "新工具参数需要描述", Content: "所有新工具参数都要声明 jsonschema_description。",
	})
	if err != nil { t.Fatal(err) }
	manifest, err := store.LoadManifest(context.Background())
	if err != nil { t.Fatal(err) }
	if len(manifest.Private) != 1 || len(manifest.Team) != 1 { t.Fatalf("manifest=%+v", manifest) }
}

func TestStoreRejectsUnsafeCandidates(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	cases := []MemoryCandidate{
		{Type: MemoryTypeUser, Scope: ScopeTeam, Topic: "preference", Description: "x", Content: "x"},
		{Type: MemoryTypeProject, Scope: ScopePrivate, Topic: "../escape", Description: "x", Content: "x"},
		{Type: MemoryTypeProject, Scope: ScopeTeam, Topic: "deployment", Description: "token", Content: "OPENAI_API_KEY=sk-secret-value"},
	}
	for _, candidate := range cases {
		if _, err := store.Upsert(context.Background(), candidate); err == nil { t.Fatalf("should fail: %+v", candidate) }
	}
}
```

- [ ] **Step 2: Verify the tests fail before production files exist**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -run 'TestStore' -count=1`

Expected: FAIL because the package and symbols do not exist.

- [ ] **Step 3: Add closed types and records**

```go
type MemoryType string
const (
	MemoryTypeUser MemoryType = "user"
	MemoryTypeFeedback MemoryType = "feedback"
	MemoryTypeProject MemoryType = "project"
	MemoryTypeReference MemoryType = "reference"
)
type Scope string
const ( ScopePrivate Scope = "private"; ScopeTeam Scope = "team" )
type MemoryCandidate struct { Type MemoryType `json:"type"`; Scope Scope `json:"scope"`; Topic, Description, Content string }
type MemoryRef struct { Scope Scope `json:"scope"`; Topic string `json:"topic"` }
type MemoryRecord struct { Ref MemoryRef; Type MemoryType; Description, Content, Path string }
type IndexEntry struct { Ref MemoryRef; Type MemoryType; Description, Path string }
type MemoryManifest struct { Private []IndexEntry `json:"private"`; Team []IndexEntry `json:"team"` }
```

Add `Valid()` methods for both enums and Chinese purpose comments for every declaration.

- [ ] **Step 4: Add deterministic frontmatter and security helpers**

Use JSON metadata between Markdown `---` delimiters, which is valid YAML and lossless with the standard library:

```go
func encodeTopic(record MemoryRecord) ([]byte, error) {
	metadata, err := json.Marshal(topicMetadata{Name: record.Ref.Topic, Description: record.Description, Type: record.Type})
	if err != nil { return nil, err }
	return []byte("---\n" + string(metadata) + "\n---\n\n" + strings.TrimSpace(record.Content) + "\n"), nil
}
```

`slugifyTopic` retains Unicode letters/numbers, collapses punctuation to hyphens, and rejects empty values, absolute paths, `..`, and normalized `MEMORY`. `containsSecret` rejects private-key headers, Bearer values, and non-empty assignments whose keys contain `api_key`, `token`, `secret`, `password`, or `cookie`.

- [ ] **Step 5: Add `Store` and stable dual indexes**

```go
type Store struct { root string }

func (s *Store) Upsert(ctx context.Context, candidate MemoryCandidate) (MemoryRecord, error) {
	if err := ctx.Err(); err != nil { return MemoryRecord{}, err }
	if err := validateCandidate(candidate); err != nil { return MemoryRecord{}, err }
	slug, err := slugifyTopic(candidate.Topic)
	if err != nil { return MemoryRecord{}, err }
	record := MemoryRecord{Ref: MemoryRef{Scope: candidate.Scope, Topic: slug}, Type: candidate.Type,
		Description: strings.TrimSpace(candidate.Description), Content: strings.TrimSpace(candidate.Content)}
	record.Path = s.topicPath(record.Ref)
	data, err := encodeTopic(record)
	if err != nil { return MemoryRecord{}, err }
	if err := atomicWrite(record.Path, data, 0o600); err != nil { return MemoryRecord{}, err }
	if err := s.rebuildIndex(candidate.Scope); err != nil { return MemoryRecord{}, err }
	return record, nil
}
```

`rebuildIndex` scans sibling `.md` files except `MEMORY.md`, decodes frontmatter, sorts by topic, and atomically writes one Markdown link per record. `LoadManifest` parses the two generated indexes. `Read` confirms the ref exists in that manifest, then applies containment and symlink checks before opening the topic.

- [ ] **Step 6: Run tests and commit**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -run 'TestStore' -count=1`

Expected: PASS.

```bash
git add memory/claude_auto_memory
git commit -m "feat: add Claude-style Markdown memory store"
```

---

### Task 2: Incremental Post-Turn Extraction

**Files:**
- Create: `memory/claude_auto_memory/extractor.go`
- Test: `memory/claude_auto_memory/extractor_test.go`
- Modify: `memory/claude_auto_memory/types.go`

**Interfaces:**
- Consumes: `Store.Upsert`, `MemoryCandidate`
- Produces: `MemoryExtractor`, `NewExtractor`, `(*Extractor).ExtractNew`, `ExtractionResult`

- [ ] **Step 1: Write failing cursor tests**

```go
func TestExtractorProcessesOnlyNewMessages(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	fake := &fakeExtractor{batches: [][]MemoryCandidate{{{
		Type: MemoryTypeUser, Scope: ScopePrivate, Topic: "comment-style",
		Description: "用户偏好中文注释", Content: "新增代码写中文用途注释。",
	}}, {}}}
	extractor, _ := NewExtractor(store, fake)
	history := []ConversationMessage{{Role: RoleUser, Content: "记住我喜欢中文注释"}, {Role: RoleAssistant, Content: "好的"}}
	first := extractor.ExtractNew(context.Background(), history)
	if len(first.Written) != 1 || first.ProcessedMessages != 2 { t.Fatalf("first=%+v", first) }
	history = append(history, ConversationMessage{Role: RoleUser, Content: "继续"}, ConversationMessage{Role: RoleAssistant, Content: "收到"})
	second := extractor.ExtractNew(context.Background(), history)
	if second.ProcessedMessages != 2 || len(fake.inputs[1]) != 2 { t.Fatalf("second=%+v inputs=%+v", second, fake.inputs) }
}

func TestExtractorRetriesAfterModelFailure(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	fake := &fakeExtractor{err: errors.New("model unavailable")}
	extractor, _ := NewExtractor(store, fake)
	result := extractor.ExtractNew(context.Background(), []ConversationMessage{{Role: RoleUser, Content: "记住这个"}})
	if result.ProcessedMessages != 0 || extractor.Cursor() != 0 { t.Fatalf("result=%+v", result) }
}
```

- [ ] **Step 2: Verify failure, then add roles, interface, and cursor**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -run 'TestExtractor' -count=1`

Expected: FAIL with undefined extractor symbols.

```go
type Role string
const ( RoleUser Role = "user"; RoleAssistant Role = "assistant" )
type ConversationMessage struct { Role Role `json:"role"`; Content string `json:"content"` }
type MemoryExtractor interface { Extract(context.Context, []ConversationMessage) ([]MemoryCandidate, error) }
type ExtractionResult struct { Written []MemoryRecord; ProcessedMessages int; Warnings []error }
type Extractor struct { store *Store; model MemoryExtractor; cursor int; mu sync.Mutex }
```

`ExtractNew` copies `history[cursor:]`, calls the model, writes each valid candidate independently, and moves the cursor to `len(history)` after the batch is processed. A model/context error returns a warning and leaves the cursor unchanged; an invalid individual candidate becomes a warning without blocking other candidates.

- [ ] **Step 3: Run all package tests and commit**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS.

```bash
git add memory/claude_auto_memory/types.go memory/claude_auto_memory/extractor.go memory/claude_auto_memory/extractor_test.go
git commit -m "feat: add incremental post-turn memory extraction"
```

---

### Task 3: Manifest-Based Recall

**Files:**
- Create: `memory/claude_auto_memory/recall.go`
- Test: `memory/claude_auto_memory/recall_test.go`
- Modify: `memory/claude_auto_memory/types.go`

**Interfaces:**
- Consumes: `Store.LoadManifest`, `Store.Read`
- Produces: `MemorySelector`, `NewRecaller`, `(*Recaller).Recall`, `RecallResult`

- [ ] **Step 1: Write failing limit and degradation tests**

```go
func TestRecallerReadsOnlyFiveUniqueManifestEntries(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	refs := seedSixMemories(t, store)
	selector := &fakeSelector{refs: append(refs, refs[0], MemoryRef{Scope: ScopeTeam, Topic: "missing"})}
	recaller, _ := NewRecaller(store, selector)
	result := recaller.Recall(context.Background(), "新增工具要注意什么")
	if len(result.Records) != 5 { t.Fatalf("result=%+v", result) }
	if !strings.Contains(result.Context, "source=") || !strings.Contains(result.Context, "scope=") { t.Fatalf("context=%s", result.Context) }
}

func TestRecallerSelectorFailureReturnsEmptyContext(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	recaller, _ := NewRecaller(store, &fakeSelector{err: errors.New("selector unavailable")})
	result := recaller.Recall(context.Background(), "问题")
	if result.Context != "" || len(result.Warnings) != 1 { t.Fatalf("result=%+v", result) }
}
```

- [ ] **Step 2: Verify failure, then add recall types and implementation**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -run 'TestRecaller' -count=1`

Expected: FAIL with undefined recall symbols.

```go
type MemorySelector interface { Select(context.Context, string, MemoryManifest) ([]MemoryRef, error) }
type RecallResult struct { Records []MemoryRecord; Context string; Warnings []error }
```

`Recall` loads both indexes, asks the selector, removes duplicate refs, stops after five successful reads, lets `Store.Read` reject unknown refs, and renders each record with `scope`, `source`, `type`, and content. Index/selector failure degrades to an empty context plus warning.

- [ ] **Step 3: Run all package tests and commit**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS.

```bash
git add memory/claude_auto_memory/types.go memory/claude_auto_memory/recall.go memory/claude_auto_memory/recall_test.go
git commit -m "feat: add manifest-based memory recall"
```

---

### Task 4: Three-Stage Runner

**Files:**
- Create: `memory/claude_auto_memory/runner.go`
- Test: `memory/claude_auto_memory/runner_test.go`
- Modify: `memory/claude_auto_memory/types.go`

**Interfaces:**
- Consumes: `Recaller.Recall`, `Extractor.ExtractNew`
- Produces: `ChatAgent`, `NewRunner`, `(*Runner).RunTurn`, `TurnResult`

- [ ] **Step 1: Write failing next-turn and main-failure tests**

```go
func TestRunnerNewMemoryAffectsNextTurnNotCurrentTurn(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	extractorModel := &fakeExtractor{batches: [][]MemoryCandidate{{{
		Type: MemoryTypeUser, Scope: ScopePrivate, Topic: "comment-style",
		Description: "用户偏好中文注释", Content: "新增函数写中文注释。",
	}}, {}}}
	extractor, _ := NewExtractor(store, extractorModel)
	recaller, _ := NewRecaller(store, &querySelector{})
	chat := &recordingChatAgent{}
	runner, _ := NewRunner(recaller, chat, extractor)
	first, err := runner.RunTurn(context.Background(), "记住我喜欢中文注释")
	if err != nil { t.Fatal(err) }
	if len(first.Recalled) != 0 || len(first.Written) != 1 { t.Fatalf("first=%+v", first) }
	second, err := runner.RunTurn(context.Background(), "我写代码有什么偏好？")
	if err != nil { t.Fatal(err) }
	if len(second.Recalled) != 1 || !strings.Contains(chat.memoryContexts[1], "中文注释") { t.Fatalf("second=%+v", second) }
}
```

Also add `TestRunnerSkipsExtractionWhenMainAgentFails`, using a failing `ChatAgent` and asserting the extractor fake receives zero calls.

- [ ] **Step 2: Verify failure, then add the ordering boundary**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -run 'TestRunner' -count=1`

Expected: FAIL with undefined runner symbols.

```go
type ChatAgent interface { Generate(context.Context, []ConversationMessage, string) (string, error) }
type TurnResult struct { Answer string; Recalled, Written []MemoryRecord; Warnings []error }

func (r *Runner) RunTurn(ctx context.Context, userInput string) (TurnResult, error) {
	// 业务边界一：只为当前问题召回相关长期记忆，不把全部记忆塞入模型。
	recall := r.recaller.Recall(ctx, userInput)
	pending := append(append([]ConversationMessage(nil), r.history...), ConversationMessage{Role: RoleUser, Content: strings.TrimSpace(userInput)})
	// 业务边界二：主 Agent 看不到提取器 prompt 和存储维护过程。
	answer, err := r.chat.Generate(ctx, pending, recall.Context)
	if err != nil { return TurnResult{}, err }
	r.history = append(pending, ConversationMessage{Role: RoleAssistant, Content: answer})
	// 业务边界三：回答后提取，所以新记忆只影响下一轮。
	extraction := r.extractor.ExtractNew(ctx, r.history)
	return TurnResult{Answer: answer, Recalled: recall.Records, Written: extraction.Written,
		Warnings: append(recall.Warnings, extraction.Warnings...)}, nil
}
```

- [ ] **Step 3: Run package tests and commit**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS.

```bash
git add memory/claude_auto_memory/types.go memory/claude_auto_memory/runner.go memory/claude_auto_memory/runner_test.go
git commit -m "feat: orchestrate recall answer and extraction"
```

---

### Task 5: Eino Model Adapters and Chinese Prompts

**Files:**
- Create: `memory/claude_auto_memory/prompts.go`
- Create: `memory/claude_auto_memory/llm.go`
- Test: `memory/claude_auto_memory/llm_test.go`

**Interfaces:**
- Consumes: `model.BaseChatModel` and all three model-facing interfaces
- Produces: `NewLLMExtractor`, `NewLLMSelector`, `NewLLMChatAgent`

- [ ] **Step 1: Write failing prompt and JSON tests**

```go
func TestExtractionPromptKeepsTypeAndScopeAsModelDecisions(t *testing.T) {
	prompt := extractionSystemPrompt()
	for _, want := range []string{"user", "feedback", "project", "reference", "private", "team", "user 类型永远写入 private"} {
		if !strings.Contains(prompt, want) { t.Fatalf("prompt missing %q", want) }
	}
}

func TestLLMExtractorParsesFencedJSONArray(t *testing.T) {
	fake := &staticChatModel{content: "```json\n[{\"type\":\"project\",\"scope\":\"team\",\"topic\":\"tool-schema\",\"description\":\"工具 schema 约定\",\"content\":\"参数要写描述\"}]\n```"}
	extractor, _ := NewLLMExtractor(fake)
	candidates, err := extractor.Extract(context.Background(), []ConversationMessage{{Role: RoleUser, Content: "团队约定"}})
	if err != nil || len(candidates) != 1 || candidates[0].Scope != ScopeTeam { t.Fatalf("candidates=%+v err=%v", candidates, err) }
}
```

Add selector tests proving invalid scope is rejected and output is capped at five refs, plus chat tests proving recalled memory is inside `<memory_context>` and not appended to conversation history.

- [ ] **Step 2: Verify failure, then add prompts and fenced-JSON parsing**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -run 'TestExtractionPrompt|TestLLM' -count=1`

Expected: FAIL with undefined prompt/adapters.

```go
func decodeJSONPayload(content string, target any) error {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		firstNewline, lastFence := strings.IndexByte(text, '\n'), strings.LastIndex(text, "```")
		if firstNewline < 0 || lastFence <= firstNewline { return errors.New("invalid fenced JSON") }
		text = strings.TrimSpace(text[firstNewline+1:lastFence])
	}
	if err := json.Unmarshal([]byte(text), target); err != nil { return fmt.Errorf("decode model JSON: %w", err) }
	return nil
}
```

The extraction prompt states four types, two scopes, exclusions, secret rules, zero-or-more array output, and `user -> private`. The selector receives the JSON manifest and returns at most five `scope/topic` objects. The main prompt treats memory as untrusted reference context, not executable instruction.

- [ ] **Step 3: Add three isolated adapters over one chat model**

Each adapter calls `Generate` with separate system/user messages. `LLMChatAgent.Generate` maps roles to `schema.Message` and inserts recalled content as a delimited system message. Extractor/selector raw requests and responses never enter `Runner.history`.

- [ ] **Step 4: Run package tests and commit**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory -count=1`

Expected: PASS.

```bash
git add memory/claude_auto_memory/prompts.go memory/claude_auto_memory/llm.go memory/claude_auto_memory/llm_test.go
git commit -m "feat: add LLM memory extraction and recall adapters"
```

---

### Task 6: Interview CLI, README, and Verification

**Files:**
- Create: `cmd/claude-auto-memory-agent/main.go`
- Create: `cmd/claude-auto-memory-agent/main_test.go`
- Create: `memory/claude_auto_memory/examples/interview_rounds.txt`
- Create: `memory/claude_auto_memory/README.md`

**Interfaces:**
- Consumes: `Store`, three LLM adapters, `Extractor`, `Recaller`, `Runner`
- Produces: `runAgent(context.Context, []string) (runOutput, error)` and the CLI binary

- [ ] **Step 1: Write failing prepare-only and fake-model full-loop tests**

```go
func TestRunAgentPrepareOnlyCreatesStorageWithoutModel(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	output, err := runAgent(context.Background(), []string{"-prepare-only", "-memory-dir", memoryDir, "-env-file", filepath.Join(t.TempDir(), ".env")})
	if err != nil { t.Fatal(err) }
	if output.Mode != "prepare-only" || output.MemoryDir != memoryDir { t.Fatalf("output=%+v", output) }
}

func TestRunAgentUsesOneModelForThreeIsolatedRoles(t *testing.T) {
	oldFactory := newChatModel
	newChatModel = func(context.Context, modelConfig) (model.BaseChatModel, error) { return newInterviewScriptModel(), nil }
	defer func() { newChatModel = oldFactory }()
	envPath := writeTestDotEnv(t, "OPENAI_API_KEY=test-key\nLLM_MODEL=test-model\n")
	output, err := runAgent(context.Background(), []string{"-env-file", envPath, "-memory-dir", t.TempDir(), "-rounds-file", writeTestRounds(t)})
	if err != nil { t.Fatal(err) }
	if output.Rounds != 3 || output.Written < 2 || output.Recalled == 0 || output.AnswerChars == 0 { t.Fatalf("output=%+v", output) }
}
```

- [ ] **Step 2: Verify failure, then add scenario and command assembly**

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./cmd/claude-auto-memory-agent -count=1`

Expected: FAIL because the command does not exist.

Default rounds:

```text
请记住，我个人更喜欢新增代码写中文用途注释。
---
团队约定：所有新工具参数都要提供 jsonschema_description。
---
以后在这个项目里新增工具要注意什么？
```

Command types:

```go
type runConfig struct { EnvPath, MemoryDir, RoundsFile string; PrepareOnly bool }
type modelConfig struct { APIKey, Model, BaseURL string }
type runOutput struct { Mode, MemoryDir string; Rounds, Recalled, Written, Warnings, AnswerChars int }
type chatModelFactory func(context.Context, modelConfig) (model.BaseChatModel, error)
```

Load `.env` first. Reuse aliases `OPENAI_API_KEY|LLM_OPENAI_API_KEY|LLM_API_KEY`, `LLM_MODEL|OPENAI_MODEL`, and existing base-URL normalization. `--prepare-only` creates the root/team directories without requiring model config. Normal mode builds one chat model, three isolated adapters, then runs the default or specified rounds. Print only model name, redacted key, memory directory, recalled/written `scope/topic`, warning summaries, and answers.

- [ ] **Step 3: Add deterministic scripted fake and pass command tests**

The fake dispatches on system prompt markers: extraction returns private user memory after round 1 and team project memory after round 2; selection returns no refs, then the private ref, then both refs; main calls return non-empty Chinese answers. This proves the real command assembly without network access.

Run: `env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./cmd/claude-auto-memory-agent -count=1`

Expected: PASS.

- [ ] **Step 4: Add README with source mapping and six interview points**

Document exact flow, directory example, difference from `hierarchical_v1`, Go-to-Claude concept mapping, post-turn extraction, semantic type/scope, topic + index, manifest recall, context isolation, security boundary, prepare-only/default/E2E commands, trace fields, and explicit non-goals.

- [ ] **Step 5: Run focused, full, and prepare-only verification**

```bash
env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1
env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache go test ./... -count=1
go run ./cmd/claude-auto-memory-agent -prepare-only -memory-dir /private/tmp/claude-auto-memory-prepare
```

Expected: both test commands PASS; prepare-only prints the resolved memory root and creates root/team directories without an API key.

- [ ] **Step 6: Run real three-round verification when `.env` is available**

```bash
rm -rf /private/tmp/claude-auto-memory-e2e
go run ./cmd/claude-auto-memory-agent -memory-dir /private/tmp/claude-auto-memory-e2e
```

Expected: three answers, at least two written topics across private/team, and relevant recall in round three. If the provider is unavailable, preserve the exact error and do not weaken deterministic tests.

- [ ] **Step 7: Commit final command and docs**

```bash
git add cmd/claude-auto-memory-agent memory/claude_auto_memory
git commit -m "feat: add interview-ready Claude auto memory demo"
```

## Plan Self-Review

- Spec coverage: all seven acceptance criteria map to Tasks 1-6; excluded systems never enter the file map.
- Placeholder scan: no TBD, TODO, “implement later,” or unnamed error-handling steps remain.
- Type consistency: `MemoryCandidate -> Store.Upsert -> MemoryRecord`, `MemoryManifest -> MemorySelector -> MemoryRef`, and `Recall -> ChatAgent -> Extract` signatures match across tasks.
- Scope: one local package and one command; no remote service or independent persistence subsystem.
