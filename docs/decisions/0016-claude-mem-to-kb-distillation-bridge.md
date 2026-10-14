---
status: accepted
date: 2026-07-16
---
# 0016. Agent memory as engine / contract / surface — two tiers, portable across harnesses

- **Status:** accepted
- **Date:** 2026-07-16
- **Deciders:** maintainer
- **Related:** ADR-0014 (KB structure + disposable memory), ADR-0015 (memory index + `memory` CLI), ADR-0011 (`kb begin/finish` write path), claude-mem plugin (`thedotmack/claude-mem` v13.3.0 — a local worker service at `127.0.0.1:37777` + SQLite/chroma + MCP `mcp-search`)
- **Update (2026-07-24):** the intuition **engine** is amended by **ADR-0033** — the owned engine (this ADR's pre-authorized follow-up) replaces claude-mem, which becomes an optional backend behind the same contract and is then retired. The **engine/contract/surface model here is unchanged**; ADR-0033 just fills in the owned engine and adds extractive episodic capture.

## Context

Two memory systems run side by side and were never reconciled, and the reconciliation
has to survive **multiple harnesses** (Claude Code, opencode, Codex, a bare shell) —
the mesh is not Claude-Code-only.

- **claude-mem** — auto-captures an observation every `PostToolUse` (9,455 stored),
  embeds them (chroma), and **pushes N semantic hits into every turn**. Zero authoring
  effort; fuzzy associative recall. Investigation established its shape: the store +
  capture + search live behind a **standalone local HTTP service + an MCP surface**
  (already used by Claude Code *and* Codex hooks; reachable by any MCP client). But its
  **push is untunable** — `handleSemanticContext` applies **no relevance/score cutoff**
  (top-N only, min 20-char query) and dumps each hit's **full narrative** verbatim; no
  compact-pointer format. Config exposes on/off + count, nothing for quality or brevity.
- **agentmesh memory** (ADR-0014/0015) — a generated, disposable, git-tracked index over
  each agent's `knowledge/` KB; **pull** recall (`memory recall`); authoritative KB
  behind it; a Go CLI callable from any harness. Portable and owned by construction.

Two reframings drove this ADR past its first draft:

1. **claude-mem is the *intuition* layer (System 1), not a disposable net.** Fast,
   associative, automatic — its *push is the feature*. The earlier "phase the push to
   zero / converge on pull" was wrong: pull-only intuition isn't intuition. The problem
   is that claude-mem's push is **blunt** (noisy + verbose), and its config can't
   sharpen it. agentmesh is the *deliberate* layer (System 2): slow, structured,
   authoritative, pulled on purpose.
2. **Cross-harness forces an engine/contract/surface split.** Auto-injection is
   inherently per-harness glue; the store and the query API are not. Portability comes
   from owning the **contract**, keeping the **engine** swappable, and writing thin
   per-harness **surfaces**.

## The model: three planes × two tiers

Split each memory tier into **engine** (stores + retrieves), **contract** (a
harness-neutral query→result API), and **surface** (how a given harness gets it into
context — auto-push hook, or MCP/CLI pull).

| Tier | Engine | Contract (owned, harness-neutral) | Surface (per-harness) |
|---|---|---|---|
| **Deliberate (System 2)** | git-tracked `knowledge/` KB | `agentmesh memory recall` (ADR-0015) | CLI/pull from any harness — **already portable** |
| **Intuition (System 1)** | **claude-mem service/MCP** (swappable) | **owned `agentmesh` recall-surface**: query → score-thresholded, deduped, **compact pointers** | thin adapter per harness: auto-push hook where supported, MCP-pull where not |

Reading it: the **engine** is where the data lives and the fuzzy/structured search runs;
the **contract** is the one API the mesh commits to (so callers don't care which engine
answers); the **surface** is the only per-harness code.

## Options considered

**Intuition engine**
1. Keep depending on claude-mem's *plugin push* as the strategy — rejected: Claude-Code-
   bound, untunable (blunt), and couples the intuition *surface* to one harness.
2. Build an owned embeddings engine now — rejected here: reinvents a working service and
   reintroduces the hidden-vector-store risk ADR-0014/0015 gated behind a *further* ADR.
3. **Reuse claude-mem as the intuition *engine* (its service + MCP), behind an owned
   contract (chosen).** It already spans Claude Code + Codex + any MCP client; treat it
   as a swappable engine, not the surface.

**Intuition surface**
1. claude-mem's built-in `SEMANTIC_INJECT` push — rejected as the long-term surface:
   untunable (no threshold, no compact format) and Claude-Code-specific.
2. **Own the surface (chosen)** — a thin per-harness adapter that calls the owned
   contract (threshold + compact pointers) and injects via that harness's mechanism.

## Decision

Adopt the **engine / contract / surface** model with **two tiers**, and make
portability a first-class axis.

1. **Deliberate tier stays as ADR-0014/0015** — KB is the authoritative source of truth;
   `agentmesh memory recall` is its harness-neutral contract; already portable. Nothing
   authoritative ever depends on a third-party plugin.

2. **Intuition tier = claude-mem engine + an owned contract + owned per-harness surface.**
   - **Engine:** claude-mem's local service / MCP `mcp-search`. Treated as **swappable**;
     never authoritative (losing it loses no durable knowledge — the KB holds truth).
   - **Contract:** an owned `agentmesh`-level recall call (e.g. `agentmesh intuition
     recall "<q>"`) that queries the engine and returns **score-thresholded, deduped,
     compact pointers** (title + date + where-to-look), NOT full narratives — the two
     dials claude-mem's own push lacks.
   - **Surface:** thin per-harness adapters call that contract. Claude Code: our own
     `UserPromptSubmit` hook (with `CLAUDE_MEM_SEMANTIC_INJECT=false` so the blunt
     built-in push is off). opencode/others: MCP-pull, or their own pre-prompt hook.
     Intuition stays **always-on where the harness supports auto-push**, but sharp + brief.

3. **Intuition stays a push, done right.** Correcting the first draft: do not phase the
   push to zero — phase claude-mem's *blunt* push → the *owned tuned* push. "Sharp,
   compact, always-on" is the endgame, not pull-only.

4. **Distillation bridge (unchanged, System 1 → System 2).** Repeated intuitive hits are
   how experience becomes learned procedure: query the engine for an agent's domain →
   draft `knowledge/candidates/` via `agentmesh kb begin` → **human/agent reviews +
   consolidates** into `playbooks/`/`experience/`/decisions via `kb finish` (never
   auto-promote raw observations). Optional later `agentmesh memory distill` does only
   the query+draft; promotion is always reviewed.

### Guardrails
- The **KB is authoritative**; the intuition engine is swappable and never the truth.
- **Portability rule:** the only harness-specific code is a surface adapter; engine +
  both contracts are harness-neutral. Adding a harness = writing one thin adapter.
- No embeddings inside agentmesh yet (still gated behind a further ADR per ADR-0015); if
  an owned intuition engine is ever built, it slots behind the *same contract* with no
  caller change.
- Distillation writes go through `kb begin/finish` (reviewed, git-tracked).

## Consequences

- **Positive:** intuition kept (push stays) but made sharp + cheap via the owned surface;
  cross-harness by design (engine + contracts neutral, surfaces thin/swappable); no lock-
  in (claude-mem is a swappable engine, not the strategy); the authoritative layer is
  yours and portable; the KB grows from real episodic signal via distillation.
- **Negative / risks:**
  - **Engine exposes no score?** The owned threshold needs a similarity score from the
    engine. If claude-mem's service/MCP doesn't return one, threshold must come from
    querying chroma directly, or from a re-rank step. **Verify before building the surface.**
  - **Per-harness surface upkeep** — each harness needs its adapter maintained. Bounded:
    adapters are thin and call one contract.
  - **Distillation quality / discipline** — mitigated by the review gate and `doctor`
    empty-area nudges.
- **Migration / sequence:**
  1. Phase-1 claude-mem trim (limit 3, obs 20, terminal-output off) — **applied.**
  2. **Verify** claude-mem service/MCP returns similarity scores.
  3. Build the owned intuition contract (`agentmesh` recall over the engine: threshold +
     dedup + compact pointers).
  4. Claude Code surface: own `UserPromptSubmit` hook calling the contract; set
     `CLAUDE_MEM_SEMANTIC_INJECT=false` (retire the blunt push). Keep claude-mem capture on.
  5. opencode/Codex surfaces: MCP-pull or per-harness hook against the same contract.
  6. Distillation ritual into `agent-comms.md` (Learn stage) + run opportunistically.
- **Follow-ups:** an owned embeddings engine (to drop the third-party dependency
  entirely) is a *further* ADR — it slots behind the contract, so it changes nothing for
  callers. Trigger: claude-mem instability, or wanting zero external LLM/API deps.
