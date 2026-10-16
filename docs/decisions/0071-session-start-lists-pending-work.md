---
status: accepted
date: 2026-10-16
---
# 0071. Session start lists a peer's pending work in one persistent block; the agent offers triage, the human decides

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16)
- **Related:** ADR-0039 (repo hygiene nag), ADR-0043 (intake is ephemeral; close = distill then archive), ADR-0063 (delegated work branches, `WorkNag`), ADR-0064 (agents propose, humans confirm), ADR-0066 (`pool/` repo nag), ADR-0067 (`needs-confirm` KB branches, unconfirmed ledger count), ADR-0068 (headless denies), ADR-0070 (reconcile report, open conflicts), `internal/mesh/hook.go` (`HookSessionStart`), `internal/mesh/work.go` (`WorkNag`), `internal/mesh/respond.go` (`ListSent`, `SentMarkdown`), `GLOSSARY.md` (pending work)

## Context

Several decisions in this series park work for a human: cross-peer proposals land as candidates (ADR-0065), headless promotions park as `needs-confirm` branches (ADR-0067), reconcile reports leave open conflicts (ADR-0070), and intake requests wait in the owner's inbox (ADR-0043). Nothing shows a peer's own waiting work at session start, so it rots. One live pool accumulated dozens of pending intake requests nobody saw.

Verified facts:
- The SessionStart hook injects a fixed sequence: identity, peers, overlap warning, repo nag, delegated-work nag, read-back of answers to the peer's own sent requests, tool reference, state digest, handoff. No overall cap.
- The peer's own pending inbox is never injected; only `meshctl inbox list` shows it on demand.
- Candidates have a 14-day age check in doctor and consolidate, not at session start. Doctor flags KB and work branches at 7 days.
- A SessionStart hook can inject text only. It cannot prompt or block.
- Headless distill runs get no session-start block. Subagents get no hook at all, only `meshctl agent brief`. Dispatched `claude -p` peers fire the hook and receive every nag today.
- The existing nag pattern is one line, recomputed every call, gone when the condition clears; `WorkNag` lists one item inline and gives a count plus command above one.

## Options considered

**Shape.**
1. Keep adding one nag paragraph per new kind, in the current order. Rejected: six unrelated paragraphs with no total.
2. One `Pending work` block with one line per kind, absorbing the delegated-work nag and the read-back list. Chosen. Repo-safety warnings stay separate: they are hygiene, not work.

**Blocking.**
1. The block asks the human to triage before work. Rejected: a hook cannot prompt, and a mandatory triage step is how humans learn to skip the block.
2. Informational block plus a standing order in core prose: offer the list, human picks triage now, some, or defer. Chosen.

**Thresholds.**
1. Every kind at count one, no exceptions. Rejected: the unconfirmed-entry count is large on every peer until the reconciliation pass runs, and a line that is always there is wallpaper.
2. Configurable thresholds in pool config. Rejected: a knob to silence the queue.
3. Structural: action items at count one; health counts collapse into one line. Chosen.

**Deferral.**
1. `snooze <id> --days N`. Rejected: snooze is how a queue becomes a graveyard again.
2. No snooze; deferral is a handoff line and the item returns next session. Chosen.

**Headless and in-session audiences.**
1. Show a one-line count so a hub human sees the peer's debt. Rejected: no one in those sessions can act on it (ADR-0067, ADR-0068), and the hub has a CLI view instead.
2. Omit the block everywhere except the peer's own interactive session. Chosen.

## Decision

- **Inventory.** Pending work is: KB branches parked `needs-confirm` with their summary file; open conflicts and days since the latest reconcile report; the peer's own inbox requests with `status: pending`; answers awaiting read-back on requests the peer sent; delegated work branches awaiting merge; and a KB health line with the unconfirmed current-tier count and the candidate count, with how many candidates are past 14 days. Dangling `mesh://` references are excluded: they need a pool-wide read and stay in doctor. Every item here is one directory listing or one hash pass over the peer's own knowledge base.
- **One block.** `# Pending work (N items)` is injected where the delegated-work nag sits today, before the tool reference and state digest. One line per kind, in this order: KB branches, open conflicts, own pending intake, read-back, delegated work, KB health. Each line carries the count, the oldest item's age and the command that clears it. `WorkNag` and the read-back section move into the block. The `⚠` repo-safety warnings for the peer and for `pool/` stay separate above it.
- **Listing cap.** Up to three items per kind are listed inline with id, requester or branch, and age. Above three, the line shows the count, the oldest age and the list command. Zero items of every kind means no block.
- **Thresholds are structural.** Action kinds (KB branches, conflicts, intake, read-back, delegated work) appear at count one. The KB health line appears only when candidates exist or a reconcile report exists. No threshold is configurable.
- **One age threshold.** An item older than 14 days renders with `⚠ stale`. The same 14 days applies to intake, KB branches, work branches and candidates; doctor's 7-day branch warning moves to 14. Nothing auto-archives: intake still closes by distill then archive (ADR-0043).
- **Standing order, prose only.** The template `CLAUDE.md` and the mesh-cognition retrieval loop gain a step before new work: when the block is present, read it to the human and offer triage now, a subset, or deferral. The human decides; the agent never triages alone. A deferral is one `Deferred: <ids>` line in the handoff. There is no snooze: the items return at the next session start. Pools may tighten this in `local.md` prose; they cannot loosen it.
- **Raw facts only.** The block states id, source, age and the clearing command. It does not guess whether a request was already addressed elsewhere; that judgment belongs to the intake triage pass (a later decision).
- **Audience.** The block is omitted when `AGENT_DISPATCH_DEPTH` or `AGENT_MESH_DISTILLING` is set and from `meshctl agent brief`. It appears only in the peer's own interactive session, the one place every listed item can be acted on.
- **CLI view.** `meshctl pending` prints the same block for the current peer; `meshctl pending --all` prints it per peer across the pool. This is the list command the block's lines point at and the human's cross-peer view; it is never injected.

## Consequences

- Positive: every kind of parked work this series creates has one place to appear, the same for every peer, and it cannot be silenced short of doing the work.
- Positive: nothing changes for headless or delegated runs; their context stays small and they never see work they cannot do.
- Positive: the human gets a pool-wide view from the shell without opening sessions.
- Negative: a peer with a large unconfirmed count sees the health line every session until the reconciliation pass runs. Accepted: that is the pressure ADR-0070 relies on.
- Negative: the standing order is prose, so a session can ignore the block. The block's persistence is the mitigation; a missed item is there again next time.
- Follow-ups: the block renderer, `meshctl pending`, the doctor age change and the template wording are implementation work. Deciding when a pending request is superseded or already done is the intake triage pass.

## Amendment (2026-10-16): one triage-signal clause on the intake line

ADR-0072 defines the intake triage pass and its signals. The "raw facts only" rule stands for judgment, with one addition to the rendering: when any of the peer's open requests has an exact, cheap triage signal (its id named in a `mesh/work/*` branch on any peer, or a `redirected_to` target that has closed), the intake line gains the clause `N have triage signals, run meshctl inbox triage`. No per-item tags, no "possibly superseded" wording; the worksheet and the agent's judgment stay in the pass.
