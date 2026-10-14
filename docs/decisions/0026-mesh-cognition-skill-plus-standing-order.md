---
status: accepted
date: 2026-07-21
---
# 0026. Mesh cognition delivered as a skill + always-present CLAUDE.md standing order

- **Status:** accepted
- **Date:** 2026-07-21
- **Deciders:** maintainer
- **Related:** ADR-0014 (KB/memory substrate), ADR-0015 (memory index), ADR-0016 (distillation ritual), ADR-0017 (session handoff), ADR-0018 (cross-agent learning capture); all 9 agent workspaces; agent-mesh templates + `agentmesh` scaffold

## Context

The mesh was designed so every agent keeps its own git-tracked `knowledge/` KB and learns over time (retrieval loop → distillation ritual → handoff). The ritual is fully specified in **one** place — `agent-comms.md` §Knowledge & Memory — and fully supported by tooling (`agentmesh memory`/`intuition`/`kb`/`handoff` + `doctor` nudges).

But it was not firing. The pm agent in particular behaved as if the pattern didn't exist: its `knowledge/` scaffold (candidates/playbooks/experience) sat empty while architect's filled to 50+ files. Investigation found the break is at the **agent instruction layer**, not the spec or tooling:

- **No agent's CLAUDE.md restated the ritual.** 0 of 9 carried the recall/distill/handoff steps inline.
- **Every agent linked `agent-comms.md` as the *comms/intake* protocol only** — never as the *learning* protocol. And `agent-comms.md` is Read-on-demand, **never auto-injected** — so the Learn loop only fired if the agent happened to read a section nobody pointed it to.
- The **onboard template was silent** on KB/distillation entirely (and its two copies had drifted).
- Only the architect and a support agent even had a `## Knowledge Base` CLAUDE.md section. Architect's was 3 lines but was a *standing order* — enough for an ADR-authoring agent to act on. PM had none, **and** its canonical spec skill said "all work follows this spec, disputes resolved by spec, not by memory," actively crowding out learning.

Net: architect learned because its CLAUDE.md carried a standing trigger; PM didn't because it carried none. The always-present per-agent trigger was the missing piece.

A key constraint shaped the fix: **skills are on-demand**. Only a skill's `name`+`description` sits in the catalog; the body loads only when the Skill tool is invoked. A once-per-session ritual (recall at start, distill at end) cannot depend on the model *choosing* to invoke a skill. The only always-present per-agent channels are **CLAUDE.md** and the **SessionStart hooks**.

## Options considered

1. **Skill only** (as first proposed — "make agent-mesh the primary-instructions skill"). Cleanest single source, but on-demand loading means the ritual may silently not fire each session. Rejected alone.
2. **Fix the pm agent only** (add a KB stanza to its CLAUDE.md). Fast, unblocks PM, but leaves the identical systemic gap in the other 8 agents. Rejected as incomplete.
3. **Hybrid: skill + always-present CLAUDE.md standing order + template/scaffold fix.** The skill gives the deep, single-source procedure and the catalog trigger; the short CLAUDE.md stanza guarantees the standing order is always in context; the template + `agentmesh` scaffold change makes new agents inherit both. Chosen.

## Decision

Deliver mesh cognition as a **hybrid**:

- A new **`mesh-cognition` skill** — the retrieval loop, distillation ritual, cross-agent capture, and handoff, deferring to `agent-comms.md` §Knowledge & Memory as authoritative. Installed into all 9 agents' `.claude/skills/`. It is thin and stable (the deep spec stays single-source in `agent-comms.md`), so copying it per-agent does not meaningfully drift.
- A short **"## Cognition — maintain your own knowledge base (standing order)"** stanza in every agent's `CLAUDE.md` — always in context — that states the standing order (recall/distill/record/handoff) and names the skill for the full procedure. Architect's existing 3-line KB section was upgraded into this; the support agent's stanza distinguishes the mesh `knowledge/` cognition tree from its read-only `knowledge-base/` load tree; the pm agent's spec-supremacy clause was reconciled so the spec governs PM domain patterns but does not override the cognition layer.
- The **onboard template** (both `templates/CLAUDE.md` and the embedded `internal/mesh/templates/CLAUDE.md`) carries the stanza, and `agentmesh new`/`onboard` now scaffold the skill (add-only `scaffoldSkills`), so future agents inherit both automatically.

Trade-off accepted: the stanza is copy-pasted across 9 CLAUDE.md files (no runtime sync), mirroring the existing guardrail/handoff-prose pattern. Acceptable because the stanza is short and stable; the deep, churn-prone content lives once in `agent-comms.md` and once in the skill.

## Consequences

- Positive: the learning loop now has a guaranteed always-present trigger in every agent; new agents inherit it by construction; the deep procedure has a single invokable home.
- Positive: `mesh-cognition` skill discovery confirmed working (appears in this session's catalog; `agentmesh new` scaffolds it verified in a throwaway agent).
- Negative / new risk: 9 copies of the stanza can drift over time; mitigate by keeping the stanza thin and treating `agent-comms.md` + the skill as the source of truth. `agentmesh doctor` already warns on empty `playbooks/`/`experience/`, which now backstops adoption.
- Migration: edits made in all 9 agent repos + agent-mesh (skill in both template roots, template CLAUDE.md ×2, `scaffold.go`, rebuilt+reinstalled binary). Each agent repo's changes are left uncommitted for its owner to commit; architect commits its own repo (this ADR + skill copy + CLAUDE.md upgrade) and the agent-mesh infra change.
- Follow-ups: consider a `doctor` check that the `mesh-cognition` skill + Cognition stanza are present per agent; consider a real single-source install (symlink or `agentmesh sync-skills`) if the stanza/skill copies start to drift.
