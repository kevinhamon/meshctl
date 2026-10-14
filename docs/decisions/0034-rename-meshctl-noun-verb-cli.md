---
status: accepted
date: 2026-07-24
---
# 0034. CLI renamed `meshctl`; noun-verb command groups; repo home on gitea

- **Status:** accepted — **implemented** (binary + module + command tree + pool-wide wiring migrated)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** renames the tool of record from ADR-0010 (`agentmesh`); restructures the command surface used across ADR-0010/0011/0026/0030/0031/0033; the `.agentmesh/` pool marker (ADR-0031) keeps its product-named form.

## Context

The CLI shipped as `agentmesh` with a flat verb surface (`init`, `new`, `onboard`, `directory`, `ask`, …). Two problems surfaced as the tool grew:

1. **`init` is overloaded.** Pools initialize, and agents initialize; a single flat `init` conflates lifecycles that will only multiply.
2. **Product vs tool naming.** "Agent mesh" is the *product*; the binary sharing that name blurs the two. The user wants the binary to be a distinct tool name and the product name reserved.

The user also wants the repository's permanent home on their own infrastructure (gitea) rather than GitHub.

## Decision

1. **Rename the binary `agentmesh` → `meshctl`.** "Agent mesh" remains the product name. Command dir `cmd/agentmesh` → `cmd/meshctl`. The module was initially bare `meshctl` — **superseded by ADR-0036** (now the self-hosted Gitea path `<gitea-host>/<owner>/meshctl` so `go install` works).
2. **Noun-verb command groups**, with top-level aliases for the highest-frequency verbs:
   ```
   meshctl pool    init [path] | upgrade | list
   meshctl agent   new | onboard | list           (list ← former `directory`)
   meshctl msg     ask | send
   meshctl inbox   list | next | claim | release
   meshctl kb | memory | session | handoff | intuition | hook   (unchanged groups)
   meshctl doctor
   meshctl ask | send                               (top-level aliases → msg ask|send)
   ```
   Aliases keep muscle memory and mean a single `Bash(meshctl:*)` preapproval still covers everything.
3. **Product-named artifacts keep their names:** the `.agentmesh/` pool marker dir (ADR-0031) and the `agent-mesh/` doctrine dir are the *product's* on-disk footprint, not the tool's — unchanged.
4. **Repo home → a self-hosted Gitea instance (`https://<gitea-host>/<owner>/meshctl`)** (SSH remote for pushes, per the SSH-remotes rule). GitHub retained as a secondary remote during transition.

## Consequences

- Positive: `init` ambiguity gone; lifecycles scale cleanly under `pool`/`agent`. Tool vs product naming is clean.
- Positive: `Bash(meshctl:*)` is the one preapproval; hooks wire to `meshctl hook …`.
- Migration (done this session): binary installed as `meshctl` (with a transitional `agentmesh`→`meshctl` symlink); module + imports + command tree updated; a pool-wide sweep rewrote `agentmesh`→`meshctl` and moved-subcommand references (`directory`→`agent list`, `new`/`onboard`→`agent …`, `init`→`pool init`) across code, embedded templates, doctrine, docs, and every agent's `settings.json` + `CLAUDE.md` + skills. KB history (ADR bodies) left as written — historical record; this ADR is the forward pointer.
- Trade-off (RESOLVED by ADR-0036): the bare module path couldn't be `go install`ed remotely. Reversed — module is now the gitea path.
- Trade-off: the `agentmesh` symlink is transitional; remove once no wiring references it.
- Done: gitea `<owner>/meshctl` seeded; `origin` now points at gitea (SSH), GitHub kept as the `github` remote (mirror/backup). Local pool dir stays `~/agents/agent-mesh` (product doctrine dir, ADR-0031).
