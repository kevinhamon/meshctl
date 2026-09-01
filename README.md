# agent mesh — `meshctl`

**agent mesh** is a lightweight system for running a team of specialized AI coding
agents that share work and build knowledge over time. **`meshctl`** is its single
self-contained Go binary — the tool of record. No daemon, no database, no server:
state is plain files in git-tracked workspaces; the CLI just makes the operations one
approvable command.

- **Product:** *agent mesh* (the concept + its on-disk footprint — `.agentmesh/`, `agent-mesh/`).
- **Tool:** `meshctl` (the binary you run).
- **Home:** `https://github.com/kevinhamon/meshctl`
- **License:** Apache-2.0.

## Why

Multi-agent setups usually mean a server, a message bus, and a database you have to
operate. agent mesh takes the opposite bet: **the filesystem is the substrate and git
is the log.** Every agent is an ordinary directory with its own git history; agents
coordinate by writing files into each other's inboxes; the knowledge each agent
accumulates is a git-tracked folder you can read, diff, and review. `meshctl` is the
only moving part, and it is a static binary you can delete at any time without losing
state.

That makes the whole system **local-first, inspectable, and reversible.** Nothing
happens that you can't see as a file change, and every coordinating action is a single
command you approve.

## Concepts

| Term | What |
|------|------|
| **Pool** | An isolated mesh rooted at a directory, marked by `.agentmesh/pool.yaml`. All comms/roster/KB scope to the pool — cross-pool contact is structurally impossible. Create with `meshctl pool init`. |
| **Agent** | A sibling workspace in the pool: `agent.yaml` (identity/discovery), `intake/` (inbox), `knowledge/` (git-tracked KB), harness wiring, terminal identity. |
| **Steward** | The onboarding-hub agent scaffolded into a fresh pool. It interviews you for the mesh's purpose and builds the agent roster. Your mesh's front door. |
| **Harness** | The coding-agent runtime an agent runs under — `claude` (Claude Code) or `opencode`. `meshctl` renders hooks/skills into each harness's native form. |
| **Comms** | File-based intake requests between agents. `meshctl msg send` (async handoff) / `meshctl msg ask` (sync headless dispatch). `meshctl agent list` is the live roster. |
| **Cognition** | Every agent recalls prior knowledge, captures episodic session-records, distills durable KB, and writes a session handoff. See "Cognition" below. |

## Install

Requires Go 1.22+ (to build) or a release binary. Any of:

```sh
# 1. go install (module path is the repo — no manual download)
go install github.com/kevinhamon/meshctl/cmd/meshctl@latest

# 2. Download a prebuilt binary from a release (macOS/Linux/Windows)
#    https://github.com/kevinhamon/meshctl/releases  → meshctl_<os>_<arch>

# 3. Build from a clone
git clone https://github.com/kevinhamon/meshctl.git agent-mesh
cd agent-mesh && task install     # sync-doctrine + build → ~/.local/bin/meshctl
```

Ensure the install dir (`$(go env GOPATH)/bin` or `~/.local/bin`) is on `$PATH`, then
`meshctl --help`. Release binaries are cross-compiled (CGO-free) and attached by the
release CI workflow on every `v*` tag.

Update doctrine after editing `doctrine/agent-comms.md` / `doctrine/distiller.md`: `task sync-doctrine`
(or `go generate ./...`) then rebuild; refresh a pool's emitted doctrine with
`meshctl pool upgrade`.

## Quickstart — stand up a mesh

```sh
meshctl pool init ~/myproject-mesh --name myproject      # emits doctrine + steward
cd ~/myproject-mesh/steward
# run your harness here (Claude Code or opencode). The steward interviews you and
# scaffolds the agent roster with `meshctl agent new …`.
```

Prefer to build the roster by hand? `meshctl pool init … --no-steward`, then
`meshctl agent new <name> …` per agent.

## The steward — your mesh's front door

`pool init` scaffolds one special agent, the **steward**, into every fresh pool. It is
not a domain worker; it is the onboarding concierge that turns your intent into a
concrete roster. Run your harness in `<pool>/steward` and it picks up the bundled
`mesh-steward` skill and drives a three-step flow:

1. **Interview** — one question at a time (not a form): what the mesh is *for*, the
   recurring kinds of work, the external systems and which ones get *written* vs only
   read, whether agents are advisory or state-mutating, the harness, and any tool/house
   rules. It reflects back a one-paragraph summary and waits for your nod.
2. **Design the roster** — it proposes a **small, bounded** roster (start with 2–4
   agents; grow later), one agent per distinct responsibility with a real ownership
   boundary. For each: `name`, `title`/`role`, `owns`/`domains`, `accepts` types, and
   `mutates` — where the invariant `mutates` non-empty ⇒ **not dispatchable** decides
   advisory (headless-dispatchable) vs state-mutating (`send`-only, human-triggered). It
   recommends one shape and waits for explicit approval.
3. **Scaffold** — on approval it stands each agent up with `meshctl agent new …`, wiring
   the harness, inbox, knowledge base, and identity.

The steward is also the **ongoing entry point**: re-invoke it any time to add or
repurpose agents, or when you're unsure what the mesh should contain. Skip it entirely
with `pool init … --no-steward` and build the roster by hand (above). Add it back to an
existing pool with `meshctl agent bootstrap`.

## Command surface (noun → verb)

```
meshctl pool    init [path] | upgrade | list
meshctl agent   new <name> | onboard <name|path> | bootstrap | list
meshctl msg     ask <target> <type> "…" | send <target> <type> "…"
meshctl inbox   list | next [--claim] | claim <id> | release <id>
meshctl kb      begin | finish [--abort]
meshctl memory  recall "<q>" | sessions | rebuild
meshctl session start | end | list
meshctl handoff show | set          (set reads the body from stdin)
meshctl intuition recall "<q>"
meshctl hook    session-start | user-prompt | session-end   (wired per-agent; not run by hand)
meshctl doctor
meshctl ask … | send …               (top-level aliases for msg ask|send)
```

Key flags: `agent new/onboard --harness claude|opencode`, `--dispatchable`,
`--mutates code,tracker` (non-empty ⇒ not dispatchable), `--accepts type:desc,…`,
`--owns`, `--domains`, `--rgb`, `--badge`, `--role`, `--title`.

## Harness integration

`meshctl` carries its own integration and renders it per harness (one binary, no
python, no loose scripts, no machine-absolute paths):

| | Claude Code | opencode |
|---|---|---|
| rules | `.claude/settings.json` | `opencode.json` `instructions:["CLAUDE.md"]` |
| skills | `.claude/skills/<n>/SKILL.md` | `.opencode/skills/<n>/SKILL.md` (native, ≥1.18) |
| hooks | settings.json → `meshctl hook …` | `.opencode/plugin/meshctl.js` → `meshctl hook …` |
| capture | SessionEnd | `session.idle` event |
| inject | `hookSpecificOutput` JSON | `chat.message` → text part |

`meshctl agent new --harness …` picks it; `onboard` autodetects; it's recorded in
`agent.yaml`. Adding a harness = one adapter (`internal/mesh/harness.go`).

## Cognition — how agents learn

Every agent maintains a git-tracked `knowledge/` KB (decisions, risks, playbooks,
experience, reference) and learns over time:

- **Recall:** `meshctl memory recall "<q>"` — live, unified over KB topics + captured
  session-records + the git commit time-spine; returns compact pointers, never bodies.
- **Episodic capture:** at session end the harness hook writes an extractive
  session-record (files touched, commands, tools, refs, commits, handoff) — no LLM.
- **Intuition:** `meshctl intuition recall` / the per-prompt hook surface compact
  past-work pointers from the **owned engine** by default (session-records + git spine).
  An external episodic engine is optional (`MESH_INTUITION_ENGINE=…`).
- **Distill:** turn recurring intuition into durable KB via `meshctl kb begin` → write a
  candidate → consolidate → `meshctl kb finish`. See the `mesh-cognition` skill.
- **Handoff:** `meshctl handoff set` writes the resume point; auto-injected at session
  start. `meshctl doctor` validates manifests + KB + harness wiring.

Authoritative protocol: `doctrine/agent-comms.md` (emitted into each pool as
`.agentmesh/doctrine/agent-comms.md`). Design rationale lives in `docs/`.

## Bootstrap a mesh from scratch (agent runbook)

A from-zero procedure an agent (or human) can follow — no prior mesh required:

1. **Build the tool.** Clone this repo; `task install` (or `go build -o ~/.local/bin/meshctl ./cmd/meshctl`). Confirm `meshctl --help`.
2. **Create the pool.** `meshctl pool init <pool-dir> --name <name> [--harness opencode]`. This emits the doctrine, runtime dirs, the `.agentmesh/pool.yaml` marker, and a **steward** agent. All later `meshctl` commands run from inside the pool (cwd resolves the pool via the marker).
3. **Design the roster.** Either run the steward under a harness (it runs the `mesh-steward` skill: interview → propose a small bounded roster → scaffold), or do it directly: for each agent,
   ```sh
   meshctl agent new <name> --title "…" --role "…" \
     --owns a,b --domains x,y --accepts "guidance:…,review:…,fyi:…" \
     [--mutates code] [--dispatchable] --harness <claude|opencode> --rgb r,g,b --badge LABEL
   ```
   Rule: one agent per distinct responsibility with a real ownership boundary; advisory agents are `dispatchable`, anything that writes code/a tracker sets `--mutates` (⇒ not dispatchable, `send`-only, human-triggered).
4. **Flesh each agent's `CLAUDE.md`** with its operating doctrine (the scaffold adds the intake + Cognition stanzas; add the role-specific substance).
5. **Verify.** `meshctl doctor` (clean but for empty-KB warnings) and `meshctl agent list`.
6. **Onboard an existing directory** into the mesh instead of creating fresh: `meshctl agent onboard <path>` (add-only; infers title/role; autodetects harness).
7. **Operate.** Reach a peer with `meshctl msg send|ask`; process your inbox with `meshctl inbox next --claim`; recall with `meshctl memory recall`; record decisions via `meshctl kb begin/finish`; leave a handoff with `meshctl handoff set`.

## Repo layout

| Path | What |
|------|------|
| `cmd/meshctl/` | CLI entrypoint (cobra). |
| `internal/mesh/` | Core: manifests, directory, intake/dispatch, concurrency (sessions/claims/kb worktrees), cognition (memory/recall/episodic/handoff/intuition), harness adapters, pool init. |
| `internal/mesh/templates/` | Embedded (`go:embed`): agent scaffold, skills (`mesh-cognition`, `mesh-steward`), steward CLAUDE.md, doctrine copies. |
| `doctrine/` | Doctrine **source of truth** (`agent-comms.md`, `distiller.md`) — copied into the embed tree by `sync-doctrine`. Edit here, not the copies. |
| `docs/` | Human docs: `DESIGN.md` (why it's built this way), `build-spec.md` (what to build), `knowledge-and-memory.md` (the cognition design brief). |
| `Taskfile.yaml` | `task install` / `build` / `sync-doctrine`. |

## Conventions

- A pool is a plain container of workspaces — not itself a git repo. `meshctl` and each
  agent are independent repos cloned into it. Runtime artifacts (`.dispatch.*`,
  `.worktrees/`, `.sessions/`, `.memory/`) live outside any repo and are git-ignored.
- Prefer SSH git remotes.

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
