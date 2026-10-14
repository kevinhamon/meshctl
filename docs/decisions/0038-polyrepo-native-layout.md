---
status: accepted
date: 2026-07-24
---
# 0038. Poly-repo-native layout: infra under `.agentmesh/`, agents as `*-agent` repos

- **Status:** accepted — **implemented** (consolidated `.agentmesh/`; `-agent` naming; `pool migrate`; `pool teardown`; doctor legacy detection)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** refines ADR-0031 (pool marker/doctrine) + ADR-0037 (registry + init-cwd) + ADR-0035 (steward). Enables meshes to live inside a poly-repo.

## Context

A pool scattered four entries at its root — `.agentmesh/` (marker), `agent-mesh/`
(doctrine), `.worktrees/`, `.dispatch.*` — plus agent subdirs. Fine for a dedicated
`~/agents` container, but messy when the pool root is a **poly-repo** (a dir holding many
product repos): mesh infra clutters the root, and it was unclear whether agents should be
nested under an `agents/` subdir or be first-class repos beside the products.

Decision (recommended + accepted): **agents are first-class repos in the poly-repo.**
Nesting under `agents/` fights the poly-repo grain; an agent *is* just another repo.

## Decision

1. **Consolidate all pool infra under a single `.agentmesh/`** at the pool root:
   `.agentmesh/{pool.yaml, doctrine/, worktrees/, dispatch.*}`. A fresh pool's footprint
   beside the product repos is exactly **one hidden dir**. (Constants relocated; call
   sites unchanged.)
2. **Agents are first-class sibling repos, flagged `<name>-agent`.** `meshctl agent new`
   appends the `-agent` suffix by default (`--bare` opts out; no double-suffix). Discovery
   is unchanged and authoritative on **`agent.yaml`** — a dir is an agent iff it has one,
   so product repos (no manifest) are ignored automatically; the `-agent` name is a human
   flag on top. `dir == manifest name` still holds (no dir≠name decouple), so `Validate`
   and `FindAgent` are untouched. Steward repo → `steward-agent`.
3. **`meshctl pool migrate`** reconciles a legacy (pre-0038) pool to the new layout: emit
   doctrine under `.agentmesh/doctrine`, move `.worktrees`/`.dispatch.*` under
   `.agentmesh/`, and remove the legacy top-level `agent-mesh/` — **but never if it is a
   git repo** (protects a pool where `agent-mesh/` is the real meshctl clone, e.g. the
   original `~/agents` pool). `meshctl doctor` detects the legacy layout and nudges `pool migrate`.
4. **`meshctl pool teardown`** removes `.agentmesh/` + deregisters (registry, ADR-0037),
   **leaving agent repos in place** (they hold committed knowledge). `--purge` also
   deletes agent workspaces — destructive, explicit-flag-guarded, warns about unpushed
   knowledge.
5. Doctrine path references in the CLAUDE.md stanzas/templates updated to the pool-relative
   `.agentmesh/doctrine/agent-comms.md`.

## Consequences

- Positive: a poly-repo gets `.agentmesh/` + `*-agent` repos beside its product repos — no
  clutter, agents are peers, discovery ignores non-agent repos automatically.
- Positive: durable knowledge safety is unchanged — it lives in each `*-agent` repo's
  `knowledge/`, committed + pushed to that repo's remote (the pool container needs no
  backup; ADR-0037 registry is regenerable).
- Positive: `pool migrate` makes the transition safe + non-destructive, protecting a
  git-repo `agent-mesh/`; `teardown` gives a clean removal path.
- Trade-off: agent NAMES now carry `-agent` (e.g. `msg ask architect-agent`) for new
  agents — verbose but unambiguous; `--bare` for the dedicated-container style. Existing
  bare-named agents (the original `~/agents` pool) are unaffected.
- Migration: legacy pools warn under `doctor` until `pool migrate` is run. The original
  `~/agents` pool keeps its real `agent-mesh/` git repo; migrate only moves runtime +
  emits doctrine.
