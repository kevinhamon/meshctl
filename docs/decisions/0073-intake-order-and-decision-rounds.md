---
status: accepted
date: 2026-10-19
---
# 0073. "check intake" shows the queue and waits for the human's order; decisions are asked as decision rounds

- **Status:** accepted
- **Date:** 2026-10-19
- **Deciders:** maintainer (C1–C3 confirmed as a round, C4 stated directly, 2026-10-19)
- **Related:** ADR-0011 (intake claims), ADR-0043 (intake is ephemeral), ADR-0063 (in-session consult and delegated work), ADR-0067 (confirmation round), ADR-0068 (outbound confirmation), ADR-0071 (session-start pending-work block), `internal/mesh/inbox_list.go`, `internal/mesh/scaffold.go`, `internal/mesh/pending.go`, `GLOSSARY.md` (decision round, confirmation round)

## Context

- The standing order said: on "check intake", run `meshctl inbox next --claim`. That claims the oldest pending request and the agent starts on it, so last week's leftovers win and the human never gets to reorder when something more urgent came in.
- `meshctl inbox list` printed every request in filename order, answered, filed and declined ones too, so the queue an agent read out was noisy and not in any useful order.
- Agents put proposals to the human as prose. The confirmation round (ADR-0067/0068) already had a format the human reads and answers quickly, but only promotions and outbound writes used it.

Constraints: core stays generic. The intake order and the decision round are standing-order and doctrine text, advisory like the rest of the doctrine; the gate does not police what an agent says. Only the queue command is code.

## Options considered

**Intake on "check intake".**
1. Keep `inbox next --claim`, and add priority ordering to `next`. Rejected: the agent still decides alone and starts work before the human sees the queue.
2. Show the queue, propose an order, wait; then claim by id in the human's order. Chosen.

**Queue command.**
1. A new verb (`inbox queue`). Rejected: `inbox list` is the verb agents and humans already use, and its full listing had no reader that depended on it.
2. `inbox list` defaults to the open queue; `--all` keeps the full listing. Chosen.

**How agents ask for decisions.**
1. Leave prose proposals, use the round only for promotions and outbound writes. Rejected by the human.
2. One general format, the decision round, with the confirmation round as the same format plus its extra fields. Chosen.

## Decision

- **C1 — "check intake" shows the queue and stops.** On "check intake" or the session-start intake line, the agent runs `meshctl inbox list`, reads it out as a short numbered queue (id, from, type, priority, age, one-line ask), proposes an order (priority, then age; flags anything blocking another peer) and waits for the human to confirm or reorder. Nothing is claimed or worked before the answer. The standing orders no longer mention `inbox next --claim`; the verb stays for scripts.
- **C2 — Work in the human's order, by id.** The agent claims each request with `meshctl inbox claim <id>` in the order given, one at a time, and reports each outcome briefly before the next. It re-plans when the human says something more important came up. Skipped ids go on the handoff's `Deferred: <ids>` line. A consulted or delegated peer (ADR-0063) has no human at its prompt: it works only the one request it was handed and puts any decision it needs into `needs-human` as a decision round.
- **C3 — Clean queue command.** `meshctl inbox list` shows pending and in-progress requests only, ordered by priority (P1 first; missing or unknown counts as P2), then created date (oldest first), then id. Each line is `N. <id> · from <who> · <type> · <priority> · <age>d [· <status>] · <ask>`, with the status named only when it is not `pending`. `--all` shows every status in the same order.
- **C4 — Decision rounds are the default way to ask the human.** Whenever an agent needs the human's decision on proposed changes (an intake order, a plan, a rule, a KB change, an outbound write), it presents one item per change: `📝 **C<n> — <short title>**`, then `From now on it says: …` / `It no longer says: …` for a change of rule or knowledge, or what it will do and why for an action, ending `➡️ ✅ Confirm`. It then says how to answer: `yes` for all, `C<n> no: <reason>` for one. Rounds are small (about 3–5 items) and the agent waits for the answer before acting. The confirmation round (ADR-0067/0068) is this format with its extra fields (Grounded in, Precedent); its item header becomes `📝 **C<n> — <entry>** (new|amended|retired)`.

## Consequences

- Positive: the human orders the queue before work starts, and every decision reaches them in one readable, answerable shape.
- Positive: `inbox list` is a queue a human can read; `--all` still shows the history.
- Negative: "check intake" now costs one round-trip with the human before any work. Accepted: that round-trip is the point.
- Negative: advisory only. An agent can still claim without asking; the gate does not catch it.
- Migration: the Agent Communication section's Inbound bullet is a managed line. Re-onboard (and `pool upgrade`) swaps any known earlier Inbound bullet for the current intake order, leaving the human's text around it alone, and `pool upgrade --check` lists `refresh Agent Communication Inbound order in CLAUDE.md`. The Cognition stanza's pending-work bullet changes too and refreshes through the existing managed-stanza refresh. A hand-edited Inbound bullet is not touched.
- Follow-ups: none.
