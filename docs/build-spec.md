# Build spec — `meshctl` Go CLI

> Point a fresh Claude Code instance at this file to build the mesh CLI.
> This spec is the authoritative *what to build* — the CLI's intended behavior.

## Goal

One Go binary, `meshctl`, on `$PATH`, that is the tool of record for the
agent mesh. It:

1. Scaffolds the mesh and its agents (new + **onboard existing**).
2. Makes cross-agent messaging **one preapprovable command** (`Bash(meshctl:*)`).
3. Computes the agent **directory on demand** from per-agent manifests — no
   hand-maintained, drift-prone roster file.

meshctl owns all agent scaffolding and cross-agent dispatch (with anti-cascade
guards) directly — no external scaffolder or dispatch script. The repo ships no
estate/org-sync scripts; any repo-fleet automation you need lives outside `meshctl`.

## Why not MCP / RPC / a database

The mesh's value model is **ephemeral**: spin an agent up in a clean context,
analyze, answer, spin down. MCP is a persistent interactive in-session server —
wrong shape. A DB/daemon is infrastructure with no state to justify it at this
scale. Messaging stays **file-based** (durable, greppable, git-tracked audit
log); the CLI just makes it one approvable action.

## Tech / conventions

- Go. Module `github.com/kevinhamon/meshctl` (installable: `go install …/cmd/meshctl@latest`).
- CLI framework: `cobra` (or stdlib `flag` if you prefer zero deps — cobra preferred for subcommands/help).
- YAML: `gopkg.in/yaml.v3`.
- Templates embedded via `go:embed` from `templates/` (migrate the existing
  `templates/` content into embeddable form).
- **The agents container is derived at runtime**, not hardcoded: it is the parent
  of the directory holding the `meshctl` binary's source repo *when run from the
  mesh*, but for a `$PATH` install resolve it as: `$AGENTS_DIR` env if set, else
  the parent of the mesh repo, else `~/agents`. Derive `AGENTS_DIR` from the
  binary's location rather than hardcoding a path.
- No AI attribution in any output, commits, or generated files (global rule).
- Commits: conventional style (`<type>: <desc>`); no AI attribution.

## `agent.yaml` schema (source of truth for identity + discovery)

Lives at `~/agents/<name>/agent.yaml`. Canonical shape:

```yaml
name: architect                       # dir name; unique mesh id
title: Software Architect
role: >                               # one paragraph
  Architecture of record for the product repos; owns decision records, KB, risk register.
inbox: intake/                        # relative to agent dir
owns: [architecture, decisions, kb, risk-register]
domains: [frontend, data-pipeline, backend]   # capability tags
accepts:
  - { type: guidance,         desc: architectural guidance on an approach }
  - { type: review,           desc: review a design/diff for architectural fit }
  - { type: decision,         desc: make/confirm a consequential call }
  - { type: story-enrichment, desc: add technical approach/AC to a tracked story }
  - { type: fyi,              desc: awareness only }
dispatchable: true                    # may `meshctl ask` run it headlessly?
mutates: []                           # external systems written: [tracker] | [code,prod]
badge: { label: ARCHITECT, r: 0.0, g: 0.3, b: 0.8 }
model: default                        # or a pin, e.g. claude-haiku-4-5
repo: git@github.com:your-org/architect.git
```

**Invariant the CLI enforces:** `len(mutates) > 0` ⇒ `dispatchable: false`.
Advisory agents (architect, domain experts) are dispatchable; state-mutating
agents (pm→tracker, developer→code) are messaged but human-triggered.

Known agents to seed manifests for (from `agent-comms.md`): architect (blue,
advisory), pm (green, mutates:[tracker]), developer (amber, mutates:[code]),
qa (mutates:[] — verdicts only, but may post tracker comments → decide with
user whether that counts as mutation), plus domain experts (e.g. a database expert, an API expert).

## Commands

### `meshctl pool init [path]` — binary-native pools
- `meshctl pool init <path>` creates a **new isolated pool** at `<path>` **from the
  binary** — emits the doctrine (`.agentmesh/doctrine/{agent-comms.md,distiller.md}`)
  from `go:embed`, creates runtime dirs (all under `.agentmesh/`), writes the
  `.agentmesh/pool.yaml` marker, and scaffolds a `steward-agent`. **No git
  clone, no network.** `--name` sets pool identity; `--no-steward` skips the steward.
- `meshctl pool init` (no path) targets the **current directory**: repairs it
  if already a pool, else creates one there — never falls back to `~/agents`.
- `meshctl pool upgrade` re-emits the embedded doctrine; `pool migrate` reconciles a
  legacy (pre-`.agentmesh/`) layout to `.agentmesh/`.

Pool detection (`AgentsDir`) resolves the root by the `.agentmesh/` marker dir
(preferred), else the legacy `agent-mesh/agent-comms.md` (back-compat), else
`$AGENTS_DIR`, else `~/agents`. Doctrine source of truth is
`internal/mesh/templates/doctrine/{agent-comms.md,distiller.md}` — embedded source,
edited in place (go:embed cannot reach outside the package, so it lives there).

### `meshctl agent new <name> [--badge LABEL --rgb r,g,b --title ... --model ...]`
Scaffold a **new** agent workspace as a sibling of the mesh, including its
`agent.yaml` manifest. Creates:
`<name>/{agent.yaml, intake/README.md, CLAUDE.md, Taskfile.yaml}` (add `--iterm2` for
an optional macOS iTerm2 profile under `.iterm2/`).
Preconditions: dest must not exist. Emit next-steps (write CLAUDE.md body, create git
remote, run `meshctl agent identity` for a portable terminal badge).

### `meshctl agent onboard <name|path>`  ← NEW capability
Upgrade a **pre-existing** agent directory into a full mesh member. Idempotent
and non-destructive — only adds what's missing:
1. If no `agent.yaml`: create one, **inferring** `title`/`role`/`owns` from the
   agent's `CLAUDE.md` where possible; prompt (or accept flags) for `domains`,
   `accepts`, `mutates`, `badge`. Never overwrite an existing manifest — merge/skip.
2. If no `intake/`: create it with `intake/README.md` from template.
3. If `CLAUDE.md` lacks the "Agent Communication (intake)" section: inject it
   (pointing at `agent-comms.md`).
4. Print a diff/summary of what it added; change nothing already correct.

### `meshctl agent list [--md | --json]`  ← replaces the committed roster
Scan `$AGENTS_DIR/*/agent.yaml`, validate, emit the roster **on demand**.
`--md` = the table format currently in `agent-comms.md`; `--json` = machine form
for routing. This is the discovery source of truth — no persistent roster file.
Optional `--write <file>` may emit a snapshot for GitHub browsing, but it must be
marked "generated — do not edit" and is never authoritative.

### `meshctl ask <target> <type> "<prompt>" [--detach]`  ← the preapprovable path
Collapse the two prompted actions into one:
1. Validate `target` exists and its manifest `accepts` `type`; **refuse if
   `dispatchable: false`** (state-mutating agent — use `send` + human trigger).
2. Write a validated intake file into `<target>/intake/` per the request schema
   (frontmatter `id,from,to,type,created,status,priority,related,response`).
   `from` = `$AGENT_NAME` or cwd basename. This is the audit trail.
3. Dispatch the peer **headlessly**: `claude -p`
   in the target dir, `--permission-mode acceptEdits`, `--allowedTools Read Grep
   Glob Edit Write`, the same headless system prompt, model from manifest/env.
4. **Sync (default):** block until it returns, then print the answer written back
   into the request file. **`--detach`:** write + dispatch async (or write-only),
   return the request id to poll later.

**These anti-cascade guards are budget/safety-critical, not optional:**
- Depth guard: refuse when `AGENT_DISPATCH_DEPTH>=1` (a dispatched agent may not
  dispatch). Set `AGENT_DISPATCH_DEPTH+1` and `AGENT_NAME=<target>` for the child.
- Single-flight lock: atomic `mkdir` at `$AGENTS_DIR/.dispatch.lock`; stale
  reclaim after `AGENT_DISPATCH_STALE_SEC` (900); never delete a lock you didn't create.
- Hourly cap: rolling-hour count in `$AGENTS_DIR/.dispatch.count`, default 12
  (`AGENT_DISPATCH_HOURLY_MAX`).
- Per-dispatch timeout (`AGENT_DISPATCH_TIMEOUT_SEC`, 420) via `timeout`/`gtimeout` or Go context.
- Log every dispatch/block to `$AGENT_DISPATCH_LOG`/`.dispatch.log`.
- Preserve exit codes: 2 unknown target · 3 depth · 4 hourly cap · 6 lock held.
- The headless system prompt must keep the "you may not dispatch; on a new
  consequential decision set status `needs-human` and stop" contract verbatim.

### `meshctl send <target> <type> "<prompt>"`
Write-only intake (no dispatch). For state-mutating peers (pm, developer)
that stay human-triggered. Same validation as `ask` step 2.

### `meshctl doctor`
Validate all manifests: missing/invalid `agent.yaml`, `mutates`/`dispatchable`
conflict, badge collisions, CLAUDE.md missing the intake section, orphaned
inboxes. Exit non-zero on any error. Use in CI / pre-onboard check.

## Claude Code integration (hooks) — self-contained

The mesh carries its own Claude Code hooks **inside the binary** — no `python3`,
no loose scripts, no machine-absolute paths in a global settings file. Three
handlers, each reading the hook JSON on stdin and printing `hookSpecificOutput`
on stdout; all **fail-open** (any error ⇒ no output, exit 0 — a hook must never
break a turn):

- `meshctl hook session-start` — registers session presence and
  injects the agent's handoff.
- `meshctl hook user-prompt` — injects compact intuition pointers
  and a one-shot distillation boundary nudge.
- `meshctl hook session-end` — deregisters session presence.

The agent is resolved from the payload `cwd` basename. Wiring lives **per-agent**
in `<agent>/.claude/settings.json` (committed, so it travels with the repo and is
correct per pool), written by `new`/`onboard` via `EnsureAgentSettings`
(add-only, idempotent, never clobbers an existing key). The command is
`meshctl hook …` resolved on `$PATH` — no absolute path. These replace the
former `hooks/{handoff-inject,intuition-inject,distill-nudge}.py`.

## Migration / sequencing

1. Build the CLI with `new`, `onboard`, `directory`, `doctor`, `send` first
   (no behavior change to dispatch).
2. `meshctl agent onboard` every existing agent → seed `agent.yaml` files. Run
   `meshctl doctor` until clean.
3. Convert `agent-comms.md`: keep the protocol **rules** prose; replace the
   **registry table** with a note that the roster is `meshctl agent list`.
   Trim peer lists out of each agent's `CLAUDE.md` (keep the pointer + own manifest).
4. Implement `meshctl ask` with all anti-cascade guards. **DONE**: `meshctl ask`
   implements depth / hourly-cap / single-flight lock and adds the intake-write
   step — one path for dispatch.
5. Add a `Bash(meshctl:*)` allow-rule to each agent's settings so messaging is
   preapproved. **DONE:** `meshctl agent new`/`onboard` now scaffold
   `<agent>/.claude/settings.json` with this allow-rule **and** the three hooks
   (see "Claude Code integration" below). Add-only + idempotent.

## Acceptance criteria

- `go build ./...` clean; `meshctl --help` lists all subcommands.
- `meshctl agent new demo …` produces a workspace that `meshctl doctor` passes.
- `meshctl agent onboard <existing>` is idempotent: second run is a no-op, never
  clobbers an existing `agent.yaml`.
- `meshctl agent list --json` reflects reality with zero committed roster file;
  adding an agent needs **no** edits to any peer.
- `meshctl ask architect guidance "…"` writes a valid intake file, dispatches
  headlessly, returns the written-back answer, and **all four anti-cascade guards
  still fire** (prove depth, lock, cap with tests).
- `meshctl ask pm …` is **refused** (`dispatchable:false`).
- Runs on macOS stock bash-free (pure Go) and Linux; no bash 3.2 landmines.

## Terminal identity (portable)

The human needs to *see* which agent a terminal is driving — the interlock that stops
asking the wrong agent and burning usage. `meshctl agent identity` prints ANSI escapes
that set the terminal title to the agent name and render a colored badge from the
manifest (`badge.label` + `badge.{r,g,b}`). It uses only OSC title-setting and SGR
truecolor, so it works in any terminal on any OS — run it on shell entry in an agent
workspace (shell rc or a direnv `.envrc`).

`meshctl agent new --iterm2` additionally writes a macOS iTerm2 dynamic profile under
`.iterm2/` (badge + per-agent color) for users who want the native iTerm treatment;
it's opt-in and platform-specific, never required.

## Concurrency & claims

Multiple sessions run in the same agent workspace concurrently. Two races must be
closed; the existing `.dispatch.lock` covers neither (it serializes only headless
dispatch — interactive sessions bypass it).

**Two layers, two mechanisms — do not conflate:**
- **Coordination/ephemeral** (claims, presence, heartbeats, dispatch lock) →
  shared filesystem, **atomic locks, never git-branched.**
- **Durable KB** (decision records, risks, docs) → **git worktree + branch + merge-back.**

### Substrate — session identity + presence
- Session id from `$CLAUDE_SESSION_ID`, else `$CLAUDE_CODE_SESSION_ID` (exported by
  Claude Code to tool subprocesses; matches the hook `session_id`), else minted.
  Presence is registered by a **session-start hook** installed per agent (during
  `onboard`).
- Registry: `$AGENTS_DIR/<agent>/.sessions/<session-id>` = `{pid, started,
  last_beat, current_claim, kb_branch?}`; heartbeat updates `last_beat`.
- `meshctl session start` registers presence and **loudly prints any other live
  session in this workspace + outstanding claims**. `session end` deregisters.
  `session list` shows live sessions + claims.

### Race A — atomic intake claims
- Claim = atomic `O_EXCL`/`mkdir` create of `<request>.claim` holding
  `{owner_session, pid, heartbeat}`. First writer wins; loser skips.
- Lease + heartbeat; stale reclaim after `AGENT_DISPATCH_STALE_SEC` (900), never
  reclaiming a claim whose session still heartbeats.
- `meshctl inbox next [--claim]` atomically claims + returns the next `pending`.
  `meshctl claim|release <id>`.
- The request `status` field stays a human display; **the sidecar is the
  authority** — never use `status` for mutual exclusion (TOCTOU).
- `meshctl ask` (dispatch) claims the target request for its dispatched session.

### Race B — KB branch-merge on write-intent
- **Reads: direct, no ceremony.** Only writes take the worktree path.
- `meshctl kb begin` → create worktree `$AGENTS_DIR/.worktrees/<agent>-<session>`
  on branch `session/<session-id>`, point the session there.
- edit + commit in the worktree.
- `meshctl kb finish` → rebase onto `main`, merge, remove worktree.
  `--abort` discards the branch + worktree.
- **Conflicts resolved before proceeding.** Interactive session resolves.
  **Headless dispatched run does NOT auto-resolve** — leave the branch, set the
  request `status: needs-human`, stop (matches the anti-cascade rule).
- Known hotspot: concurrent new decision records both grab the next `NNNN` + append an index
  row → add/add conflict at merge; resolver renumbers the second. Rare, fine.

### Acceptance additions
- Two concurrent `meshctl inbox next` calls never return the same request id.
- A claim held by a live (heartbeating) session is never reclaimed; a claim from a
  dead session is reclaimed after the stale window.
- `session start` in a workspace with another live session prints the overlap.
- Two `kb begin`/edit/`kb finish` cycles on **different** files both land on `main`
  with no loss; on the **same** lines, `kb finish` surfaces a conflict (interactive)
  or leaves the branch + `needs-human` (headless).

## Non-goals

- No database, no daemon, no MCP/RPC transport. Files remain the store.
- No cross-agent parallel fan-out (mesh is deliberately single-flight).
- No repo-fleet/org-sync automation — that lives outside `meshctl`.
