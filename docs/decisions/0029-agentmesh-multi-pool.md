---
status: accepted
date: 2026-07-21
---
# 0029. Multiple isolated mesh pools; `agentmesh init --pool`

- **Status:** accepted
- **Date:** 2026-07-21
- **Deciders:** maintainer
- **Related:** ADR-0010 (agentmesh CLI + manifests), ADR-0011 (concurrency/claims), ADR-0026/0027 (cognition/distiller); enables additional, independent meshes (e.g. `~/academic`, `~/sourceatlas`)
- **Update (2026-07-24):** the pool **creation mechanism** here (clone the doctrine repo) is **superseded by ADR-0031** (binary-native `agentmesh init` emits from embed; marker → `.agentmesh/pool.yaml`). The **isolation model** in this ADR (cwd-ancestor resolution, distinct-names caveat, `pool.local.yaml` identity) still stands.

## Context

The mesh was built as a single container at `~/agents`. The user wants **multiple independent pools** — one per project domain (`~/academic`, `~/sourceatlas`, …) — with a hard guarantee of **no cross-pool communication** (an academic agent must never see or message an agent in the original work pool).

Investigation showed the isolation primitive already exists: `AgentsDir()` resolves the pool root by (1) `$AGENTS_DIR`, else (2) the nearest cwd-ancestor containing `agent-mesh/agent-comms.md`, else (3) `~/agents`. Every comms/roster/KB operation (`directory`/`send`/`ask`/`kb`/`memory`) enumerates only that root. Proven: an agent run inside a second pool sees only its own pool's roster — the other pool's agents are structurally absent. What was missing was a clean way to *create* a pool (previously hand-copying scaffolding) and a name to disambiguate the episodic layer.

## Options considered

1. **Document a manual recipe only** (clone agent-mesh into a new root, `init`, `new`). Zero code, but pool creation is undiscoverable and error-prone.
2. **Embed the doctrine in the binary**; `init --pool` writes it out. Offline/self-contained, but embedded copies drift from the canonical repo (the CLAUDE.md-template drift class).
3. **`init --pool` clones the doctrine repo** into the new root. Consistent with the git-native mesh, doctrine stays canonical in one repo, each pool updates via `git pull`, no copy-drift. Needs network + a remote. Chosen.

## Decision

Pools are first-class and isolated by the existing cwd-ancestor resolution; add `agentmesh init --pool <path>` to bootstrap one:

- Clones the mesh **doctrine** repo into `<poolRoot>/agent-mesh` (the `agent-comms.md` marker + `distiller.md` + skill templates). Remote resolves flag → `$AGENT_MESH_REMOTE` → **the current pool's `agent-mesh` origin** (a new pool inherits the same doctrine source you run).
- Creates runtime dirs (`InitMesh`) and writes a **local, untracked** `pool.local.yaml` identity (each pool clones the *same* repo, so identity must never be committed).
- Bare `agentmesh init` is unchanged (repair runtime in the current pool).

**Isolation is structural, not a setting to remember:** the roster only ever contains the pool root's agents, so cross-pool `send`/`ask` is impossible.

## Consequences

- Positive: `agentmesh init --pool ~/academic && cd ~/academic && agentmesh new <agent>` stands up a fully isolated mesh; the shared `agentmesh` binary re-scopes per invocation.
- Positive: doctrine is single-source (one repo, cloned per pool, `git pull` to update) — no embed-drift.
- **Leak vectors to avoid (documented for operators):**
  1. **Do not set `$AGENTS_DIR` globally** — it pins every invocation to one pool regardless of cwd. Leave unset (cwd-walk isolates) or set per-terminal.
  2. **Use distinct agent names across pools.** Git KB + comms are path-isolated, but the claude-mem / `intuition recall` episodic layer keys by bare agent name, so two pools each with a "developer" can bleed episodic signal. `pool.local.yaml` exists to fix this — a follow-up will prefix the episodic project key with the pool name.
  3. Global SessionStart/UserPromptSubmit hooks are pool-safe (they invoke `agentmesh` from the session cwd), but the distill-nudge allowlist is basename-based (same-name caveat).
- Negative / risk: `init --pool` needs network + a valid remote; a wrong remote is caught (missing `agent-comms.md` after clone → error).
- Follow-ups: wire `pool.local.yaml` name into episodic namespacing (closes leak vector #2); optional per-pool distill-nudge allowlist keyed by pool+agent; an `agentmesh pools` lister.
