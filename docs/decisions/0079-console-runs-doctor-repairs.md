---
status: accepted
date: 2026-10-28
---
# 0079. The console can run doctor's automatic repairs

- **Status:** accepted
- **Date:** 2026-10-28
- **Deciders:** maintainer (accepted 2026-10-28)
- **Related:** ADR-0039 (repo hygiene), ADR-0077 (mesh console), `internal/mesh/hygiene.go`, `internal/console/server.go`

## Context

The console is read-only (ADR-0077). Doctor marks the findings it can repair itself (`fixable`): stale runtime state, retired files, legacy status words, leftovers of earlier upgrades. Each repair is defined as safe, local, and reversible or regenerable, and text a person or agent wrote is never edited. The page said "fixable by doctor --fix", but the person using the console does not run meshctl directly, so applying a repair meant opening an agent session to run one command.

## Options considered

1. **Keep the console read-only.** Rejected: the most common pool chore stays a session round-trip for no safety gain, because the repairs are the same ones `pool upgrade` already applies unasked.
2. **A general action surface (run any meshctl command).** Rejected: every action is its own trust question; answering rounds in the console stay parked (ADR-0077).
3. **One action: doctor's automatic repairs, pool-wide, confirmed first.** Chosen.

## Decision

- **One action that changes the pool.** `POST /api/doctor/fix?pool=` runs exactly what `meshctl doctor --fix` runs for that pool, then publishes the new doctor report. ADR-0077's "No endpoint changes pool state" now reads: no endpoint changes pool state except this one. Any further action needs its own decision.
- **Confirmed, then reported.** When doctor has fixable findings, the pool page shows a repair bar. Its button opens a list of exactly those items; nothing runs until the person confirms. The result lists what was repaired and what failed. Items that need a person are left alone and stay listed.
- **Pool-wide only.** One button per pool, repairing every fixable item, as `doctor --fix` does. No per-agent or per-item repairs.
- **Guarded like every POST.** Host, token and Origin, as ADR-0077. One repair runs at a time per pool; a second request while one runs gets 409.

## Consequences

- Positive: the person clears doctor's routine drift from the page, without an agent session.
- Negative: anything holding the console token can trigger the repairs. Agents can read the token (ADR-0077), but any agent can already run `meshctl doctor --fix`, so this grants nothing new.
- Negative: the console is no longer strictly read-only; the README and ADR-0077 say so.

## Amendment (2026-10-28, v1.10.0): the console saves its own settings in the pool repo

The console's settings (layout, agent order, the attention filter) are per pool and live in the pool repo, so a pool cloned on another machine opens the same way. Browser storage kept them on one machine only.

- **A file of its own.** `pool/console.yaml`, beside `config.yaml`. The console never writes `config.yaml`, which meshctl never rewrites (ADR-0066).
- **Committed, never pushed.** `POST /api/settings?pool=` writes the file and commits only that path in the pool repo, with the person's git identity, like `pool upgrade`; anything else uncommitted is left alone. Pushing stays with the person. "Apply to every pool" writes and commits the same settings in each pool the console serves.
- **The pool on screen decides.** Each snapshot carries its pool's settings; a pool without the file gets the defaults (Classic, activity order, every agent shown). Which sidebars are open and the footer's filter stay in the browser: they are view state, not settings.
- **Guarded like every POST.** An agent holding the token could change how the console looks for a pool, nothing more.

This is the console's second change to a pool. Each further one still needs its own decision.

## Amendment (2026-10-28, v1.10.0): the console can upgrade a pool

A pool whose last `pool upgrade` ran with an older meshctl shows **Needs upgrade** on Home, with an Upgrade button there and in the pool's footer. Before, the upgrade meant asking an agent to run one command.

- **Preview first.** The button runs `meshctl pool upgrade --check` for that pool and shows its output: the doctrine diff, each agent's changes, and the warnings (open sessions, uncommitted changes). Nothing runs until the person confirms. When the check finds nothing to change but the pool's record of its last upgrade is behind, the dialog says so and offers to record the upgrade, which clears the notice.
- **The same command.** On confirm, `POST /api/pool/upgrade?pool=` runs the console's own binary as `meshctl pool upgrade` in the pool root, so the button and the command never differ. It never passes `--force`: locally edited doctrine gets its `.upstream` copy and the result says it needs a person. Commits happen as the command makes them; nothing is pushed. One upgrade runs at a time per pool.
- **Then restart.** The result lists the pool's open sessions to restart, each with Bring to front.
- **Guarded like every POST.** An agent holding the token could start an upgrade, but any agent can already run `meshctl pool upgrade`, so the button grants nothing new.

This is the console's third change to a pool.

## Amendment (2026-10-28): the console can push the pool repo

When the pool repo is clean and ahead of the remote branch it tracks, doctor's finding ("pool directory pool/ is N commit(s) ahead…") carries a **Push** button. Before, the person opened a terminal to run the `git push` the finding named. ADR-0066's "meshctl commits, humans push" stands: the button is the person pushing, from the page.

- **Preview first.** The button lists the commits that would go and the branch they go to (`POST /api/pool/push/check?pool=`). Nothing runs until the person confirms.
- **Narrow.** On confirm, `POST /api/pool/push?pool=` pushes only the pool repo's current branch to the branch it tracks. It never forces: when the remote has commits the pool does not, the push is refused and the result says to pull first. Agent repos get no button.
- **Never waits for a password.** git runs with prompts off and no terminal, using the person's existing credentials (SSH key, keychain). Without usable credentials (for example a console started at login with no SSH agent) the result says so and shows the command to run in a terminal. One push runs at a time per pool.
- **Agents cannot reach it.** Agents never run git in `pool/` (ADR-0066), so unlike the repairs and the upgrade, this endpoint would grant something new to an agent holding the token. The gate denies a shell command that names the endpoint, under the same check as git in `pool/`. The gate matches text, so this closes the plain route (`curl … /api/pool/push`), not every route.
- **Guarded like every POST.** Host, token and Origin, as ADR-0077.

This is the console's fourth change to a pool, and the first that leaves the machine.

## Amendment (2026-10-28): the console can commit and push an agent repo

ADR-0039 keeps a peer's knowledge safe only once committed and pushed, but doctor named only a workspace with no repo or no remote. Doctor now also warns, per agent, when a workspace with a remote has uncommitted paths ("N uncommitted path(s)…") or, once clean, commits its remote branch lacks ("N commit(s) ahead of origin/main…"). Each finding carries a button. This replaces the previous amendment's "Agent repos get no button."

- **Commit** lists the paths a commit takes (`POST /api/agent/commit/check?pool=&agent=`) and asks for a message, pre-filled with a suggestion the person can edit (below). On confirm, `POST /api/agent/commit` stages everything and commits it as the person (their git identity). The request carries the status the person saw; when the tree changed since, nothing is committed and the person looks again. A commit that leaves the repo ahead offers Push in the same dialog.
- **Push** works like the pool's: the commits that would go first, then a push of the current branch to the branch it tracks, never forced, prompts off, with the same outcomes (`POST /api/agent/push/check`, `POST /api/agent/push`).
- **One at a time per pool**, across commits and pushes.
- **Agents cannot reach it.** An agent commits and pushes its own workspace with git; the buttons are the person's. The gate denies a shell command naming either endpoint, with the same text-match limit as the pool push.
- **Guarded like every POST.** Host, token and Origin, as ADR-0077.

- **The suggested message names what changed.** Recall's git spine, session records and the console's recent commits all read commit subjects back, so a fixed "Update <agent> workspace" is useless to them. The check answers `suggested`, built from the paths and their diffs alone (no model call). Changes fall into three kinds. *Wiring* is a `mesh-*` skill, a file carrying the meshctl-managed marker, or `.gitignore` edits inside its managed block; CLAUDE.md, AGENTS.md, settings and template READMEs count only alongside one of those. It is named by the headings the skill diff adds, else by the meshctl version. *Knowledge* is a `knowledge/` entry, named by area and slug ("Add candidates: <slug>, …"). *Other* is anything else, named by its top-level path. The kind with the most paths is the subject, cut near 72 characters. The other kinds, plus the full list when the subject was cut, go in an "Also:" body, the same shape as the pool commit.

This is the console's first change to an agent's workspace.
