---
status: accepted
date: 2026-10-26
---
# 0074. The status line is a meshctl hook subcommand, wired per agent and refreshed on a timer

- **Status:** accepted
- **Date:** 2026-10-26
- **Deciders:** maintainer (accepted 2026-10-26)
- **Related:** ADR-0030 (per-agent settings wiring), ADR-0071 (pending work), `internal/mesh/statusline.go`, `internal/mesh/settings.go` (`ensureStatusLine`), `GLOSSARY.md` (pending work)

## Context

ADR-0071 puts a peer's pending work in front of the human at session start and in `meshctl pending`. Between those moments nothing shows it: work that lands mid-session is invisible until the next session or a manual command.

Claude Code can show one line under the prompt, produced by a command named in `settings.json` under `statusLine`. A hand-written script had filled that role outside the binary.

Verified facts:
- Claude Code re-runs the status-line command on its own events (a new message, a mode change). An optional `refreshInterval` (seconds) re-runs it on a timer as well; without it, an idle session never redraws.
- The command receives JSON on stdin with `cwd` and `workspace.current_dir` / `workspace.project_dir`, and its stdout is shown as is, ANSI colors included.
- Project settings override user settings, so a `statusLine` in an agent workspace's `.claude/settings.json` wins over a user-level one inside that workspace only.
- The hand-written script ran `meshctl pending` as a subprocess and parsed its markdown, so any wording change in the block broke the counts silently. Because the subprocess was assumed slow, it showed cached counts and refreshed them in the background; the fresh counts appeared only on the next redraw.
- Computing `Pending` in process takes well under a second on a live pool.
- The mesh's harness integration otherwise lives in the binary with no python and no loose scripts (`internal/mesh/hook.go`).

## Options considered

1. **Keep a separate script that calls `meshctl pending`.** Rejected: parses human-facing text, ships to no one else, and needs a cache to hide subprocess cost.
2. **`meshctl pending --json` plus a thin script.** Rejected: still a loose script per machine, and the wiring is still by hand.
3. **A `meshctl hook status-line` subcommand that computes `Pending` in process, wired per agent with a refresh interval.** Chosen.

## Decision

- **Command.** `meshctl hook status-line` reads the status-line JSON on stdin, resolves the agent from `workspace.project_dir`, then `workspace.current_dir`, then `cwd`, walking up to the nearest agent workspace in the pool, and prints one line. It calls `Pending` directly: no cache, no subprocess.
- **Content.** `[agent: <name>]`, then what waits on the human: `needs-confirm` (parked KB branches) and `conflicts` in red; `intake` and `dispatched` (delegated work) with their count and oldest age; `[kb: N overdue, say "consolidate"]` when candidates are past the 14-day threshold. With none of these, `[pending: none]`. Read-back is left out: answers are read at session start.
- **Fail-open.** No pool or no agent renders the directory name, dimmed. The command always exits 0.
- **Wiring.** `agent new` and `agent onboard` add `statusLine` with `refreshInterval: 10` to the agent's `.claude/settings.json` only when the workspace has none. An existing status line, hand-set or the mesh's own, is never changed. Claude Code only; other harnesses have no status line.
- **No wiring-version bump.** The status line carries no gate policy, so doctor does not ask every peer to re-onboard; the next `agent onboard` or `pool upgrade` adds it.

## Consequences

- Positive: pending work is visible within ten seconds of landing, idle or not, in every agent workspace, with no per-machine setup.
- Positive: the line and `meshctl pending` share one computation; they cannot disagree.
- Negative: inside agent workspaces the mesh's line replaces a user-level status line the human may have set. Accepted: setting `statusLine` in the workspace's own settings keeps theirs, and meshctl will not touch it.
- Negative: `Pending` runs every ten seconds per open session. Accepted at its measured cost; revisit if a pool's KB grows enough to make the hash pass slow.
- Migration: a pool that used a hand-written status-line script removes it from user settings once its agents are re-onboarded.

## Amendment (2026-10-26): every group names the phrase that starts its review

Each pending group ends in a dimmed `say "<phrase>"`: `confirm kb` (needs-confirm), `resolve conflicts` (conflicts), `check intake` (intake), `review work` (dispatched), `consolidate` (kb overdue). `[agent: …]` and `[pending: none]` carry none.

A hint is only as good as the agent's reading of it, so each phrase is defined in the standing orders: the managed Cognition section of `CLAUDE.md`, the new-agent template and the mesh-cognition skill. Every definition starts from a fresh `meshctl pending`, because the pending block is injected only at session start. A phrase starts a review; it never acts without the human. A test fails if the line shows a phrase that the standing orders do not define.

## Amendment (2026-10-26): unconfirmed entries show, with "reconcile"

The line counted candidates only, so the unconfirmed current-tier entries that `meshctl pending` reports never reached it, however long they waited, and nothing named the review that works them off. A new group `[unconfirmed: N, say "reconcile"]` shows whenever the KB health line counts unconfirmed entries, in the terminal's own color (red stays for what blocks). It is the same count as `meshctl pending`, which keeps its rule: the KB health line appears only once the agent has candidates or a reconcile report. The candidate rule is unchanged: `[kb: N overdue, say "consolidate"]` only past the 14-day threshold, since a fresh candidate is not yet debt.

"reconcile" is defined in the standing orders: `meshctl kb reconcile --peer <you>` writes the worksheet, the agent writes the reconcile report from it, then reads the clean entries to the human as confirmation rounds, at most five paths per `meshctl kb confirm` (ADR-0070). Conflicts go to "resolve conflicts". The count is a visible nudge, not a schedule; the pass still runs only when the human says so. Phrases are now keyed by the group's label, since one pending line feeds two groups.
