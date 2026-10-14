---
status: accepted
date: 2026-07-15
---
# 0010. Agent-mesh: `agentmesh` Go CLI, self-describing manifests, batch dispatch

- **Status:** accepted
- **Date:** 2026-07-15
- **Deciders:** maintainer
- **Related:** an earlier decision standardizing on Go; agent-mesh repo (`~/agents/agent-mesh`); `agent-comms.md` protocol; supersedes the Taskfile `task new` scaffolder and folds in `bin/ask-agent`

## Context

The mesh is a set of independent agent workspaces under `~/agents/*` coordinating by file-based handoff (`agent-comms.md`). Three frictions have surfaced now that the roster is growing (architect, pm, developer, a QA agent, + domain experts):

1. **Permission fatigue.** Every cross-agent message is two prompted actions — a `Write` into the peer's `intake/` (arbitrary path, always prompts) and a `Bash(ask-agent …)` dispatch. There is no single action to pre-approve.
2. **Discovery by duplication.** "Who is in the mesh and what do they accept" lives in a hand-maintained registry table in `agent-comms.md` **and** is restated across each agent's `CLAUDE.md`. Onboarding the QA agent meant editing the shared registry by hand and hand-writing its files — O(N) edits per new agent, and the registry drifts from reality.
3. **Wrong transport instinct.** An MCP server / local RPC / database was considered for messaging. But the value model is **ephemeral**: spin an agent up in a clean context, analyze, answer, spin down. MCP is a persistent, in-session, interactive server — the opposite shape. A standing daemon + schema is infrastructure with no state to justify it.

Tooling today is bash (`bin/ask-agent`, `bin/*.sh`) + a Taskfile scaffolder. Bash has already cost us a portability bug (macOS bash 3.2 `set -u` empty-array crash in `ask-agent`). The house language is Go (standardized by an earlier decision).

## Options considered

**Transport / messaging**
1. **MCP server / local RPC daemon** — interactive, persistent, in-session. Wrong shape for cold-start→answer→exit; a process to babysit. Rejected.
2. **Database-backed message bus** — buys queries/status/notify, but for a handful of agents at low volume it loses the file model's wins (human-readable, git-tracked, greppable, no daemon). Premature. Rejected.
3. **Batch dispatch over files (chosen)** — keep files as the durable audit log; a peer is launched headlessly, writes its answer back, exits. This is what `bin/ask-agent` already does — the ephemeral model was already correct.

**Discovery**
1. **Keep the hand-maintained registry table + CLAUDE.md duplication** — status quo; O(N) drift. Rejected.
2. **Per-agent manifest as single source of truth (chosen)** — each agent ships an `agent.yaml` describing itself; the directory is **computed on demand** by scanning `~/agents/*/agent.yaml`. Onboarding a specialist = drop one file, zero edits to peers (O(1)). No stale committed roster.

**Tooling**
1. **Keep bash scripts + Taskfile** — portability landmines, untyped, no manifest parsing. Rejected as the primary surface.
2. **Single Go CLI `agentmesh` on `$PATH` (chosen)** — aligns with the Go standardization, typed manifest parsing, `go:embed` templates, one binary to scaffold the mesh, scaffold/onboard agents, dispatch, and emit the directory on demand. One preapprovable tool: `Bash(agentmesh:*)`.

## Decision

Build a **single Go CLI, `agentmesh`**, installed on `$PATH`, as the mesh's tool of record. It replaces the Taskfile scaffolder and absorbs `bin/ask-agent`'s dispatch + anti-cascade guards. It is the access path for messaging **and** discovery.

**Ephemeral batch dispatch, not RPC/DB/MCP.** Messaging stays file-based. `agentmesh ask` collapses the two prompted actions into one preapprovable command: it writes a validated intake file (audit trail), dispatches the peer headlessly in a clean context, blocks for the answer, returns it, peer exits. Sync by default; `--detach` for long jobs that shouldn't block (write-only, poll later). Approve `Bash(agentmesh:*)` **once**.

**Self-describing manifests, on-demand directory.** Each agent owns an `agent.yaml` (identity + capabilities). The roster is never a committed file — `agentmesh directory` scans manifests live and emits it (table or JSON) when required. `CLAUDE.md` stops listing peers; it keeps only its own behavior and points at its manifest. `agent-comms.md` keeps the protocol **rules** (prose), not the roster table.

**Capability data drives safety mechanically.** `dispatchable`/`mutates` in the manifest let `agentmesh ask` *refuse* to headlessly dispatch a state-mutating agent (a pm agent→the issue tracker, developer→code) as data, not as prose convention.

### `agent.yaml` schema

```yaml
# ~/agents/<name>/agent.yaml — the agent's authoritative self-description.
name: architect                       # dir name; unique mesh id
title: Software Architect
role: >                               # one paragraph: what it is for
  Architecture of record for ~/projects — owns ADRs, KB, and the
  risk register.
inbox: intake/                        # relative to the agent dir
owns: [architecture, adrs, kb, risk-register]
domains: [frontend, data-pipelines, data-architecture]   # capability tags → routing
accepts:                              # request types this inbox handles
  - { type: guidance,         desc: architectural guidance on an approach }
  - { type: review,           desc: review a design/diff for architectural fit }
  - { type: decision,         desc: make/confirm a consequential call (ADR-backed) }
  - { type: story-enrichment, desc: add technical approach/AC to a tracker story }
  - { type: fyi,              desc: awareness only, no action }
dispatchable: true                    # may `agentmesh ask` run it headlessly?
mutates: []                           # external systems it writes: [tracker]|[code,prod]|…
                                      #   non-empty ⇒ dispatchable must be false
badge: { label: ARCHITECT, r: 0.0, g: 0.3, b: 0.8 }   # iTerm2 identity
model: default                        # or a pin, e.g. claude-haiku-4-5
repo: git@example.com:owner/architect.git
```

Rule enforced by the CLI: `mutates` non-empty ⇒ `dispatchable: false`. Advisory agents (architect, domain experts) are dispatchable; state-mutating agents (pm, developer) are messaged but human-triggered.

### Command surface

| Command | Purpose |
|---|---|
| `agentmesh init` | Scaffold/repair the mesh kit itself on a fresh machine (idempotent). |
| `agentmesh new <name>` | Scaffold a **new** agent workspace (manifest + intake + iTerm + CLAUDE.md skeleton). Replaces `task new`. |
| `agentmesh onboard <name\|path>` | **Upgrade a pre-existing agent** into the mesh: add `agent.yaml` (inferred from CLAUDE.md where possible, prompt for the rest), create `intake/`, inject the intake section into its CLAUDE.md if absent. Idempotent — safe to re-run. |
| `agentmesh directory [--md\|--json]` | Emit the roster **on demand** from a live scan of `~/agents/*/agent.yaml`. The source of truth for discovery. |
| `agentmesh ask <target> <type> "<prompt>" [--detach]` | Batch dispatch: write validated intake file → dispatch peer headlessly → block → return answer. Refuses non-`dispatchable` targets. Carries the anti-cascade guards. |
| `agentmesh send <target> <type> "<prompt>"` | Write-only intake (no dispatch) — for state-mutating peers that stay human-triggered. |
| `agentmesh doctor` | Validate every manifest; report drift (missing/invalid `agent.yaml`, `mutates`/`dispatchable` conflicts, CLAUDE.md without intake section). |

Anti-cascade guards from `bin/ask-agent` are ported **faithfully** into `agentmesh ask`: depth limit (a dispatched agent may not dispatch), single-flight `mkdir` lock, rolling hourly cap, per-dispatch timeout, dispatch log. These are budget/safety-critical, not optional.

## Consequences

- **Positive:** one preapprovable tool kills permission fatigue; discovery is O(1) to extend and can't go stale (computed, not committed); Go removes bash portability bugs and gives typed manifests + embedded templates; safety rules (advisory-only dispatch) become enforced data, not prose.
- **Negative / work:** build the CLI; migrate each existing agent to an `agent.yaml` (via `agentmesh onboard`); convert the `agent-comms.md` registry table into generated output and trim peer lists from CLAUDE.md files; keep `bin/ask-agent` working until `agentmesh ask` reaches parity, then deprecate.
- **Scope kept out:** `clone-missing.sh`, `fork-migrate.sh`, `prune-archived.sh` stay bash — they are run-outside-Claude estate ops, orthogonal to the mesh runtime.
- **Open sub-decisions (resolve during build, not blocking):**
  1. Does `agent-comms.md` embed a CLI-generated roster block (between markers) for GitHub browsing, or do we rely purely on `agentmesh directory`? Lean: rely on the command; optional `--write` snapshot for humans, always marked "generated — do not edit."
  2. Distribution: `go install` from the agent-mesh repo vs a committed built binary. Lean: `go install`, with a `task build` convenience.
- **Follow-ups:** build spec lives at `~/agents/agent-mesh/MESH-CLI-SPEC.md` (point a fresh Claude at it). After parity, update `agent-comms.md` (§ synchronous unblock → `agentmesh ask`) and the README.
