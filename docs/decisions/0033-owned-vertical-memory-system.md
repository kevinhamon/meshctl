---
status: accepted
date: 2026-07-24
---
# 0033. Owned, vertically-integrated agent memory — KB + episodic + recall as one system; retire the claude-mem dependency

- **Status:** accepted — **stages 1–3 shipped; claude-mem retired 2026-10-26.** Stage 1: extractive capture + live unified recall. Stages 2–3: the injection surface (`meshctl hook user-prompt`) + `intuition recall` use the **owned engine** (session-records + git time-spine via `Recall`). The claude-mem engine, its `MESH_INTUITION_ENGINE` switch and every claude-mem reference in the gate, doctrine and skills are removed (see the amendment). Stage 4 (embeddings/RAG) remains future/gated.
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** exercises the follow-up ADR-0016 and ADR-0015 both pre-authorized ("an owned engine slots behind the same contract"); amends ADR-0016 (the intuition *engine* swaps from claude-mem to owned) and extends ADR-0015 (recall backend scales); builds on ADR-0014 (KB structure), ADR-0017 (handoff), ADR-0027 (distiller). Injection surfaces are rendered via ADR-0032.

## Context

Cognition today spans an authored KB (durable, git) plus **claude-mem** as the episodic engine behind `agentmesh intuition recall` (ADR-0016). Assessment this session clarified what claude-mem actually contributes and what it costs:

- **What it gives that the KB can't:** *effortless passive capture* of the operational long-tail — the day-to-day investigations and ticket work that never get distilled into an ADR. Organized by time. This is real and load-bearing **today** (the KB captures only what is deliberately authored; distillation is lossy).
- **What it costs:** it is an external, Claude-Code-only daemon (a plugin + local HTTP worker). In an opencode pool it is dead weight. It is unversioned, unreviewed, can fabricate observations, and taxes every turn's context. It is a **bolt-on** — the exact friction the user wants gone.

Two facts reframe the build-vs-adopt question:

1. **The distinctive primitive is passive capture, not "sense of time."** Time is the navigation axis; capture is the substance. And a **time-spine already exists for free**: git history of `knowledge/` (dated ADRs), `handoff.md` rewritten per session (its git log is a session-by-session autobiography), dated `intake/` files, and `.dispatch.log`. Nothing reads that spine as episodic recall *yet*.
2. **Transcript access is not a blocker.** Claude Code's SessionEnd hook provides `transcript_path`; the transcript is structured JSONL (prompts, tool calls). The mesh can capture **extractively — zero LLM** — one structured record per session (files touched, commands/tools run, commits/ADRs, the handoff written, first prompt). That yields ~most of claude-mem's felt recall value ("which session dealt with X → go read it") without a mining pipeline.

The costly parts of an episodic system are exactly two: **LLM-mined per-observation capture** and **vector/semantic search**. Everything else (store, serve, inject, recency) the mesh already owns. Neither costly part is needed on day one.

## Options considered

1. **Keep claude-mem as the engine (ADR-0016 status quo).** Real value today, but keeps the bolt-on, the Claude-Code lock-in, and the multi-harness dead weight.
2. **Drop episodic; KB + handoff only.** Loses the passively-captured long-tail the user explicitly values ("remembering what we did last week matters"). Rejected.
3. **Clone claude-mem's full pipeline (per-turn LLM mining + embeddings) day one.** High, ongoing complexity; re-learns capture-quality problems claude-mem already solved. The trap. Rejected.
4. **Owned, vertically-integrated memory, staged cheap-first (chosen).** One store, one recall contract, one injection surface for KB + episodic + git-spine. Start extractive + recency/keyword; defer embeddings behind the same contract. claude-mem stays pluggable through transition, then retired. Removes the bolt-on and stays harness-neutral.

## Decision

Build an **owned memory engine inside `agentmesh`** that unifies the durable and episodic tiers behind the recall contracts ADR-0015/0016 already defined. The KB stays authoritative; episodic is first-class but never the source of truth.

### Tiers (unchanged roles, owned engines)
- **Durable / deliberate** — the git `knowledge/` KB. Authoritative. Unchanged (ADR-0014).
- **Episodic** — first-class, with **two backends behind one contract**:
  - **Floor (always on, harness-neutral):** recall derived from the git/handoff/intake **time-spine** — free, in every pool including opencode.
  - **Records (owned capture):** one **session-record** per session, captured extractively at session end.
  - **Rich (pluggable, optional):** an external engine (claude-mem where present) queried as a *backend*, never a second injector. Retired once records prove out.

### Capture contract (extractive, no LLM)
- Rendered per harness (ADR-0032). Claude Code: a **SessionEnd** handler reads `transcript_path` and writes one session-record. opencode: its equivalent, or none → the floor still works.
- **Session-record schema** (append-only, one file per session under a pool-level, git-ignorable episodic store, e.g. `<agent>/.memory/sessions/<id>.json`):
  ```json
  {
    "id": "<session-id>", "agent": "architect",
    "started": "<ts>", "ended": "<ts>",
    "first_prompt": "<verbatim opening ask>",
    "summary": "<extractive: opening ask + closing handoff delta>",
    "files_touched": ["…"], "commands": ["…"], "tools": ["…"],
    "commits": ["<sha> <subject>"], "adrs": ["0033"],
    "intake": ["<request-id>"], "handoff_ref": "knowledge/handoff.md@<sha>",
    "refs": ["PROJ-123", "R-1"]
  }
  ```
  All fields are parsed structurally from the transcript + git; **no model call**. `summary` is extractive (concatenated signals), not generated.

### Recall (one contract, backend scales — extends ADR-0015)
- `agentmesh memory recall "<q>"` and `agentmesh intuition recall "<q>"` keep their contracts. The engine behind them unifies **KB topics + session-records + time-spine**, ranked by relevance then recency, returning **compact pointers** (title/date/where-to-look), never bodies (ADR-0015 §9, ADR-0016 compact-pointer rule).
- **Backend staging (all behind the same contract, no caller change):**
  1. **Now:** live keyword/BM25 over KB + session-records + the git/handoff spine. Replaces the persisted `.memory/index.json` staleness model — recall reads live, so the ADR-0015 rebuild-and-staleness ritual (and its `doctor` warnings) is retired for the routing index.
  2. **Later (deep-topic RAG):** embeddings/vector recall over `knowledge/` + records, added when the corpus outgrows keyword recall. This is the "lightweight sense it knows something → read only the top-k" model. Same contract; opt-in per pool.

### claude-mem: pluggable, then retired
- Amends ADR-0016: the intuition **engine** is no longer claude-mem — it is the owned engine. claude-mem is demoted to an **optional rich backend** during transition (queried behind the contract, `SEMANTIC_INJECT` off so it never double-injects). Once session-records prove out, the claude-mem dependency (plugin + worker) is **dropped** — the mesh owns capture and recall end to end.

## Consequences

- Positive: **no bolt-on** — KB, episodic capture, and recall are one owned, git-native, harness-neutral system. −2 external deps (plugin + worker) at the end of transition.
- Positive: "what did we do last week" works in **every** pool from the floor alone; session-records add the flesh; both without an LLM pipeline.
- Positive: the deferred embeddings ADR (0014/0015/0016 all pointed here) now has a home — it slots behind the existing contract, changing nothing for callers.
- Positive: retires the persisted-index staleness failure mode (live recall).
- Trade-off: the mesh inherits capture-quality ownership (noise, dedup, what-to-keep). **Mitigated** by extractive-structural capture — it records facts (files/commands/commits), not fuzzy prose, so there is far less to get wrong than LLM mining.
- Trade-off: extractive per-session granularity is coarser than claude-mem's per-observation mining. Accepted: recall routes to a session; the session's handoff/commits/KB are the authoritative read. If sub-session recall misses become common, that is the trigger for the embeddings/summarization stage — not to bolt LLM mining on now.
- Trade-off: capture depends on the harness exposing a transcript (Claude Code does). Where it doesn't, the floor still delivers episodic recall from the time-spine.
- Migration / sequence:
  1. **Records + floor recall (LLM-free):** SessionEnd extractive capture (via ADR-0032 surface) + session-record schema; unify `memory recall` over KB + records + time-spine with live keyword/BM25; retire the persisted-index staleness path.
  2. **Transition:** keep claude-mem as an optional backend behind the contract; compare felt recall vs records.
  3. **Retire claude-mem** once records suffice; drop the plugin/worker dependency. **Done 2026-10-26.**
  4. **Embeddings/RAG (further, gated) — PARKED:** add vector recall for deep topics behind the same contract when corpus size warrants; opt-in per pool. **Intended engine: an optional `postgres + pgvector` backend** selected via `MESH_MEMORY_BACKEND=postgres://…`, holding a `sessions` table (episodic, queryable, cross-agent/cross-machine) + a `chunks` table with `vector` embeddings of KB + records. **Durable KB stays in git** (never moves to the DB); the server is the *recall* layer only, and recall **fails open to the file/git-spine floor** when the backend is absent (self-contained mode preserved). This also supersedes the "commit `.memory/sessions/` to git" idea — episodic persistence/sync belongs in the shared DB, not git blobs. **Trigger to build:** file/keyword recall demonstrably misses (corpus growth), or a multi-machine / shared cross-agent recall need. Open new-dependency decision then: embeddings source (local model vs embed API). Parked 2026-07-24 at user direction — not warranted at current scale.

## Amendment (2026-10-26): claude-mem removed from meshctl

The owned engine has been the default since stage 2 and produced every intuition line since; no pool sets `MESH_INTUITION_ENGINE`. The planned felt-recall comparison (stage 2) is skipped: there is no live use of the claude-mem path to compare. The plugin's background observer, running in every session whether the mesh used it or not, was a large share of measured usage cost, which settles the trigger in the follow-ups above.

Removed: the claude-mem recall client and the `MESH_INTUITION_ENGINE` switch (a leftover setting is ignored); the gate's claude-mem checks (its write tools by name, its on-disk store path) and the `memory-store` check name; the claude-mem mining steps in the distiller doctrine and the mesh-cognition skill. The gate's other competing-store rule, Claude Code's built-in auto-memory, and the hand-written intake rules stay. Removing the plugin itself is a per-machine step outside meshctl.
