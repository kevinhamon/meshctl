---
status: accepted
date: 2026-07-24
---
# 0031. Pools are created by the binary, not by cloning a doctrine repo

- **Status:** accepted — **implemented** (`agentmesh init [path]` / `--upgrade`; `.agentmesh/pool.yaml` marker; `AgentsDir` detects it with legacy back-compat; `~/agents` migrated)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** supersedes the *creation mechanism* of ADR-0029 (multi-pool); builds on ADR-0010 (agentmesh CLI + embedded templates), ADR-0030 (self-contained hooks in the binary). The multi-pool *isolation model* (cwd-ancestor resolution, `pool.local.yaml` identity) from ADR-0029 stands.

## Context

ADR-0029 bootstrapped a new pool by **cloning the mesh doctrine repo** (`agent-mesh`) into the pool root; `AgentsDir()` then keyed pool detection off the marker file `agent-mesh/agent-comms.md`. That choice was correct *at the time* — the binary didn't yet carry the doctrine or the scaffolding.

It no longer holds. Since ADR-0010/0030 the binary already `go:embed`s the templates, skills, and (now) the hooks, and is the self-contained tool of record. Requiring a git clone to stand up a pool adds a network dependency, a remote to configure, and a git repo to babysit and `git pull` per pool — for content the binary already contains. The user's target workflow is **"init a mesh with the CLI, then join agents to it"** — not "clone a repo first."

## Options considered

1. **Keep ADR-0029 clone-doctrine.** No change; retains the network/remote/per-pool-repo friction and the "which clone is canonical" drift surface.
2. **Binary-native init: `agentmesh init <path>` emits the pool from embed; agents join via `new`/`onboard`. (chosen).** No clone, no remote. The binary is the single source of truth; drift collapses to "which binary version," resolved by `go install` + `agentmesh init --upgrade` re-emitting docs.

ADR-0029 rejected embed (its option 2) specifically over drift ("embedded copies drift from the canonical repo"). That objection **inverts** now: with the binary as the tool of record, one binary = one source. Versioning the binary is a cleaner drift story than N git clones each needing a pull.

## Decision

`agentmesh init <path>` bootstraps an isolated pool **from the binary**, no git clone:

1. Creates the pool root + runtime dirs (`.worktrees`, dispatch artifacts) — the `InitMesh` behavior.
2. **Emits the doctrine docs from embed** to the pool (`agent-comms.md`, `distiller.md`, and any doctrine agents read by path), sourced from the binary — not cloned. `agentmesh init --upgrade` re-emits them from a newer binary (idempotent; overwrites only mesh-owned doctrine, never agent content).
3. Writes a binary-managed **pool marker** (`.agentmesh/pool.yaml`) holding pool identity (name, created, doctrine version). This replaces `agent-mesh/agent-comms.md` as the detection marker.
4. `agentmesh new <agent>` / `agentmesh onboard <path>` join agents to the pool (unchanged, already binary-driven).

**Pool detection changes:** `AgentsDir()` resolves the pool root by walking cwd ancestors for the **`.agentmesh/` marker dir** (was `agent-mesh/agent-comms.md`), else `$AGENTS_DIR`, else `~/agents`. Marker precedence is documented so a stray `agent-mesh/` clone doesn't shadow a real pool. The original pool at `~/agents` is migrated by writing its `.agentmesh/pool.yaml` (one-time; the old `agent-mesh/` repo may remain as the *build source* of the binary but stops being the pool marker).

**The `agent-mesh` git repo keeps one job:** it is the **source that builds the binary** (doctrine docs, templates, Go). It is no longer a runtime artifact every pool must clone. Editing the protocol = edit the repo, rebuild/`go install`, `init --upgrade` pools.

## Consequences

- Positive: `agentmesh init ~/academic && cd ~/academic && agentmesh new <agent>` stands up an isolated pool with zero network, zero clone, zero remote config. Matches the intended "init then join" workflow.
- Positive: single source of truth (the binary); drift managed by version, not by per-pool `git pull`.
- Positive: pools carry no `.git` doctrine repo — lighter; the marker is a small managed file.
- Trade-off: doctrine docs now ship *inside* the binary — editing the protocol requires a rebuild/`go install`, not a `git pull`. Acceptable: the protocol changes rarely and the agent-mesh repo remains its authoring home.
- Trade-off: a marker migration is needed for the existing `~/agents` pool and the detection change touches `AgentsDir()`; guard marker precedence so old `agent-mesh/`-based detection doesn't double-resolve during transition.
- Migration: (1) change the marker + `AgentsDir()` resolution with back-compat for the old marker during transition; (2) implement `init` emit-from-embed + `--upgrade`; (3) write `.agentmesh/pool.yaml` into `~/agents`; (4) drop the `git clone` path from `InitPool`; (5) update docs (README, MESH-CLI-SPEC, ADR-0029 cross-link).
