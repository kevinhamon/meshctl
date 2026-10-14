---
status: accepted
date: 2026-07-16
---
# 0017. Session handoff — an authoritative, resumable "where we are + next step"

- **Status:** accepted
- **Date:** 2026-07-16
- **Deciders:** maintainer
- **Related:** ADR-0016 (engine/contract/surface memory model — this adds a third surface on the same pattern), ADR-0014 (KB State area — handoff is distinct from strategic State), ADR-0015 (memory index — handoff.md is a KB file, so `memory recall` routes to it), ADR-0011 (`kb` write path)

## Context

The two memory tiers cover recall but not **resume**:

- **Intuition (claude-mem)** — fuzzy episodic recall; its SessionStart recap says
  *roughly* what happened, but it is plugin-owned, not authoritative, and not a clean
  "current task + next action."
- **Deliberate (agentmesh KB)** — durable *knowledge* (decisions, playbooks, experience,
  strategic State in `direction.md`/`risks.md`), plus cross-agent `intake/` requests. It
  answers "what do we know / what did we decide," **not "what am I mid-way through and
  what's the immediate next step."**

So nothing is an **authoritative, resumable handoff**. This bites hardest for agents whose
work spans sessions with interruptions — the PM especially: when the user's attention is
pulled to a higher-priority fire, the march toward organizational goals should **resume
exactly**, not be reconstructed from memory or a fuzzy recap.

## Decision

Add a **session handoff** — a per-agent, git-tracked, authoritative record of current
focus + next step — as a third surface on the ADR-0016 engine/contract/surface model.

- **Engine:** a single git-tracked file per agent, `knowledge/handoff.md`. One *current*
  handoff (overwritten); **git history is the log of prior handoffs.** It lives under
  `knowledge/`, so `memory recall` indexes it too.
- **Contract (owned, harness-neutral):** `agentmesh handoff`:
  | Command | Purpose |
  |---|---|
  | `agentmesh handoff show` | Print the current handoff (empty if none) — read at session start. Fail-open. |
  | `agentmesh handoff set` | Write the handoff body from stdin; stamps a timestamp; overwrites current. |
- **Surface (per-harness):** a **SessionStart** adapter injects `handoff show` at session
  start. Claude Code: `hooks/handoff-inject.py` (fail-open). opencode/others: their
  SessionStart equivalent, or `agentmesh handoff show` on demand.

### Ritual
- **Read at start:** the SessionStart surface shows the handoff automatically.
- **Write at end / on attention-shift:** the agent composes the handoff body and
  `agentmesh handoff set` (Learn stage / "checkpoint before I go"). The agent authors it
  (it knows the WIP); the CLI only stamps + persists.
- **Suggested body:** *Current focus/goal · Status (done so far) · Next concrete step (the
  resume point) · Open threads/blockers · Pointers (KB/intake/tracker refs).* Keep it a
  handoff, not a journal.

### What handoff is NOT (boundaries)
- Not strategic **State** (`direction.md`/`risks.md`) — that's the durable north star; the
  handoff is the volatile "current position toward it."
- Not **intake** — that's cross-agent requests; the handoff is one agent's own WIP.
- Not **decisions/experience/playbooks** — durable knowledge; the handoff is transient
  position, expected to churn every session.
- Not **memory/intuition** — those *recall*; the handoff *resumes*.

## Consequences

- **Positive:** precise cross-session continuity; an interrupted agent (esp. the PM)
  resumes marching toward org goals from the exact next step, not a reconstruction;
  authoritative + git-tracked (history + review) — unlike the fuzzy plugin recap; reuses
  the engine/contract/surface pattern (one more thin per-harness adapter); indexed by
  `memory recall` for free.
- **Negative / risks:**
  - **Stale handoff** if not rewritten at end. Mitigations: SessionStart shows the
    `updated` timestamp; the ritual says rewrite-before-leaving; a future Stop-hook
    *reminder* (not auto-write — authoring the WIP is model work) and a `doctor` staleness
    warn can nudge.
  - **Discipline** — value depends on the write actually happening. It's part of the Learn
    stage; cheap (`handoff set` from stdin).
- **Migration / sequence:**
  1. `handoff.go` + `agentmesh handoff show|set` — **done.**
  2. Claude Code SessionStart surface hook + wire into settings — **done.**
  3. Document the ritual in `agent-comms.md` — **done.**
  4. Seed handoffs for the agents that most need it (pm, architect).
  5. Later (optional): `doctor` staleness warn; a Stop-hook rewrite *reminder*; opencode
     SessionStart adapter.
- **Follow-ups:** none blocking. If handoffs prove they want structure, a light schema
  (fields) can be added behind the same `handoff set` — no surface change.
