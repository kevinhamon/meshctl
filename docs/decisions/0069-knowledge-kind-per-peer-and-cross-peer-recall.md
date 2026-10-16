---
status: accepted
date: 2026-10-16
---
# 0069. Knowledge kind is declared per peer; canonical peers recall each other and classify conflicts at promotion

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16)
- **Related:** ADR-0064 (agents propose, humans confirm), ADR-0065 (one fact owner; `mesh://` references), ADR-0066 (pool repo, `pool/config.yaml`), ADR-0067 (confirmed-knowledge ledger, `--summary-file`), `internal/mesh/manifest.go` (`Agent`), `internal/mesh/toolref.go` (`PoolConfig`), `internal/mesh/recall.go` (`recall`, `RecallWithBodies`), `internal/mesh/kb.go` (`KBFinish`), `internal/mesh/doctor.go`, `GLOSSARY.md` (canonical knowledge, perspective knowledge, fact conflict, judgment difference)

## Context

`GLOSSARY.md` splits a peer's knowledge into **canonical** (part of the pool's one shared truth, must not contradict other canonical peers) and **perspective** (a viewpoint, free to contradict). ADR-0065 keys its rules on that split: `owns` uniqueness, link direction, which targets recall follows. Nothing declares the split yet, and nothing lets a peer see what other canonical peers already hold before it promotes a fact.

Verified facts:
- `Agent` carries `Owns` and `Domains`, no kind. `PoolConfig` carries name, created, doctrine version and tools, no defaults. The live pool has no `config.yaml` yet.
- `recall()` indexes only `computeTopics(agent)`: a peer's own KB plus its session records and git spine. `RecallWithBodies` inlines current-tier bodies within a token budget that defaults to zero. The session-start brief calls it with a fixed small budget.
- Tiers: `candidates/` is inbox; `decisions/` and `experience/` are log; everything else is current.
- `meshctl doctor` has per-peer KB consistency checks and nothing that reads across peers.
- The gate is pattern-only with a 10s hook timeout; it cannot judge whether two entries contradict.

## Options considered

**Declaration.**
1. Per-entry kind in frontmatter. Rejected: ADR-0066 kept glossary files free of metadata for the same reason; per-entry kind also makes the `owns` and link-direction rules unenforceable without parsing every file.
2. Per-tier kind (for example `experience/` always perspective). Rejected: the tier already says what an entry is for; judgment is never checked anyway, so no mechanism is needed to exempt it.
3. One kind per peer, with a pool default. Chosen.

**Cross-peer visibility.**
1. Leave it to the reconciliation sweep (ticket 15). Rejected: contradictions would be found only after promotion.
2. A pool-wide index that every peer searches. Rejected: breaks isolation as the default; recall would return perspective peers' entries.
3. A named recall scope that reads only canonical peers' current and log tiers, on demand and automatically at promotion. Chosen.

**Who judges a conflict.**
1. meshctl, by text similarity. Rejected: the gate is pattern-only and similarity cannot tell a contradiction from a restatement.
2. The agent, stating a classification in the summary the human reads. Chosen.

## Decision

- **Kind is declared with one key at two levels.** `knowledge: canonical | perspective` in `pool/config.yaml` is the pool default; the same key in `agent.yaml` overrides it for one peer. Absent at both levels means `canonical`. Doctor errors on any other value. `pool` is always canonical (ADR-0066).
- **One kind per peer.** A peer is wholly canonical or wholly perspective. A pool that needs both (a world bible and characters who misremember it) splits into peers. A canonical peer still holds opinions and experience; those are judgment and are never checked.
- **The roster shows kind.** `meshctl agent list` and the directory print it. The `owns`-uniqueness warning (ADR-0065) considers canonical peers only.
- **Recall scope.** `meshctl recall --scope canonical <query>` searches the peer's own KB plus every canonical peer's current and log tiers, read-only, hits tagged `via <peer>`. Default scope stays own-only. Peers declared perspective are never read, neither index nor bodies. A perspective peer may still call the scope, since it may reference canonical entries. Candidates, session records and the git spine of other peers are never read.
- **Unconfirmed entries are included.** Cross-peer hits whose ledger hash mismatches (ADR-0067) render `·unconfirmed` and rank below confirmed ones. Excluding them would return nothing until the reconciliation pass runs.
- **Budget.** At most 5 cross-peer hits per call. Bodies inline only with an explicit `--budget`; own-KB bodies spend it first, peer bodies take the remainder. The session-start brief never runs the cross-peer scope.
- **Automatic at promotion.** When a canonical peer runs `kb finish` and the finish is a promotion, meshctl runs the cross-peer scope over the promoted entries' topics and appends a **Related canonical entries** section (pointer plus first line, no bodies) to the ask text after the summary and the diff stat.
- **The agent classifies; meshctl lists.** The agent's `--summary-file` carries a **Coherence** section marking each related entry `consistent`, `conflict` or `judgment`. When related entries exist and the section is missing, the gate still asks, with "coherence not stated" in the ask text. The gate never judges meaning.
- **Fact conflict and judgment difference** (GLOSSARY). A fact conflict is two canonical entries asserting incompatible states of the same referent, which the ecosystem or the record can settle. A judgment difference is two peers evaluating, ranking or recommending differently, which nothing settles and which is never flagged. The test: can a human verify it against the ecosystem?
- **A conflict never blocks promotion by itself.** The human decides at the ask: deny and fix the proposing entry; confirm it and send a correction to the owner through the ask path, landing as a candidate (ADR-0065); or replace the proposing peer's copy with a `mesh://` reference. Nothing else is persisted. Sweep-side conflict reports belong to the reconciliation pass.
- **No hub exemption.** A hub session acting as a peer (ADR-0063) uses that peer's kind and roster view. Headless promotion already returns `needs-confirm` (ADR-0067); the related-entries section travels with the summary.

## Consequences

- Positive: the canonical/perspective split ADR-0065 depends on is now declared, visible and checked, with one key and no per-entry metadata.
- Positive: a canonical peer sees what other canonical peers hold before it promotes, and the human sees the agent's coherence claim in the same ask that confirms the content.
- Positive: isolation stays the default. Nothing is read across peers unless a canonical scope is asked for or a promotion is in flight, and perspective peers are never read.
- Negative: cross-peer scope computes each canonical peer's topic index on every call. On a sixteen-peer pool that is sixteen read-only directory walks; a cache is an implementation choice, not a rule here.
- Negative: the coherence classification is the agent's word. A wrong `consistent` passes the gate; only the human reading the related entries catches it.
- Negative: until the reconciliation pass (ticket 15), every related entry renders `·unconfirmed`, so the human weighs hypotheses against hypotheses.
- Follow-ups: rollout adds `knowledge:` to the live pool's `config.yaml` (all canonical) and the roster display; ticket 15 reuses the `consistent` / `conflict` / `judgment` vocabulary for its contradiction report; ticket 14's prototype should exercise `kb finish` with a related entry present and the Coherence section missing.
