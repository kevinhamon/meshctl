---
status: accepted
date: 2026-07-17
---
# 0019. External reference corpus → distilled `reference/` KB (expert priming)

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** maintainer
- **Related:** ADR-0014 (KB areas — this adds an optional `reference/` area), ADR-0016 (engine/contract/surface — raw corpus = a domain engine, reference notes = deliberate tier), ADR-0018 (distillation-with-provenance — same loop applied to a doc pile), ADR-0011 (`kb` write path); an earlier product ADR whose domain expert agent is the first consumer

## Context

Domain experts (e.g. a domain expert agent backed by a large regulatory corpus, or
datastore-expert agents) are only as good as the authoritative external material
behind them. The real workflow today: the user **fetches public reference docs** —
published rulebooks, protocol/operating manuals, regulatory filings — into the
agent's workspace (`domain-expert/pdf_downloads/` already holds 10 such documents:
protocols, operating guides, market/procedure manuals, filings, …). But nothing
turns that pile into **durable, recallable** knowledge. It sits as loose PDFs; the expert re-reads them
ad hoc, and insight never hardens into the KB or the memory index.

This is the same shape as claude-mem's `learn-codebase` (prime by reading every
source file), but for an external *document* corpus rather than code — and it must
land in our two-tier model, not a plugin.

Two forces specific to a large domain reference corpus make this load-bearing:
- **Rulebooks are versioned and dated.** A new dated edition (e.g.
  `April-1-2026-Protocols`) supersedes prior editions; a rule distilled from a
  stale edition is a defect source. Reference knowledge without an effective/version date silently rots.
- **Verify-against-source is the expert's prime directive** (the domain expert's
  CLAUDE.md: "don't assert protocol facts you haven't verified against a source"). That
  only works if the source-of-record is captured with a citation.

## Options considered

1. **Leave PDFs loose; re-read ad hoc.** Zero structure. Con: no recall, no
   citations, no version tracking; every session re-derives; facts asserted without
   a durable source. Status quo — insufficient.
2. **Commit raw PDFs into the KB.** Durable + self-contained. Con: heavy versioned
   binaries in git, re-fetchable from public sources anyway, and still undistilled —
   a 900-page protocol PDF is not recallable knowledge. Rejected (raw corpus is a
   disposable engine, like `.memory/` and fetched `files/`, both already gitignored).
3. **Distill into the existing `experience/` or `knowledge/` facts.** Reuses areas.
   Con: conflates hard-won *lessons* (experience) and internal *system facts* with
   external *authoritative rules* that carry citations + effective dates — different
   provenance and lifecycle. Muddies both.
4. **New `reference/` area: raw corpus gitignored, distilled cited notes committed,
   filled by a `prime-reference` skill.** Clean separation, recallable, versioned,
   review-gated. Chosen.

## Decision

Adopt Option 4. Add an **optional, tiered `reference/` KB area** for distilled
external-source knowledge, with a priming skill and a raw corpus that stays out of
git.

- **Corpus = disposable engine.** Fetched docs (`pdf_downloads/`, and any future
  per-expert fetch dir) are **gitignored** — big, versioned, re-fetchable from the
  public source. They are inputs, never authoritative on their own (mirrors ADR-0016:
  the engine is disposable; and `.memory/` + `files/` already gitignored).
- **`knowledge/reference/<topic>.md` = deliberate tier.** One note per topic,
  **committed**, indexed by `memory recall`. Each note is *distilled* (not a dump)
  and **cited**. Mandatory provenance frontmatter:
  `source:` (doc title) · `section:` · `effective:` (version/effective date) ·
  `url:` (public origin). Missing effective date = the note cannot be trusted as
  current; `doctor` may later warn on reference notes lacking it.
- **`prime-reference` skill** — the expert analog of `learn-codebase`: read the
  corpus in full → distill cited reference notes into `knowledge/reference/` →
  `kb finish` (ADR-0011) → memory rebuild (ADR-0015) so recall routes to them.
  Review-gated: raw docs never become truth automatically (ADR-0018 guardrails).
- **Area is optional/tiered** (ADR-0014): required only for doc-heavy agents
  (experts; architect for platform/system docs). Empty/absent `reference/` is a
  healthy state for agents that don't consume external corpora — `doctor` warns at
  most, never errors.

## Consequences

- **Positive:** experts' external source-of-record becomes durable, recallable, and
  version-aware; citations make "verify against source" enforceable; raw binaries
  stay out of git; reuses the existing engine/deliberate/distillation machinery — the
  skill is the only new component. Generalizes to every doc-heavy agent.
- **Negative / new risks:**
  - **Stale reference note** — a new rulebook edition supersedes what a note distilled.
    Mitigation: mandatory `effective:` date + `url`; re-prime on new editions; a
    `doctor` staleness check can compare against a known-current marker later.
  - **Distillation effort/discipline** — priming a large corpus is real work; it is
    review-gated by design (quality over volume). Not automatic.
  - **Corpus loss is acceptable** — gitignored PDFs can be lost, but they are
    re-fetchable from the public source and the committed notes carry the `url`.
- **Migration / sequence:**
  1. Add `reference/` scaffold (README) to the domain expert agent; gitignore `pdf_downloads/`. **Ships with this ADR.**
  2. `prime-reference` skill in the domain expert agent (`.claude/skills/`). **Ships with this ADR.**
  3. Follow-up: add `reference/` + a fetched-corpus gitignore entry to the agentmesh
     `templates/knowledge/` so `onboard` scaffolds it fleet-wide (optional tier); extend
     `doctor` with the `effective:`-date warn. (agent-mesh change — separate.)
  4. Roll the skill to the datastore-expert agents and architect as their corpora appear.
- **Follow-ups:** the `doctor` staleness/effective-date check; template rollout. Neither
  blocks the domain expert's adoption now.
