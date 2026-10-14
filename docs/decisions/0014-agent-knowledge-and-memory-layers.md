---
status: accepted
date: 2026-07-16
---
# 0014. Agent knowledge base structure + disposable memory layer

- **Status:** accepted
- **Date:** 2026-07-16
- **Deciders:** maintainer
- **Related:** ADR-0010 (agentmesh CLI + manifests — this rides its scaffold/doctor surface); ADR-0011 (KB branch-merge — reused as the governance write path); **ADR-0015** (memory index schema + `memory` CLI — split out of this ADR, gated on a topic-extraction spike); `agent_knowledge_memory_high_level_design.md` (design brief, the input to this ADR); MESH-CLI-SPEC.md

## Context

Two specs in `~/agents/agent-mesh` describe different planes and were not yet
reconciled:

1. **MESH-CLI-SPEC / ADR-0010+0011 — the coordination plane** (built). `agentmesh`
   provides identity (`agent.yaml`), discovery (`directory`), messaging
   (`ask`/`send` + anti-cascade guards), concurrency (claims, session presence),
   and a durable-write substrate (`kb begin/finish` git-worktree merge). It answers
   *who agents are and how they talk*.
2. **`agent_knowledge_memory_high_level_design.md` — the cognition plane** (design
   brief only). It defines *how one agent organizes what it knows and recognizes
   what it has seen*: a structured **Knowledge Base** (7 cognitive areas) plus a
   disposable **Memory** layer (recognition + routing into the KB), and a 7-stage
   retrieval loop (Recognize → Recall → Retrieve → Reconcile → Verify → Act →
   Learn).

Forces:

- The cognition plane needs a home. Its philosophy — files, no DB, memory
  disposable and rebuildable, "no hidden irreplaceable source of truth" — already
  matches ADR-0010's file-based/no-daemon stance. It should *bind to* the existing
  substrate, not spawn a parallel one.
- Today's KBs are inconsistent. architect's KB is ~60% aligned already
  (`decisions/`, `architecture/`, `glossary.md`, `system-map.md`, `direction.md`,
  `risks.md`); domain-expert KBs are near-empty. Playbooks, Experience, and a
  candidate-learning holding area are missing across the board.
- The design's **Inbox** area name collides with the load-bearing mesh `intake/`
  vocabulary (cross-agent messages). Two different things must not share a word.
- The design's **Agent Contract** area overlaps what already lives in each agent's
  `CLAUDE.md` + `agent.yaml`. A second copy would drift.

This ADR is the synthesis of the two specs and was reviewed by the architect agent
(headless guidance dispatch, 2026-07-16). Distinguish verified from judgment: the
build state of the coordination plane is verified from source; the cognition plane
is a design not yet implemented.

## Options considered

**Where cognition lives**
1. **New store / DB / vector index for memory** — a second source of truth, an
   irreplaceable hidden layer. Directly violates design §11–13. Rejected.
2. **Bind cognition to the existing file substrate (chosen)** — KB is git-tracked
   files under `knowledge/`; memory is a *generated, disposable* index computed
   from the KB, exactly as `agentmesh directory` computes the roster from
   manifests. No new persistence mechanism.

**Doctor/onboard enforcement of the 7 areas**
1. **Enforce all 7 (hard-fail)** — blocks migration on legitimately-empty areas
   (a fresh domain expert has no Experience yet); false-negatives stall onboarding.
   Rejected.
2. **Additive + tiered (chosen)** — `onboard` scaffolds missing areas without ever
   moving existing content; `doctor` *warns* on absent/empty areas and on stale
   candidates, and errors only on the structurally-enforceable defect (an ADR
   marked `superseded by NNNN` with NNNN missing/non-back-referencing). Migration is
   never blocked by a warning. (Semantic "should-be-superseded" is not mechanizable.)

**Inbox naming (open decision A)**
- Overload `intake/` — rejected; `intake/` is mesh transport vocabulary.
- **Rename the knowledge holding area to `candidates/` (chosen).**

**Agent Contract home (open decision B)**
- New `knowledge/contract.md` — a second source that drifts from `CLAUDE.md` /
  `agent.yaml`. Rejected (design §7/§12).
- **Contract stays in `CLAUDE.md` + `agent.yaml` (chosen).**

## Decision

Adopt the two-plane model and bind the cognition plane to the existing agentmesh
substrate. Coordination plane (ADR-0010/0011) is unchanged; this ADR adds the
knowledge/memory layer on top of it.

### Canonical knowledge-base layout

Each agent's `knowledge/` holds these areas. `onboard` scaffolds what is missing;
it **never moves or overwrites** existing content.

| Design area | Landing spot | Notes |
|---|---|---|
| Agent Contract | `CLAUDE.md` + `agent.yaml` | **No** `knowledge/contract.md` — single source. |
| Knowledge | `knowledge/` domain files + `architecture/` | e.g. `glossary.md`, `system-map.md`. |
| Decisions | `knowledge/decisions/` | Existing ADRs **are** this area — no parallel store. |
| Playbooks | `knowledge/playbooks/` | New. Repeatable procedures / decision frameworks. |
| Experience | `knowledge/experience/` | New. Retrospectives, recurring failure modes. |
| State | `direction.md` + `risks.md` (existing) | Existing files **are** this area — no new `state.md`. Mirrors the Decisions ruling. |
| Candidates | `knowledge/candidates/` | New. Renamed from design "Inbox". Proposed learning awaiting consolidation. |

### Memory — principle approved here; schema + CLI deferred to a follow-up ADR

This ADR approves the **principle** only, and defers the engineering to **ADR-0015**
(memory index schema + `memory` CLI). The principle:

- Memory is a computed index — **never hand-authored**, `.gitignore`d, and
  rebuildable from the KB alone. Modeled on `agentmesh directory`.
- Memory is **advisory**; the KB is authoritative (design §4, retrieval stage 4).
- Recovery is a first-class property: deleting the index and rebuilding must
  reconstruct it fully — no state lives only in memory.

**Deferred to ADR-0015 (the genuinely-new, still-underspecified part):**
- **Topic-extraction method** — the load-bearing signal that drives `recall`
  routing is unspecified. Candidate signals to spike: section headings +
  front-matter tags + filenames. This must be resolved before the schema is fixed.
- **"Familiarity" definition** — a static file scan yields only presence + recency
  + reference-count, not a learned "strength" scalar. ADR-0015 defines familiarity
  as that explicit proxy (presence + freshness + reference-count) or drops the word.
- **Index freshness + rebuild trigger** — `freshness=mtime` and relationship tags
  from manifest `owns`/`domains` are computable now; rebuild-after-`kb finish` and
  the concurrency handling (see risk (e) below) belong with the schema.
- **CLI surface** — `agentmesh memory rebuild|recall` shapes, `.memory/index.json`
  location, "generated — do not edit" marking.

Splitting lets the uncontroversial layout + binding decision land without dragging
the underspecified memory design. This mirrors the follow-up already gating
embeddings — the initial schema deserves the same treatment.

### Governance reuses `kb begin/finish` — no new mechanism

The design's proposed → accepted → consolidated flow (superseded stays traceable)
**is** the ADR-0011 worktree write path. Candidate learnings land in
`knowledge/candidates/` via `kb begin`; consolidation into an area and marking old
guidance superseded is an ordinary KB commit through `kb finish`. Reads stay direct.

### Retrieval loop stays prose, not code

The 7-stage loop (Recognize → … → Learn) is agent **behavior**, documented in
`agent-comms.md` + per-agent `CLAUDE.md`, invoking CLI primitives at the mechanical
steps (Recall → `memory recall`; Retrieve → KB reads; Verify → live systems/MCP;
Learn → `kb begin`). It is **not** encoded in the binary.

### Cross-agent knowledge references via manifest, not paths

Shared org knowledge (architect's ADRs, `system-map`) is referenced through
manifest `owns`/`domains` tags, **never hardcoded paths**, so an agent move or
rename doesn't break references.

## Consequences

- **Positive:** cognition plane gets a home with zero new persistence; memory is
  disposable/recoverable by construction; governance and cross-agent references
  reuse mechanisms that already exist (`kb begin/finish`, manifest tags); a single
  contract source (no drift); migration is non-destructive and never blocked by an
  empty area.
- **Negative / new risks** (ADR-local; lettered to avoid collision with the global
  `R-NN` register in `risks.md`):
  - **(a) memory staleness misroutes.** Mitigation: rebuild on `kb finish` +
    `doctor` staleness check (index older than newest KB file → warn). Detail in ADR-0015.
  - **(b) `doctor` false-negatives block migration.** Mitigation: additive/tiered
    checks; empty/legit area = warn, never error.
  - **(c) `onboard` moves/clobbers existing files.** Mitigation: `onboard` is
    add-only; assert no existing file is moved; second run is a no-op.
  - **(d) duplicate durable stores.** Mitigation: existing `decisions/` **is** the
    Decisions area and `direction.md`/`risks.md` **are** the State area — do not
    scaffold a second of either. State's authoritative home stays those existing
    files; memory only *points at* them, never mirrors, and there is no `state.md`.
  - **(e) `.memory` rebuild race.** The gitignored index lives *outside* ADR-0011's
    branch-merge protection (that layer guards only git-tracked KB); concurrent
    `kb finish` rebuilds race on `index.json`. Mitigation: atomic write-and-rename
    on rebuild; the index is idempotent/regenerable, so a lost race self-heals on
    the next rebuild. (Full handling in ADR-0015.)
  - **(f) shared-org-layer resolved via hard paths.** Mitigation: resolve through
    manifest `owns`/`domains`, never hardcoded paths.
- **Migration / implementation sequence:**
  1. Extend `templates/` + `scaffold.go` with the `playbooks/`, `experience/`,
     `candidates/` skeleton (empty + README each). No `state.md`.
  2. Extend `doctor.go` with tiered checks:
     - **warn** on absent/empty areas;
     - **warn** on candidates older than a defined staleness threshold (recent /
       pending candidates are healthy — never error on them);
     - **error** only on the structurally-enforceable case: an ADR whose status is
       `superseded by NNNN` where NNNN is absent or does not back-reference it.
     (Semantic "should have been marked superseded" is not mechanizable — omit.)
  3. `onboard`-migrate existing agents to the skeleton (add-only). Separately and
     **manually** (not `onboard`), reorg architect's flat files into areas where
     useful — this is the one place content moves; use `git mv` to preserve history.
  4. Write the retrieval-loop protocol into `agent-comms.md` + per-agent `CLAUDE.md`.
  5. (Deferred to ADR-0015) build `agentmesh memory rebuild|recall` once
     topic-extraction is spiked.
- **Follow-ups:**
  - **ADR-0015** — memory index schema + `memory` CLI (topic-extraction method,
    familiarity proxy, rebuild trigger + `.memory` race handling). Gates on a
    topic-extraction spike.
  - If memory recall later proves too coarse for routing, a *further* ADR gates any
    move to embeddings/vector search — that reintroduces the rejected hidden-store
    risk and needs its own decision.
