---
status: accepted
date: 2026-07-24
---
# 0039. Agent repo hygiene: `agent new` inits a repo; persistent nag until a remote exists

- **Status:** accepted — **implemented** (git init on scaffold; session-start nag; doctor check; doctrine amended)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** closes the durable-knowledge-safety gap discussed under ADR-0037/0038 — an agent's `knowledge/` is only preserved if its repo is pushed to a remote. Builds on ADR-0014 (KB), ADR-0017 (handoff), ADR-0030/0033 (session-start hook).

## Context

An agent's durable knowledge (KB + handoff) lives in `knowledge/`, inside the agent's
workspace. The pool container is not a repo, so if the agent workspace isn't itself a git
repo **with a remote that's been pushed**, that knowledge exists only on one machine and
is lost if the machine dies. Nothing enforced or reminded this — a freshly scaffolded (or
onboarded) agent could accumulate valuable KB that was never version-controlled or backed
up.

## Decision

Make repo hygiene a first-class, self-reinforcing part of the mesh:

1. **`meshctl agent new` inits a local git repo** in the new workspace (`git init -b main`
   + a best-effort initial commit; tolerates a missing git identity — init still
   succeeds). Knowledge is version-controlled from creation.
2. **Persistent session-start nag until a remote exists.** The SessionStart hook injects a
   reminder when the workspace is not a repo, or is a repo with no configured remote. It
   recomputes each session and **clears automatically once a remote is pushed** — no state
   to manage.
3. **`meshctl doctor` warns** on the same conditions (no repo / no remote), so it's visible
   in a health check, not only at session start.
4. **Doctrine amended** — the always-loaded CLAUDE.md Cognition stanza, the `mesh-cognition`
   skill, and `agent-comms.md` §Knowledge now state the rule: your workspace must be its
   own git repo with a remote; `knowledge/` is durable only once committed **and** pushed;
   `git init` + commit if it isn't a repo; create a remote and push.

Creating the *remote* stays a human action (it needs credentials + a naming choice), so
the design **nags** rather than automates it — persistently, until done.

## Consequences

- Positive: every new agent is version-controlled immediately; the loudest failure mode
  (unpushed knowledge lost on machine death) is continuously surfaced until fixed.
- Positive: the nag is self-clearing and needs no tracking flag — presence of a remote is
  the signal.
- Trade-off: `agent new` now shells `git` (best-effort; a git failure never fails
  scaffolding). Onboarded agents that predate this get the nag + doctor warning until they
  `git init` + add a remote.
- Note: this is orthogonal to the parked pg+pgvector recall backend (ADR-0033 stage 4);
  git remotes preserve the *durable* KB, which stays in git regardless.
