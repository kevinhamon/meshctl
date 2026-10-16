---
status: accepted
date: 2026-10-16
---
# 0070. Existing knowledge is reconciled by an agent-written report and confirmed in bulk, five entries at a time

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16)
- **Related:** ADR-0064 (agents propose, humans confirm), ADR-0065 (one fact owner; `mesh://` references), ADR-0066 (pool repo, `pool/config.yaml`, doctrine), ADR-0067 (confirmed-knowledge ledger, confirmation round, atomicity), ADR-0068 (`mutates` tags), ADR-0069 (canonical scope, `consistent` / `conflict` / `judgment`), `internal/mesh/kb.go`, `internal/mesh/recall.go`, `internal/mesh/doctor.go`, `GLOSSARY.md` (reconciliation pass, reconcile report, ungrounded claim)

## Context

ADR-0067 made confirmed knowledge a content-hash ledger that starts empty, so every entry in every existing knowledge base reads as a hypothesis until something confirms it. ADR-0069 checks coherence only at promotion and left sweep-side conflict reports to this pass. Knowledge bases written before these rules may contradict each other, contradict pool doctrine, or assert facts nobody grounded. Standing orders (`CLAUDE.md`) may contradict doctrine too.

Verified facts:
- The ledger is per file, per section for the template's sectioned files (`state.md`, `glossary.md`). It is written only by `kb finish` after a fast-forward merge.
- `kb finish` refuses more than one promotion ask per finish; the first live run put four entries in one round and the human asked for smaller batches.
- The gate is pattern-only with a 10s hook timeout. meshctl cannot judge whether two passages contradict.
- Headless and delegated runs have no Bash and no git, and return `needs-confirm` on promotion.
- Doctor already reports the unconfirmed current-tier count and accepted-but-unconfirmed ADRs, per peer.

## Options considered

**What the sweep reads.**
1. Each knowledge base alone. Rejected: cross-peer contradictions are the incident class.
2. Knowledge bases plus a full check of every claim against the ecosystem. Rejected: open-ended work over every repository, and grounding an uncited claim is a human judgment.
3. Within-peer and cross-canonical in full, ecosystem only as a citation-resolve check, uncited claims flagged. Chosen.

**Who writes the verdicts.**
1. meshctl, by text similarity. Rejected: same reason as ADR-0069; similarity cannot separate a contradiction from a restatement.
2. A headless dispatch of the peer. Rejected: no Bash, no git, and the result would still need an interactive session to confirm.
3. meshctl builds a worksheet and validates grammar; an agent in an interactive session writes the verdicts with quoted grounding. Chosen.

**Fate of entries never confirmed.**
1. Expire to candidates after a period. Rejected: moving a long reference file into the inbox destroys its structure and improves nothing.
2. Delete. Rejected: destroys knowledge over a process gap.
3. Stay in place, rendered unconfirmed, with doctor and the session-start list applying pressure. Chosen.

**Walk order across peers.**
1. A list in core. Rejected: pool-specific; meshctl stays agnostic to pool type.
2. A rank computed from pool facts, overridable in pool config. Chosen.

## Decision

- **The reconciliation pass is on demand and incremental.** `meshctl kb reconcile [--peer <name>]` runs when asked, never on a schedule. The first run per peer is the bulk pass; later runs skip pairs whose entries are confirmed and unchanged in the ledger. Without `--peer` it prints the walk order (below).
- **Inputs.** For one peer: every current- and log-tier entry; every other canonical peer's current and log tiers through the canonical recall scope (ADR-0069); pool doctrine; the peer's own standing orders (`CLAUDE.md`). Perspective peers are never read. Doctrine and standing orders are conflict inputs only, never confirm targets: pool files are confirmed by construction (ADR-0066).
- **Ecosystem check is citation-resolve only.** Every reference an entry cites (`mesh://`, repository path, URL) is marked resolved or unresolved. An entry with no citation for a factual claim is `ungrounded`. meshctl does not verify a claim's content against the ecosystem; the human does, in the round.
- **meshctl builds a worksheet; an agent writes the report.** The worksheet holds, per entry, the body, cross-canonical hits, citation results and any doctrine or standing-order passage that shares its terms. An agent in an interactive session (the peer's own, or a hub session doing the peer's work, ADR-0063) writes the **reconcile report** at `<peer>/.memory/reconcile/<date>.md`: one line per entry with a verdict and a quoted grounding. meshctl validates the grammar and rejects a verdict line with no quote. The report is runtime state, never committed, never promoted, and is itself a hypothesis.
- **Verdict vocabulary.** `consistent`, `conflict`, `judgment` (ADR-0069), plus `ungrounded` (factual claim with no resolvable citation) and `term-vs-fact` (a glossary entry that asserts a fact instead of defining a term). A conflict line names the counterpart as a `mesh://` link and quotes both claims. A knowledge entry contradicting doctrine is `conflict → doctrine`; doctrine wins. Standing orders contradicting doctrine are listed in a separate **Standing orders** section at the top of the report and are fixed by hand, with no ledger involvement.
- **Bulk confirm.** `meshctl kb confirm <path>... --report <file>` runs on main, no `kb begin` branch. It writes the ledger entries and the `Confirm:` commit in one step (ADR-0067 atomicity). The gate asks on it as on `kb finish`: a confirmation round in chat first, the permission prompt as signature listing the paths, the report as the summary file. **At most five paths per call**; meshctl refuses more. A sectioned template file is addressed as `<path>#<heading-slug>`; a bare path means every section and counts as one path. Every other file is one whole-file unit.
- **Refusals.** `kb confirm` refuses a path whose latest report verdict is `conflict`, `ungrounded` or `term-vs-fact`. `ungrounded` is overridable per path with `--accept-ungrounded <path>` after the human has overridden it in the round. `term-vs-fact` is cleared by splitting the entry in the same session: the fact part moves to a fact tier, the term part stays a definition, both are then confirmed. The glossary migration (ticket 17) takes only clean terms.
- **Cross-peer conflicts resolve in the fact owner's session only.** Both peers' reports list the conflict. The owner (ADR-0065 `owns`; when silent, the peer whose entry has the earlier git commit date) picks one of ADR-0069's outcomes: keep and send a correction candidate, replace the other copy with a `mesh://` reference, or fix its own entry. The non-owner's line reads `conflict → owner <peer>, pending` and its `kb confirm` refuses the path until the owner's ledger confirms.
- **Walk order is computed, not named.** Rank peers by: `mutates` surfaces declared in `agent.yaml` (ADR-0068), more first; conflict count in the latest reports, with conflicting pairs walked interleaved; inbound `mesh://` link count; unconfirmed entry count. Perspective peers are skipped. `pool/config.yaml` `reconcile.order:` overrides the rank. Inside a peer the walk follows the template tiers: `state` → `handoff` → `glossary` → `decisions` → `architecture` / `reference` → `playbooks` → `experience`. A session is about five rounds of five.
- **Never-confirmed entries stay.** No expiry, no automatic move. Recall keeps rendering them `·unconfirmed` with the `_hypothesis_` prefix (ADR-0067).
- **Doctor reports, never sweeps.** Per peer: unconfirmed count (existing), days since the latest reconcile report, and that report's open-conflict count. Doctor does not read across peers.

## Consequences

- Positive: existing knowledge bases get a path to confirmed status that uses the same confirmation round and ledger as new promotions, with the same human in the loop.
- Positive: contradictions between canonical peers, and between knowledge and doctrine, are found and listed before anyone builds on them, with the owner deciding once.
- Positive: nothing pool-specific enters core. Order, surfaces and owners come from pool facts or pool config.
- Negative: the verdicts are an agent's word. A wrong `consistent` is caught only by the human reading the quoted grounding in the round.
- Negative: five paths per call makes a large knowledge base many sessions of work. That is the cost of reading rather than rubber-stamping.
- Negative: the owner tiebreak by commit date is arbitrary when two peers wrote the same fact independently; a pool fixes that by declaring `owns`.
- Follow-ups: `kb confirm`, `kb reconcile`, the worksheet and report grammar, the refusal rules and the doctor fields are implementation work; rollout for a live pool is pool fog on the map, not core.
