---
status: accepted
date: 2026-07-21
---
# 0027. Background distiller — off-thread learning, surface only decisions

- **Status:** accepted
- **Date:** 2026-07-21
- **Deciders:** maintainer
- **Related:** ADR-0016 (distillation ritual + its reserved future `memory distill`), ADR-0026 (cognition skill + standing order); piloted on a tracker-owning pm agent

## Context

ADR-0026 gave every agent an always-present standing order to distill learnings, but the ritual still ran **inline on the main thread** — slow, and it made the user feel like they had to prompt each step (mine → draft → consolidate → handoff). The desired experience: agents **learn on their own**, the slow work happens **in the background**, and the live session sees **only the learnings that need a human decision** — nothing else.

Two mechanism families were weighed. A **detached OS process** (SessionEnd hook / cron running headless `claude -p`) runs with no live session but can only surface results at the *next* session start — wrong for "surface into my current session." A **background subagent** (the parallel-agent pattern: Agent tool `run_in_background`) runs off the main thread *during* a live session and reports back into it — matching the goal. The user also flagged portability: the mesh may move to opencode, whose background primitive differs, so nothing Claude-Code-specific should live in the core.

## Options considered

1. **Inline distillation (status quo, ADR-0026).** Simple, but slow and main-thread-blocking; feels like manual prompting.
2. **Detached process (SessionEnd hook + cron).** Fully off-session, but surfaces only next start; hook/cron plumbing is Claude-Code-specific and won't port.
3. **Background subagent, agent-self-fired, with a harness-neutral core.** Off-thread + surfaces live; core procedure is harness-agnostic with a thin per-harness binding. Chosen.

## Decision

Distillation runs as a **background distiller**, split into a neutral core + a per-harness binding:

- **Neutral core — `~/agents/agent-mesh/distiller.md`.** The "what": inputs, steps (mine → reconcile → draft → triage), the triage rubric (**AUTO** = concrete low-risk fact → candidate written silently; **DECISION** = consequential/ambiguous/conflicting → surfaced, *default when unsure*; **NOISE** = dropped), the output contract, and guardrails. Harness-agnostic.
- **Claude Code binding — a `distiller` subagent** (`.claude/agents/distiller.md`), launched with `run_in_background: true` at a work boundary. opencode (or any harness) binds the same core to its own background primitive.
- **Trigger = agent self-fire.** The owning agent, per its cognition standing order, spawns the distiller at a work boundary / wind-down. No hooks, no cron.
- **Surface = decisions only.** The subagent's return (counts + DECISION one-liners + candidate paths) is the *sole* thing the main thread relays. DECISIONs also persist to `knowledge/pending-decisions.md` (a review queue). AUTO candidates land silently; NOISE is dropped.
- **Draft-only — the review gate holds.** The distiller writes **only** into `knowledge/candidates/` + `knowledge/pending-decisions.md`, never into `playbooks/`/`experience/`/`decisions/` and never runs `kb finish`. Promotion into truth stays a reviewed step the owning agent does interactively — exactly ADR-0016's gate, with the *labor* (mining/drafting/triage) moved off the live thread and the *judgment* reduced to adjudicating real decisions.

Piloted on **a tracker-owning pm agent** before fleet-wide rollout (per ADR-0026 the shared `mesh-cognition` skill is unchanged for the other 8; the pm agent's copy carries the pilot self-fire section).

## Consequences

- Positive: learning accumulates without the user driving steps; the main thread is never blocked; only genuine decisions reach the user, at natural moments.
- Positive: harness-portable — the core procedure is written once; only the launch binding swaps (Claude Code subagent now, opencode later).
- Positive: safety preserved — draft-only + conservative triage (default DECISION) keeps noise out of "truth"; the ADR-0016 review gate is intact.
- Negative / risks: self-fire is model-driven (an agent may forget to spawn it) — mitigated by a **boundary-detecting nudge hook** (`agent-mesh/hooks/distill-nudge.py`, UserPromptSubmit): it deterministically detects a work boundary (explicit wind-down phrase, or a per-session staleness threshold), fires at most once per session, is pilot-gated to an agent allowlist (`$AGENT_MESH_DISTILL_NUDGE_AGENTS`, default `pm`), and injects a nudge that the model acts on by spawning the in-session background distiller (a hook cannot spawn a subagent itself). AUTO mis-classification could write a low-value candidate (bounded — candidates are proposals, reviewed/cleaned later); a background subagent can't outlive the session that spawned it, so end-of-session firing risks an orphaned run — the nudge therefore targets mid-session boundaries where the session stays alive to receive the result. A detached/SessionEnd backstop for truly-closed sessions remains deferred (non-portable).
- Migration: created `agent-mesh/distiller.md` (neutral), `pm/.claude/agents/distiller.md` (binding), `pm/knowledge/pending-decisions.md` (queue), and a pilot self-fire section in the pm agent's `mesh-cognition` skill copy. Validated with a live background distill run against the pm agent.
- Follow-ups: tune the triage rubric on the pm pilot; then fold the background-distiller section into the shared `mesh-cognition` skill + add a `distiller` binding to the other 8 agents; optional SessionStart re-surface of an unresolved `pending-decisions.md`; optional opencode binding when the mesh lands there.
