# Eino Skill Mode Agent Design

## Goal

Build a Go + Eino ADK demo that shows the three Skill Middleware modes with business reasons, runnable command entrypoints, and concise trace.

## Scenarios

The demo uses customer-support operations instead of a coding task:

- Inline skill: a duty manager applies a short reply-style and escalation checklist. The rule is lightweight and should be injected into the main agent context.
- `fork_with_context` skill: a compensation specialist reviews the current complaint with the conversation history. The specialist needs the user facts that led to the escalation.
- `fork` skill: a compliance reviewer checks the reply in an isolated context. The reviewer should not inherit uncertain claims from the main conversation.

## Architecture

`reasoning/skillmode` owns the reusable ADK construction: real `SKILL.md` files, a small read-only local filesystem adapter, a fork agent hub, scenario config, and runner helpers. `cmd/skill-mode-agent` stays thin: load env and input, choose one scenario mode, install CozeLoop from env, run the ADK runner, and emit a compact root callback node.

The actual skill integration follows CloudWeGo's current API: `skill.NewMiddleware(ctx, &skill.Config{...})` is attached to `adk.ChatModelAgentConfig.Handlers`.

## Trace

Trace is command-boundary only. The root trace input records mode, scenario, and input character count. The output records answer length and whether ADK returned customized data. Full customer messages and skill contents are not copied into the root trace payload.

## Files

- `reasoning/skillmode/skills/*/SKILL.md`: the three real skill definitions.
- `reasoning/skillmode/skills.go`: scenario metadata and backend construction.
- `reasoning/skillmode/local_filesystem.go`: read-only adapter from local files to Eino `filesystem.Backend`.
- `reasoning/skillmode/agent.go`: scenario config, ADK agent construction, runner query, and response summary.
- `reasoning/skillmode/hub.go`: sub-agent hub used by `fork` and `fork_with_context`.
- `reasoning/skillmode/README.md`: source-grounded explanation of the three modes.
- `cmd/skill-mode-agent/main.go`: runnable command.
- `cmd/skill-mode-agent/trace.go`: concise command-level trace.

## Verification

Targeted checks:

- `env GOCACHE=/private/tmp/ai-designing-gocache go test ./reasoning/skillmode ./cmd/skill-mode-agent -count=1`
- `go run ./cmd/skill-mode-agent -prepare-only`
