# Plan first, then delegate to the cheapest capable model

Goal: save API credits by doing the expensive reasoning once, up front, and handing
well-defined pieces of work to cheaper models.

## 1. Plan before doing

For any task bigger than a quick one-file change:

1. Read only what you need to understand the task.
2. Write a short plan: a numbered list of subtasks, each with
   - what it changes (files/functions),
   - what "done" looks like (tests pass, builds, output matches),
   - the model tier it needs (see below).
3. Note which subtasks are independent so they can run in parallel.

Skip the plan for trivial tasks (typo fixes, one-line edits, answering a question).
Planning a two-minute task costs more than it saves.

## 2. Pick the cheapest model that can do each subtask

Use the Agent tool's `model` parameter when delegating:

| Tier | Use for |
|------|---------|
| `haiku` | Mechanical work: searching/locating code, renames, boilerplate, formatting, simple tests, summarizing files or logs, running commands and reporting results |
| `sonnet` | Normal implementation: writing a function or handler from a clear spec, adding tests, routine bug fixes, small refactors |
| `opus` (main session) | Planning, architecture, concurrency/queue-correctness logic, tricky debugging, reviewing subagent output |

When unsure between two tiers, try the cheaper one first and escalate only if the
result is wrong or incomplete.

## 3. Write self-contained subagent prompts

Subagents start with no context. Every delegated prompt must include:

- the exact goal and the files involved,
- relevant constraints and conventions (Go version, package layout, error style),
- the acceptance check (e.g. `go test ./...` passes, `go vet` clean),
- what to report back (a short summary and the diff or file list, not full file dumps).

A vague prompt to a cheap model that has to be redone costs more than doing it right once.

## 4. When NOT to delegate

Do it inline instead of spawning an agent when:

- the subtask takes only a few tool calls; spawning overhead outweighs the savings,
- it needs most of the context you already have loaded,
- subtasks are tightly coupled and must be designed together.

## 5. Verify and integrate

- Review each subagent's result before building on it. Do not assume it worked.
- Run the build/tests after integrating (`go build ./... && go test ./...`).
- If a cheaper model failed twice on the same subtask, take it over or bump the tier.
