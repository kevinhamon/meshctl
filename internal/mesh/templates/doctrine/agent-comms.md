# Agent Communication Protocol

Canonical reference for how an agent mesh coordinates. Agent workspaces in a pool (sibling directories of the pool root, which holds `pool/`) are independent — there is no direct call between them. Coordination is **file-based handoff on the shared filesystem**, and the **human triggers each agent**.

> The roster used in the examples below (architect, pm, developer, qa) is **illustrative** — a mesh defines whatever agents its work needs. The protocol is what's normative; the specific agents are not.
>
> **Pool-local additions live in `local.md` beside this file** (house rules, the real roster, tool policy). Read it when present; it takes precedence over the defaults here. Keep this file as meshctl emits it so `meshctl pool upgrade` can refresh it without conflicts.

## Core rules

1. **Every agent has an inbox:** `<agent>/intake/` in its pool.
2. **To ask something of an agent, use `meshctl send` or `meshctl ask`** — never hand-write a
   file into a peer's `intake/`, and never mutate another agent's KB/board directly. The two
   verbs are defined below; they write the request file for you with a well-formed header, a
   stable id (and, for `ask`, a `dispatch.log` entry). Hand-authored intake files skip all of that and
   drift from the format the inbox owner's `meshctl inbox` expects.
3. **The inbox owner writes the outcome back with `meshctl inbox respond`** (`status` + a
   response section; intake files are meshctl-owned and the PreToolUse gate blocks hand
   edits). The requester reads it back via `meshctl sent`, then `meshctl sent ack <id>`.
4. **Requests are proposals, not commands** — the owner may decline or ask `needs-info`.
   **A misrouted request is declined, not half-answered:** `meshctl inbox decline <id>
   --reason "…" --redirect <owner>` forwards the original ask to the agent that owns it
   (original requester kept as `from`) and closes this copy as `declined`. Without a clear
   owner, decline with no redirect and say why — the requester re-routes.
5. **Don't cross ownership boundaries directly:** an agent doesn't mutate what another agent owns (e.g. the architect doesn't file tracker issues the PM owns; the PM doesn't author decision records the architect owns). Route through intake.
6. Every request should trace to a reason (a decision record, a risk, a tracker key, or an explicit user ask).
7. **Internal references never leave the mesh (default-deny).** Mesh-internal references must not appear in **any** artifact outside the mesh's own repos — including but not limited to issue-tracker items/comments, wiki/roadmap pages, **git commits & PRs in product repos**, shipped code comments, emails, chat/IM — **unless the user explicitly asks**. Banned when crossing out: decision-record ids, risk ids, `knowledge/…` paths, `candidates/`/`playbooks/` slugs, memory slugs, intake filenames/ids, `[[wikilinks]]`, mesh workspace paths. State the *decision* product-plain ("the new importer runs nightly and retries failed batches"), never the internal record id. **Boundary:** the mesh's own repos (KB, decision records, intake files, their git history) are internal — citing internal ids there is correct (rule 6); the rule governs content **crossing out**. Product-technical detail (repo names, languages, services, endpoints, tables) is fine — only mesh-internal *governance* artifacts are the leak. **Writes to outbound surfaces are gate-asked:** a tool call to a surface the pool declares outbound (`outbound:` in `pool/config.yaml`) is asked once per summary (`.memory/confirm/<session>.md`, the ask text, written and read out as a confirmation round first); a call with no summary, or with no human at the prompt, is denied.
8. **Tact on external, human-facing surfaces (default).** When you author content a **human** will read on an external/product surface — tracker items/comments, PRs, code review, email, chat — **especially when speaking as the user**, communicate with tact. Critique the *work*, not the person or their competence; don't flatly say a solution is "wrong" — frame it as an observation, a trade-off, a risk, or a recommendation, and lead with what's sound before what to change. Be direct about substance, diplomatic about tone. (This is the *tone* companion to rule 7's *content* rule — both govern what crosses out of the mesh.) Where this rule 8 does **not** apply: internal mesh artifacts (your KB, decision records, intake) — there, be blunt and precise; and the chat with the user, unless the user says otherwise.
9. **Mesh doctrine is a set of defaults; an agent's own role guidance overrides it where they conflict.** These rules (including rule 8's tone) are baseline assumptions for a general-purpose mesh. An agent whose **role defines a different communication style or standard** — e.g. an academic tutor, a brand-voice writer, a deliberately-blunt reviewer, a customer-support persona — follows *its* guidance; mesh defaults must not override an agent whose primary purpose *is* a communication style. State such overrides explicitly in that agent's `CLAUDE.md`. (Layering: mesh = shared defaults + coordination protocol; the agent's `CLAUDE.md` is authoritative for its own domain and voice.)
10. **When meshctl itself gets in your way, tell the steward.** Friction, a missing capability, a bug or an improvement idea in meshctl (not in your own work): `meshctl send steward fyi "meshctl <friction|gap|bug|suggestion>: …"`, describing meshctl's behaviour only — no pool, organization, people or data details. Never file an issue yourself; the steward collects reports and files them after the human's yes. In a pool without a steward, tell the human.

## Agent registry (example)

An illustrative four-agent product mesh. Substitute your own roster.

| Agent | Inbox | Owns | Accepts (types) |
|-------|-------|------|-----------------|
| **architect** | `architect/intake/` | Architecture, decision records, KB, risk register | guidance, story-enrichment, review, decision, fyi |
| **pm** | `pm/intake/` | The issue tracker / roadmap | work-request (→ tracked stories) |
| **developer** | `developer/intake/` | Implementation — works tracked issues end-to-end | implement, fix, review-fix, fyi |
| **qa** | `qa/intake/` | Acceptance-criteria validation + QA of changes (evidence-based verdicts) | validate, qa, fyi |
| domain experts (e.g. a database expert, an API expert) | `<agent>/intake/` | domain specialties | adopt protocol as needed |

New agents adopt the same convention (copy an `intake/README.md`, add an Intake section to their CLAUDE.md pointing here).

**Developer flows:** developer → architect (`guidance`/`review`/`decision`) when work implies an architectural choice or crosses a decision guardrail; developer → pm (`work-request`) when it discovers new/under-specified stories; developer transitions its own in-flight issue's status directly. Architect/pm → developer (`implement`) to hand off a scoped, ready issue.

**QA flows:** anyone → qa (`validate` an issue's acceptance criteria, or `qa` a change). qa validates against reality (code / databases / dashboards / logs), posts an evidence-based verdict, and on failure → developer (`fix`) with repro; ambiguous spec → architect (`guidance`); missing scope → pm (`work-request`). qa does **not** transition tracker issues or mutate prod — verdicts only; status changes stay with the owner (developer/pm) unless the user asks.

## Request schema (shared)

Filename `YYYY-MM-DD-<from>-<slug>.md`. Frontmatter: `id, from, to, type, created, status, priority, related[], (tracker_keys[] | response)`. Body: the ask + context + (for work-requests) per-story acceptance criteria. Full field docs live in each inbox's `README.md`.

Statuses (enforced by `meshctl inbox respond`; `meshctl doctor` warns on anything else):

| Status | Set by | Meaning |
|--------|--------|---------|
| `pending` | `send`/`ask` | Awaiting the owner. |
| `in-progress` | owner | Claimed and being worked. |
| `needs-info` | owner | Blocked on the requester — the response says what is missing. |
| `needs-human` | owner | Blocked on a person or another agent (a dispatched peer never chains). |
| `answered` / `filed` | owner | Done — `filed` when the outcome is tracker items. Closed. |
| `declined` | owner (`inbox decline`) | Not this agent's to do; the reason (and any redirect) is in the file. Closed. |

Legacy spellings are read as their canonical form: `rejected`→`declined`, `done`/`resolved`→`answered`.
`meshctl inbox archive --closed` sweeps closed requests the requester has read back (`--force` for the rest).

**Intake order — the human orders the queue.** On "check intake" or the session-start
intake line, the inbox owner runs `meshctl inbox list` (pending + in-progress only,
priority then age, a one-line ask each), reads it to the human as a short numbered queue,
proposes an order (priority, then age; flags anything blocking another peer) and waits:
nothing is claimed or worked before the answer. It then works in the human's order, by
id (`meshctl inbox claim <id>`), one at a time, reporting each outcome briefly before the
next, and re-plans when the human says something more important came up. Skipped ids go
on the handoff's `Deferred: <ids>` line. A consulted or delegated peer has no human to
ask: it works only the one request it was given. (`meshctl inbox list --all` shows every
status.)

**Intake lifecycle — an ephemeral discussion, not a ledger.** A request is
transient coordination; it is **git-ignored** (`intake/*.md`), and its *durable residue*
lives in the KB, not in the inbox. To **close** an exchange: (1) write the outcome back
into the request (`status: answered` + response); (2) the requester reads it back on a
later turn; (3) **distill** the durable residue into the **right KB artifact by type** —
a consequential decision → a **decision record** (single owner: the domain owner writes it, the
other party's input folds in; refine by amending while `proposed` or superseding once
`accepted`); a reusable procedure → a **playbook**; a lesson/gotcha → an **experience**
note; `fyi`/routine → often nothing; (4) `meshctl inbox archive <id>` (or `--answered`)
moves the spent request into the git-ignored `.intake-archive/`. **Don't inflate decision records** —
most requests are not decisions. The KB artifact, not the intake file, is the record;
the coordination audit ("who asked whom, when") is `dispatch.log`. A request closed by
the triage pass (`meshctl inbox triage apply`) owes no distillation: its residue already
lives at the verdict's ref, or there is none.

## Architect ↔ PM (two example flows)

**Architect → PM (get work onto the backlog):** `meshctl send pm work-request "…"`, pre-shaped to the PM's Definition of Ready (context, acceptance criteria, value, dependencies, links to a driving decision/risk; the PM's own role guidance names any further fields). PM files stories and writes back `status: filed` + `tracker_keys`. PM processing rules live in the Intake section of the PM's `CLAUDE.md`.

**PM → Architect (enrich a story / seek guidance):** `meshctl send architect story-enrichment "…"` (a tracker key needing technical approach/acceptance criteria/affected repos/risks) or `meshctl send architect guidance "…"`. Architect fills the enrichment/response block; PM pulls it back into the tracker. **The enrichment must include a clearly-marked tracker-ready block written product-plain (no internal refs, per Core rule 7)** — that is what the PM pastes; any internal rationale stays in a separate "internal — not for the tracker" section.

## Two verbs — `meshctl send` (async) vs `meshctl ask` (sync)

There is **one** way to reach a peer: the `meshctl` CLI. Pick the verb by whether you need
an answer *now*:

| Verb | What it does | Use when |
|------|--------------|----------|
| **`meshctl send <target> <type> "…"`** | Writes a request file into the target's `intake/`. The **human triggers** the target later; you do not block. | Handing off work (most cases). The default. |
| **`meshctl ask <target> <type> "…"`** | Writes the intake request **and** dispatches the target **headlessly now**, blocking for the answer, which the peer writes back into the request file. | You are **blocked mid-task** on a decision only an advisory peer can make. |

Both write the same request file (schema below) — `ask` just also runs the peer immediately.
`meshctl ask --detach` writes the request only — no dispatch; poll the id later.

**`meshctl ask … --in-session`** (Claude Code callers): same request file, same guards and
audit trail, but the peer runs as a **subagent of your own session** instead of a second
process in a second terminal. The command records the dispatch and prints the exact
Agent-tool call to make next (`subagent_type: mesh-<peer>`, prompt naming the request id);
make it, and when it returns read the answer with `meshctl sent`. Each dispatchable peer has
a generated definition in your `.claude/agents/mesh-<peer>.md` (rendered from the roster by
`meshctl agent onboard` / `pool upgrade`; session start lists the ones installed). Inside
it the peer starts with `meshctl agent brief --agent <peer> --request <id>` (its identity,
handoff, recall and the ask in one shot — its own hooks do not fire in a subagent), reads
its KB read-only, and answers through `meshctl inbox respond … --agent <peer>`. It has no
Agent tool and its gate denies dispatch, file writes, git and KB writes, so it cannot chain
or drift. Limits: the caller's session pays the peer's tokens; opencode callers use plain
`ask`.

**`meshctl ask … --in-session --work`** (delegated work, the hub-session mode — meshctl
ADR-0063): for when the task is the peer's *work* — its KB, decisions, repo — not its
advice. Same request file, guards and log (`mode=in-session-work`), but the delegation names
`subagent_type: mesh-<peer>-work`, a write-capable definition rendered for every
**work-delegable** peer (dispatchable, or declaring `mutates`). Inside it the peer:
- works by its own KB and conventions: KB changes through `meshctl kb begin|finish --agent
  <peer>`, path-limited commits in its own workspace, and its declared `workdirs:` (product
  repos, which follow their branch/PR convention — never a push to a default branch);
- edits in a **worktree** (`<pool root>/pool/worktrees/<peer>-work-<id>`, branch
  `mesh/work/<id>`) instead of its checkout when its own session is live, so the two never
  share a working tree;
- may not dispatch (no Agent, no `meshctl ask|send|pool|update|session`) and may not run
  `meshctl work merge` — its gate denies writes outside its workspace, its worktrees and its
  workdirs;
- ends with a **receipt** (files per repo, commits/branches, KB candidates, open questions),
  which the caller relays to the user;
- stops at an **outbound write or a promotion**: no human is at its prompt, so either one
  ends in `needs-confirm`. The peer responds `needs-human` with a body opening
  `needs-confirm:` that carries the confirmation round and the summary path, lists it in
  the receipt, and never relays a confirmation; the human confirms from the peer's own
  session.

A delegation to a peer that declares `mutates` (tracker, deploys) is **asked**, not
silently allowed: the caller's PreToolUse gate turns that Agent call into a human approval,
once per delegation. Worktree branches are landed by the peer's **own** session with
`meshctl work merge <id>` (rebase + ff-only, like `kb finish`; `--abort` discards); session
start lists pending ones, `meshctl work list` shows them all. On stop, the peer's session is
captured and distilled into **its** KB, consult or work alike.

**Synchronous unblock flow** (`meshctl ask`): the target runs **headlessly in its own
workspace** with a scoped allowlist (Read/Grep/Glob plus the `meshctl inbox respond|decline` and recall verbs — **no file edits**; on opencode the same policy is passed as an inline permission config) — the peer
reads the request + code and writes its answer back into the request file, nothing else. When it
returns, re-read the request file and continue.

Rules:
- **Advisory targets only** — the architect (guidance/review/decision) and domain experts, whose output is a file. Do **not** auto-dispatch state-mutating agents (pm → tracker, developer → code); those stay human-triggered. (`--in-session --work` is the one exception, and only with the human approving each such delegation — see above.)
- **A dispatched agent never chains.** If answering would need *another* agent's input (or a genuinely new consequential decision not covered by an accepted decision record), the peer sets `status: needs-human`, names the agent/decision + the exact question, gives a recommendation, and **stops**. The human drives the next hop — cascades are never automatic.
- The caller reads `needs-human` back and surfaces it to the user; it does not proceed on that point.

Anti-cascade guards (budget protection) — layered, cheapest→strongest, so a runaway/expensive chain can't happen even if a model ignores the prompt:
1. **Prompt** — the dispatched agent is told it may not dispatch (soft).
2. **Depth guard** — `AGENT_DISPATCH_DEPTH>=1` refused (env-based).
3. **Single-flight lock** — a `mkdir` lock at `<pool root>/pool/dispatch.lock` means **one dispatch at a time per pool**; hard, independent of the model and of env propagation. A caller queues behind an in-flight dispatch (up to `AGENT_DISPATCH_LOCK_WAIT_SEC`, default 120s) and then reports BUSY — a queue, not an unreachable peer. A lock whose holder process is dead is reclaimed at once; one past the stale window (`AGENT_DISPATCH_STALE_SEC`, 900s) with an unknown holder is reclaimed; a live holder is never reclaimed. Ctrl-C releases the lock and the request claim.
4. **Hourly cap** — max dispatches per rolling hour (default 12, from `<pool root>/pool/dispatch.count`), charged before the lock wait and refunded if the dispatch never runs; a circuit-breaker against fan-out across many experts.

Tunables (env): `AGENT_DISPATCH_HOURLY_MAX`, `AGENT_DISPATCH_STALE_SEC`, `AGENT_DISPATCH_TIMEOUT_SEC` (per-dispatch, default 420s), `AGENT_DISPATCH_MODEL` (pin a cheaper model for dispatched runs). All dispatches, queues, blocks and aborts are logged to `<pool root>/pool/dispatch.log`. Exit codes: 2 unknown target · 3 depth · 4 hourly cap · 6 lock held (BUSY) · 124 timed out · 130 interrupted.

In-session dispatch keeps guards 1, 2 and 4 (the prompt, the depth guard, the hourly cap — logged as `mode=in-session`) and replaces the process-level ones: the peer subagent has no Agent tool (it cannot spawn), and its PreToolUse gate denies `meshctl ask|send`, so chaining is impossible rather than discouraged. No lock is needed — there is no second process to serialize.

## Request log (example)

| Request | Dir | Dir'n | Type | Status | Refs |
|---------|-----|-------|------|--------|------|
| `2026-01-14-add-export-api` | pm/intake | out | work-request | filed → PROJ-101–105 | ADR-0008, R-12 |

## Knowledge & Memory

Coordination (above) is *how agents talk*. **Cognition** is *how one agent
organizes what it knows and recognizes what it has seen* — bound to the same
file substrate, no new store.

**Knowledge base** — each agent's git-tracked `knowledge/` holds these areas, in two
tiers. The **current tier** states what is true now and is maintained in place:
`state.md` (a ≤40-line digest — Now / Active / Recently decided / Open questions —
injected at session start beside the handoff), `direction.md` + `risks.md`, domain
files + `architecture/`, `playbooks/` (repeatable procedures), `reference/` (dated
external-source notes), rule notes. The **log tier** is append-only history:
`decisions/` (decision records) and `experience/` (retrospectives / failure modes).
`candidates/` is the KB inbox (proposed learning awaiting consolidation, distinct
from the mesh `intake/`). The agent **contract** stays in `CLAUDE.md` + `agent.yaml`
(single source — no `knowledge/contract.md`). `meshctl agent onboard` scaffolds
missing areas add-only.

**Documents** — what an agent writes *for the person* (designs written before a piece
of work, reports, briefs, mockups) lives in the agent's `docs/` folder, never in a repo
the agent works on, where it could be committed and pushed by accident. Name each
`YYYY-MM-DD-<slug>.<ext>`; write designs in markdown with diagrams as mermaid fences.
`docs/` is committed with the workspace; `docs/local/` is ignored, for documents that
must never leave the machine. An agent with existing folders lists them under `docs:` in
`agent.yaml` (workspace-relative; default `[docs]`). A document is not knowledge: what
it teaches that should last goes through `candidates/` like any other learning. The
mesh console lists and renders every agent's documents (markdown with mermaid, HTML,
PDF, images), read-only and sandboxed.

**Decision records are machine-readable.** Every ADR carries YAML frontmatter —
`status:` (`proposed | accepted | rejected | deprecated | superseded`), `date:`,
`supersedes:` / `superseded_by:` (numbers), and `updates:` (the current-tier docs the
decision changes) — with the human `- **Status:**` bullet kept for the reader.
`meshctl doctor --fix` adds the block to decisions that only have the bullet. Recall
never returns a superseded, deprecated or rejected decision, so retiring a decision
means marking it, not deleting it (superseded knowledge stays traceable, clearly
inactive).

**A decision must land.** `meshctl doctor` errors when a doc named in an accepted
decision's `updates:` is older than the decision, warns when `state.md` predates an
accepted decision, warns when a candidate is older than 14 days, and warns on a
handoff or digest that fails lint. `meshctl kb consolidate` lists that debt in the
order to work it off; the agent does the work in a `kb begin`/`kb finish` worktree and
rewrites the digest with `meshctl state set --summary-file <summary>` (fixed template,
refuses a body that fails lint; the digest is current-tier knowledge, so the human
confirms it like a promotion).

**Memory** — computed live from `knowledge/` + `agent.yaml` on every call; nothing is
persisted. Advisory; the KB is authoritative. `meshctl memory recall "<topic>"` returns
tiered pointers (current tier outranks log, which outranks `candidates/`) with
freshness and refs; `--budget N` inlines the top current-tier bodies within about N
tokens so what the KB knows is in view rather than behind a pointer. Log-tier hits stay
pointers.

**Retrieval loop (agent behavior, not code)** — Recognize → **Recall**
(`memory recall`) → **Retrieve** (read the KB files it points to — authoritative)
→ Reconcile → **Verify** (live systems / MCP) → **Align** (reach shared understanding
with the human: intent, grounding, precedent) → Act → **Learn** (`kb begin` a
note into `candidates/`, consolidate later via `kb finish`). Memory routes; the KB
answers; live systems confirm; the human agrees.

**Shared understanding before truth.** Before a promotion (a `kb finish` that touches more
than `candidates/`, `state set`, `kb confirm`) or an outbound write, the agent writes one
summary file per session, `.memory/confirm/<session>.md` (Intent, Grounding, Precedent,
Coherence when related entries exist), and reads it to the human in chat as a
**confirmation round**: one item per entry, what it says from now on and no longer says,
its grounding and precedent, ending in a recommended confirm. The format is in the
`mesh-cognition` skill.

**Decision rounds are the default way to ask the human.** Any time an agent needs the
human's decision on proposed changes (an intake order, a plan, a rule, a KB change, an
outbound write), it presents a decision round: one item per change,
`📝 **C<n> — <short title>**`, then `From now on it says: …` / `It no longer says: …` (a
change of rule or knowledge) or what it will do and why (an action), ending
`➡️ ✅ Confirm`; then how to answer: `yes` for all, `C<n> no: <reason>` for one. Small
batches (about 3–5 items); it waits for the answer before acting. The confirmation round
is this format with its extra fields (Grounded in, Precedent). The permission prompt that follows is the signature of that
round, never the reading surface.

### Distillation ritual — System 1 → System 2

Turn recurring **intuition** (episodic signal) into durable **deliberate**
knowledge (the KB). This is the retrieval loop's **Learn** stage, made concrete. It is
**reviewed, never automatic** — raw observations never become truth on their own.

**When to run** (opportunistic + light periodic — quality over volume):
- You notice a *recurring* pattern, gotcha, or hard-won lesson worth keeping.
- A task ends and left a reusable procedure or a failure mode behind.
- `meshctl doctor` warns your `playbooks/`/`experience/` are still empty.

**Steps:**
1. **Mine** the intuition engine for your domain — `meshctl intuition recall "<topic>"` (compact
   pointers). Look for signal that repeats across sessions, not one-offs.
2. **Draft a candidate.** `meshctl kb begin`, then write one note per learning into
   `knowledge/candidates/<slug>.md`: *what* it is, *why* it matters, and the *evidence*
   (session/obs/file refs). Candidates are proposals, not truth.
3. **Review + consolidate.** Judge each candidate. If durable and verified, move/rewrite
   it into the right area — `playbooks/` (repeatable procedure), `experience/` (retro /
   failure mode), a **decision record**, or `glossary.md`/`system-map.md` (facts). Drop the
   rest. Write the summary file, read the confirmation round, then `meshctl kb finish
   --summary-file <summary>`: the human confirms at the prompt, and the merge
   auto-rebuilds the memory index, so `memory recall` now routes to it.
4. **Leave candidates/ clean.** It is a holding area, not a graveyard — consolidate or
   delete; `doctor` warns on any candidate older than 14 days.
5. **Consolidate.** Distilling only adds. After a decision is accepted, rewrite the
   current-tier docs it `updates:` and fold it into `state.md`; `meshctl kb consolidate`
   says what is outstanding.

**Guardrails:**
- **Review gate** — only the human confirms before a candidate becomes truth: the
  confirmation round in chat, then the signature at `kb finish`. An agent never confirms
  its own writes.
- **KB is authoritative; the episodic engine is never edited** — it is the disposable episodic
  engine; you distill *from* it, you don't curate *in* it.
- **All writes via `kb begin/finish`** — git-tracked, reviewable, recoverable.

**Standing rules are gated, not prose (meshctl ADR-0062).** A standing instruction ("never do X",
"do not repeat this", a recorded failure, a user directive) is written as a KB note whose
frontmatter carries `rule:` + `triggers:`. `meshctl hook pre-tool-use` matches every
Bash / Write|Edit / MCP tool call against those triggers; the first match per session is
**denied** with the rule text + note path, and the retry is allowed. Zero context cost
unless a rule fires. The gate is fail-closed: a tool call it cannot read is denied, not waved through; a rule note that does not parse is skipped. Shell commands that write a gated path (`> intake/x.md`, `sed -i`, `rm` a claim) are denied like file-edit tools. Prose "recall before acting" is skipped under
task pressure — only gates hold. **Every new standing rule is written this way.**

```
---
rule: "<one line: what to do / never do>"
triggers:
  - '<case-insensitive Go regex on the action text>'
---
```

- Match the **action text** (command, file path, MCP tool name/args), not the topic.
- Allow flags between words: `git\b.*\bremote\b`, not `git\s+remote` (misses `git -C <p> remote`).
- Too broad nags on every call; too narrow never fires. Verify both ways: pipe
  `{"cwd":"<agent dir>","session_id":"test-x","tool_name":"Bash","tool_input":{"command":"…"}}`
  into `meshctl hook pre-tool-use` — a matching action denies, an unrelated one prints
  nothing — then delete `.memory/rule-acks/test-*.json`.
- Rules about chat only (nothing to match in a tool call) stay prose.
- `candidates/` and edits under `knowledge/` are exempt from the gate.

**Optional automation:** a future `meshctl memory distill` may do steps 1–2 (mine +
draft candidates) only. Step 3 (promotion into truth) always stays human-confirmed.

**Capture cross-agent learning on dispatch return.** A dispatched peer (headless or
in-session consult) may not commit, so anything the answering peer *learned* lives only in the answered
`intake/` file and is lost if never distilled. Close the loop from the **caller** side:
when a `meshctl ask` dispatch returns, judge the answer, and for any durable learning write
a **candidate into the OWNING agent's KB** — not your own:

```
meshctl kb begin  --agent <owner>
# write knowledge/candidates/<slug>.md in the owner's worktree, with provenance
#   frontmatter:  source: dispatch <caller> <intake-id>
meshctl kb finish --agent <owner>     # run from a normal cwd, not inside the worktree
```

- Domain knowledge lands with its owner (a database gotcha → the database expert, an
  org-level pattern → the architect) — never scatter it into the requester's KB.
- It lands as a **candidate** (a proposal), so the review gate still holds — the owning
  agent proposes it for promotion in its next *interactive* session, and the human
  confirms it there.
- Interactive sessions still distill their *own* learning the normal way (steps 1–4); this
  rule only covers the dispatched path (headless or in-session consult), where the peer itself
  can't commit. A peer doing **delegated work** (`--work`) commits its own learning through
  `kb begin|finish --agent <peer>`; the caller only relays its receipt.
- `kb --agent` uses the same worktree+merge conflict guard, so a concurrent write on the
  owner is handled (conflict → `needs-human`).

### Session handoff — resume where you left off

An authoritative, git-tracked **`knowledge/handoff.md`** per agent: *where we are + the
next concrete step*. The deliberate-tier complement to the episodic engine's fuzzy recap; distinct
from strategic State (`direction.md`/`risks.md`) and from cross-agent `intake/`.

- **Read at start:** the SessionStart surface auto-injects it (`meshctl handoff show`),
  after the `state.md` digest — the digest says what the world looks like, the handoff
  says where the last session stopped.
- **Write at end / when attention shifts:** fill in `meshctl handoff template` (Current
  State · Next Step · Open Threads · Pointers, ≤40 lines) and `meshctl handoff set` (Learn
  stage). `set` refuses a body with a missing section, a leftover placeholder or over
  budget. Keep it a handoff, not a journal; rewrite it (don't append). git history
  preserves prior handoffs.

### Tool reference — what's in your toolbox + the house rules

Distinct from skills (procedures) and the KB (knowledge): a **tool reference** declares
which CLI tools to use and the mesh's house rules (e.g. "prefer `tea` over `gh`"), as
**pointers** — you self-serve detail with `<cmd> --help`; meshctl never embeds the help.

- Declared mesh-wide in `pool/config.yaml` (`tools:`) and/or per-agent in `agent.yaml`
  (`tools:`); the agent's entries override the pool's by `cmd`.
- Schema: `{ cmd, use, avoid, help }` — `use` = one-line purpose; `avoid` = "don't use
  this here (why)"; `help` defaults to `<cmd> --help`.
- The SessionStart surface injects the effective list; `meshctl doctor` warns if a
  referenced tool isn't on `$PATH`, or if an `avoid` tool is present.

### Repo hygiene — your knowledge is only as safe as your remote

The pool container is not a repo; **each agent must be its own git repo, with a
remote.** Your `knowledge/` (KB + handoff) is durable only once **committed and
pushed** — until then it lives on one machine and dies with it.

- `meshctl agent new` inits the local repo for you. If your workspace is *not* a git
  repo (e.g. an onboarded dir), run `git init` and commit now.
- Create a remote and push. Commit `knowledge/` as you distill.
- The SessionStart surface injects a **persistent reminder** whenever your workspace
  has no remote; it clears automatically once you push. `meshctl doctor` flags it too.
