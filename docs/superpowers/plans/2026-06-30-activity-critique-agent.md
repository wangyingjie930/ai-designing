# Activity Critique Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go + Eino ADK activity-plan and marketing-copy quality iteration Agent from the Python generator-critic loop.

**Architecture:** Add a focused `reflection/critique` package that owns the deterministic generator-critic loop, parser, prompts, and ADK wrapper. Add a thin `cmd/activity-critique-agent` entrypoint whose tests can be clicked directly without CLI parameters.

**Tech Stack:** Go 1.25.8, CloudWeGo Eino, Eino ADK, OpenAI-compatible chat model, Go unit tests with fake models.

---

## File Structure

- Create `reflection/critique/types.go` for structs and config.
- Create `reflection/critique/prompts.go` for Chinese generator and critic prompts.
- Create `reflection/critique/parser.go` for critic JSON extraction and normalization.
- Create `reflection/critique/agent.go` for `GeneratorCriticLoop`.
- Create `reflection/critique/adk_agent.go` for Eino ADK integration.
- Create `reflection/critique/agent_test.go` and `reflection/critique/parser_test.go`.
- Create `cmd/activity-critique-agent/main.go` for direct default execution.
- Create `cmd/activity-critique-agent/main_test.go` for click-to-run tests.

### Task 1: Core Critique Types And Parser

**Files:**
- Create: `reflection/critique/types.go`
- Create: `reflection/critique/parser.go`
- Test: `reflection/critique/parser_test.go`

- [ ] **Step 1: Write failing parser tests**

Create tests that call `ParseCritiqueResult` with fenced JSON and malformed JSON. Expected: valid JSON returns normalized score and feedback text; malformed JSON fails.

- [ ] **Step 2: Run parser tests to verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique -run 'TestParseCritique' -count=1`

Expected: FAIL because package or function is missing.

- [ ] **Step 3: Implement minimal parser and types**

Add `CritiqueResult`, `IterationRecord`, `Request`, `Response`, `Config`, `ToolFeedbackFunc`, `ParseCritiqueResult`, JSON fence stripping, first-object extraction, and score normalization.

- [ ] **Step 4: Run parser tests to verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique -run 'TestParseCritique' -count=1`

Expected: PASS.

### Task 2: Generator-Critic Loop

**Files:**
- Create: `reflection/critique/prompts.go`
- Create: `reflection/critique/agent.go`
- Test: `reflection/critique/agent_test.go`

- [ ] **Step 1: Write failing loop tests**

Create fake model tests proving `Refine` calls generator, tool feedback, critic, and regeneration in order. The fake critic should reject the first draft with score `0.62` and approve the second with score `0.93`.

- [ ] **Step 2: Run loop tests to verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique -run 'TestRefine' -count=1`

Expected: FAIL because `GeneratorCriticLoop` is not implemented.

- [ ] **Step 3: Implement loop**

Add `NewGeneratorCriticLoop`, `Generate`, `Critique`, `Refine`, `callModel`, message text extraction, Chinese prompts, history recording, threshold approval, and max-iteration fallback.

- [ ] **Step 4: Run loop tests to verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique -count=1`

Expected: PASS.

### Task 3: ADK Wrapper

**Files:**
- Create: `reflection/critique/adk_agent.go`
- Modify: `reflection/critique/agent_test.go`

- [ ] **Step 1: Write failing ADK test**

Create a test that builds `NewRunner`, calls `runner.Query`, and asserts `event.Output.CustomizedOutput` is a `*critique.Response`.

- [ ] **Step 2: Run ADK test to verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique -run 'TestADK' -count=1`

Expected: FAIL because ADK wrapper is missing.

- [ ] **Step 3: Implement ADK wrapper**

Add `ADKAgent`, `NewADKAgent`, `NewRunner`, `Name`, `Description`, and `Run`, following `reasoning/cot/adk_agent.go`.

- [ ] **Step 4: Run ADK test to verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique -count=1`

Expected: PASS.

### Task 4: Click-To-Run Cmd Agent

**Files:**
- Create: `cmd/activity-critique-agent/main.go`
- Create: `cmd/activity-critique-agent/main_test.go`

- [ ] **Step 1: Write failing cmd tests**

Create tests for `defaultActivityRequest`, `activityChecklistTool`, and `runAgent` with a fake model factory. The test calls `runAgent(context.Background())` directly, with no CLI args.

- [ ] **Step 2: Run cmd tests to verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/activity-critique-agent -count=1`

Expected: FAIL because cmd package is missing.

- [ ] **Step 3: Implement cmd**

Add default activity input, env loading, model creation, `activityChecklistTool`, `queryRunner`, `summarizeResponse`, and a direct `main` that calls `runAgent(context.Background())`.

- [ ] **Step 4: Run cmd tests to verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/activity-critique-agent -count=1`

Expected: PASS.

### Task 5: Final Verification

**Files:**
- All files above.

- [ ] **Step 1: Run focused package tests**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique ./cmd/activity-critique-agent -count=1`

Expected: PASS.

- [ ] **Step 2: Inspect git diff**

Run: `git diff -- reflection/critique cmd/activity-critique-agent docs/superpowers`

Expected: Changes stay inside requested package, cmd entrypoint, and Superpowers docs.
