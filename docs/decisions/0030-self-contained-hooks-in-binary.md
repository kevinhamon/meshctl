---
status: accepted
date: 2026-07-24
---
# 0030. Claude Code hooks fold into the `agentmesh` binary; wired per-agent

- **Status:** accepted
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** ADR-0010 (agentmesh CLI + manifests), ADR-0016 (intuition tier), ADR-0017 (session handoff), ADR-0027 (background distiller + boundary nudge), ADR-0029 (multi-pool). Supersedes the Claude Code *surface* of ADR-0016/0017/0027 (the python hook scripts), not their contracts.

## Context

The mesh's three Claude Code hooks — handoff injection (SessionStart), intuition
injection + distillation boundary nudge (UserPromptSubmit) — shipped as loose
`python3` scripts under `agent-mesh/hooks/*.py`, wired by **absolute path in the
machine-global `~/.claude/settings.json`**. Each was a thin JSON-in/JSON-out
wrapper that shelled back to `agentmesh` (handoff show, intuition recall) plus a
small stateful counter (the nudge).

This broke the "self-contained CLI" goal (MESH-CLI-SPEC): the mesh needed a
python3 runtime it doesn't otherwise use, a machine-specific hardcoded path, and
a hand edit of a global file the binary couldn't manage. It also worked against
ADR-0029 multi-pool: global hooks fire for every pool from one path, so a second
pool's clone couldn't carry its own wiring.

## Options considered

1. **Keep python, point global hooks at `agentmesh` subcommands.** Smallest
   change, but still python-dependent, still global, still not workspace-portable.
2. **Fold the hook logic into the Go binary as `agentmesh hook …` subcommands
   and wire them per-agent in each `<agent>/.claude/settings.json`.** No python,
   no loose files, no global edit; the wiring travels with each workspace repo
   and each pool. Chosen.

## Decision

- **`agentmesh hook session-start|user-prompt|session-end`** — each reads the
  Claude Code hook JSON on stdin and prints `hookSpecificOutput` on stdout. All
  fail-open (any error ⇒ no output, exit 0; a hook must never break a turn).
  `session-start` also **registers session presence** (ADR-0011 substrate) and
  `session-end` deregisters it, folding the spec's "session-start presence hook"
  in with handoff injection. The agent is resolved from the payload `cwd`
  basename.
- **`agentmesh new`/`onboard` scaffold `<agent>/.claude/settings.json`** via
  `EnsureAgentSettings`: wire the three hooks (command `agentmesh hook …`,
  resolved on `$PATH` — no absolute path) plus a `Bash(agentmesh:*)` permission
  so mesh messaging/cognition is preapproved (MESH-CLI-SPEC migration step 5,
  previously unimplemented). Add-only and idempotent: it inserts only what is
  missing, never rewrites or clobbers existing keys, and refuses to touch an
  unparseable file.
- **Global `~/.claude/settings.json` loses the three mesh python-hook entries;**
  the `.py` files are deleted. The caveman/claude-mem global hooks are untouched.
- All nine existing agents were re-onboarded to gain the wiring.

## Consequences

- Positive: the mesh is genuinely self-contained — one Go binary on `$PATH`
  carries its own Claude Code integration; no python3, no loose scripts, no
  machine-absolute paths.
- Positive: hook wiring is **per-workspace and committed**, so it travels with
  the repo and is correct in every pool (tightens ADR-0029 — wiring is no longer
  a single global path shared across pools).
- Positive: `Bash(agentmesh:*)` preapproval closes the last open MESH-CLI-SPEC
  migration step.
- Trade-off: the hook JSON/output shape now lives in Go (`internal/mesh/hook.go`)
  instead of a per-harness script; a non-Claude-Code harness needs its own thin
  surface calling the same neutral subcommands (the ADR-0016/0017 contracts are
  unchanged, so this is a surface port, not a redesign).
- Trade-off: existing agents required a one-time re-onboard; a future agent gets
  the wiring automatically from `new`/`onboard`.
- Migration nuance: the distill-nudge allowlist stays basename-based
  (`$AGENT_MESH_DISTILL_NUDGE_AGENTS`, default `pm`) — same caveat noted in
  ADR-0029 leak-vector #3, now local to each workspace's settings rather than
  global.
