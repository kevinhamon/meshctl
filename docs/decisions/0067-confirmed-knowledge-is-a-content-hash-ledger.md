---
status: accepted
date: 2026-10-16
---
# 0067. Confirmed knowledge is a content-hash ledger written at promotion; the gate asks on `kb finish` and `state set`

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16, grilling of 18 questions)
- **Related:** ADR-0064 (agents propose, humans confirm), ADR-0062 (KB-rule gate), ADR-0063 (delegated work), ADR-0066 (pool repo), `internal/mesh/kb.go` (`KBFinish`), `internal/mesh/state.go` (`StateSet`), `internal/mesh/gate.go`, `internal/mesh/recall.go`, `internal/mesh/doctor_wiring.go`
- **Precedent:** ADR-0064 fixed the rule and the enforcement point; this ADR fills the four open items it listed. The content-hash ledger is new: no precedent found in the ecosystem, the pool doctrine or confirmed KB entries.

## Context

ADR-0064 decided that only a human promotes knowledge into the current tier, enforced by a gate ask at `kb finish`, and left open how the hook tells a candidates-only merge from a promotion, how confirmation is recorded per entry type, how recall marks unconfirmed entries, and what headless runs do.

Verified facts:
- `KBFinish` rebases the session branch and fast-forward merges it. `--headless` changes only conflict handling. There is no approval step and no record of one.
- The gate runs on `PreToolUse` with a 10s timeout and sees the Bash command string. A `git diff --name-only` in the agent dir takes milliseconds.
- Nothing stops an agent editing `knowledge/playbooks/x.md` on the main checkout and committing, which never passes `kb finish`.
- `meshctl state set` rewrites current-tier `state.md` on main directly, with no worktree and no commit.
- Entry formats differ: ADRs carry `status:` frontmatter; playbooks carry optional `rule`/`triggers`; experience carries `tags`; `state.md` is machine-written; the pool glossary (ADR-0066) uses the Pocock format, which forbids metadata inside an entry.
- Gate approval leaves no trace a later process can read: an allowed call and an approved ask look identical to the command that then runs.
- `doctor` already checks whether an agent's gate is wired and whether the harness has an ask path.

**Promotion** (GLOSSARY) names the event: a KB write landing in the current or log tier, where recall treats it as truth. A write landing only in `candidates/` is a proposal.

## Options considered

### Recording confirmation

1. **Frontmatter key per entry** (`confirmed: <date>`). Rejected: impossible in the Pocock-format glossary and in machine-written `state.md`; every entry type needs its own rule.
2. **Derive from git history** (a confirm merge commit; an entry is confirmed if its last touching commit is reachable from one). Rejected: a git log per recall hit is slow, and fast-forward merges leave nothing to anchor on.
3. **A tracked content-hash ledger** (`knowledge/.confirmed.yaml`: path → hash, with per-section hashes for sectioned files). One scheme for every entry type, cheap to check, and an edit after confirmation unconfirms the entry by construction.

### Where the ask text comes from

1. Commit messages on the branch. Rejected: not the three-part summary ticket 08 requires, and nothing forces them to be.
2. A `--summary-file` the agent writes before `kb finish`, read by the gate. Chosen: the summary is required, visible at the prompt, and missing means deny.

### Headless promotion

1. Relay the ask to the hub and re-dispatch with the human's "yes" appended. Rejected: a relayed word in a prompt is not a human at the permission prompt; the gate cannot tell it from the agent's own text.
2. Refuse, leave the branch, confirm later in the peer's own session. Chosen.

## Decision

- **Detection.** The gate matches Bash `meshctl kb finish` (not `--abort`), resolves the session branch and runs `git diff --name-only <main>...mesh/kb/<session>`. All changed paths under `knowledge/candidates/` → allow. Any path in the log or current tier → **ask**. Diff failure → deny.
- **Ask text.** `meshctl kb finish --summary-file <agent>/.memory/confirm/<session>.md`. The gate's reason is that file (intent, grounding, precedent) plus `git diff --stat` of the promoted paths. A promotion with no summary or an empty one is denied. Candidates-only finishes ignore the flag.
- **Bypass closed.** The gate denies file-tool and shell writes to `<agent>/knowledge/**` outside `candidates/` when the path is in the main checkout rather than a worktree, and denies any Bash naming a `mesh/kb/` branch outside `meshctl kb`.
- **`state set` is a promotion.** The gate asks on `meshctl state set` with the same `--summary-file`. meshctl writes section hashes to the ledger and commits `state.md` with it. Headless `state set` is refused.
- **Ledger.** `knowledge/.confirmed.yaml` maps path → `{hash: sha256 of file bytes, confirmed: date, session}`. `state.md` and `glossary.md` carry a `sections:` map of anchor → hash of trimmed section text; the `state.md` header comment is excluded. Sorted keys, one entry per line. An entry is confirmed iff its current hash matches.
- **Ledger placement.** Never on the session branch. After the ff-merge, meshctl writes the ledger on main and commits `Confirm: <first line of summary>`.
- **Trust guard.** Because approval leaves no trace, `kb finish` writes the ledger on every non-headless promotion, trusting the gate. Before doing so it runs the `doctor` gate-wiring check and refuses promotion when the gate is absent or the harness has no ask path.
- **Headless and delegated.** `--headless` promotion returns `ErrKBNeedsConfirm`, leaves branch and worktree intact and prints branch and summary path. The receipt reports `needs-confirm`. The peer's own session start lists pending KB branches; the human runs `kb finish` there.
- **Granularity.** One ask per finish confirms every promoted entry. Partial agreement → deny; the agent moves disputed entries back to candidates and re-runs.
- **ADR status is not confirmation.** `proposed`/`accepted` stays decision state. Doctor warns on an `accepted` ADR that is unconfirmed.
- **Recall.** Current/log hits whose hash mismatches render `[kb/current·unconfirmed]`, rank below confirmed log and above inbox, and inline their body prefixed `_hypothesis_`. No ledger → everything unconfirmed.
- **Precedent on entry.** ADRs: a `- **Precedent:**` bullet. Playbooks and experience: a `Precedent:` prose line. Glossary terms and `state.md`: none.
- **Pool files.** Every write under `pool/` is gate-asked and meshctl-committed (ADR-0066), so tracked pool files at HEAD are confirmed by construction. No ledger there; recall marks `via pool` terms confirmed unless the file is dirty or uncommitted.
- **Doctor.** Flags `mesh/kb/*` branches and worktrees older than 7 days, ledger paths with no file, accepted-but-unconfirmed ADRs, and reports the count of unconfirmed current-tier entries. No auto-fix.
- **Doctrine.** Review-gate wording drops "the owning agent" everywhere; the distiller doctrine says the human confirms at `kb finish`; the template CLAUDE.md KB flow gains the `--summary-file` step. Core text only; pools cannot loosen.

## Consequences

- Positive: one recording scheme covers every entry type, including the Pocock-format pool glossary and machine-written `state.md`. Confirmation is checkable without git calls. An edit after confirmation unconfirms the entry automatically. Each confirmation leaves a `Confirm:` commit.
- Positive: the two write paths that skipped `kb finish` (direct edits on main, `state set`) now meet the same ask.
- Negative: every existing KB reads as unconfirmed until the reconciliation pass (ticket 15) provides a bulk confirm path.
- Negative: the ledger trusts the gate. A harness without an ask path cannot promote at all, which blocks opencode V1 peers until re-onboarded.
- Negative: delegated work that reaches a promotion stops short and waits for the peer's own session. The hub cannot complete it.
- Negative: one more file to write before every promotion. The summary requirement is what makes the ask readable, so this is the cost ADR-0064 already accepted.
- Follow-ups: ticket 12 (outbound gate) reuses `--summary-file` and the wiring guard; ticket 14 prototypes the full path; ticket 16 adds pending branches and the unconfirmed count to the session-start list.
