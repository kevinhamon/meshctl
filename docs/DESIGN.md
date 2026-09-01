# Design notes

Why `meshctl` is shaped the way it is. The [`README`](../README.md) covers what it
does and how to use it; [`build-spec.md`](./build-spec.md) is the build spec.

## The bet: the filesystem is the substrate

Most multi-agent frameworks reach for a server, a message bus, and a database. That is
infrastructure you have to run, secure, and reason about — and it hides coordination
behind a network boundary you can't `grep`. `meshctl` takes the opposite bet:

- **State is plain files in git-tracked workspaces.** Every agent is an ordinary
  directory with its own git history. Its inbox is a folder; its knowledge base is a
  folder; its identity is a YAML file.
- **git is the log.** Coordination is durable and auditable because it is committed.
  "Who asked whom, when" is `dispatch.log`; what an agent learned is a diff.
- **The CLI is the only moving part**, and it is a static binary. Delete it and you lose
  no state. There is no daemon to keep alive and nothing to migrate.

The result is **local-first, inspectable, and reversible**: nothing happens that you
can't see as a file change, and every coordinating action is a single command you
approve.

## Core design decisions

- **No MCP / RPC / database.** The value model is ephemeral — spin an agent up in a
  clean context, answer, spin down. A persistent in-session server is the wrong shape; a
  DB/daemon is infrastructure with no state to justify it at this scale.
- **The roster is computed, not maintained.** The agent directory is derived on demand
  from per-agent `agent.yaml` manifests, so there is no hand-edited roster file to drift.
- **Messaging is one preapprovable command.** `meshctl send`/`ask` write a well-formed
  request file so a harness can allow `Bash(meshctl:*)` once instead of approving ad-hoc
  file writes into peers' inboxes.
- **Dispatch is single-flight and guarded.** Headless dispatch (`meshctl ask`) is
  protected by four layered anti-cascade guards (prompt, depth, a filesystem lock, an
  hourly cap) so a runaway or expensive chain cannot happen even if a model ignores the
  prompt. See [`../doctrine/agent-comms.md`](../doctrine/agent-comms.md).
- **Cognition rides the same substrate.** Recall, episodic capture, distillation, and
  handoff are all files under each agent's `knowledge/` — no new store. Memory is a
  disposable, regenerable index; the git-tracked KB is authoritative.
- **Harness-neutral core, thin adapters.** The mesh's behavior is harness-independent;
  each supported runtime (Claude Code, opencode) binds it through one small adapter that
  renders hooks/skills into that runtime's native form.

## A note on `ADR-NNNN` references

Some code comments and docs cite `ADR-NNNN` — Architecture Decision Records, the standard
[Nygard](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions)
convention for capturing the *why* behind a decision. Those specific records are part of
the author's private design history and are **not shipped in this repo**; the citations
are pointers, not links. `meshctl doctor` also recognizes `ADR-NNNN` tokens when checking
consistency of an *end user's own* `knowledge/decisions/` KB — that is a supported feature
for anyone who keeps ADRs, not a dependency on any external record.
