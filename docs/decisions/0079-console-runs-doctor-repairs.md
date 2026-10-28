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
