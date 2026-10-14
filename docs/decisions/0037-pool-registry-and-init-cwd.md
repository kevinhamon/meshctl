---
status: accepted
date: 2026-07-24
---
# 0037. Machine-level pool registry (discovery-only) + `pool init` targets cwd

- **Status:** accepted — **implemented** (registry at `~/.config/meshctl/pools.json`; `pool list/register/forget`; bare `pool init` = cwd)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** builds on ADR-0031 (pool marker), ADR-0035 (steward); reinforces the ADR-0029 isolation guarantee (the registry must not weaken it).

## Context

Two problems surfaced in use:

1. **No way to enumerate pools.** `meshctl` scopes everything to the cwd pool, so there was no answer to "what pools do I have on this machine, and where?" — you had to remember paths.
2. **`pool init` with no path was a footgun.** No-path meant "repair the current pool," and `AgentsDir()` fell back to its `~/agents` default — so running bare `pool init` from an unrelated project dir silently resolved to `~/agents`, ignored `--name`/`--harness`, and printed the wrong pool. (Harmless — a no-op — but wrong and confusing.)

## Decision

1. **A machine-level pool registry** — meshctl's single piece of global state — at
   `$XDG_CONFIG_HOME/meshctl/pools.json` (else `~/.config/meshctl/pools.json`). Entries:
   `{name, path, created}`.
   - `pool init <path>` auto-registers the new pool.
   - `pool list` enumerates registered pools, verifying each still has the `.agentmesh/`
     marker and flagging stale ones; `pool list --current` shows just the cwd pool.
   - `pool register [path]` adds an existing pool (default cwd); `pool forget <name|path>`
     removes an entry (never touches the pool on disk).
   - **Discovery-only, non-authoritative.** `AgentsDir()` still scopes every roster/comms/KB
     op to the cwd pool — the registry never enables cross-pool messaging or a merged
     roster, preserving the ADR-0029 "pools can't see each other" guarantee. Losing the
     registry loses no pool data (re-register). Corrupt/missing registry reads as empty.
   - **One platform-specific point** (`registryPath`): *nix uses XDG now; Windows is
     deferred and changes only that function.
2. **`pool init [path]` targets the current directory when no path is given** — bare
   `pool init` means "make a pool here." If the target is already a pool it repairs
   (idempotent, no steward re-scaffold); otherwise it creates one. It **never** falls back
   to `~/agents`.

## Consequences

- Positive: `meshctl pool list` from anywhere; `pool init` in a project dir does the
  obvious thing; stale entries are visible + prunable.
- Positive: isolation is intact — the registry is a convenience index, not a channel.
- Migration: existing pools predating the registry are added with `pool register` (the
  original `~/agents` pool was registered under its own name).
- Trade-off: introduces global state. Bounded by making it non-authoritative +
  reconstructable, and by the hard rule that it is discovery-only.
- Windows: registry path + (later) any home-dir semantics deferred; the seam is one
  function so no platform branching leaks into the rest of the CLI.
