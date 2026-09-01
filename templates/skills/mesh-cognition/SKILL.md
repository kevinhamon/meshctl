---
name: mesh-cognition
description: How this agent maintains its own knowledge base and learns over time — the retrieval loop (recall → retrieve → verify → act → learn), the distillation ritual (turn recurring intuition into durable KB via kb begin/finish), cross-agent learning capture on dispatch return, and session handoff. Invoke at session start (to recall), whenever a session yields a reusable procedure / failure mode / consequential decision (to distill), when recording an ADR or logging a risk, on returning from an `meshctl ask` dispatch, and at session end (to write the handoff). Every mesh agent's primary cognition instructions.
---

# Mesh cognition — maintain your own KB, learn over time

Coordination (intake) is how agents *talk*. **Cognition** is how one agent
organizes what it knows and recognizes what it has seen — on the same file
substrate, no new store. This skill is the operative trigger; the **authoritative
full spec is `.agentmesh/doctrine/agent-comms.md` §Knowledge & Memory (ADR-0014–0018)**.
When this skill and that section disagree, that section wins — read it.

Your KB is your own git-tracked `knowledge/` tree:
`decisions/` (ADRs) · domain files + `architecture/` · `playbooks/` (repeatable
procedures) · `experience/` (retros / failure modes) · `direction.md` + `risks.md`
(state) · `candidates/` (the KB inbox — proposed learning awaiting review) ·
`reference/` (cited external-doc notes). Your contract stays in `CLAUDE.md` +
`agent.yaml`. Memory (`.memory/index.json`) is a generated, disposable index —
advisory; the KB is authoritative.

## Retrieval loop (do this every session)

Recognize → **Recall** → **Retrieve** → Reconcile → **Verify** → Act → **Learn**

1. **Recall.** `meshctl memory recall "<topic>"` returns KB locations + freshness
   without reading bodies. Your `knowledge/handoff.md` is auto-injected at start.
2. **Retrieve.** Read the KB files recall points to — they are authoritative.
3. **Reconcile** conflicts in favor of the maintained KB; **Verify** against live
   systems / MCP before acting.
4. **Act**, then **Learn** — distill anything durable (below).

## Distillation ritual — System 1 → System 2 (reviewed, never automatic)

Run it opportunistically: a *recurring* pattern/gotcha/lesson, a task that left a
reusable procedure or failure mode, a consequential decision, or when `meshctl
doctor` warns `playbooks/`/`experience/` are empty. Quality over volume.

1. **Mine** the intuition engine — `meshctl intuition recall "<topic>"` (or the
   claude-mem `mcp-search` tools for depth). Keep signal that repeats, not one-offs.
2. **Draft a candidate.** `meshctl kb begin`, then write one
   `knowledge/candidates/<slug>.md` per learning: *what* it is, *why* it matters,
   the *evidence* (session/obs/file refs). Candidates are proposals, not truth.
3. **Consolidate.** Judge each candidate; if durable + verified, move/rewrite it
   into the right area — `playbooks/`, `experience/`, a **decision** (ADR in
   `decisions/`), or `glossary.md`/`system-map.md`. Log new risks in `risks.md`.
   Drop the rest. `meshctl kb finish` merges + auto-rebuilds the memory index.
4. **Leave `candidates/` clean** — a holding area, not a graveyard.

Guardrails: **review gate** (a human/the owning agent approves before a candidate
becomes truth) · **KB is authoritative, claude-mem is never edited** (distill *from*
it) · **all writes via `kb begin`/`kb finish`** (git-tracked, recoverable).

## Cross-agent learning capture on dispatch return (ADR-0018)

A headless `meshctl ask` run may not commit, so anything the answering peer learned
lives only in the answered `intake/` file and is lost if never distilled. On
returning from a dispatch, judge the answer and write durable learning as a
candidate **into the OWNING agent's KB, not your own**:

```
meshctl kb begin  --agent <owner>
# write knowledge/candidates/<slug>.md in the owner's worktree, with provenance
#   frontmatter:  source: dispatch <caller> <intake-id>
meshctl kb finish --agent <owner>     # from a normal cwd, not inside the worktree
```

Domain knowledge lands with its owner (a database gotcha → the database expert, an
org-level pattern → `architect`). It lands as a candidate so the review gate holds;
the owner promotes it on its next interactive session.

## Session handoff — resume point (ADR-0017)

Keep an authoritative, git-tracked `knowledge/handoff.md`: *where we are + the next
concrete step*. Read at start (auto-injected). Write at end / when attention shifts:
`meshctl handoff set` (from stdin). Body — *Current focus · Status · Next step ·
Open threads/blockers · Pointers*. It is a handoff, not a journal — **rewrite it,
don't append**; git history preserves prior handoffs.

## Repo hygiene — knowledge is only safe once pushed (ADR-0039)

Your workspace must be its own git repo **with a remote**. `knowledge/` (KB + handoff)
is durable only once committed **and pushed**. `meshctl agent new` inits the local repo;
if yours isn't a repo, `git init` + commit. Create a remote and push. Session start nags
until a remote exists.

## Guardrail

Everything in `knowledge/` and the ADR/risk ids you cite are **mesh-internal** —
correct to reference here, banned from crossing out to any product/external surface
(issue tracker, wiki, product-repo commits/PRs, code comments, email, chat). See
`agent-comms.md` Core rule 7.
