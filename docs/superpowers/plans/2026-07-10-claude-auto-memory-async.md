# Claude Auto Memory Async Extraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace synchronous post-turn extraction with Claude Code style fire-and-forget scheduling, single-flight extraction, latest-wins coalescing, and explicit Drain.

**Architecture:** Keep `Extractor` synchronous and focused on cursor plus persistence. Add `ExtractionScheduler` as the only concurrency owner; `Runner` schedules a copied history snapshot after the answer and returns immediately, while the scripted CLI prints the answer before draining background results.

**Tech Stack:** Go 1.25.8, goroutines/channels/mutexes, existing Eino adapters, table-driven tests.

## Global Constraints

- `RunTurn` must not wait for extraction model or storage I/O.
- At most one extraction may run at a time.
- Calls received while busy overwrite one pending snapshot; only the latest snapshot gets a trailing run.
- `Drain` waits for current and trailing work and returns accumulated results without losing warnings.
- Background work uses a context detached from request cancellation but retains context values.
- CLI prints the answer before calling a 60-second soft-timeout Drain.
- Every new function and struct has a Chinese purpose comment; core lifecycle comments explain the business boundary.

---

### Task 1: Async Scheduler RED/GREEN

**Files:**
- Create: `memory/claude_auto_memory/scheduler.go`
- Create: `memory/claude_auto_memory/scheduler_test.go`
- Modify: `memory/claude_auto_memory/types.go`

**Interfaces:**
- Produces: `NewExtractionScheduler(*Extractor) (*ExtractionScheduler, error)`
- Produces: `(*ExtractionScheduler).Schedule(context.Context, []ConversationMessage)`
- Produces: `(*ExtractionScheduler).Drain(context.Context) (DrainResult, error)`

- [ ] Write a blocking fake test: `Schedule` returns before the fake extractor is released.
- [ ] Write a coalescing test: while first extraction blocks, submit two larger snapshots; after release, assert only first plus latest snapshot are processed.
- [ ] Run focused tests and confirm RED because scheduler symbols are missing.
- [ ] Implement one goroutine loop, a single pending snapshot, an idle channel, and accumulated results.
- [ ] Run focused tests and confirm GREEN.

### Task 2: Runner and CLI Lifecycle RED/GREEN

**Files:**
- Modify: `memory/claude_auto_memory/runner.go`
- Modify: `memory/claude_auto_memory/runner_test.go`
- Modify: `cmd/claude-auto-memory-agent/main.go`
- Modify: `cmd/claude-auto-memory-agent/main_test.go`

**Interfaces:**
- Produces: `(*Runner).Drain(context.Context) (DrainResult, error)`
- Changes: `TurnResult` contains immediate answer/recall only; background written memories arrive from Drain.

- [ ] Add a runner test whose extractor blocks and assert `RunTurn` returns the answer before release.
- [ ] Add a main-flow test that records output timing and confirms answer printing precedes Drain completion.
- [ ] Run tests and confirm RED against the current synchronous `ExtractNew` call.
- [ ] Make Runner own the scheduler, call `Schedule` after history commit, and return immediately.
- [ ] Print answer/recall first in CLI, then Drain with a 60-second soft timeout and print written/warnings.
- [ ] Run package and command tests and confirm GREEN.

### Task 3: Documentation and Verification

**Files:**
- Modify: `memory/claude_auto_memory/README.md`

- [ ] Document interactive fire-and-forget versus scripted/headless Drain semantics.
- [ ] Run focused tests, `go vet`, and full `go test ./... -count=1`.
- [ ] Run the real CLI and verify the answer appears before extraction completion or warning.
- [ ] Commit implementation and documentation.

## Plan Self-Review

- The plan changes only scheduling and result delivery; classification, storage, recall, and prompts stay untouched.
- Scheduler, Runner, and CLI responsibilities remain in separate files.
- The RED tests directly prove the mismatch observed in the existing synchronous implementation.
