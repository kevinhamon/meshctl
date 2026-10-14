---
status: accepted
date: 2026-07-25
---
# 0043. Intake is an ephemeral discussion: git-ignored, distilled to the KB, then archived

- **Status:** accepted — **implemented** (scaffold gitignores `intake/`; `meshctl inbox archive`; doctrine)
- **Date:** 2026-07-25
- **Deciders:** maintainer
- **Related:** revises the "never delete a request — it is the audit trail" line in `agent-comms.md`; applies the distillation ritual (ADR-0016/0018) to intake close; ADR-0011 (claims are ephemeral).

## Context

Intake request files (`intake/*.md`) accumulated with no lifecycle end, and it was
unclear whether they were durable (commit) or transient (gitignore). Initial instinct was
"commit as an audit trail," but that conflates two things: the mesh already has the
*coordination* audit (`dispatch.log`) and the *durable outcome* (the KB). The raw
request/response is scaffolding for an exchange whose residue belongs in the KB.

A proposed pattern — "turn every answered exchange into an ADR, both parties" — is the
right instinct (capture before clearing) but over-reaches: not every exchange is a
consequential decision, so ADR-per-exchange causes **ADR inflation** (ADRs must stay scarce
to mean anything), and both-parties-author causes duplicate/divergent records.

## Decision

Treat intake as an **ephemeral discussion**, not a ledger:

1. **Git-ignore the inbox.** The scaffold's `.gitignore` ignores `intake/*.md` (keeping
   `intake/README.md`), `intake/*.claim` (runtime locks, ADR-0011), `.memory/`, and the
   archive dir. Intake is local/transient; the KB is the record.
2. **Close = distill-then-archive.** To close an exchange: write the outcome back
   (`status: answered` + response) → requester reads it back → **distill the durable
   residue to the RIGHT KB artifact by type**, then `meshctl inbox archive <id>` (or
   `--answered`) moves the spent request into the git-ignored `.intake-archive/`
   (recoverable trail, not deleted).
3. **Distill to the right artifact — don't inflate ADRs.** A consequential *decision* →
   an ADR; a reusable procedure → a playbook; a lesson/gotcha → an experience note;
   `story-enrichment` → the tracker story; `fyi`/`review`/routine → often nothing. ADRs stay
   scarce.
4. **Single owner per artifact.** The domain owner writes the resulting artifact (architect
   owns ADRs); the other party's input folds in. Refinement over rounds = amend while
   `proposed`, or supersede once `accepted` (ADRs are immutable-once-accepted).

## Consequences

- Positive: inboxes stay small; agent repos aren't polluted with transient traffic; the
  record is the KB (distilled, authoritative), with `dispatch.log` as the who/when audit.
- Positive: "distill gates the clear" makes closing loss-safe — you archive *after*
  capturing the residue.
- Positive: avoids ADR inflation (the failure mode of "every exchange → an ADR") and
  dueling records (single-owner artifacts).
- Trade-off: the raw request/response text isn't in git history (only the distilled
  outcome + `dispatch.log` metadata + the recoverable `.intake-archive/`). Accepted — if an
  exchange mattered, its residue is in the KB; if it didn't, the raw text isn't worth
  keeping.
- Migration: already-committed intake files in existing agents stay tracked until manually
  `git rm`'d; the gitignore only affects new traffic. `onboard` adds the ignore block to
  existing agents (idempotent, marker-guarded).
