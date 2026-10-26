---
status: accepted
date: 2026-10-26
---
# 0076. opencode V2 in-session peers: rendered subagents, one plugin maps child sessions to the peer gate

- **Status:** accepted
- **Date:** 2026-10-26
- **Deciders:** maintainer (accepted 2026-10-26)
- **Related:** ADR-0032 (multi-harness adapter seam), ADR-0063 (hub session, delegated peer work), ADR-0073 (one request per consulted peer), ADR-0075 (opencode's own ask; parity table), `internal/mesh/peers.go`, `internal/mesh/opencode_v2.go`, `internal/mesh/hook.go`, `internal/mesh/insession.go`

## Context

`meshctl ask <peer> <type> "…" --in-session [--work]` runs a peer as a subagent of the caller's own session. Until now it ran on Claude Code only. Each peer definition (`.claude/agents/mesh-<peer>.md`) carries its own hooks, `meshctl hook … --agent <peer> --in-session [--work]`, and those hooks apply the peer gate and capture the peer's run when it stops. opencode V2 has a `subagent` tool, so the same flow is possible there, with one difference: V2 has no per-agent hooks, and the caller's one project plugin sees every session.

Verified against opencode 2.0.28 with a scripted model, the real binary and a probe plugin:
- A custom subagent is a `.opencode/agents/<name>.md` file whose frontmatter holds `description` and `mode: subagent` and may hold `permissions`; the body is the system prompt. An `agents` entry in `opencode.json` works too. Both appear in the `subagent` tool's catalog.
- Per-agent `permissions` apply inside the child session. With `edit` denied, the write and edit tools are absent from the child's tool list.
- A subagent runs in a child session with its own id. The child's shells see that id as `OPENCODE_SESSION_ID`, not the parent's.
- The plugin's `tool.hook("execute.before")` event carries `agent`, the id of the agent running the session (`mesh-<peer>` in the child), along with `sessionID`. `ctx.session.get({sessionID})` returns the session with `parentID` and `agent`, and `session.created` carries the same fields.
- A child's run ends with `session.execution.succeeded` for the child id, in foreground and in `background: true` mode.
- The child's own context hook fires, so without a change the caller's doctrine and session start would be injected into the peer.
- By default a V2 session reads and writes outside its project without asking.
- The `subagent` tool asks the parent agent's `subagent` permission for the target agent before launching it. A project rule `{action: subagent, resource: <agent>, effect: ask}` makes opencode ask the human.

## Options considered

1. **Keep in-session peers Claude Code only.** Rejected: an opencode V2 pool would keep the second-process headless `ask` as its only path, and the coordinator session (ADR-0063) would be Claude Code only.
2. **Rely on the rendered definition's `permissions` as the only peer gate.** Rejected: permissions cannot express the peer contract (no `meshctl ask|send`, no git, no KB writes in a consult, writes confined to the peer's own roots in work mode), and the peer's run would not be captured.
3. **The caller's plugin maps child sessions to their peer, and the existing hooks apply the peer gate.** Chosen.

## Decision

- **Definitions.** For an opencode V2 caller, `agent new|onboard` and `pool upgrade` render each dispatchable peer as `.opencode/agents/mesh-<peer>.md` and each work-delegable peer as `mesh-<peer>-work.md`, from the same roster, templates and managed marker as on Claude Code. The frontmatter has `mode: subagent`, the description and `permissions` that deny nested subagents and, in a consult, edits. No `model`: a subagent without one inherits the caller's, and a Claude model name would not resolve. A comment in the body names the peer and mode (`--agent <peer> --in-session [--work]`). Doctor flags stale definitions. On opencode V1 the managed definitions are removed, and `ask --in-session` refuses.
- **Mapping in the plugin.** The plugin looks each session up once (`ctx.session.get`). For a child session running a `mesh-` agent, it sends each tool call to `meshctl hook pre-tool-use` in the shape Claude Code's subagent hooks use: `session_id` is the caller's session, `agent_id` the child's, `agent_type` the agent. `meshctl` resolves `agent_type` against the caller's rendered definition and applies the peer or work gate. A `mesh-` agent without a meshctl-rendered definition is refused, and so is a call whose agent cannot be told. Other subagents (`general`, `explore`) send no `agent_id`, so they run under the caller's full gate. On Claude Code a hub's own hook steps aside for a subagent, but on opencode the plugin is the only hook the subagent has.
- **Lifecycle.** A child session gets no session start, intuition, heartbeat or idle close. A peer child does not get the caller's `CLAUDE.md` injected; other subagents keep it. When a peer child's run ends, the plugin calls `hook session-end` with the same mapping, which writes the peer's `<session>-peer-<child>` record, starts its distillation and logs `DONE`, as Claude Code's subagent Stop hook does.
- **No claim.** The child's `inbox respond` runs under the child's session id, and that id does not exist yet when `ask --in-session` runs. An opencode caller therefore marks the request in-progress without a claim, which is the path already used when no session id is exported. `inbox next` skips in-progress requests either way.
- **Delegation asks.** Delegating work to a state-mutating peer is asked of the human each time (ADR-0063). On opencode V2 that ask is opencode's own: wiring appends a `subagent` ask rule for each such `mesh-<peer>-work` to the caller's `opencode.json`, and the gate lets the delegation through only when opencode will ask, as ADR-0075 does for promotions. The gate now treats `subagent` as a delegation tool alongside `Agent` and `Task`, reading its `agent` input.
- **No pool-access grant.** Claude Code needs the pool root in `additionalDirectories`. V2 already lets the session reach outside its project, so the peer and work gates draw the line there.
- **Parity.** The "In-session peers" row reads ✅ for opencode V2.

## Consequences

- Positive: an opencode V2 session can consult peers and delegate work to them inside the session, with the same intake, cap, audit line, gates and captured record as Claude Code.
- Positive: the peer gate's inputs are the hook payload and a meshctl-rendered file; the plugin chooses nothing, and an unknown `mesh-` agent fails closed.
- Negative: opencode loads the caller's `AGENTS.md` into every session, the peer's included. The plugin can only withhold the `CLAUDE.md` it injects itself, so where `AGENTS.md` is the caller's doctrine the peer also reads it. The definition and `agent brief` state the peer's identity first.
- Negative: no claim means a crashed peer leaves its request in-progress until the human or the owner resolves it. The no-session-id path already has this property.
- Negative: opencode transcripts are not mined; a peer's record holds the git context only, as for any opencode session.
- Negative: the gate costs one session lookup per new session, cached for the plugin's life.
- Follow-ups: the after-call record (PostToolUse) is still not called on opencode, so pool commits and outbound acks remain Claude Code only.
