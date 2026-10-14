---
status: accepted
date: 2026-07-25
---
# 0042. Tool reference — a capability-awareness layer (pool + agent `tools:`)

- **Status:** accepted — **implemented** (pool.yaml + agent.yaml `tools:`; session-start injection; doctor PATH/avoid check)
- **Date:** 2026-07-25
- **Deciders:** maintainer
- **Related:** complements ADR-0014 (KB) + the skills layer (ADR-0026); a third, lighter tier. Injected via the SessionStart surface (ADR-0030/0039).

## Context

On a machine running several meshes, different meshes should use different CLI tools —
one prefers `tea` (Gitea CLI) over `gh`; another has bespoke commands its agents should
reach for. This is real, per-mesh guidance, but it doesn't warrant **skills** (there's no
procedure — a tool's `--help` already carries the detail) and it isn't durable **KB**.
Putting it in each `CLAUDE.md` as prose works but doesn't inherit across a mesh (copy-paste
drift) and isn't validated.

The missing tier: **capability awareness** — "what's in my toolbox + the house rules,"
declared once, injected, self-documenting via `--help`.

## Decision

Add a **tool reference**: a list of `{ cmd, use, avoid, help }` pointers.

- **Where:** mesh-wide on `.agentmesh/pool.yaml` (`tools:`), and/or per-agent on
  `agent.yaml` (`tools:`). Effective set = pool ∪ agent, with agent entries overriding the
  pool's by `cmd` (agent wins). Mesh-wide "use `tea` not `gh`" is written **once** on the
  pool and inherited by every agent.
- **Pointer, not procedure:** `use` = one-line purpose; `avoid` = "don't use this here
  (why)"; `help` = the self-serve help invocation (default `<cmd> --help`). meshctl
  **never fetches or embeds** the help text — the agent runs it.
- **Injected at session start** (stable context, like the handoff) as a compact
  "## Tools for this mesh — self-serve `--help`" block.
- **`meshctl doctor` validates:** warns if a referenced `cmd` isn't on `$PATH` (install it
  / fix the ref), and nudges if an `avoid` tool *is* present.
- Discoverability: `pool init` writes a commented `tools:` example into `pool.yaml`.

## Consequences

- Positive: mesh-scoped tool policy is declared once and inherited; agents always know
  their toolbox + house rules without a skill or `CLAUDE.md` prose duplication. Fits the
  user's multi-mesh, multi-toolchain machine (tea-meshes vs gh-meshes, bespoke commands).
- Positive: pointer-only keeps it tiny and never stale — the source of truth is the tool's
  own `--help`.
- Positive: `doctor` catches "this mesh expects `tea` but it's not installed" and the
  inverse (an `avoid` tool present).
- Clean tier separation: **skills = how to do a thing · KB = what I know · tool reference
  = what's in my toolbox + house rules.**
- Trade-off: no writer command yet — `tools:` is hand-edited in `pool.yaml`/`agent.yaml`
  (matches its low-churn, reference nature). A `meshctl tools add` could follow if it
  proves tedious.
- **Steward integration:** the `mesh-steward` skill now interviews for tools/house rules
  and writes the tool reference into `pool.yaml`/`agent.yaml` on the user's behalf — so the
  user just says "use tea not gh" and the steward records it.
- **Skill propagation fix (spun out of this):** `writeSkillFile` now OVERWRITES a mesh-owned
  skill whose on-disk copy differs from the embedded template (was add-only), and
  `scaffoldSkills`/`onboard` also refreshes `mesh-steward` when the agent already has it.
  So skill updates (this one, and future ones) reach existing agents via
  `meshctl agent onboard`, not only new scaffolds — the same gap the opencode-plugin fix
  (ADR-0032) closed for plugins.
