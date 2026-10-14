---
status: accepted
date: 2026-10-14
---
# 0063. Hub session: peers do delegated work in-session, in their own repos

- **Status:** accepted
- **Date:** 2026-10-14
- **Deciders:** maintainer (chose "both, A first", "worktree branch on live peer"; accepted 2026-10-14)
- **Related:** meshctl v1.3.0 3c9f221 (`internal/mesh/peers.go`, `insession.go`, gate peer mode), harness-aware dispatch (`dispatch.go`), KB-rule gate (`kbrules.go`), session-end distillation (`distill.go`)

## Context

The goal is one hub session. It may be any agent the human is working in, or later a dedicated coordinator agent. When work lands on another agent's plate, that agent does it inline from the hub, with no switch to that agent's window. The observed pain: every bounce to another window breaks flow, the original session is often never finished, and the day goes to context switching.

Per-agent workspaces are non-negotiable: own KB, own ADRs, own handoff, own git history. A plain Claude Code subagent carries only the parent's context, so it does not qualify.

v1.3.0 `ask --in-session` delivers half of this. The `mesh-<peer>` subagent loads the peer's identity, handoff and recall through `agent brief`, and it reads the peer's workspace. But by design it is a read-only consult:
- the gate's peer mode denies edits, git, KB writes and dispatch;
- SubagentStop captures the transcript as a peer session record but distills nothing (`hook.go`: "nothing to distil");
- `mutates` peers are refused outright.

The result: a correction sent to a peer from the hub is answered, but the peer never learns it, and no work gets done.

## Options considered

1. **A. Delegated work mode.** A write-capable peer subagent works in its own repo, scoped by the gate, and distills into its own KB. Cannot chain. The hub's context stays clean, with only the result and a diff receipt coming back. Cost: new subagent definition, gate mode, Stop path, worktree handling.
2. **B. Hat switch (`meshctl become <peer>`).** The main thread assumes the peer's identity for a stretch. Fully visible and interactive. Cost: identity bleed, because hub assumptions share one context with the peer's decisions (the drift that separate KBs exist to prevent), and the hub's context fills with peer KB.
3. **Status quo plus `send`.** Corrections reach the peer's KB only via its inbox in its own window. That is the friction being removed.

## Decision

Build A now (meshctl v1.5.0). B follows as an opt-in for live design conversation (separate ADR, v1.6.0).

**A. Delegated work mode**

1. **Verb:** `meshctl ask <peer> <type> "…" --in-session --work`.
   - Same intake file, hourly cap and dispatch.log (`mode=in-session-work`).
   - Prints the Agent call for `subagent_type: mesh-<peer>-work`.
   - The consult path (`--in-session` alone) is unchanged.
2. **Definition:** `pool upgrade` / `agent new|onboard` render `.claude/agents/mesh-<peer>-work.md` alongside the consult definition.
   - Tools: Read, Grep, Glob, Bash, Edit, Write, MultiEdit. No Agent.
   - Frontmatter hooks run with `--in-session --work`.
3. **Gate, peer-work mode:**
   - Allow: edits and git in the peer's workspace (or its worktree, see 4) and in repos the peer `owns`. The peer's own KB rules (`kbrules.go`) apply. KB writes go through the peer's `kb begin|finish`.
   - Deny: writes in the hub's workspace or any other agent's workspace, `meshctl ask|send` (no chaining; a needed third agent is reported back to the hub, which dispatches it), and pool-mutating commands.
   - Fail-open on gate errors, as with every gate. The deny list is the contract.
4. **Concurrency (chosen: worktree).** If the peer has a live session (presence), the job runs in a git worktree of the peer workspace on branch `mesh/work/<request-id>`, under the pool's `.agentmesh/worktrees/`.
   - The hub reports the branch.
   - The peer's next session start lists pending work branches to merge, and so does `meshctl work merge <id>` when the peer is idle.
   - With no live session, the job works on the peer's normal branch directly.
   - Product repos the peer owns follow that repo's own git convention (branch/PR), never a direct trunk commit.
5. **Learning:** on SubagentStop in work mode:
   - capture the transcript as the peer's session record (as today);
   - run the peer's distillation headlessly in the peer workspace (`SpawnDistill` with a Claude headless binding; candidates only, never promotion);
   - close the audit line.
   This applies to consults too. A correction sent through a consult now produces a candidate in the peer's KB.
6. **Transparency:** the peer's response must end with a work receipt: files touched per repo, commits/branches, KB candidates written and open questions. `meshctl sent` and the intake response keep it. Follow-ups continue the same subagent via SendMessage. Each stop re-captures and re-distills that agent_id's record, and the distiller drafts candidates only, so duplicates are judged at consolidate.
7. **`mutates` peers:** dispatchable in work mode only. The caller's PreToolUse matcher includes the Agent tool. For a `mesh-<peer>-work` call to a mutating peer, the gate returns `ask`, so a human approves each one. The hub's model cannot trigger a state-mutating agent unprompted. opencode has no ask path and treats it as deny.
8. **`workdirs:`** is a new manifest field: the local repo paths (outside the pool) a peer may write in work mode. The gate scopes writes to the peer's workspace plus `workdirs`. The caller's `additionalDirectories` gets the same paths. `owns` (tags) and `repo` (a remote URL) cannot serve as paths.

**B (later):** `meshctl become <peer>` / `become --done`. It injects the peer's brief, switches gate identity and write scope to the peer, and on `--done` captures the stretch as a peer session record and distills it. Opt-in, for interactive work where the hub wants the peer's voice live.

## Consequences

- Positive:
  - One window. Work and corrections land in the owning agent's repo and KB.
  - The hub's context stays clean, which fits a future coordinator agent.
  - Consults start feeding peer learning too (5).
- Negative / new risks:
  - Peer writes happen in a subagent the human sees only as a summary. The receipt (6) and git history are the mitigation, not live visibility.
  - The worktree adds a merge step and can produce stale branches. `doctor` must flag `mesh/work/*` branches older than N days.
  - Claude Code subagents inherit the hub's cwd and permission scope. Peer and owned-repo paths must be in the hub's `additionalDirectories` (extend v1.3.0's settings.local.json write to include `owns` repos), or every write prompts.
  - Headless distill per stop costs a model run. It is bounded by the hourly cap and the once-per-record marker.
- Implementation sequence (v1.5.0):
  1. headless Claude distill binding plus distill on in-session Stop (also fixes consults);
  2. `-work` definition rendering;
  3. gate peer-work mode plus tests;
  4. worktree on live presence and `work merge`;
  5. receipt contract in `agent brief`;
  6. `mutates` ask-hook;
  7. doctor checks, README, doctrine.
- Follow-ups: ADR for B; a dedicated coordinator agent role template; `doctor` stale-worktree check.
