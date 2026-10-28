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
