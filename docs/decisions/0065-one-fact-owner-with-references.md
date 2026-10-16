---
status: accepted
date: 2026-10-15
---
# 0065. One fact owner per canonical fact; other peers hold references, and recall follows them one hop

- **Status:** accepted
- **Date:** 2026-10-15
- **Deciders:** maintainer (accepted 2026-10-15)
- **Related:** ADR-0064 (agents propose, humans confirm), `internal/mesh/manifest.go` (`owns`, `domains`), `internal/mesh/recall.go` (`recall`, `RecallWithBodies`), `internal/mesh/memory.go` (manifest tags), `GLOSSARY.md` (fact owner, canonical knowledge, knowledge reference)

## Context

Peers in a pool keep separate knowledge bases, and recall reads only the calling peer's own KB. That isolation is deliberate: a role's judgments stay its own. It also means the same fact can be written independently by several peers, drift, and contradict. Once ADR-0064 makes the human the only one who confirms truth, the pool still needs a rule for *where* a confirmed fact lives, so there is one place to confirm it and one place to change it.

Verified facts:
- `agent.yaml` already carries `owns` and `domains` per peer. The code uses both only as recall-scoring tags and for directory display. Neither means ownership today.
- In one live pool, `owns` and `domains` overlap between peers in several places (an estate-wide architect and a sub-estate architect both claim the same area; a tracker owner and the implementer both claim the tracker).
- KB entries are topic files holding many facts each, not atomic facts.
- `recall()` builds its index from `computeTopics(agent)` only. There is no cross-peer read path.

The pool must stay agnostic to its kind. A pool of novel characters wants peers that contradict each other on purpose, so the rule applies to canonical knowledge only (see `GLOSSARY.md`).

## Options considered

1. **No ownership; the reconciliation sweep catches contradictions.** Cheapest, but every contradiction is found after the fact, and there is still no answer to "which copy is right".
2. **A pool-level shared KB for all canonical facts.** One place to confirm, but it removes the role-owned judgment the pool exists for, and the pool root is not a repo today.
3. **One fact owner per fact, decided by a new ownership schema** (a registry of facts to peers). Precise but a second source of truth to keep in sync with the KBs.
4. **One fact owner per fact, where the owner is simply the peer whose KB holds the confirmed entry.** `owns` becomes the routing declaration for new facts; other peers hold references, not copies; recall follows a reference one hop.

## Decision

Option 4.

- **Granularity is the entry.** Ownership attaches to a KB entry (a file, or a heading in it), never a sentence. A file that mixes two owners' facts gets split.
- **Owner is whose KB holds the entry.** No separate registry. `owns` in `agent.yaml` routes: a canonical fact proposed under another peer's `owns` is flagged in the confirmation summary and sent to that peer through the ordinary ask path, landing as a candidate in the owner's KB (ADR-0064 headless rule). `domains` keeps its current meaning, interest for recall scoring, and may overlap freely.
- **`owns` tags are unique across canonical peers in a pool.** `meshctl doctor` warns on duplicates. Perspective peers are exempt.
- **Uncovered facts default to the proposer.** The confirmation summary says no declared owner covers it; the human may redirect at confirm time.
- **References, not copies.** A peer that needs another peer's canonical fact holds a markdown link, `[gist](mesh://<peer>/<kb-relative-path>#<heading>)`. The gist is for readability and is never confirmed knowledge; the owner's entry is authoritative.
- **Recall follows references one hop.** Search stays scoped to the peer's own KB. When a hit's body contains a `mesh://` link, body expansion fetches the target entry, read-only, marked as coming via the owner. Only canonical targets are followed, and never transitively.
- **Direction.** A perspective peer may reference canonical entries. A canonical entry never references a perspective entry; doctor flags it.
- **Deletes, renames and moves are human-confirmed.** The `kb finish` summary lists inbound links (a pool-wide, read-only grep) before a confirmed entry is removed or renamed. Moving a fact to a new owner is three confirmed steps: confirm at the new owner, delete at the old, rewrite inbound links. Neither the sweep nor a headless run does any of it. Dangling links are flagged by doctor, never auto-fixed.

## Consequences

- Positive: every canonical fact has exactly one place to confirm and one place to change. Contradiction between canonical peers becomes a detectable copy rather than two equally plausible versions.
- Positive: no new schema. Ownership is derivable from where entries live plus the existing `owns` field.
- Positive: isolation stays the default. A peer still cannot search another peer's KB; it can only read what a confirmed reference points at.
- Negative: existing pools carry overlapping `owns` tags and copied facts. Both surface as doctor warnings and reconciliation work, not as breakage.
- Negative: references make KB entries depend on other repos. A renamed or deleted owner entry breaks readers until the dangling link is fixed by hand.
- Negative: one-hop expansion adds cross-repo reads to body recall, bounded by the hop limit and the recall budget.
- Open (to be settled before implementation): how the ask path carries a routed proposal so the owner's human can confirm it in context; what a peer shows at session start so routed candidates do not sit unseen; whether `owns` values must be pool-glossary terms.
