---
status: accepted
date: 2026-07-15
---
# 0011. Agent-mesh concurrency: intake claims, session presence, KB branch-merge

- **Status:** accepted
- **Date:** 2026-07-15
- **Deciders:** maintainer
- **Related:** ADR-0010 (agentmesh CLI, manifests, batch dispatch — this extends it); `bin/ask-agent` single-flight lock (precedent); MESH-CLI-SPEC.md

## Context

Multiple Claude sessions frequently run **in the same agent workspace at the same
time** (e.g. two `~/agents/architect` sessions). Nothing today coordinates them:

- **Race A — intake double-pickup.** Two sessions both read a request as
  `status: pending` and both start it → duplicated work, racing writes to the
  same request file. The `status` field cannot serve as the lock: both read
  `pending` then both write `in-progress` (TOCTOU); file writes are not atomic
  across readers.
- **Race B — KB concurrent edit.** Two sessions edit the same ADR/risk/doc file
  in the shared working tree → last-write-wins, silent lost update.

The mesh's existing `.dispatch.lock` does **not** help: it serializes only
*headless dispatch* (`ask-agent`); interactive human-driven sessions bypass it
entirely. Coordination must be a first-class layer.

Key mechanical fact: a git **branch** does not isolate concurrent sessions in one
directory — they share a single working tree and checked-out branch, so
`checkout -b` in one disrupts the other. Only a git **worktree** (its own
directory + branch) isolates.

## Options considered

**Race A (intake claim)**
1. **Status-field transition as the claim** — racy (TOCTOU), rejected as the authority.
2. **Atomic claim sidecar (chosen)** — `O_EXCL`/`mkdir` create of `<id>.claim`; first writer wins, atomic on POSIX. Lease with heartbeat + stale reclaim (reuses the `.dispatch.lock` pattern). The `status` field remains a human-readable display; the sidecar is the authority.

**Race B (KB edit)**
1. **Hard-route every KB edit through the CLI (flock)** — true mutual exclusion but fights the agent's native Edit tool and adds friction to every write. Rejected.
2. **Git-only, no isolation** — rely on frequent commits; lost-update window on the uncommitted working tree stays open. Rejected.
3. **Worktree + branch per write, merge back (chosen)** — a session that intends a KB mutation works in its own git worktree on its own branch, then merges to `main`. Concurrent writers are isolated; merge serializes them and surfaces real collisions as conflicts to resolve *before proceeding*. Turns the race into git's well-understood merge problem on the layer that is already git-backed.

**Scope split**
- **Coordination/ephemeral state** (intake claims, session presence, heartbeats,
  dispatch lock) → shared filesystem, **atomic locks, never branched.** (Branching
  claims would hide them from concurrent sessions until merge — strictly worse.)
- **Durable knowledge** (ADRs, risks, docs) → **git worktree + branch + merge.**

## Decision

Add a concurrency-control layer to `agentmesh`, built on one substrate and two
mechanisms.

### Substrate — session identity + presence registry
Each live session registers under `$AGENTS_DIR/<agent>/.sessions/<session-id>`
with `{pid, started, last_beat, current_claim, kb_branch?}` and heartbeats. Session
id from `$CLAUDE_SESSION_ID` (or minted), set by a **session-start hook**. On start
the hook registers presence and **loudly surfaces any other live session in the
same workspace** plus outstanding claims — so an accidental double is caught before
work begins, not after.

### Race A — atomic intake claims
- Claim = atomic create of `<request>.claim` next to the request, holding
  `{owner_session, pid, heartbeat}`. Loser sees it exists and skips.
- Lease with heartbeat; stale reclaim after `AGENT_DISPATCH_STALE_SEC` (900s),
  never reclaiming a claim whose session still heartbeats.
- `agentmesh inbox next` atomically claims the next `pending` and returns it owned
  by the caller's session. `agentmesh claim/release <id>`. The request `status`
  still transitions for humans; the sidecar is the mutual-exclusion authority.
- Never branch intake — it lives on the shared filesystem.

### Race B — KB branch-merge on write-intent
- **Reads:** direct, no ceremony (the common case).
- **Writes:** `agentmesh kb begin` creates a worktree at
  `$AGENTS_DIR/.worktrees/<agent>-<session>` on branch `session/<session-id>` and
  points the session there; edit + commit; `agentmesh kb finish` rebases onto
  `main`, merges, and **removes the worktree**.
- **Conflicts are resolved before proceeding.** Interactive session resolves them.
  A **headless dispatched run does NOT auto-resolve** — it leaves the branch intact
  and sets the request `status: needs-human` (consistent with ADR-0010 /
  `ask-agent`'s anti-cascade rule); the human drives the merge.
- Known hotspot: concurrent new ADRs both take the next `NNNN` and both append an
  index row → add/add conflict at merge; resolver renumbers the second. Rare,
  acceptable.

### CLI surface (adds to ADR-0010)
| Command | Purpose |
|---|---|
| `agentmesh session start\|end\|list` | Register/deregister presence; list live sessions + claims; warn on overlap. |
| `agentmesh inbox next [--claim]` | Atomically claim + return the next `pending` request. |
| `agentmesh claim\|release <id>` | Explicit claim lifecycle. |
| `agentmesh kb begin\|finish [--abort]` | Worktree+branch for a KB write, then merge back to `main` and clean up. |

`agentmesh ask` (ADR-0010) claims the target request for its dispatched session so
a concurrent interactive session can't double-process it.

## Consequences

- **Positive:** intake double-pickup is impossible (atomic claim); accidental
  concurrent sessions are loud, not silent (presence hook); KB collisions become
  git conflicts resolved deliberately, never silent lost updates; reuses existing
  primitives (lock pattern, needs-human, git).
- **Negative / cost:** a KB write now carries worktree+branch+merge overhead
  (negligible for an infrequently-written KB, and only on write-intent, not reads);
  the ADR-number/index-row hotspot needs manual renumber on the rare concurrent
  ADR; requires a session-start hook per agent to register presence + heartbeat.
- **Migration:** implement after the ADR-0010 core CLI; add the session-start hook
  to each agent's settings during `agentmesh onboard`; keep `bin/ask-agent` until
  `agentmesh ask` (with claiming) reaches parity.
- **Follow-ups:** open a risk if heartbeat staleness proves fiddly on macOS; revisit
  if headless KB writes hit `needs-human` often enough to warrant a serialized
  KB-write queue instead of optimistic branch-merge.
