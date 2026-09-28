# Agent Communication Protocol

Canonical reference for how an agent mesh coordinates. Agent workspaces under `~/agents/*` (or wherever the pool lives) are independent — there is no direct call between them. Coordination is **file-based handoff on the shared filesystem**, and the **human triggers each agent**.

> The roster used in the examples below (architect, pm, developer, qa) is **illustrative** — a mesh defines whatever agents its work needs. The protocol is what's normative; the specific agents are not.

## Core rules

1. **Every agent has an inbox:** `<agent>/intake/` in its pool.
2. **To ask something of an agent, use `meshctl send` or `meshctl ask`** — never hand-write a
   file into a peer's `intake/`, and never mutate another agent's KB/board directly. The two
   verbs are defined below; they write the request file for you with a well-formed header, a
   stable id, and a `dispatch.log` entry. Hand-authored intake files skip all of that and
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
7. **Internal references never leave the mesh (default-deny).** Mesh-internal references must not appear in **any** artifact outside the mesh's own repos — including but not limited to issue-tracker items/comments, wiki/roadmap pages, **git commits & PRs in product repos**, shipped code comments, emails, chat/IM — **unless the user explicitly asks**. Banned when crossing out: decision-record ids, risk ids, `knowledge/…` paths, `candidates/`/`playbooks/` slugs, memory slugs, intake filenames/ids, `[[wikilinks]]`, mesh workspace paths. State the *decision* product-plain ("the new ETL is Go on the batch scheduler with a date-range trigger"), never the internal record id. **Boundary:** the mesh's own repos (KB, decision records, intake files, their git history) are internal — citing internal ids there is correct (rule 6); the rule governs content **crossing out**. Product-technical detail (repo names, languages, services, endpoints, tables) is fine — only mesh-internal *governance* artifacts are the leak.
8. **Tact on external, human-facing surfaces (default).** When you author content a **human** will read on an external/product surface — tracker items/comments, PRs, code review, email, chat — **especially when speaking as the user**, communicate with tact. Critique the *work*, not the person or their competence; don't flatly say a solution is "wrong" — frame it as an observation, a trade-off, a risk, or a recommendation, and lead with what's sound before what to change. Be direct about substance, diplomatic about tone. (This is the *tone* companion to rule 7's *content* rule — both govern what crosses out of the mesh.) Where this rule 8 does **not** apply: internal mesh artifacts (your KB, decision records, intake) — there, be blunt and precise; and the chat with the user, unless the user says otherwise.
9. **Mesh doctrine is a set of defaults; an agent's own role guidance overrides it where they conflict.** These rules (including rule 8's tone) are baseline assumptions for a general-purpose mesh. An agent whose **role defines a different communication style or standard** — e.g. an academic tutor, a brand-voice writer, a deliberately-blunt reviewer, a customer-support persona — follows *its* guidance; mesh defaults must not override an agent whose primary purpose *is* a communication style. State such overrides explicitly in that agent's `CLAUDE.md`. (Layering: mesh = shared defaults + coordination protocol; the agent's `CLAUDE.md` is authoritative for its own domain and voice.)

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
the coordination audit ("who asked whom, when") is `dispatch.log`.

## Architect ↔ PM (two example flows)

**Architect → PM (get work onto the backlog):** `meshctl send pm work-request "…"`, pre-shaped to the PM's Definition of Ready (context, per-story acceptance criteria, value/stakeholder, dependencies, `bucket`, `proposed_epic`, links to a driving decision/risk). PM files stories and writes back `status: filed` + `tracker_keys`. PM processing rules live in the Intake section of the PM's `CLAUDE.md`.

**PM → Architect (enrich a story / seek guidance):** `meshctl send architect story-enrichment "…"` (a tracker key needing technical approach/acceptance criteria/affected repos/risks) or `meshctl send architect guidance "…"`. Architect fills the enrichment/response block; PM pulls it back into the tracker. **The enrichment must include a clearly-marked tracker-ready block written product-plain (no internal refs, per Core rule 7)** — that is what the PM pastes; any internal rationale stays in a separate "internal — not for the tracker" section.

**Reading the roadmap:** keep a local roadmap cache in the PM's workspace and read it instead of crawling the source-of-truth wiki on every turn.

## Two verbs — `meshctl send` (async) vs `meshctl ask` (sync)

There is **one** way to reach a peer: the `meshctl` CLI. Pick the verb by whether you need
an answer *now*:

| Verb | What it does | Use when |
|------|--------------|----------|
| **`meshctl send <target> <type> "…"`** | Writes a request file into the target's `intake/`. The **human triggers** the target later; you do not block. | Handing off work (most cases). The default. |
| **`meshctl ask <target> <type> "…"`** | Writes the intake request **and** dispatches the target **headlessly now**, blocking for the answer, which the peer writes back into the request file. | You are **blocked mid-task** on a decision only an advisory peer can make. |

Both write the same request file (schema below) — `ask` just also runs the peer immediately.
`meshctl ask --detach` writes + dispatches without blocking (poll the id later).

**Synchronous unblock flow** (`meshctl ask`): the target runs **headlessly in its own
workspace** with a scoped allowlist (Read/Grep/Glob/Edit/Write, auto-accept edits) — the peer
reads the request + code and writes its answer back into the request file, nothing else. When it
returns, re-read the request file and continue.

Rules:
- **Advisory targets only** — the architect (guidance/review/decision) and domain experts, whose output is a file. Do **not** auto-dispatch state-mutating agents (pm → tracker, developer → code); those stay human-triggered.
- **A dispatched agent never chains.** If answering would need *another* agent's input (or a genuinely new consequential decision not covered by an accepted decision record), the peer sets `status: needs-human`, names the agent/decision + the exact question, gives a recommendation, and **stops**. The human drives the next hop — cascades are never automatic.
- The caller reads `needs-human` back and surfaces it to the user; it does not proceed on that point.

Anti-cascade guards (budget protection) — layered, cheapest→strongest, so a runaway/expensive chain can't happen even if a model ignores the prompt:
1. **Prompt** — the dispatched agent is told it may not dispatch (soft).
2. **Depth guard** — `AGENT_DISPATCH_DEPTH>=1` refused (env-based).
3. **Single-flight lock** — a `mkdir` lock at `<pool>/.dispatch.lock` means **no dispatch can start while one is in flight**; hard, independent of the model and of env propagation. Stale locks (>15m) auto-reclaimed; a lock the dispatcher didn't create is never deleted.
4. **Hourly cap** — max dispatches per rolling hour (default 12, from `<pool>/.dispatch.count`); a circuit-breaker against fan-out across many experts.

Tunables (env): `AGENT_DISPATCH_HOURLY_MAX`, `AGENT_DISPATCH_STALE_SEC`, `AGENT_DISPATCH_TIMEOUT_SEC` (per-dispatch, default 420s), `AGENT_DISPATCH_MODEL` (pin a cheaper model for dispatched runs). All dispatches + blocks logged to `<pool>/.dispatch.log`. Exit codes: 2 unknown target · 3 depth · 4 hourly cap · 6 lock held.

## Request log (example)

| Request | Dir | Dir'n | Type | Status | Refs |
|---------|-----|-------|------|--------|------|
| `2026-01-14-consolidate-clients` | pm/intake | out | work-request | filed → PROJ-101–105 | ADR-0008, R-12 |

## Knowledge & Memory

Coordination (above) is *how agents talk*. **Cognition** is *how one agent
organizes what it knows and recognizes what it has seen* — bound to the same
file substrate, no new store.

**Knowledge base** — each agent's git-tracked `knowledge/` holds these areas:
`decisions/` (decision records), domain files + `architecture/`, `playbooks/` (repeatable
procedures), `experience/` (retrospectives / failure modes), `direction.md` +
`risks.md` (state), and `candidates/` (proposed learning awaiting consolidation —
the KB inbox, distinct from the mesh `intake/`). The agent **contract** stays in
`CLAUDE.md` + `agent.yaml` (single source — no `knowledge/contract.md`).
`meshctl agent onboard` scaffolds missing areas add-only; `meshctl doctor` warns on
empty areas and errors only on a broken `superseded by NNNN`.

**Memory** — a generated, disposable, `.gitignore`d index (`.memory/index.json`),
a pure function of `knowledge/` + `agent.yaml`. Advisory; the KB is authoritative.
`meshctl memory rebuild` (auto-run after `kb finish`) and `memory recall
"<topic>"` return KB locations + familiarity facts (freshness, refs) **without
reading file bodies**. Recovery is a property: `rm -rf .memory && meshctl memory
rebuild` fully reconstructs it.

**Retrieval loop (agent behavior, not code)** — Recognize → **Recall**
(`memory recall`) → **Retrieve** (read the KB files it points to — authoritative)
→ Reconcile → **Verify** (live systems / MCP) → Act → **Learn** (`kb begin` a
note into `candidates/`, consolidate later via `kb finish`). Memory routes; the KB
answers; live systems confirm.

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
   rest. `meshctl kb finish` merges the change and auto-rebuilds the memory
   index, so `memory recall` now routes to it.
4. **Leave candidates/ clean.** It is a holding area, not a graveyard — consolidate or
   delete; `doctor` warns if candidates pile up unconsolidated.

**Guardrails:**
- **Review gate** — a human or the owning agent approves before a candidate becomes truth.
- **KB is authoritative; the episodic engine is never edited** — it is the disposable episodic
  engine; you distill *from* it, you don't curate *in* it.
- **All writes via `kb begin/finish`** — git-tracked, reviewable, recoverable.

**Optional automation:** a future `meshctl memory distill` may do steps 1–2 (mine +
draft candidates) only. Step 3 (promotion into truth) always stays human/agent-reviewed.

**Capture cross-agent learning on dispatch return.** A headless `meshctl ask`
run may not commit, so anything the answering peer *learned* lives only in the answered
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
  agent promotes it on its next *interactive* session.
- Interactive sessions still distill their *own* learning the normal way (steps 1–4); this
  rule only covers the headless dispatched path, where the peer itself can't commit.
- `kb --agent` uses the same worktree+merge conflict guard, so a concurrent write on the
  owner is handled (conflict → `needs-human`).

### Session handoff — resume where you left off

An authoritative, git-tracked **`knowledge/handoff.md`** per agent: *where we are + the
next concrete step*. The deliberate-tier complement to the episodic engine's fuzzy recap; distinct
from strategic State (`direction.md`/`risks.md`) and from cross-agent `intake/`.

- **Read at start:** the SessionStart surface auto-injects it (`meshctl handoff show`).
- **Write at end / when attention shifts:** compose the body and
  `meshctl handoff set` (Learn stage). Suggested body — *Current focus · Status · Next
  step (the resume point) · Open threads/blockers · Pointers*. Keep it a handoff, not a
  journal; rewrite it (don't append). git history preserves prior handoffs.

### Tool reference — what's in your toolbox + the house rules

Distinct from skills (procedures) and the KB (knowledge): a **tool reference** declares
which CLI tools to use and the mesh's house rules (e.g. "prefer `tea` over `gh`"), as
**pointers** — you self-serve detail with `<cmd> --help`; meshctl never embeds the help.

- Declared mesh-wide in `.agentmesh/pool.yaml` (`tools:`) and/or per-agent in `agent.yaml`
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
