---
name: mesh-cognition
description: How this agent maintains its own knowledge base and learns over time — the pending-work offer and the intake queue shown at session start, the decision round used to ask the human for any decision, the retrieval loop (recall → retrieve → reconcile → verify → align → act → learn), the confirmation round read to the human before any promotion or outbound write, the distillation ritual (turn recurring intuition into durable KB via kb begin/finish), consolidation (land decisions in the current tier, keep the state digest true, judge candidates), cross-agent learning capture on dispatch return, and session handoff. Invoke at session start (to recall), whenever a session yields a reusable procedure / failure mode / consequential decision (to distill), when recording an ADR or logging a risk, when `meshctl doctor` or `kb consolidate` reports KB debt, on returning from an `meshctl ask` dispatch, and at session end (to write the handoff). Every mesh agent's primary cognition instructions.
---

# Mesh cognition — maintain your own KB, learn over time

Coordination (intake) is how agents *talk*. **Cognition** is how one agent
organizes what it knows and recognizes what it has seen — on the same file
substrate, no new store. This skill is the operative trigger; the **authoritative
full spec is `../__POOL_DIR__/doctrine/agent-comms.md` §Knowledge & Memory (meshctl ADR-0014–0018)**.
When this skill and that section disagree, that section wins — read it.

Your KB is your own git-tracked `knowledge/` tree, in **two tiers**:

- **Current tier — what is true now**, maintained in place: `state.md` (the ≤40-line
  digest, injected at session start), `direction.md` + `risks.md`, domain files +
  `architecture/`, `playbooks/` (repeatable procedures), `reference/` (cited
  external-doc notes), rule notes.
- **Log tier — history**, append-only: `decisions/` (ADRs) and `experience/` (retros /
  failure modes). A decision is a *change* to the current tier, not a place to read
  the current tier from.
- `candidates/` is the KB inbox — proposed learning awaiting review, not truth.

Your contract stays in `CLAUDE.md` + `agent.yaml`. Memory is computed live from
`knowledge/` — advisory; the KB is authoritative.

## Retrieval loop (do this every session)

Recognize → Recall → Retrieve → Reconcile → Verify → **Align** → Act → Learn

0. **Pending work first.** When session start shows a `# Pending work` block, read it to
   the human before any new work and offer triage now, a subset, or deferral. The human
   decides; never triage alone. A deferral is one `Deferred: <ids>` line in the handoff
   (Open Threads). There is no snooze: the items return at the next session start.
   **Intake shows the queue and stops.** On "check intake" or the intake line, run
   `meshctl inbox list` (pending + in-progress, priority then age, a one-line ask each),
   read it as a short numbered queue (id, from, type, priority, age, ask), propose an
   order (priority, then age; flag anything blocking another peer) and wait. Claim and
   work nothing before the answer. Then work in the human's order, by id: `meshctl inbox
   claim <id>`, one at a time, a one-line outcome before the next; re-plan when the human
   says something more important came up; skipped ids go on the `Deferred: <ids>` line.
1. **Recall.** `meshctl memory recall "<topic>"` returns tiered pointers — current
   tier outranks log, superseded decisions never appear. Add `--budget 4000` to inline
   the top current-tier bodies so the knowledge is in view. Your `state.md` digest and
   `handoff.md` are auto-injected at start.
2. **Retrieve.** Read the KB files recall points to — they are authoritative.
3. **Reconcile** conflicts in favor of the maintained KB; **Verify** against live
   systems / MCP before acting.
4. **Align.** Reach **shared understanding** with the human: their agreement to your
   intent, your grounding (cite each claim, or mark it *assumption*) and your precedent
   (cite where the pattern already exists, or mark it *new*). Required when the work
   touches more than one artifact, makes any outbound write, makes any promotion, or
   rests on any claim you cannot ground. Pools may tighten this in `local.md`, never
   loosen it. For a promotion or an outbound write, this is the confirmation round (below).
5. **Act**, then **Learn** — distill anything durable (below).

## Decision round — the default way to ask the human

Whenever you need the human's decision on proposed changes (an intake order, a plan, a
rule, a KB change, an outbound write), present a decision round, one item per change:

```
📝 **C<n> — <short title>**
From now on it says: …        (a change of rule or knowledge)
It no longer says: …
  — or, for an action — what you will do, and why
➡️ ✅ Confirm
```

Then say how to answer: `yes` for all, or `C<n> no: <reason>` for one. Keep rounds small
(about 3–5 items) and wait for the answer before acting. The confirmation round below is
this same format with its extra fields.

## Confirmation round — read before truth changes or anything leaves the mesh

Before a promotion (a `kb finish` that touches more than `candidates/`, `state set`,
`kb confirm`, `inbox triage apply`) or an outbound write, write ONE summary file for the
session, `.memory/confirm/<session>.md`:

```
## Intent
<one line: what changes and why>

## Grounding
<the source of each claim, or *assumption*>

## Precedent
<where the pattern already exists, or *new*>

## Coherence
<how it agrees with related canonical entries — required when there are any>
```

Then read it to the human **in chat** as a decision round, one item per entry, ending in a
recommended confirm, with its grounding and precedent:

```
📝 **C<n> — <entry>** (new|amended|retired)
From now on it says: …
It no longer says: …
Grounded in: …
Precedent: …
➡️ ✅ Confirm
```

- "yes" confirms the round. Only then run the command with the summary
  (`meshctl kb finish --summary-file <summary>`, `meshctl state set --summary-file
  <summary>`, or the outbound call); the permission prompt is the signature of what was
  read, never the reading surface.
- "C2 no: reason" disputes that entry: move it back to `candidates/` (or drop the
  write), update the summary, and confirm the rest.
- A promotion denied with no change is never resubmitted as is:
  stop and ask which entry is disputed.
- With no human at your prompt (headless, or delegated work), a promotion or outbound
  write ends in `needs-confirm`: respond `needs-human` with a body opening
  `needs-confirm:` carrying the round and the summary path. Never relay a confirmation.

## Distillation ritual — System 1 → System 2 (reviewed, never automatic)

Run it opportunistically: a *recurring* pattern/gotcha/lesson, a task that left a
reusable procedure or failure mode, a consequential decision, or when `meshctl
doctor` warns `playbooks/`/`experience/` are empty. Quality over volume.

1. **Mine** the intuition engine — `meshctl intuition recall "<topic>"` (or the
   claude-mem `mcp-search` tools for depth). Keep signal that repeats, not one-offs.
2. **Draft a candidate.** `meshctl kb begin`, then write one
   `knowledge/candidates/<slug>.md` per learning: *what* it is, *why* it matters,
   the *evidence* (session/obs/file refs). Candidates are proposals, not truth.
3. **Consolidate.** Judge each candidate; if durable + verified, move/rewrite it
   into the right area — `playbooks/`, `experience/`, a **decision** (ADR in
   `decisions/`), or `system-map.md`; a new term goes to the pool glossary
   (`mesh://pool/glossary.md`), not a peer glossary. Log new risks in `risks.md`.
   Drop the rest. Write the summary file, read the confirmation round, then
   `meshctl kb finish --summary-file <summary>`: the human confirms, and the merge
   auto-rebuilds the memory index.
4. **Leave `candidates/` clean** — a holding area, not a graveyard. `doctor` warns
   when a candidate is older than 14 days.

## Consolidation — keep the current tier true (do this when doctor or `kb consolidate` says so)

Distilling only ever *adds*. Consolidation is what keeps a growing KB a brain rather
than a log. `meshctl kb consolidate` lists the debt in the order to work it off:

- **Decisions must be machine-readable.** Every ADR carries YAML frontmatter the
  tooling reads (the human `- **Status:**` bullet stays for the reader):
  ```yaml
  ---
  status: accepted            # proposed | accepted | rejected | deprecated | superseded
  date: 2026-10-14
  supersedes: [0045]          # when this replaces an earlier decision
  superseded_by: [0050]       # set on the OLD decision when it is replaced
  updates: [architecture/backend-apis.md, state.md]   # the current-tier docs this changes
  ---
  ```
  `meshctl doctor --fix` adds it to decisions that only have the bullet. Recall drops
  `superseded` / `deprecated` / `rejected` decisions, so marking the old one is what
  retires it. Write new ADRs with the frontmatter from the start.
- **A decision must land.** Each doc in `updates:` is rewritten to reflect the decision;
  until it is, `doctor` reports an error (the doc is older than the decision that changed
  it). Accepted decisions newer than `state.md` are a warning until the digest absorbs them.
- **Keep the digest true.** `knowledge/state.md` is the current tier's front page — Now /
  Active / Recently decided / Open questions, ≤40 lines, standing facts only. Rewrite it
  after consolidating, in one pass, from the fixed template:
  ```
  meshctl state set --summary-file <you>/.memory/confirm/<session>.md <<'EOF'
  ## Now
  <3-8 lines: the standing truths of this domain today>

  ## Active
  - <initiative / migration in flight — where it stands> (or: none)

  ## Recently decided
  - ADR-NNNN <what changed because of it> (or: none)

  ## Open questions
  - <unresolved conflict / pending decision — who owns it> (or: none)
  EOF
  ```
  `state set` refuses a body with a missing section, a leftover placeholder or over
  budget (`--force` overrides). `meshctl state template` prints the shape. The digest is
  current-tier knowledge, so `state set` is a promotion: summary and round first.
- **Judge aged candidates.** Promote into `playbooks/` / `experience/` / a decision, or
  delete. All of this is ordinary KB work: `meshctl kb begin` → edit → commit →
  `meshctl kb finish`; the digest and handoff go through `state set` / `handoff set`.

**Off the main thread.** Steps 1–2 are slow and mechanical, so they run in the
background: on Claude Code, spawn the scaffolded `distiller` subagent
(`.claude/agents/distiller.md`) with `run_in_background: true` and relay only the
DECISION items it returns; on opencode the session-end hook runs the same procedure
(`../__POOL_DIR__/doctrine/distiller.md`). The per-prompt hook nudges you once per
session at a work boundary (a wind-down phrase, or ~8 exchanges in) — act on it then.
The distiller drafts candidates only; **you** do step 3.

Guardrails: **review gate** (only the human confirms before a candidate becomes truth,
after the confirmation round; you never confirm your own writes) · **KB is authoritative, claude-mem is never edited** (distill *from*
it) · **all writes via `kb begin`/`kb finish`** (git-tracked, recoverable).

**Standing rules → gated (meshctl ADR-0062).** A "never / do not repeat / user directive" note gets
`rule: "<one line>"` + `triggers: ['<case-insensitive Go regex on the action text>']`
frontmatter, so `meshctl hook pre-tool-use` denies the first matching tool call with the
rule. Match the command/path/MCP args, not the topic; allow flags between words
(`git\b.*\bremote\b`). Verify by piping a sample payload into the hook (match → deny,
unrelated → empty). Chat-only rules stay prose. Every new standing rule is written this way.

## Cross-agent learning capture on dispatch return (meshctl ADR-0018)

A consulted peer (headless `meshctl ask`, or `--in-session` as a subagent) may not
commit, so anything the answering peer learned lives only in the answered `intake/`
file and is lost if never distilled. On returning from a dispatch, judge the answer and
write durable learning as a candidate **into the OWNING agent's KB, not your own**:

```
meshctl kb begin  --agent <owner>
# write knowledge/candidates/<slug>.md in the owner's worktree, with provenance
#   frontmatter:  source: dispatch <caller> <intake-id>
meshctl kb finish --agent <owner>     # from a normal cwd, not inside the worktree
```

Domain knowledge lands with its owner (a database gotcha → the database expert, an
org-level pattern → the peer that owns architecture). It lands as a candidate so the
review gate holds; the owner proposes it in its next interactive session, where the
human confirms it.

**Delegated work is different** (`--in-session --work`, meshctl ADR-0063): the peer runs
write-capable, so it commits its *own* learning through `kb begin|finish --agent <peer>`
and lists it in its receipt. Don't re-file it — relay the receipt. When **you** are the
delegated peer, do the same: KB writes via `--agent <you>`, commits in your workspace (or
the worktree the brief names), a standalone receipt at the end. A promotion or an
outbound write ends in `needs-confirm` with the round and your summary path — you never
relay a confirmation.

## Closing an intake exchange (meshctl ADR-0043)

Intake is an **ephemeral, git-ignored discussion** — the record is the KB, not the
inbox. To close a request: write the outcome back, let the requester read it, **distill
the durable residue to the right artifact by type** — a *decision* → an ADR (don't
inflate: most requests aren't decisions; one owner writes it, the other's input folds
in, refine by amend/supersede); a reusable procedure → a playbook; a lesson → an
experience note; `fyi`/routine → often nothing — then `meshctl inbox archive <id>` (or
`--answered`). The coordination audit lives in `dispatch.log`.
A triage-closed request owes no distillation: its residue already lives at the verdict's ref.

## Intake triage — closing what no longer stands (meshctl ADR-0072)

`meshctl inbox triage` writes a worksheet of signals over your open requests; you judge
each and write the report beside it, one line per request:

```
- <id> · <stands | superseded → <ref> | done → <ref> | withdrawn → <ref> | unclear> · "<quoted grounding>" · <source>
```

`superseded`, `done` and `withdrawn` close the request and need a ref and a quote;
`stands` and `unclear` carry no ref (the quote is the live need, or the signals checked).
Read the report to the human as a confirmation round, item by item, then
`meshctl inbox triage apply --report <file> <id>...` (at most five) closes what they
confirmed.

## Session handoff — resume point (meshctl ADR-0017)

Keep an authoritative, git-tracked `knowledge/handoff.md`: *where we are + the next
concrete step*. Read at start (auto-injected). Write it at session end, or when the
user asks for a handoff — **in one pass, with no deliberation about format**: the
format is fixed. Fill in exactly this (also printed by `meshctl handoff template`)
and send it in one command:

```
meshctl handoff set <<'EOF'
## Current State
<2-6 lines: what is true right now — done, in progress, where things live>

## Next Step
<the single next action, concrete enough to start cold>

## Open Threads
- <blocker / pending decision / follow-up — who owns it> (or: none)
- Deferred: <ids> (only when the human deferred pending work)

## Pointers
- <paths, commits, intake ids, URLs the next session needs> (or: none)
EOF
```

Rules — that is all there is to decide:
- **Facts only**, no session narrative ("I then…"), no hedging. At most 40 lines.
- **Rewrite the whole thing** each time; never append. Git keeps prior handoffs.
- Keep the quoted `'EOF'` so nothing is shell-expanded. meshctl adds the timestamp.
- Don't re-read or second-guess it after writing; `meshctl handoff set` refuses a body
  with a missing section, a leftover placeholder or over budget — fix and resend
  (`--force` writes it anyway).

## Repo hygiene — knowledge is only safe once pushed (meshctl ADR-0039)

Your workspace must be its own git repo **with a remote**. `knowledge/` (KB + handoff)
is durable only once committed **and pushed** — otherwise it dies with this machine.
`meshctl agent new` inits the local repo; if yours isn't a repo, `git init` + commit.
Create a remote and push, and commit `knowledge/` as you distill. Session start injects
a persistent reminder until a remote exists (it clears when you push).

## Guardrail

Everything in `knowledge/` and the ADR/risk ids you cite are **mesh-internal** —
correct to reference here, banned from crossing out to any product/external surface
(issue tracker, wiki, product-repo commits/PRs, code comments, email, chat). See
`agent-comms.md` Core rule 7.
