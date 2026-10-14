---
status: accepted
date: 2026-07-24
---
# 0035. Mesh steward — an interview-and-build onboarding hub agent

- **Status:** accepted — **implemented** (`pool init` scaffolds a steward by default; `agent bootstrap`; `mesh-steward` skill + steward CLAUDE.md; both harnesses)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** builds on ADR-0031 (pool init), ADR-0032 (harness seam), ADR-0010 (`agent new`); the steward runs the `mesh-cognition` loop (ADR-0014–0018) like any agent.

## Context

Standing up a new mesh required the user to know the roster up front and hand-run
`meshctl agent new …` per agent. For migrating personal projects (and for anyone new to
the mesh), that is a blank-page problem: *which* agents, with *what* boundaries? The mesh
needed a front door — an agent that interviews the user for the mesh's purpose and builds
the relevant roster, then stays as the community's onboarding hub.

## Decision

Add a first-class **steward** agent — the mesh's onboarding concierge.

- **`meshctl pool init <path>` scaffolds a `steward` by default** (`--no-steward` to skip;
  `--harness` sets its harness). `meshctl agent bootstrap` adds/repairs the steward in an
  existing pool. Idempotent — an existing steward is left untouched.
- The steward is a normal agent workspace (manifest, intake, KB, harness wiring) with:
  - a **steward `CLAUDE.md`** role (front door; on a fresh pool, run the skill) plus the
    standard comms + Cognition stanzas;
  - the **`mesh-steward` skill** — the playbook: interview the user (purpose, kinds of
    work, systems/boundaries, operating style, harness) → design a small well-bounded
    roster (recommend, get approval) → scaffold each via `meshctl agent new …` and flesh
    its CLAUDE.md → record the design in the steward's KB → be the ongoing hub.
- Identity: `dispatchable: false` (human-interactive; it scaffolds local files), distinct
  STEWARD badge. It builds/coordinates the team; it does not do their domain work or
  commit/push their repos.

The interview + roster judgment live in the **skill (LLM-driven)**, not in Go — the CLI
only scaffolds the steward workspace and provides `agent new` as the build primitive.

## Consequences

- Positive: a fresh pool greets the user with a working front door — "run your harness on
  the steward, it interviews you and builds the mesh." Removes the blank-page problem;
  makes the mesh self-onboarding for new communities and personal projects.
- Positive: harness-agnostic (ADR-0032) — steward works under claude or opencode; skill
  rendered to the right location.
- Positive: the roster design is captured in the steward's KB (ADR/system-map), so later
  sessions and new members get oriented.
- Trade-off: `pool init` is now opinionated (creates an agent by default); `--no-steward`
  preserves the lean path.
- Trade-off: roster quality depends on the interview skill + the model; mitigated by the
  "recommend, user approves, start minimal" discipline baked into the skill.
- Follow-up: a library of role templates (architect/dev/qa/researcher…) the steward can
  offer as starting points; richer `agent new` flags if the interview surfaces needs the
  current flags can't express.
