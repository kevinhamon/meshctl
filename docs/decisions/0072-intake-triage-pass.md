---
status: accepted
date: 2026-10-16
---
# 0072. Stale inbox requests are closed by an agent-written triage report and per-item confirmation

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16)
- **Related:** ADR-0043 (intake is ephemeral; distill then archive), ADR-0063 (hub session does a peer's work in-session; consults are read-only), ADR-0064 (agents propose, humans confirm), ADR-0067 (confirmation round), ADR-0070 (reconciliation pass; report grammar, five per call, computed walk order), ADR-0071 (session-start pending-work block, raw facts only), `internal/mesh/intake.go`, `internal/mesh/respond.go`, `internal/mesh/hook.go`, `GLOSSARY.md` (intake triage pass, triage report)

## Context

ADR-0071 lists a peer's pending inbox requests at session start as raw facts and decides nothing about whether a request still stands. Requests go stale for reasons the owner cannot see from the request file: the human addressed the ask another way in another peer, other work usurped it, or the requester moved on. Left alone they sit in the block forever, with `⚠ stale` after 14 days, and the only clearing paths are to answer or decline them by hand.

Verified facts:
- Intake status vocabulary is `pending`, `in-progress`, `needs-info`, `needs-human`, `answered`, `filed`, `declined`, with aliases. `filed` and `declined` close an exchange without an answer owed.
- Intake is meshctl-owned: the PreToolUse gate blocks hand edits and `inbox respond` (`RespondRequest`) is the only sanctioned write-back. It stamps `responded`, which puts the request on the requester's read-back list (`ListSent`, `SentMarkdown`). `ArchiveClosed` waits for read-back unless the requester is `user` or the owner itself.
- A request file carries `related:` ids, and a redirecting decline stamps `redirected_to: <peer>:<id>`. Delegated work (ADR-0063) stamps `mode: work` and `worktree:`, and runs on branch `mesh/work/<request-id>`.
- An in-session consult (ADR-0063) is read-only and costs one model run. A headless ask writes a new intake file.
- The reconciliation pass (ADR-0070) already established the shape: meshctl builds a worksheet, an agent in an interactive session writes a report with quoted grounding, the human confirms in a round, and a gate-asked command applies at most five items per call.

## Options considered

**Verdict → intake status.**
1. New closing statuses `superseded` and `withdrawn`. Rejected: every status consumer (read-back, archive, doctor, hygiene) would need the new values, and `filed` already means "closed, no answer owed".
2. Map onto the existing vocabulary and add a `triage:` frontmatter stamp so meshctl can tell a triage close from an answer without parsing prose. Chosen.

**How the owner checks in with other agents.**
1. Headless ask to the requester. Rejected: it creates new intake traffic to triage intake, and the answer arrives in a later session.
2. In-session consult of every requester, every pass. Rejected: a model run per requester when the worksheet usually already answers the question.
3. Worksheet first (free: KB, handoff, branches, sibling requests), consult only when the worksheet is inconclusive and the requester is a peer, one consult per requester per pass with that requester's requests batched. Chosen.

**Control on the close.**
1. The confirmation round in chat is the only control, since closing intake changes no knowledge. Rejected: closing a request changes what the requester treats as true about their ask.
2. The gate asks on the apply command as it does on `kb finish`, the round first, the prompt as signature. Chosen.

**Session-start hints.**
1. Per-item "possibly superseded" tags in the pending-work block. Rejected: the worksheet costs reads across peers, and the block must stay cheap and must not pre-judge.
2. One count line from the two exact, cheap signals (branch name match, closed redirect chain) pointing at the triage command. Chosen.

## Decision

- **The intake triage pass is on demand, per peer, in an interactive session.** `meshctl inbox triage [--peer <name>]` runs when asked. It covers the peer's own requests with status `pending` or `in-progress`. `needs-info` and `needs-human` wait on the requester and are the requester's to triage from their read-back line. Without `--peer`, from a hub, it prints pool-wide open counts and a walk order (oldest stale first) and judges nothing. The worksheet and the apply step run only in the owner's own interactive session or in a hub session doing that peer's work (ADR-0063 work mode); under `AGENT_DISPATCH_DEPTH`, `AGENT_MESH_DISTILLING` or consult mode the command refuses and says why.
- **Verdict vocabulary.** `stands` (still valid, owner should work it); `superseded → <ref>` (another request, work branch or knowledge entry replaced it); `done → <ref>` (the outcome already exists elsewhere, nothing left to do); `withdrawn` (the requester no longer wants it); `unclear` (nothing grounds any of the above; stays open, the human decides or defers). Request state such as `in-progress` is never a verdict.
- **meshctl builds a worksheet; an agent writes the report.** The worksheet gathers, per request: the request id or any `related:` id named in a `mesh/work/<id>` branch, merged or live, on any peer; the id or `related:` ids in a `Confirm:` commit message or a knowledge entry body in the peer's own knowledge base and the canonical scope (ADR-0069); sibling requests from the same requester with overlapping `related:` or the same type and a similar ask, already closed; `redirected_to` chains whose target closed; mentions of the id or the ask's words in the requester's current handoff and `state.md`; age, priority and type. Nothing outside the pool is queried. The sibling signal is a candidate list: meshctl lists, the agent judges. The agent writes the **triage report** at `<peer>/.memory/triage/<date>.md`: one line per request, `- <id> · <verdict>[ → <ref>] · "<quoted grounding>" · <source>`, with an optional `Consulted:` section at the top quoting each peer consulted. The report is runtime state, never committed, and is itself a hypothesis.
- **Grounding per verdict.** `superseded`, `done` and `withdrawn` need a ref and a quote from the artifact that grounds them: the superseding artifact, the artifact holding the outcome, or the requester's own words (consult answer, handoff or `state.md` line). `stands` quotes the requester's live need or states "no supersession signal" with the signals checked. `unclear` lists the signals checked. meshctl validates the grammar and refuses a closing verdict with no ref or no quote.
- **Ref format.** A request is `<peer>:<id>` (the `redirected_to` form); a knowledge entry is `mesh://<peer>/<path>#<slug>`; a branch is `<peer>@mesh/work/<id>`; a commit is `<peer>@<sha>`. Apply refuses a ref that does not resolve.
- **Check-in with other agents.** When the worksheet is inconclusive and the requester is a peer (not `user`), the agent consults that peer in-session (ADR-0063 consult, read-only) once per pass, batching every inconclusive request from that requester into one prompt: the asks, and the question "still stands, superseded, done or withdrawn, cite". The answer is quoted in the `Consulted:` section as grounding. The requester's own pending block does not change. Headless asks are never used.
- **Confirmation round, then apply.** The agent reads the report to the human in chat, one item per request (id, requester, ask gist, verdict, ref, quote), and the human confirms item by item (ADR-0067 confirmation round). The human may override a verdict in the round; the agent rewrites that line in the same session with the human's words as the quote and `human, confirmation round` as the source, which meshctl accepts as grounding. `meshctl inbox triage apply --report <file> <id>...` then closes the confirmed ids through `RespondRequest`, so claim rules and gate rules apply unchanged: `superseded` and `done` become `filed`, `withdrawn` becomes `declined`, each with `triage: <verdict> → <ref>` stamped and a response body `Closed by triage as <verdict> → <ref>. Grounding: "<quote>". Dispute: re-ask with related: <id>.` **At most five ids per call**; apply refuses `stands` and `unclear` ids (nothing to do). The gate asks on apply with the report lines for those ids as the ask text; no report means deny.
- **Live work branch blocks the close.** Apply refuses to close a request whose `mesh/work/<id>` branch or worktree still exists; the report line carries `· branch live`. The human merges or discards the branch first (`meshctl work merge <id>` or a discard), then re-applies. Nothing deletes a branch.
- **Duplicates.** Two open requests with the same ask: the newer closes `superseded → <older>`, the older `stands`. With different requesters the newer requester's response body points at the older request to read back on.
- **Requester notification rides read-back.** The `responded` stamp puts a triage-closed request on the requester's read-back list (ADR-0071). No other channel. A requester of `user` or the owner itself owes no read-back, as for any close.
- **No distillation owed.** A triage-closed request never has to distill before archive: for `superseded` and `done` the residue already lives at the ref (the agent may add a `mesh://` reference candidate when the ref is another peer's knowledge), and `withdrawn` leaves none. Archive follows the normal rule (closed and read back).
- **Incremental.** A later run skips a request whose latest report line is `stands` and whose worksheet signals hash unchanged.
- **Session-start hint.** The pending-work block's intake line gains one clause when it applies: `N have triage signals, run meshctl inbox triage`, computed from the two exact signals only (id named in a `mesh/work/*` branch, `redirected_to` target closed). No per-item tags, no judgment (amends ADR-0071).
- **Doctor reports, never closes.** Per peer: days since the latest triage report, and the count of `pending` plus `in-progress` requests older than 14 days.

## Consequences

- Positive: a stale request has a clearing path that is neither a fake answer nor a silent archive, with the same round, cap and gate the knowledge paths use.
- Positive: no new status values; every existing consumer of intake status keeps working, and the `triage:` stamp carries the distinction for the ones that care.
- Positive: nothing pool-specific enters core; signals come from pool files and git, order from age.
- Negative: the verdicts are an agent's word. A wrong `done` is caught only by the human reading the quote in the round, and by the requester on read-back.
- Negative: an in-session consult costs a model run per requester per pass. Bounded by the worksheet-first rule and the batching.
- Negative: a request blocked by a live work branch stays open until a human acts on the branch, so a forgotten branch keeps a dead request alive. Doctor's stale-branch warning is the pressure.
- Follow-ups: the worksheet, report grammar, `inbox triage` and `inbox triage apply`, the `triage:` stamp, the ref resolver, the live-branch refusal, the pending-block clause and the doctor fields are implementation work. Running the pass over a live pool's backlog is pool hygiene, not core.
