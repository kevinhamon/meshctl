# Build spec — `meshctl` Go CLI

> Point a fresh Claude Code instance at this file to build the mesh CLI.
> Authoritative design rationale: architecture decision record ADR-0010.
> This spec is the *what to build*; the ADR is the *why*.

## Goal

One Go binary, `meshctl`, on `$PATH`, that is the tool of record for the
agent mesh. It:

1. Scaffolds the mesh and its agents (new + **onboard existing**).
2. Makes cross-agent messaging **one preapprovable command** (`Bash(meshctl:*)`).
3. Computes the agent **directory on demand** from per-agent manifests — no
   hand-maintained, drift-prone roster file.

It **replaces** the Taskfile `task new` scaffolder and **absorbs** `bin/ask-agent`
(dispatch + anti-cascade guards). The repo ships no estate/org-sync scripts.
Any repo-fleet automation you need lives outside `meshctl`.

## Why not MCP / RPC / a database

The mesh's value model is **ephemeral**: spin an agent up in a clean context,
analyze, answer, spin down. MCP is a persistent interactive in-session server —
wrong shape. A DB/daemon is infrastructure with no state to justify it at this
scale. Messaging stays **file-based** (durable, greppable, git-tracked audit
log); the CLI just makes it one approvable action. See ADR-0010.

## Tech / conventions

- Go (house language, ADR-0003). Module `github.com/kevinhamon/meshctl` (installable: `go install …/cmd/meshctl@latest`).
- CLI framework: `cobra` (or stdlib `flag` if you prefer zero deps — cobra preferred for subcommands/help).
- YAML: `gopkg.in/yaml.v3`.
- Templates embedded via `go:embed` from `templates/` (migrate the existing
  `templates/` content into embeddable form).
- **The agents container is derived at runtime**, not hardcoded: it is the parent
  of the directory holding the `meshctl` binary's source repo *when run from the
  mesh*, but for a `$PATH` install resolve it as: `$AGENTS_DIR` env if set, else
  the parent of the mesh repo, else `~/agents`. Match the relocatable pattern in
  `bin/ask-agent` (derives `AGENTS_DIR` from the script location).
- No AI attribution in any output, commits, or generated files (global rule).
- Commits: conventional style (`<type>: <desc>`); no AI attribution.

## `agent.yaml` schema (source of truth for identity + discovery)

Lives at `~/agents/<name>/agent.yaml`. Full schema in ADR-0010; canonical shape:

```yaml
name: architect                       # dir name; unique mesh id
title: Software Architect
role: >                               # one paragraph
  Architecture of record for the product repos; owns decision records, KB, risk register.
inbox: intake/                        # relative to agent dir
owns: [architecture, adrs, kb, risk-register]
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

### `meshctl pool init [path]` — binary-native pools (ADR-0031)
- `meshctl pool init <path>` creates a **new isolated pool** at `<path>` **from the
  binary** — emits the doctrine (`.agentmesh/doctrine/{agent-comms.md,distiller.md}`)
  from `go:embed`, creates runtime dirs (all under `.agentmesh/`, ADR-0038), writes the
  `.agentmesh/pool.yaml` marker, and scaffolds a `steward-agent` (ADR-0035). **No git
  clone, no network.** `--name` sets pool identity; `--no-steward` skips the steward.
- `meshctl pool init` (no path) targets the **current directory** (ADR-0037): repairs it
  if already a pool, else creates one there — never falls back to `~/agents`.
- `meshctl pool upgrade` re-emits the embedded doctrine; `pool migrate` reconciles a
  legacy (pre-ADR-0038) layout to `.agentmesh/`.

Pool detection (`AgentsDir`) resolves the root by the `.agentmesh/` marker dir
(preferred), else the legacy `agent-mesh/agent-comms.md` (back-compat), else
`$AGENTS_DIR`, else `~/agents`. Doctrine source of truth is the repo-root
`agent-comms.md`/`distiller.md`, copied into `templates/doctrine/` for embedding —
keep them in sync (`go generate ./...` / the Taskfile `sync-doctrine` target).

### `meshctl agent new <name> [--badge LABEL --rgb r,g,b --title ... --model ...]`
Scaffold a **new** agent workspace as a sibling of the mesh. Reproduces what
`task new` did, **plus** writes `agent.yaml`. Creates:
`<name>/{agent.yaml, intake/README.md, CLAUDE.md, .iterm2/profile.json.tmpl, .iterm2.values.yaml, Taskfile.yaml}`.
Preconditions: dest must not exist. Emit next-steps (write CLAUDE.md body, add
iTerm icons, create git remote).

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
3. Dispatch the peer **headlessly** (the `bin/ask-agent` mechanism): `claude -p`
   in the target dir, `--permission-mode acceptEdits`, `--allowedTools Read Grep
   Glob Edit Write`, the same headless system prompt, model from manifest/env.
4. **Sync (default):** block until it returns, then print the answer written back
   into the request file. **`--detach`:** write + dispatch async (or write-only),
   return the request id to poll later.

**Port these anti-cascade guards faithfully from `bin/ask-agent` — they are
budget/safety-critical, not optional:**
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

## Claude Code integration (hooks) — self-contained (ADR-0030)

The mesh carries its own Claude Code hooks **inside the binary** — no `python3`,
no loose scripts, no machine-absolute paths in a global settings file. Three
handlers, each reading the hook JSON on stdin and printing `hookSpecificOutput`
on stdout; all **fail-open** (any error ⇒ no output, exit 0 — a hook must never
break a turn):

- `meshctl hook session-start` — registers session presence (ADR-0011) and
  injects the agent's handoff (ADR-0017).
- `meshctl hook user-prompt` — injects compact intuition pointers (ADR-0016)
  and a one-shot distillation boundary nudge (ADR-0027).
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
4. Implement `meshctl ask` at **parity** with `bin/ask-agent` (all guards). **DONE**:
   `meshctl ask` reached parity (depth / hourly-cap / single-flight lock) and adds the
   intake-write step; `bin/ask-agent` is **retired** to a deprecation shim. One path now.
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

## Agent background generation (iTerm2 dynamic profiles)

Each agent gets an AI-generated iTerm2 background so the human can *see* which
agent they're talking to. This is not decoration — it's the interlock that stops
asking the wrong agent and burning usage. Treat it as a first-class flag, not an
afterthought. The image is written to `<agent>/.iterm2/bg.png`; the existing
`profile.json.tmpl` already references that path, so the dynamic profile picks it
up on `cd`.

Generation is **infrequent** (once per `new`/`onboard`), so latency/quota are
irrelevant — optimize for zero-cost and repeatability, not throughput.

**Pluggable backend** — one interface, selectable per mesh/agent:

```go
type ImageGen interface { Generate(prompt string, w, h, seed int) ([]byte, error) }
```

| Backend | Cost | Key | Notes |
|---|---|---|---|
| `mflux` (recommended if deployed) | free, offline | none | Shell out to `mflux-generate` (MLX/FLUX on Apple Silicon). Full local stack; `--seed` ⇒ deterministic. ~5-6s/512px on M3 Max. First run downloads weights. |
| `pollinations` (default, zero-setup) | free | none | `GET https://image.pollinations.ai/prompt/<url-enc prompt>?width=W&height=H&seed=S&nologo=true`. Anon ~1 req/15s + watermark; free account token removes watermark. No SLA — fine for one-shot. |
| `openai` (optional, house style) | ~$0.005–0.02/img | API key | `gpt-image-1.5` or `gpt-image-1-mini`. **Do NOT use `gpt-image-1` (deprecated 2026-10-23).** Uses a platform API key, NOT a ChatGPT subscription. |

Flags on `new`/`onboard`: `--gen-bg` (opt in), `--bg-backend mflux|pollinations|openai`,
`--bg-prompt "<override>"`. Backend default + any key/token come from mesh config
(env or a mesh-level config file), never committed.

**Prompt construction** — build from the manifest for a coherent set:
`"<role/domains keywords>, <badge label>"` + a **fixed style suffix** shared by all
agents (e.g. `", isometric, muted palette, dark background, subtle"`) so every
agent's art reads as one family. `seed = hash(name)` ⇒ stable re-generation.

`mflux` example (exec):
```
mflux-generate --model schnell --prompt "<built prompt>" --width 1600 --height 1000 --seed <h> --steps 4 --output <agent>/.iterm2/bg.png
```

## Concurrency & claims (ADR-0011)

Multiple sessions run in the same agent workspace concurrently. Two races must be
closed; the existing `.dispatch.lock` covers neither (it serializes only headless
dispatch — interactive sessions bypass it).

**Two layers, two mechanisms — do not conflate:**
- **Coordination/ephemeral** (claims, presence, heartbeats, dispatch lock) →
  shared filesystem, **atomic locks, never git-branched.**
- **Durable KB** (ADRs, risks, docs) → **git worktree + branch + merge-back.**

### Substrate — session identity + presence
- Session id from `$CLAUDE_SESSION_ID` (or minted), set by a **session-start hook**
  installed per agent (during `onboard`).
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
  request `status: needs-human`, stop (matches ADR-0010 anti-cascade rule).
- Known hotspot: concurrent new ADRs both grab the next `NNNN` + append an index
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
