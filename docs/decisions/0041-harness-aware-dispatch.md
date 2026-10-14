---
status: accepted
date: 2026-07-25
---
# 0041. Headless dispatch (`meshctl ask`) picks the runner by the target's harness

- **Status:** accepted — **implemented** (`realDispatch` branches claude/opencode; argv unit-tested; live E2E pending)
- **Date:** 2026-07-25
- **Deciders:** maintainer
- **Related:** fixes an ADR-0010 (dispatch) assumption under ADR-0032 (multi-harness). Found in use: an opencode agent's `meshctl ask` timed out.

## Context

`meshctl ask` dispatches the target peer headlessly to process one intake request.
`realDispatch` was **hardcoded to Claude Code** (`claude -p …`). In an opencode mesh, an
agent's `ask` therefore launched *Claude Code* against an opencode-configured target —
wrong runtime, no proper model/wiring — which hung and hit the 420s timeout, leaving the
intake request `pending` with an empty response. (The agent worked around it by using
async `send` instead — no data lost; the audit trail, `.agentmesh/dispatch.log` + the
target's `intake/*.md` status, made the failure diagnosable after the fact.)

Dispatch was harness-blind on the runner side while the rest of the mesh (hooks, skills,
wiring) became harness-aware in ADR-0032.

## Decision

`realDispatch` chooses the runtime from the **target agent's manifest harness**:

- **claude:** `claude -p <msg> --permission-mode acceptEdits --allowedTools Read Grep Glob
  Edit Write --append-system-prompt <sys>` (unchanged).
- **opencode:** `opencode run --pure --auto [--model provider/model] "<sys>\n\n<msg>"`, run
  in the target dir.
  - `--pure` = no external plugins → the dispatched run doesn't fire the mesh hook plugin
    (no injection/`session.idle` capture recursion mid-dispatch); the agent's
    `instructions` (CLAUDE.md) still load.
  - `--auto` = auto-approve non-denied permissions (headless; the scoped "do only this,
    touch no external systems" system prompt constrains behavior — opencode `run` has no
    granular allowlist like Claude's).
  - No `--append-system-prompt` in opencode, so the headless system prompt is **prepended**
    to the message.
  - `--model` only when the manifest model is `provider/model`-shaped (a bare gateway alias
    is left to opencode's own default).
  - opencode `run` takes a **random port** per invocation → dispatch never collides with a
    live TUI's server.

The anti-cascade guards (depth / hourly-cap / single-flight lock / timeout), the intake
write, the claim, and the headless system-prompt contract are unchanged. Argv construction
is factored into `dispatchArgv(harness,…)` and unit-tested per harness.

## Consequences

- Positive: `ask` (sync dispatch) works in an opencode mesh, not just Claude Code — the
  advisory-dispatch flow (architect/domain experts answering headlessly) is available on
  either harness.
- Positive: `--pure` avoids the dispatched run re-triggering the mesh plugin (cleaner,
  avoids nested capture/injection).
- Trade-off: opencode `--auto` is coarser than Claude's `--allowedTools` allowlist (approve
  vs. scoped tools). The system-prompt contract + `dispatchable:false`-for-mutators
  invariant remain the guardrails; a mutating agent is never dispatched.
- Untested: type-correct + argv-unit-tested, but a live opencode dispatch (real model turn
  writing back to the intake file) hasn't been exercised end-to-end — validate on next use.
- Recovery for the timed-out request that surfaced this: it's still `pending` in
  `reviewer-agent/intake/` — open that agent and "check intake" to process it.
