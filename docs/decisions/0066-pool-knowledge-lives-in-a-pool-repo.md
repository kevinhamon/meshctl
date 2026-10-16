---
status: accepted
date: 2026-10-16
---
# 0066. Pool knowledge lives in a visible `pool/` git repo; meshctl commits, humans push

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16)
- **Related:** ADR-0031 (pools are created by the binary; `.agentmesh/pool.yaml` marker), ADR-0039 (agent repo hygiene: persistent nag until a remote exists), ADR-0062 (KB-rule gate), ADR-0064 (agents propose, humans confirm), ADR-0065 (one fact owner per canonical fact; `mesh://` references), `internal/mesh/paths.go` (marker path), `internal/mesh/gitutil.go` (`RepoNag`), `internal/mesh/recall.go`, `GLOSSARY.md` (pool knowledge, pool glossary, fact owner)

## Context

A pool has knowledge that belongs to no single peer: the pool glossary (the one vocabulary every peer uses), the pool's local doctrine (house rules), and pool-wide settings. Today that knowledge sits in the pool root's `.agentmesh/` directory, next to the binary-managed pool marker and runtime state. The directory is not a git repo. Nothing in it has history, a remote, or review, and it is lost with the machine. ADR-0039 solved the same problem for peers by giving each one a repo and a nag; pool knowledge was left out.

ADR-0064 and ADR-0065 make the problem sharper. Every canonical fact needs one owner and one place to confirm it. Glossary terms are canonical facts, so they need a home that can be referenced (`mesh://<owner>/<path>#<anchor>`), written only through a confirmation, and recovered if the machine dies.

Verified facts:
- `.agentmesh/` holds `pool.yaml` (binary-managed identity), `doctrine/` (emitted `agent-comms.md` and `distiller.md`, human-authored `local.md`), `dispatch.*` runtime files and `worktrees/` (git worktrees of peer repos used by `kb begin` and delegated work).
- The marker path is one constant in `internal/mesh/paths.go`. Pool discovery walks up from a peer looking for it.
- `RepoNag` checks only whether a peer is a repo with a remote. Nothing checks dirty or unpushed state. Peers never auto-push; the human pushes.
- Recall indexes only the calling peer's `knowledge/`. Only two peers in the live pool have a `knowledge/glossary.md`, and both hold pool-wide vocabulary.
- The live pool root also holds repos that are not peers (the meshctl source, personal repos, an MCP stack).
- No interactive session ever runs in the pool root. Every write there comes from a peer session or from meshctl itself.

## Options considered

1. **Leave it.** Pool knowledge stays unversioned. Rejected: the highest-leverage knowledge in the mesh is the least protected.
2. **Make `.agentmesh/` a git repo as is.** Smallest change. Rejected: a hidden directory with history is forgotten; nobody opens it, nobody notices it is unpushed.
3. **Split: a visible `pool/` repo for human-owned files, `.agentmesh/` kept for machine-owned ones.** Rejected: two directories for one concept. A `.gitignore` draws the human/machine line just as well.
4. **Pool as a monorepo of all peers.** Onboarding gets simpler and cross-peer edits become one commit. Rejected: the live root holds non-peer repos; `kb begin/finish`, delegated-work worktrees, hygiene and the recall time-spine all assume repo root = peer root; per-peer remotes, identities and access disappear; sixteen histories would have to merge into one. Consolidation was already rejected once in this effort.
5. **Rename `.agentmesh/` to `pool/` and make that directory the repo**, with runtime state ignored. One directory, one marker, one repo, visible.

## Decision

Option 5.

- **`pool/` is the pool directory and a git repo.** Tracked: `pool.yaml`, `doctrine/` (emitted files and `local.md`), `glossary.md`, `config.yaml`. Ignored: `dispatch.*`, `worktrees/`. Peer worktrees stay nested there; each worktree's own `.git` file governs commands run inside it.
- **Discovery.** `pool/pool.yaml` is the marker. `.agentmesh/pool.yaml` is recognised as legacy until `pool upgrade` renames it, initialises the repo, writes `.gitignore` and makes the first commit. Peers' rendered pointers follow in their next re-onboard.
- **Human-owned pool settings go in `pool/config.yaml`.** `pool.yaml` stays binary-managed identity. Emitted doctrine is tracked so an upgrade shows as a reviewable diff instead of a silent overwrite.
- **`pool` is a reserved owner.** Every glossary term is canonical with fact owner `pool`, referenced as `mesh://pool/glossary.md#<slug>`. The anchor is the kebab slug of the term; the resolver matches a `**Term**:` line or a heading, case-insensitive. Doctor rejects a peer named `pool`.
- **Writes are confirmed at the gate.** Any Edit, Write or Bash path under `pool/` triggers the gate ask carrying the shared-understanding summary (intent, grounding, precedent). The ask is the confirmation; there is no `kb begin/finish` for `pool/` and no candidates detour. Delegated and headless runs are denied and return `needs-info`.
- **meshctl commits, humans push.** A PostToolUse hook commits each confirmed agent write with the user's git identity, a subject such as `glossary: <term>` or `doctrine: <section>`, and the peer and session in the body. meshctl commits its own writes the same way (`doctrine: upgrade to <version>`). Agents never run git in `pool/`. `RepoNag` extends to `pool/`: every peer's session start prints one persistent line while `pool/` is not a repo, has no remote, is dirty, or is ahead of its remote. Doctor, `hygiene` and `pool upgrade` report the same finding. No auto-push, no cron.
- **Glossary format and surfacing.** Pocock's glossary format unchanged (`**Term**:`, definition, optional `_Avoid_:`), H2 groups allowed, no implementation detail inside entries. Recall indexes `pool/glossary.md` as a current-tier source for every peer, marked `via pool`, ranked and budgeted like the peer's own current tier. The peer CLAUDE.md template points domain docs at `../pool/glossary.md` and the peer's `knowledge/decisions/`.
- **Peer glossaries retire.** `knowledge/glossary.md` leaves the KB template. Existing files migrate by hand, entry by entry, after the reconciliation pass. Doctor then warns on any peer glossary, and warns softly when an `owns` value has no matching glossary term.

## Consequences

- Positive: pool knowledge gets history, a remote and review with no change to peer repos, `kb` plumbing or delegated work.
- Positive: one place for everything the pool owns, visible in a directory listing, and the clobber of `local.md` on upgrade becomes impossible by construction (a tracked, meshctl-committed diff).
- Positive: `mesh://pool/...` references resolve to a real path, and the reserved owner slots into ADR-0065 without a new concept.
- Negative: one more repo for the human to push, with no session of its own to remind them. The nag on every peer is the mitigation; the human is still the single point of failure for durability, as with peers.
- Negative: every existing pool on a machine runs through `pool upgrade` once; the legacy marker path must be kept until all have.
- Negative: committing from a hook makes `pool/` history fine-grained (one commit per confirmed edit). Acceptable: the glossary changes rarely, and the history is the audit trail.
- Open (to be settled by later tickets): how confirmation is recorded on a glossary term in a format that stays free of metadata; which keys `config.yaml` carries for outbound-write patterns and the canonical/perspective default; the migration of the two existing peer glossaries.
