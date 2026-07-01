# Eino Skill Mode Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build three reasoned Go + Eino ADK Skill Middleware mode demos with concise trace.

**Architecture:** Add a focused `reasoning/skillmode` package for reusable skill-mode agent construction and a thin `cmd/skill-mode-agent` entrypoint. The package uses CloudWeGo `skill.NewMiddleware` in `ChatModelAgentConfig.Handlers`, plus an `AgentHub` for fork modes.

**Tech Stack:** Go, CloudWeGo Eino ADK v0.9.6, `adk/middlewares/skill`, Eino callbacks, existing `observability/cozeloop`.

---

### Task 1: Skillmode Package Tests

**Files:**
- Create: `reasoning/skillmode/agent_test.go`
- Create: `reasoning/skillmode/agent.go`
- Create: `reasoning/skillmode/skills.go`
- Create: `reasoning/skillmode/hub.go`

- [ ] **Step 1: Write failing tests**

Create tests that assert:

- `DefaultScenarios()` returns inline, `fork_with_context`, and `fork`.
- Each scenario has a non-empty business rationale.
- A fake tool-calling model can run through ADK, call the `skill` tool, and receive an assistant result.

- [ ] **Step 2: Run red test**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reasoning/skillmode -count=1`

Expected: package or symbols do not exist.

- [ ] **Step 3: Implement minimal package**

Implement scenario definitions, real `SKILL.md` files, local filesystem skill backend, `NewRunner`, `QueryRunner`, and `ScenarioAgentHub`.

- [ ] **Step 4: Run green test**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reasoning/skillmode -count=1`

Expected: PASS.

### Task 2: Command Entrypoint Tests

**Files:**
- Create: `cmd/skill-mode-agent/main_test.go`
- Create: `cmd/skill-mode-agent/main.go`
- Create: `cmd/skill-mode-agent/trace.go`

- [ ] **Step 1: Write failing tests**

Create tests that assert:

- `-prepare-only` loads the default customer-support request without model calls.
- The fake model run returns a stable summary.
- `withRunAgentTrace` records only mode, scenario, input chars, and output summary.

- [ ] **Step 2: Run red test**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/skill-mode-agent -count=1`

Expected: package or symbols do not exist.

- [ ] **Step 3: Implement minimal command**

Implement env loading consistent with nearby commands, OpenAI-compatible model creation, mode flag parsing, CozeLoop install, ADK runner query, optional JSON output, and concise trace.

- [ ] **Step 4: Run green test**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/skill-mode-agent -count=1`

Expected: PASS.

### Task 3: Docs And Final Verification

**Files:**
- Create: `reasoning/skillmode/README.md`

- [ ] **Step 1: Document the three mode reasons**

Explain inline, `fork_with_context`, and `fork` using the customer-support scenarios and CloudWeGo API shape.

- [ ] **Step 2: Run full targeted verification**

Run: `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reasoning/skillmode ./cmd/skill-mode-agent -count=1`

Run: `go run ./cmd/skill-mode-agent -prepare-only`

Expected: tests pass and prepare-only prints the customer-support request.
