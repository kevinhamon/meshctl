# agent mesh — `meshctl`

**agent mesh** is a lightweight system for running a team of specialized AI coding
agents that share work and build knowledge over time. **`meshctl`** is its single
self-contained Go binary — the tool of record. No daemon, no database, no server:
state is plain files in git-tracked workspaces; the CLI just makes the operations one
approvable command.

- **Product:** *agent mesh* (the concept + its on-disk footprint — the `pool/` repo, `*-agent` repos).
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
| **Pool** | An isolated mesh rooted at a directory, marked by `pool/pool.yaml`. `pool/` is a visible git repo holding the marker, doctrine and glossary (runtime state beside them, ignored); meshctl commits there, you push. A pool still on the hidden legacy directory keeps working until `meshctl pool upgrade` moves it. All comms/roster/KB scope to the pool — cross-pool contact is structurally impossible. Create with `meshctl pool init`. |
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
cd agent-mesh && task install     # build → ~/.local/bin/meshctl
```

Ensure the install dir (`$(go env GOPATH)/bin` or `~/.local/bin`) is on `$PATH`, then
`meshctl --help`. Release binaries are cross-compiled (CGO-free) and attached by the
release CI workflow on every `v*` tag.

Doctrine is embedded source: edit `internal/mesh/templates/doctrine/agent-comms.md` /
`distiller.md`, rebuild, and refresh a pool's emitted doctrine with
`meshctl pool upgrade`. Upgrade only replaces files unchanged since meshctl wrote them; a
pool whose doctrine was edited locally keeps its copy, gets the new version as
`<file>.upstream` to merge by hand, and the command exits non-zero (`--force` overwrites,
backing each edited file up to `<file>.bak-<timestamp>`). Pool-specific doctrine belongs
in `pool/doctrine/local.md`, which upgrades never touch.

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
meshctl pool    init [path] | upgrade [--check] [--force] [--doctrine-only] | list | teardown [--purge --yes] [--dry-run]
meshctl agent   new <name> | onboard <name|path> | bootstrap | list | identity | brief --agent <peer> [--request <id>]
meshctl msg     ask <target> <type> "…" [--in-session [--work] | --detach] | send <target> <type> "…"
meshctl inbox   list [--all] | next [--claim] | claim <id> | release <id>   # list: open queue, priority then age
                respond <id> --status <s> --response "…" | decline <id> --reason "…" [--redirect <agent>]
                archive <id> | --answered | --closed [--force]
                triage [--peer <name>]   # worksheet over open requests; from a hub, the walk order
                triage apply --report <file> <id>...   # close confirmed verdicts (≤5 ids; gate-asked)
meshctl sent    [--all] | ack <id>...      # responses to requests you sent
meshctl work    list [--agent <a>] [--json] | merge <id> [--abort]   # delegated-work branches
meshctl pending [--all] [--agent <a>] [--json]   # the session-start pending-work block; --all per peer
meshctl kb      begin | finish [--summary-file <summary>] [--abort] | consolidate [--json]
                reconcile [--peer <name>]   # walk order, or a canonical peer's reconcile worksheet
                confirm <path>[#<slug>]... --report <file> [--accept-ungrounded <path>]...   # ≤5 reviewed entries → ledger + Confirm: on main
meshctl memory  recall "<q>" [--budget N] [--scope own|canonical] | sessions
meshctl session start | end | list
meshctl handoff show | template | set   (set reads the body from stdin; refuses a body that fails lint)
meshctl state   show | template | set --summary-file <summary>   (the ≤40-line current-state digest, injected at session start; set is a promotion)
meshctl intuition recall "<q>"
meshctl hook    session-start | user-prompt | session-beat | session-end | pre-tool-use | post-tool-use | status-line   (wired per-agent; not run by hand)
meshctl doctor  [--fix] [--json]     # manifests, wiring, KB consistency, pool hygiene
meshctl console [--open] [--print-url] | install | uninstall   # live view of this machine's pools on localhost
meshctl ask … | send …               (top-level aliases for msg ask|send)
```

**Keeping a pool clean across upgrades.** A pool accumulates drift as meshctl evolves:
stale claims and presence records, orphan KB worktrees, a dead dispatch lock, retired
files (`.memory/index.json`), legacy intake statuses, per-session state from long-dead
sessions, leftovers of earlier doctrine upgrades, and references to retired command
names in hand-written files. `meshctl doctor` reports every such item and marks the
ones it can repair as *fixable*; `meshctl doctor --fix` repairs them. `meshctl pool
upgrade` runs the same repairs as its last step and ends with `pool clean ✓`, or lists
exactly what still needs a human. Items that need a human are never edited by meshctl:
a doctrine file you changed (merge the `.upstream` copy, or `--force` with a backup) and
your own text that names the retired `agentmesh` command. Put pool-specific doctrine
(house rules, the real roster, tool policy) in `pool/doctrine/local.md` — upgrades
never touch it, agents read it first, and the core doctrine stays upgradable.

**Updating meshctl.** `meshctl update` replaces the binary with the latest GitHub release
(checksum-verified, in place; `--check` only reports, `--version vX.Y.Z` pins a release).
`meshctl version` and `meshctl doctor` mention a newer release (checked at most once a day;
`MESHCTL_NO_UPDATE_CHECK=1` turns it off). A local build (`task install`) is only replaced
with `--force`. After updating, run `meshctl pool upgrade --check` in each pool. Each
`pool upgrade` records the version that ran it, so the console and `doctor` say when an
installed meshctl is newer than the pool's wiring.

**The console (ADR-0077).** `meshctl console` serves
a live view of the whole pool at `http://127.0.0.1:7717/`: every peer's pending
work with the phrase to say and to which agent, doctor findings (with a re-check button),
pool notices such as a pending upgrade, and the open agent sessions with their scheduled
jobs (Claude Code reports them at the end of every turn). A link beside each session brings
its iTerm2 tab to the front. One console serves every pool on the machine: the live pools in
the registry (`meshctl pool list`) and the pool it was started from, which it opens on. A
switcher in the header moves between them and shows which other pools need attention; each
pool has its own address (`#/pool/<name>`). An agent's page also shows its documents
(designs, reports and mockups it keeps in `docs/` or folders its manifest lists under
`docs:`, rendered with mermaid diagrams, sandboxed), its knowledge base and its recent
history (ADR-0078). Home (`#/`) shows a card per
pool with what needs attention. Two layouts share one frame: **Classic**, a grid of agent
cards, and **Workspace**, the agent list beside the selected agent so the pool stays in view;
an agent's page is a content area with a sidebar of sections, and scheduled jobs read in plain
words. Settings (layout, agent order, attention filter) are saved per pool in
`pool/console.yaml`, committed by the console and never pushed. It watches the pool's state
files, so the view moves within a moment of a change. Cards sort by activity (open
sessions first, then agents that need you) or by name. The things it changes in the pool
are its own settings, doctor's automatic repairs, `pool upgrade` for a pool that needs
one, a push of the pool repo when it is ahead of its remote, and a commit or push of an agent's
repo when doctor finds uncommitted or unpushed work (never forced; agents cannot call them), each run from a button after you see what it will do
(ADR-0079). Pin
the page as a browser app window to keep it beside your terminals. `meshctl console install`
starts it at login (macOS LaunchAgent; `meshctl update` restarts it on the new binary);
`--print-url` prints the address with its token. It listens on 127.0.0.1 only and every
request needs the token kept in your user config (`meshctl/console.json`). `meshctl pending
--json` and `meshctl doctor --json` give agents the same data, versioned.

Key flags: `agent new/onboard --harness claude|opencode`, `--dispatchable`,
`--mutates code,tracker` (non-empty ⇒ not dispatchable), `--accepts type:desc,…`,
`--owns`, `--domains`, `--rgb`, `--badge`, `--role`, `--title`, `--iterm2`.

**Terminal identity (portable).** `meshctl agent identity` prints an ANSI title +
colored badge (from the manifest's `--badge`/`--rgb`) so you can see which agent a
terminal is driving — works in any terminal on any OS. Run it on shell entry in an
agent workspace (shell rc or a direnv `.envrc`). `agent new --iterm2` additionally
writes an optional macOS iTerm2 profile; it's off by default and never required.

## Harness integration

`meshctl` carries its own integration and renders it per harness (one binary, no
python, no loose scripts, no machine-absolute paths):

| | Claude Code | opencode V1 (1.x) | opencode V2 (2.x) |
|---|---|---|---|
| rules | `.claude/settings.json` | `opencode.json` `instructions:["CLAUDE.md"]` | plugin injects `CLAUDE.md` (V2 ignores `instructions`) |
| skills | `.claude/skills/<n>/SKILL.md` | `.opencode/skills/<n>/SKILL.md` | `.opencode/skills/<n>/SKILL.md` |
| hooks | settings.json → `meshctl hook …` | `.opencode/plugin/meshctl.js` → `meshctl hook …` | `.opencode/plugins/meshctl.js` → `meshctl hook …` |
| inject | `hookSpecificOutput` JSON | `experimental.chat.system.transform` | `session.hook("context")` |
| capture | SessionEnd | `session.idle` = heartbeat; session-end after an idle window | `session.execution.succeeded` = heartbeat; session-end after an idle window (`AGENT_MESH_OPENCODE_IDLE_SEC`, 600s) or `session.deleted` |
| gate | PreToolUse deny envelope | — (advisory only) | `tool.hook("execute.before")` throws the reason |
| ask | PreToolUse ask envelope | — | `opencode.json` `permissions` rules with effect `ask` for the promotion commands; the gate lets those calls through to opencode's prompt |
| status line | settings.json `statusLine` → `meshctl hook status-line`, every 10s | — | — |
| dispatch | `claude -p` | `opencode run --pure --auto` | `opencode run --standalone --auto` |

### Feature parity

What works where (ADR-0075). The table is generated from `internal/mesh/parity.go`; a test
fails when a command is not classified there or when this block is out of date, and the
release workflow runs it before building.

<!-- parity:begin (generated from internal/mesh/parity.go; do not edit by hand) -->
| Feature | Claude Code | opencode V2 (2.x) | opencode V1 (1.x) |
|---|---|---|---|
| Doctrine and mesh skills loaded | ✅ CLAUDE.md, `.claude/skills/` | ✅ plugin injects CLAUDE.md; `.opencode/skills/` | ✅ `instructions`; `.opencode/skills/` |
| Session start: handoff, state digest, pending work, intuition | ✅ | ✅ | ✅ |
| Session presence and capture | ✅ SessionEnd | ✅ heartbeat, idle window | ✅ idle event |
| Pre-tool gate: memory stores, intake, KB rules, peer contracts | ✅ | ✅ | ✗ no pre-tool hook |
| Promotion asks: `kb finish`, `kb confirm`, `state set`, `inbox triage apply` | ✅ gate ask | ✅ opencode's permission ask | ✗ refused |
| Agent writes to `pool/` | ✅ asked, committed after | ✗ refused; edit by hand | ✗ |
| Outbound-write gate | ✅ asked once per summary | ✗ refused, no ask path | ✗ |
| After-call record: pool commits, outbound acks | ✅ PostToolUse | ✗ not called | ✗ |
| Headless dispatch | ✅ `claude -p` | ✅ `opencode run --standalone`, locked-down permissions | ✅ `opencode run --pure` |
| In-session peers, consult and work (`ask --in-session [--work]`) | ✅ Agent tool | ✅ `subagent` tool; mutating peers asked by opencode | ✗ refused |
| Distillation at a boundary | ✅ distiller subagent | ✅ at session end | ✅ at session end |
| Status line | ✅ every 10s | ✗ none | ✗ none |
| Console: live pool view, open sessions (`meshctl console`) | ✅ with scheduled jobs | ✅ no scheduled jobs | ✅ no scheduled jobs |
<!-- parity:end -->

`meshctl agent new --harness …` picks it; `onboard` autodetects; it's recorded in
`agent.yaml`. Adding a harness = one adapter (`internal/mesh/harness.go`).

**Safety defaults.** The PreToolUse gate is fail-closed: a tool call it cannot read is
denied, and shell commands that write a mesh-owned path (`> intake/x.md`, `sed -i`,
`rm` a claim) are denied like file-edit tools; the intake rule is scoped to the current
pool. Onboarding pre-approves a scoped set of `meshctl` verbs (messaging, inbox,
cognition, roster, doctor) — not `pool teardown`, `pool forget` or `update`; `pool
teardown --purge` asks for the pool name (or `--yes`) and supports `--dry-run`. A
dispatched peer (`meshctl ask`) gets Read/Grep/Glob plus the `meshctl inbox respond|decline`
verbs and no file edits, on both harnesses. Request headers are validated (agent names,
requester, priority, related refs) and written once with `O_EXCL`; state files
(claims, presence, counts, stamps) are written atomically.

**In-session dispatch (Claude Code).** `meshctl ask <peer> <type> "…" --in-session` runs an
advisory peer as a subagent of the caller's own session instead of a second `claude`
process in a second terminal. Every dispatchable peer is rendered from the roster into
each Claude agent's `.claude/agents/mesh-<peer>.md` (by `agent new|onboard` and `pool
upgrade`; refreshed when the roster changes, removed when a peer stops being
dispatchable; hand-authored files are left alone). The command writes the same intake
file, charges the same hourly cap, logs `mode=in-session`, claims the request for the
harness session, and prints the exact Agent-tool call to make (`subagent_type:
mesh-<peer>`). Inside the subagent the peer starts with `meshctl agent brief --agent
<peer> --request <id>` (identity, handoff, recall pointers and the ask in one shot, since
its own session hooks do not fire there), reads its KB read-only, and answers with
`meshctl inbox respond … --agent <peer>`. Its definition grants no Agent/Write/Edit tools
and its frontmatter hooks run the mesh gate in peer mode, which denies dispatch, file
writes, git and KB writes — so a consulted peer cannot chain or drift. The pool root is
added to the caller's `.claude/settings.local.json` `additionalDirectories` (machine-local,
git-ignored) so the peer can read its own workspace. Session start lists the installed
peers.

**In-session dispatch (opencode V2) (ADR-0076).**
The same command works from an opencode 2.x caller. Peers are rendered into
`.opencode/agents/mesh-<peer>[-work].md` (`mode: subagent`; no nested subagents, and no
edit tools in a consult) and `ask --in-session` prints the `subagent` tool call (`agent:
mesh-<peer>`). V2 has no per-agent hooks, so the caller's mesh plugin looks each session
up once: a child session running a `mesh-` agent sends its calls to the gate with the
caller's session, the child id and the agent, and meshctl maps the agent back to the
rendered definition — peer gate, work gate and the peer's session record, as on Claude
Code. A `mesh-` agent without a meshctl-rendered definition, or a child whose agent
cannot be told, is refused. Other subagents (`general`, `explore`) run under the caller's
full gate. The child has its own session id, so the request is marked in-progress
without a claim. Delegating work to a `mutates` peer goes through opencode's own ask: a
`subagent` ask rule per such peer in the caller's `opencode.json`. No pool-access grant
is needed (V2 lets a session reach outside its project; the gates draw the line). opencode
V1 keeps the headless `ask`.

**Delegated work — the hub session (ADR-0063).** Add `--work` and the peer does the work
instead of advising: `meshctl ask <peer> <type> "…" --in-session --work` prints
`subagent_type: mesh-<peer>-work`, a write-capable definition (Read/Grep/Glob/Bash/Edit/
Write, no Agent) rendered for every work-delegable peer — dispatchable, or declaring
`mutates`. One session can then drive the whole pool while each agent keeps its own KB,
decisions and repo. The peer edits by its own conventions: KB writes through `kb
begin|finish --agent <peer>`, path-limited commits in its workspace, and product repos it
declares under `workdirs:` in `agent.yaml` (added to the caller's `additionalDirectories`).
If the peer's own session is live, the request gets a worktree + branch
(`pool/worktrees/<peer>-work-<id>`, `mesh/work/<id>`) so the two never share a
checkout; the peer's own session lands it with `meshctl work merge <id>` (rebase + ff-only;
`--abort` discards). The work-mode gate denies writes outside those roots, dispatch and
`work merge`; delegating to a `mutates` peer asks the human once per delegation. The peer
ends with a receipt for the caller to relay, and its stop is distilled into its own KB.
`meshctl doctor` flags missing `-work` definitions and workdir grants; hygiene reaps
work worktrees already merged by hand and reports branches unmerged for over 14 days.

**opencode V1 vs V2.** Both lines are supported under the one `opencode` harness; their
plugin APIs are incompatible (V2 skips a V1 plugin with only a log warning). meshctl
wires for the installed binary's major (`opencode --version`); pin it with
`agent new/onboard --opencode-version 1|2` or `MESHCTL_OPENCODE_MAJOR`. After upgrading
opencode, re-run `meshctl agent onboard <name>` — it swaps the plugin — and `meshctl
doctor` flags any agent still wired for the other major.

## Cognition — how agents learn

Every agent maintains a git-tracked `knowledge/` KB in two tiers — a **current tier**
(`state.md` digest, `direction.md`, `risks.md`, `architecture/`, `playbooks/`,
`reference/`) that states what is true now, and a **log tier** (`decisions/`,
`experience/`) that is append-only history — and learns over time:

- **Recall:** `meshctl memory recall "<q>"` — live, unified over KB topics + captured
  session-records + the git commit time-spine. Current tier outranks log, `candidates/`
  ranks last, superseded decisions never appear. Pointers by default; `--budget N`
  inlines the top current-tier bodies within about N tokens.
- **Decisions are machine-readable:** ADR frontmatter (`status`, `supersedes`,
  `superseded_by`, `updates`); `meshctl doctor --fix` migrates the bullet form.
- **Consolidate:** `meshctl kb consolidate` lists the KB's debt — accepted decisions not
  landed in the docs they `updates:` (a doctor error), decisions the `state.md` digest
  predates, candidates older than 14 days, a handoff or digest that fails lint. The agent
  works it off in a `kb begin`/`finish` worktree and rewrites the digest with
  `meshctl state set --summary-file <summary>`.
- **Episodic capture:** at session end the harness hook writes an extractive
  session-record (files touched, commands, tools, refs, commits, handoff) — no LLM.
- **Intuition:** `meshctl intuition recall` / the per-prompt hook surface compact
  past-work pointers from the **owned engine** (session-records + git spine).
- **Distill:** turn recurring intuition into durable KB via `meshctl kb begin` → write a
  candidate → consolidate → summary + confirmation round → `meshctl kb finish
  --summary-file <summary>`. See the `mesh-cognition` skill. The
  boundary nudge (wind-down phrase or 8 prompts) is on for every agent; on Claude Code it
  spawns the scaffolded `.claude/agents/distiller.md` subagent, on opencode the session-end
  hook runs the distiller. Narrow it with `AGENT_MESH_DISTILL_NUDGE_AGENTS=a,b` or `none`.
- **Handoff and state:** `meshctl handoff set` writes the resume point and `meshctl state
  set --summary-file <summary>` the current-state digest, both from a fixed template and both refusing a body that
  fails lint; both are injected at session start. `meshctl doctor` validates manifests +
  KB + harness wiring.

### Shared understanding before truth — the confirmation flow

Agents propose; only the human confirms. Before anything changes what other sessions treat
as true, or leaves the mesh, the agent reaches **shared understanding** with the human:
agreement to its intent, its grounding (cited, or marked *assumption*) and its precedent
(cited, or marked *new*). The flow is the same for every gated write:

1. **Summary.** The agent writes one file per session, `<peer>/.memory/confirm/<session>.md`
   (Intent, Grounding, Precedent, Coherence when related canonical entries exist).
2. **Confirmation round.** It reads that summary to the human in chat, one item per entry
   (`📝 **C<n> — <entry>** (new|amended|retired)`, what it says from now on, what it no
   longer says, grounding, precedent, `➡️ ✅ Confirm`). "yes" confirms; "C2 no: reason"
   sends that entry back to candidates.
3. **Signature.** It runs the command naming the summary; the PreToolUse gate asks, with the
   summary as the ask text:
   - `meshctl kb finish --summary-file <summary>` when the merge touches more than
     `candidates/` (a promotion); the merge writes the content-hash ledger
     (`knowledge/.confirmed.yaml`) and a `Confirm: <intent>` commit on main;
   - `meshctl state set --summary-file <summary>` for the digest;
   - `meshctl kb confirm <path>... --report <file>` for up to five existing entries a
     reconcile report reviewed (`meshctl kb reconcile` writes the worksheet);
   - `meshctl inbox triage apply --report <file> <id>...` for up to five intake requests the
     triage pass judged closed (`meshctl inbox triage` writes the worksheet);
   - a write to a surface the pool declares under `outbound:` in `pool/config.yaml`, asked
     once per summary and audited per session (`hook post-tool-use` records the ack).
4. **No human, no confirmation.** Headless runs and delegated work cannot confirm: a
   promotion or an outbound write ends in `needs-confirm` (the KB branch is parked, the
   response carries the round and the summary path) for the peer's own session to finish.

Recall marks unconfirmed entries; `memory recall --scope canonical` also reads canonical
peers' knowledge. Session start shows a `# Pending work` block (parked branches, open
conflicts, inbox, read-back, delegated work, KB health; `meshctl pending [--all]` prints
it); the agent offers triage now, a subset, or deferral, and a deferral is one
`Deferred: <ids>` line in the handoff. On "check intake" the agent shows the queue
(`meshctl inbox list`; `--all` for every status), proposes an order (priority, then age)
and claims nothing until the human confirms or reorders; it then claims by id in that
order (ADR-0073). Any decision the agent needs from the human is asked as a **decision
round**: one `📝 **C<n> — <short title>**` item per change, `From now on it says:` /
`It no longer says:` (or the action and why), `➡️ ✅ Confirm`, answered `yes` or
`C<n> no: <reason>`. `meshctl pool upgrade --check` shows doctrine
changes as a diff before `pool upgrade` commits them in `pool/` as
`doctrine: upgrade to <version>`.

Authoritative protocol: `internal/mesh/templates/doctrine/agent-comms.md` (emitted into
each pool as `pool/doctrine/agent-comms.md`). Design rationale lives in `docs/`.

## Bootstrap a mesh from scratch (agent runbook)

A from-zero procedure an agent (or human) can follow — no prior mesh required:

1. **Build the tool.** Clone this repo; `task install` (or `go build -o ~/.local/bin/meshctl ./cmd/meshctl`). Confirm `meshctl --help`.
2. **Create the pool.** `meshctl pool init <pool-dir> --name <name> [--harness opencode]`. This creates `pool/` as a git repo on `main` (doctrine, glossary, the `pool/pool.yaml` marker, an ignore file for runtime state, a first commit in your git identity) and a **steward** agent. All later `meshctl` commands run from inside the pool (cwd resolves the pool via the marker).
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
7. **Operate.** Reach a peer with `meshctl msg send|ask`; on "check intake" read `meshctl inbox list` to the human (pending + in-progress, priority then age, a one-line ask each), propose an order and wait, then `meshctl inbox claim <id>` in the order they give, answer with `meshctl inbox respond` (or `inbox decline --redirect` when it isn't yours), and read replies with `meshctl sent`; recall with `meshctl memory recall`; record decisions via `meshctl kb begin/finish`; leave a handoff with `meshctl handoff set`.

## Repo layout

| Path | What |
|------|------|
| `cmd/meshctl/` | CLI entrypoint (cobra). |
| `internal/mesh/` | Core: manifests, directory, intake/dispatch, concurrency (sessions/claims/kb worktrees), cognition (memory/recall/episodic/handoff/intuition), harness adapters, pool init. |
| `internal/mesh/templates/` | Embedded (`go:embed`) source, edited in place: agent scaffold, skills (`mesh-cognition`, `mesh-steward`), steward CLAUDE.md, and the doctrine (`templates/doctrine/{agent-comms,distiller}.md`) the binary emits into each pool. |
| `docs/` | Human docs: `DESIGN.md` (why it's built this way), `build-spec.md` (what to build), `knowledge-and-memory.md` (the cognition design brief). |
| `Taskfile.yaml` | `task install` / `build`. |

## Conventions

- A pool is a plain container of workspaces — not itself a git repo. `meshctl` and each
  agent are independent repos cloned into it; `pool/` (doctrine, glossary, config) is its
  own git repo. Runtime artifacts (`pool/dispatch.*`, `pool/worktrees/`, `.sessions/`,
  `.memory/`) are git-ignored.
- Prefer SSH git remotes.
- A change to what a harness can do updates `internal/mesh/parity.go` in the same commit:
  every command is classified there, and the README table above is generated from it.

## Versioning

Releases follow semantic versioning against meshctl's contract: the CLI and its flags,
`--json` output, on-disk formats, the wiring version and console URLs. Major breaks something
`pool upgrade` cannot migrate. Minor adds a capability, and any wiring change is at least
minor. Patch only fixes what already exists (ADR-0080).

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
