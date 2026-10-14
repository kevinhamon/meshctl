---
status: accepted
date: 2026-07-16
---
# 0018. Cross-agent learning capture — dispatched exchanges land in the owner's candidates/

- **Status:** accepted
- **Date:** 2026-07-16
- **Deciders:** maintainer
- **Related:** ADR-0016 (distillation / Learn stage — this closes its loop for the dispatched path), ADR-0014 (per-agent KB + candidates/ area; architect KB = shared org layer), ADR-0011 (`kb begin/finish` durable-write substrate, incl. `--agent`), ADR-0010 (files-only, ownership boundaries, no shared store)

## Context

Cross-agent knowledge access is already solved by **`ask-agent`** (ADR-0011 messaging):
agent A asks the owning agent B a question; B answers from **its own** KB. Ownership-
respecting message-passing — not reaching into another agent's store. That is the correct
mechanism and needs no change.

The gap is **persistence of what is learned during a dispatched exchange.** A headless
`ask-agent` run is, by explicit design (dispatch prompt), forbidden to commit, push, or
deploy — it only writes the outcome back into the driving `intake/` request file. So any
durable insight B derives while answering:

- is **not** committed into B's KB (B is headless — cannot `kb finish`), and
- lives only in the answered intake file until *someone* later distills it interactively.

If no one does, the insight is lost to durable, recallable KB. This is the same failure
class this project just hit at scale — knowledge left uncommitted on disk, nearly discarded
(agent-mesh's own source). "Robust" here means **committed immediately**, not
memory-dependent and not a loose file.

Two facts make a clean fix available *without new engine code*:

- `agentmesh kb begin/finish` already takes **`--agent <name>`**, so an interactive caller
  can write into *another* agent's KB through the same worktree + merge + conflict-guard
  substrate (ADR-0011) — ownership-safe, not a raw cross-repo `git` stomp.
- `ResolveKBSession` (this session's fix) lets `kb finish` find the worktree even when
  begin/finish run in different shells (the minted `s-<pid>-<nano>` id differs). Cross-shell
  `kb --agent` is therefore reliable — the fix that unlocks this ADR.

## Options considered

1. **Leave as-is — distill later, interactively.** The answer sits in the intake file
   (git-tracked once committed) and is promoted whenever that agent's next interactive
   session runs the ritual. Pro: zero work. Con: relies on someone remembering; the insight
   is not in recallable KB until then; commonly forgotten → lost. Not robust.
2. **Requester folds the answer into its *own* `candidates/`.** Pro: the requester commits
   (it is interactive). Con: **ownership-wrong** — B's domain knowledge lands in A's KB
   (a datastore gotcha in architect's KB), scattering knowledge and creating the exact
   duplicate/parallel-store anti-pattern ADR-0014 forbids. Partial robustness.
3. **Headless B writes an uncommitted candidate file.** Headless runs *may* Write files,
   so B drops a `candidates/<slug>.md` in its own KB. Pro: ownership-correct, on disk.
   Con: **uncommitted** — precisely the loose-file-on-disk fragility this project just spent
   a session recovering from. A `git clean` or a fresh clone loses it. Not robust.
4. **Caller captures on return into the *owner's* `candidates/`, committed via
   `kb --agent`.** The interactive caller, on `ask-agent` return, judges the answer and —
   for durable learning — writes a candidate into the **owning** agent's KB via
   `agentmesh kb begin/finish --agent <owner>`. Committed immediately, ownership-correct,
   review-gated (a candidate is a proposal, not truth). Uses only existing tooling +
   this session's `ResolveKBSession` fix. Most robust.

## Decision

Adopt **Option 4.** Cross-agent knowledge access stays `ask-agent`; cross-agent knowledge
**capture** is the responsibility of the **interactive caller on return from a dispatch.**

- **Interactive sessions** persist their own learning the normal way (ADR-0016 ritual: own
  `candidates/` → promote → `kb finish`). Unchanged.
- **Headless dispatched exchanges** are captured by the caller: after `ask-agent` returns,
  the caller distills the answered request and, for durable learning, writes a **candidate**
  into the **owning agent's** KB with `agentmesh kb begin/finish --agent <owner>` — a
  committed, conflict-guarded, review-gated write. Domain knowledge lands with its owner
  (datastore gotcha → datastore-expert/candidates/); org-level knowledge → architect/
  candidates/. The owning agent promotes it (candidate → playbook/experience) on its next
  interactive session.

Trade-off accepted: an agent's `candidates/` may now receive entries authored *by another
agent* (the caller). This is bounded and safe — candidates are explicitly un-promoted
proposals, the write goes through the owner's own `kb` substrate (mergeable, reviewable),
and promotion to truth remains the owner's interactive, human/agent-reviewed act. The
alternative (ownership-wrong placement, or uncommitted loss) is worse.

## Consequences

- **Positive:** no dispatched exchange silently loses knowledge; learning is committed
  immediately (no loose-file / memory-dependent window); ownership stays correct; the
  review gate is preserved (lands as a candidate, not truth); **zero new engine code** —
  reuses `kb --agent`, `candidates/`, the distillation ritual, and `ResolveKBSession`.
- **Negative / new risks:**
  - **Caller discipline** — capture depends on the caller actually doing it on return. It is
    a protocol step in the distillation ritual; cheap, but a Stop/`doctor` nudge may help
    (see follow-ups). Open as a risk if it slips in practice.
  - **Cross-authored candidates** — an owner may find candidates it did not write. Mitigated
    by frontmatter provenance (`source: dispatch <caller> <intake-id>`) so the owner can see
    origin + evidence on review.
  - **Concurrency** — a caller writing `--agent B` while B runs interactively is handled by
    the same worktree/merge conflict guard as any KB write (ADR-0011); a headless-style
    conflict leaves `needs-human`.
- **Migration / sequence:**
  1. Document the on-return capture step in `agent-comms.md` (distillation ritual). **This ADR ships with it.**
  2. Add a `source:` provenance convention for dispatch-captured candidates.
  3. Optional later: a thin `agentmesh learn --agent <owner> --from <intake>` helper that
     writes the candidate + `kb finish` in one command (only if the manual step proves
     repetitive — protocol-first, codify on evidence).
  4. Optional: `doctor`/Stop-hook nudge if a dispatched exchange returned but no capture
     happened.
- **Follow-ups:** none blocking. Does not change `ask-agent`, recall (stays per-agent
  local), or the ownership model — it only adds a committed capture step on the return path.
