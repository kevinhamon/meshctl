# Background distiller — harness-neutral procedure

The distiller is the **Learn** stage of the retrieval loop (agent-comms.md §Knowledge
& Memory, ADR-0016) run **off the main thread**. It exists so an agent learns over
time *without* the user driving the slow steps and *without* blocking the live
session. It drafts; it never promotes consequential knowledge on its own.

This file is the **neutral "what"** — the procedure and contracts. Each harness binds
it to its own background-task primitive (the **"how"**):

- **Claude Code:** a `distiller` subagent (`.claude/agents/distiller.md`) launched via
  the Agent tool with `run_in_background: true`.
- **opencode / other:** map to that harness's background-task primitive; the procedure
  and contracts below are unchanged.

## Inputs

- `agent` — the owning agent whose KB is being distilled (its `knowledge/` tree).
- `window` — observations to consider: since the last distill watermark, else "this
  session".

**Always pass the owner explicitly — never rely on cwd.** `meshctl intuition recall`
and `meshctl memory recall` resolve the agent from `$AGENT_NAME` or the cwd basename,
so a distiller running off another agent's cwd (e.g. a subagent spawned elsewhere)
silently queries the WRONG namespace and returns nothing. Pass `--agent <agent>` on every call
(there is no `--project` flag; passing one fails the call).

## Steps (all off the main thread)

1. **Mine.** Pull the owner's recent episodic signal. Use, in order of reliability:
   - `meshctl intuition recall --agent <agent> "<topic>"` — compact pointers, the
     primary source (works on the default `worker` runtime).
   - claude-mem `search` and `timeline` (mcp-search) — these work on `worker`. The
     `observation_search` / `observation_context` / `memory_search` tools require the
     `server-beta` runtime and will FAIL on the default `worker` — **do not depend on
     them**; if present they are a bonus.
   Look for signal that **repeats across sessions**, not one-offs.
2. **Reconcile against everything already known — both stores:**
   - the git KB: `meshctl memory recall --agent <agent>` + read what it points to;
   - the owner's **private recall-memory store**, if the harness maintains one (a
     separate, already-distilled set of durable facts). The binding names its concrete
     location. `meshctl memory recall` does **not** read it — you must read it
     directly, or you will re-derive facts already captured there.
   Discard anything already captured in either store. If a learning is already a private
   fact but belongs in the **shared** git KB as a playbook, frame it as *"promote
   existing private fact into shared KB"*, not as net-new. Anything that *conflicts* with
   current truth in either store is always a DECISION.
3. **Draft** one candidate note per surviving learning (see output contract).
4. **Triage** each candidate into exactly one bucket (rubric below).

## Triage rubric (conservative — when unsure, never AUTO)

- **AUTO** — a concrete, low-blast-radius fact/procedure with clear evidence that does
  **not** change or contradict existing truth (e.g. "command X needs flag `--foo`",
  "endpoint Y requires header Z"). Written silently as a candidate; not surfaced.
- **DECISION** — anything consequential, ambiguous, judgment-bearing, conflicting with
  existing KB, or that would **create/modify** a playbook, experience note, or ADR.
  **Default here whenever unsure.** Surfaced to the live session.
- **NOISE** — one-offs, session-local chatter, already-known, or unverifiable. Dropped.

## Output contract

- **Every kept learning** → write `knowledge/candidates/<slug>.md` in the owner's tree,
  frontmatter:
  ```yaml
  ---
  source: distiller <YYYY-MM-DD>
  triage: auto | decision
  evidence: [session/obs ids, file refs]
  ---
  ```
  Body: *what* it is, *why* it matters, the *evidence*. Candidates are **proposals, not
  truth** — plain-written files, not committed by the distiller.
- **Each DECISION** → also append an entry to `knowledge/pending-decisions.md`:
  `- [ ] <id> — <one-line> · rec: <keep as playbook | open ADR | drop | …> · candidate: candidates/<slug>.md`
- **Return** (to whatever launched it) a short summary: counts per bucket + the DECISION
  one-liners **only**. This return is the sole thing the main thread relays to the user.

## Guardrails

- **Draft only — never promote.** The distiller never writes into `playbooks/`,
  `experience/`, `decisions/` (ADRs), `glossary.md`, or `direction.md`, and never runs
  `meshctl kb finish`. Promotion of a candidate into truth stays a **reviewed** step
  the owning agent does interactively (ADR-0016 review gate).
- **Writes confined to `knowledge/candidates/` + `knowledge/pending-decisions.md`.**
- **claude-mem is read-only** — mine *from* it, never edit it.
- **Idempotent** — re-running must not duplicate an existing candidate; skip slugs that
  already exist and update the watermark.
- **Mesh-internal** — candidate/decision text may cite ADRs/R-NN/KB paths (internal); it
  must never be surfaced onto a product/external artifact (agent-comms.md Core rule 7).
- **Silent when empty** — no new durable signal ⇒ write nothing, surface nothing.
