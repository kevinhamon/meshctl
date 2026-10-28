---
status: accepted
date: 2026-10-26
---
# 0077. The mesh console: a live, read-only view of the pool served by meshctl

- **Status:** accepted
- **Date:** 2026-10-26
- **Deciders:** maintainer (accepted 2026-10-26)
- **Related:** ADR-0011 (presence, claims), ADR-0039 (repo hygiene), ADR-0071 (pending work), ADR-0074 (status line), `internal/console`, `internal/mesh/report_json.go`, `internal/mesh/upgrade_stamp.go`

## Context

People run a pool through coding agents. The meshctl CLI is how those agents and their hooks reach the mesh; a person seldom types it. So a person sees the pool through three surfaces: the status line (one line, per agent, in that agent's terminal), the rounds an agent reads out in its chat, and the harness's permission prompts. None shows the pool as a whole. Pending work, doctor findings and a pending upgrade appear only when an agent's session starts, and a person running many terminals cannot tell which agents have sessions open, which have scheduled work running, or which window is which.

Verified while building (2026-10-26):
- `Pending` for every peer of a 16-agent pool takes about 0.6 s; `Doctor` took about 3.9 s serially, almost all of it waiting on child processes, and 1.2 s once peers are checked concurrently.
- Every pool state that pending reads is a file at a known path (intake requests and claims, park markers, the worktree list, presence, reconcile reports); git is needed only for details. fsnotify (kqueue, inotify) reports changes to those directories within milliseconds. On macOS kqueue opens one descriptor per watched file and does not recurse.
- Claude Code's Stop hook payload carries the session's scheduled jobs (`session_crons`: id, schedule, recurring, prompt). No command outside a session lists them.
- Hooks run without a controlling terminal; the harness process above them has one. iTerm2's scripting can find a session by `tty` and select it.

## Options considered

1. **More status-line content.** Rejected: one line per agent cannot show the pool, and it lives in the terminals the person is trying to find.
2. **A native app (for example Wails).** Deferred: per-OS builds, signing and version skew with meshctl, for two advantages only (a menu-bar badge, notifying with no window open). Focus, notifications while open and passkeys all work in a browser.
3. **A page served by meshctl, read-only, from the files it already reads.** Chosen.
4. **Answering rounds in the console** (confirmations, decisions). Parked: a console answer would be a new way to sign a promotion. Per-answer biometrics are inconvenient off one platform, an unlock per sitting is workable but adds a trust path, and attaching a web terminal to sessions needs every session to run inside a multiplexer. Questions stay answered in agent sessions.

## Decision

- **`meshctl console`, in the binary.** One process watches, recomputes and serves on `127.0.0.1` (default port 7717). It is always the same version as meshctl and needs no daemon; `meshctl console install` adds a macOS login item that runs it from login, and `meshctl update` restarts that item on the new binary. Doctor warns when the item's program is gone or nothing answers on the port.
- **Read-only.** No endpoint changes pool state. The one local action is bringing a session's terminal tab to the front. (Narrowed by ADR-0079: the console can also run doctor's automatic repairs.)
- **Guarded.** Every request must name the console in its Host header and carry the token stored in the user's config directory (traded on first visit for an HttpOnly, SameSite=Strict cookie); a non-GET request must also come from the console's own origin. Any local program or website can reach a localhost port, so all three apply.
- **Files are the bus.** The console watches state directories only: the pool directory and its worktrees and runtime state, and per peer the workspace root, inbox, presence, runtime state, knowledge top level and candidates, and the git files that name branches. Inside `.git` only `HEAD`, `packed-refs` and `config` count; the index and locks change on every git command, including the console's own, and would loop. Pending is recomputed on any relevant change (debounced 250 ms); doctor at start, every 10 minutes, on the re-check button, and on change at most once a minute; a minute tick covers state that changes with time alone.
- **Machine-readable reports.** `meshctl pending --json` and `meshctl doctor --json` emit versioned reports (`schema`), each pending line with its status-line phrases, so agents and the console read the same data and the line and the report cannot disagree.
- **Upgrade notice.** `meshctl pool upgrade` stamps the version that ran it (`pool/.memory/upgraded.json`, ignored runtime state). The console and doctor say when the running release is newer. Not on agents' status lines or in their standing orders: it is pool news, not any agent's work.
- **Sessions and scheduled jobs.** Wiring v6 adds a Stop hook (`meshctl hook session-beat`) that records the session's scheduled jobs in its presence record each turn. Presence also records the harness process and its terminal, found by walking up from the hook. A session counts as open while its harness process runs, so an idle session still shows, marked idle; an idle session with a live harness is no longer reaped or flagged stale. Read-only: jobs are stopped in their session. Jobs scheduled outside a coding agent are out of scope.
- **Jump to a session.** On macOS the console brings the iTerm2 tab on the session's terminal to the front (the terminal path is an argument to the script, never part of it). Elsewhere it shows the terminal and says focus is unavailable.

## Consequences

- Positive: the whole pool's waiting work, health, open sessions and scheduled jobs are visible in one window, within moments of a change, without opening any agent's session.
- Positive: doctor is about three times faster for everyone, and agents get versioned JSON for pending and doctor.
- Negative: every peer needs a re-onboard (wiring v5 → v6) before its sessions report scheduled jobs; doctor flags it.
- Negative: a long-running local server is a new surface. The guard keeps it to this user's browser, and it can read but not change the pool; the token file is readable by the user's own processes, including agents, which can therefore read the console's view, no more.
- Negative: when a job last fired is not known; the harness does not report it.
- Negative: an upgrade notice appears only after a `pool upgrade` with this version has written the stamp.
- Follow-ups: answering in the console (option 4) if a signing path the human accepts emerges; a native shell if a menu-bar badge proves worth its cost; opencode scheduled work once it has an equivalent.

## Amendment (2026-10-26, v1.8.1): scheduled jobs belong to the harness process

Claude Code keeps a session's scheduled jobs across `/clear`, which ends one session id (SessionEnd) and starts another (SessionStart) in the same process. Stored in the presence record, the jobs vanished from the console with the old id and came back only after the next turn's Stop hook. The Stop hook now writes the job list to `<agent>/.sessions/jobs/pid-<harness pid>.json` (per session id when the process is unknown), and the console shows a process's jobs on whichever session it runs. Hygiene removes the list once the process has exited. The session listings skip the `jobs/` folder.

## Amendment (2026-10-27, v1.8.2): one console for every pool

A machine can hold several pools, and people who use several want them in one place. A console that served only the pool it was started from meant one console, one port and one login item per pool, with no view of where work waits across them.

- **One console, every pool.** `meshctl console` serves every live pool in the machine registry (ADR-0037) and the pool it was started from, registered or not. It opens on that pool; started outside any pool, it opens on the first registered one instead of failing. Each pool keeps its own engine, so pending work, doctor and watching are unchanged and per pool. The registry is re-read every minute: a pool registered or forgotten appears in or drops out of the console without a restart. The login item opens on the pool it was installed from, or on the first registered pool when installed outside one.
- **Switcher.** The page header lists every pool with its state (agents that need you, doctor errors, waiting items, open sessions, a pending upgrade) and says when another pool needs attention. A page's address names its pool (`#/pool/<key>`, `#/pool/<key>/agent/<name>`); the key is the registry name made URL-safe, with `-2`, `-3` for a repeated name in registry order. A link to a pool no longer served falls back to the default pool.
- **API.** Every endpoint takes `?pool=<key>`; without it, the default pool. Snapshots gain `pool_key` and `pools` (each pool's summary), both additive, so a reader of a single-pool snapshot is unaffected.
- **Visibility across pools.** The console now shows several pools to anything holding its token, and agents can read the token (see Consequences above). An agent in one pool can therefore read another pool's view through the console. This is read-only and shows nothing the same user's processes cannot already read from disk. meshctl commands still act only on the pool they run in, so ADR-0029's separation of pools, which is about what meshctl routes and changes, holds.

## Amendment (2026-10-27, v1.9.0): documents are served without the token

Two paths are exempt from the token, never from the Host check: `/c/...`, where a capability admits one folder of one agent's documents, and `/vendor/...`, the public viewer libraries. Sandboxed documents fetch both without the cookie. See ADR-0078.

## Amendment (2026-10-28, v1.9.1): sorting, and doctor's repairs

- **Sort.** The pool page sorts by **Activity** (default) or **Name**, remembered per browser. Activity puts agents with an open session first, most recently active first by the session badge's idle time; then agents that need the person (Needs you, Waiting, Doctor error, most pending first); then the rest by name.
- **Repairs.** The console can run doctor's automatic repairs, confirmed first. See ADR-0079.

## Amendment (2026-10-28, v1.10.0): layouts, one frame, and Home

The page grew panel by panel, and opening an agent hid the pool. The console now has a written design language and one frame around every page. What the console may change is unchanged here; its settings and the Upgrade button are ADR-0079's amendments.

- **Design language.** `internal/console/ui/DESIGN.md` fixes the tokens (type sizes, spacing, radii, colours) and the components; a test rejects raw sizes outside the token block. Icons are drawn inline SVG.
- **One frame.** A header ("Agent Mesh" and the version, linking Home; live state; settings), a breadcrumb bar under it (Home › pool › agent › section), and a pool footer (a "Pool:" selector, the summary numbers as filters, Re-check, Repair, Upgrade). Sidebars collapse from their own edge. Under ~900px they become overlays and the footer wraps.
- **Two layouts.** **Classic**, the card grid, and **Workspace**, the agent list on the left with the pool overview or the selected agent in the centre, so the pool stays in view. Classic is the default.
- **The agent page** is a content area and a sidebar of section cards (Overview, Waiting on you, Sessions & jobs, Doctor, Documents, Knowledge base, Recent history). The section is in the address: `#/pool/<key>/agent/<name>/<section>`. Agents are shown by their manifest title, with the folder name beneath.
- **Home** at `#/` (`GET /api/home`): totals over every pool and a card per pool with what needs attention and whether it needs an upgrade. It has no pool footer.
- **Plain-language schedules.** Each scheduled job gains an additive `schedule_text` (computed in Go, tested); the cron expression stays in a tooltip, and one it cannot describe is shown raw.
- **API.** Additions only: `/api/home`, `schedule_text`, and each snapshot's pool settings.
