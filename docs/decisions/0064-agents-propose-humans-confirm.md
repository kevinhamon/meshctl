---
status: accepted
date: 2026-10-15
---
# 0064. Agents propose, humans confirm: KB truth needs human confirmation, enforced at `kb finish`

- **Status:** accepted
- **Date:** 2026-10-15
- **Deciders:** maintainer (accepted 2026-10-15)
- **Related:** `internal/mesh/kb.go` (`KBFinish`), gate ask-path (ADR-0063), KB-rule gate (ADR-0062), mesh-cognition skill (distillation ritual, review gate), `distill.go`

## Context

A peer's knowledge base is the ground truth it, and every peer that consults it, builds on. A wrong outbound artifact costs one correction. A wrong KB entry is recalled again and again, without anyone noticing, and every answer built on it inherits the error.

Verified facts:
- The mesh-cognition doctrine names the review gate as "a human/**the owning agent** approves". An agent may approve its own writes.
- `KBFinish` rebases and fast-forward merges the session branch with no approval step.
- The distiller drafts only `candidates/`, but the agent itself promotes candidates into the current tier.
- In one live pool, a single peer made 59 KB commits in 30 days, none passing through an explicit human confirmation.
- Observed failure: in an interactive session a peer invented conventions that existed nowhere in the ecosystem. It recorded them as decisions and pushed work built on them to an external tracker, and correcting it took hours.

Doctrine alone, a standing order in prose, did not prevent this. The gate is pattern-only with a 10s hook timeout, so it can't judge meaning, but it can ask.

## Options considered

1. **Doctrine only.** Tighten the mesh-cognition wording to "human approves". No code. This already exists in weaker form and failed, because the agent judges for itself when approval is needed.
2. **Gate ask on `kb finish` when the merge touches more than candidates.** The agent shows a plain-language summary of what changes; the human approves or denies at the permission prompt. Reuses the existing gate ask-path. Cost: a prompt on every truth-changing merge, and the risk of rubber-stamping.
3. **Human merges KB branches by hand** (PR-style review of every KB change). Strongest, but heavy. It breaks the flow of an interactive session and does not fit headless runs.
4. **Post-hoc review** (doctor lists unconfirmed entries for later review). Cheap, but the error is already being recalled before review happens.

## Decision

Option 2, with Option 1's wording change:
- **Agents propose.** A peer writes `candidates/` entries and decisions at `status: proposed` freely.
- **Humans confirm.** Only the human promotes into the current tier or to `accepted`. "Or the owning agent" is dropped from the review gate.
- **Enforced at `kb finish`.** The gate asks when the merge touches anything beyond candidates. Before calling it, the agent states in plain language what changes and why.
- **Headless runs** (distiller, `meshctl ask`) can land candidates only; they have no human to confirm.
- **Confirmed knowledge** is recorded per entry, and recall marks unconfirmed entries as hypotheses rather than ground truth.

Trade-off accepted: more prompts in interactive sessions, and a confirmation can become a rubber stamp. The summary requirement is what keeps it meaningful. An ask the human can't read at a glance is a defect of the summary, not a reason to drop the gate.

## Consequences

- Positive: no entry becomes ground truth without the human agreeing to its content. KB errors are caught at write time, not after they have been recalled.
- Positive: makes room for a coherence check between peers whose knowledge must agree. The check's findings belong in the same confirmation summary.
- Negative: existing KBs are unconfirmed by this definition. Recall would mark all of them as hypotheses until a reconciliation pass confirms, rewrites or deletes them.
- Negative: peers on older wiring do not get the gate change until re-onboarded.
- Open (to be settled before implementation): how confirmation is recorded on each entry type; how the hook tells a candidates-only merge from one touching the current tier within its timeout; how recall marks unconfirmed entries; the reconciliation pass for existing KBs.
