---
status: accepted
date: 2026-10-16
---
# 0068. Outbound writes are gate-asked once per summary; surfaces are pool config keyed by `mutates`

- **Status:** accepted
- **Date:** 2026-10-16
- **Deciders:** maintainer (accepted 2026-10-16, grilling of 16 questions)
- **Related:** ADR-0064 (agents propose, humans confirm), ADR-0067 (confirmation at `kb finish`), ADR-0062 (KB-rule gate), ADR-0063 (delegated work, `mutates`), ADR-0066 (pool repo, PostToolUse commit), agent-comms rule 7, `internal/mesh/gate.go`, `internal/mesh/kbrules.go`, `internal/mesh/settings.go` (hook wiring), `internal/mesh/toolref.go` (pool config)
- **Precedent:** the KB-rule gate (ADR-0062) matches regex triggers against tool name and input and acks once per session in `.memory/rule-acks/`; ADR-0067 reuses one summary file as the ask text. The per-batch ack learned from a PostToolUse hook is new: no precedent found in the ecosystem, the pool doctrine or confirmed KB entries.

## Context

The incident that redrew this effort: a peer invented release-workflow conventions and wrote them to the tracker board. ADR-0064 and ADR-0067 gate what a peer treats as true in its own knowledge base. Nothing yet gates what a peer makes other people treat as true: tracker items, wiki pages, pull requests.

Verified facts:
- The gate already fires on every `mcp__*` tool and on Bash (matcher `Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__|Agent|Task`, 10s timeout), so no new hook event is needed to see an outbound call.
- Tool names differ per harness and per server: `mcp__<connector>__createIssue`, `mcp__<proxy>__createIssue`, opencode `<proxy>_createIssue`. The suffix is stable.
- Core must not name any tracker; meshctl is pool-type agnostic.
- `agent.yaml` already declares `mutates:` tags (`[tracker]`, `[code]`), used by the delegation ask (ADR-0063 §7).
- A consult-mode peer subagent is denied file and shell writes but not MCP mutators: it could still create a tracker item.
- Headless runs expose `AGENT_DISPATCH_DEPTH` (dispatch) or `AGENT_MESH_DISTILLING=1` (distiller) to the harness and hence to hooks.
- Gate approval leaves no trace. Each tool call is one hook invocation, so "ask once per batch" needs a record written after the human says yes. No PostToolUse hook is wired today; ADR-0066 already plans one for pool commits.
- The shared-understanding summary (intent / grounding / precedent) lives at `<agent>/.memory/confirm/<session>.md` and is the ask text for `kb finish` (ADR-0067).

## Options considered

### Declaring outbound tools
1. **Hardcode known mutators in core.** Rejected: names a tracker in core.
2. **One trigger list matched against tool name plus flattened input**, as the KB-rule gate does. Rejected: a `Write` of a document that mentions `gh pr create` would trip the ask.
3. **Pool config with `tools:` (tool-name regexes) and `commands:` (Bash command regexes) per surface.** Chosen.

### Who may write a surface
1. **Any peer, always ask.** Rejected: pool doctrine already forbids some peers from a surface, as prose.
2. **Surface name equals a `mutates` tag; peers without the tag are denied, peers with it are asked.** Chosen. Reuses an existing field and turns the prose rule into a gate.

### Ask cadence
1. **Ask on every outbound call.** Simple, every call seen. Rejected: a task of ten tracker writes is ten prompts, and ADR-0064 already noted that unreadable or repetitive asks become rubber stamps.
2. **Ask once per summary content, acked by PostToolUse.** Chosen, with an audit log so later calls in the batch stay visible.
3. **Ask once per session.** Rejected: one early summary would cover every later write.

### Headless and delegated runs
1. **Relay the ask to the hub and re-dispatch with the answer.** Rejected for the write itself: a relayed "yes" is not a human at the prompt (same stance as ADR-0067).
2. **Deny headless; let in-session work subagents ask through the hub's prompt.** Chosen.

## Decision

- **Config.** `pool/config.yaml` gains `outbound:`, a map of surface name to `{tools: [regex], commands: [regex]}`. Regexes are case-insensitive and unanchored on the tool name, so server prefixes do not matter; `commands:` apply to shell tools only; file-edit tools are never outbound. Core ships no entries; `pool init` writes a commented, surface-neutral example. Pools list only mutators, so reads never match.
- **Surface = `mutates` tag.** A peer whose `mutates:` lacks the surface is denied with a reason naming the owner path. A peer with the tag gets the ask. Doctor warns on a `mutates` tag with no surface and on a surface no peer mutates.
- **Summary.** The first outbound call reads `<agent>/.memory/confirm/<session>.md`. Missing or empty → deny "summary required". Ask text = summary verbatim, then `Outbound: <surface> via <tool>` and the flattened tool input truncated to about 1000 characters.
- **Batch = summary hash.** The gate asks once per summary content. A PostToolUse hook records `(session, sha256 of the summary)` in `.memory/confirm/<session>.acks`; later outbound calls with the same hash pass silently. Rewriting the summary re-asks. A denied ask leaves no ack. Acks are per gate: a `kb finish` approval does not ack an outbound batch, nor the reverse. Ack and audit files are session-scoped and cleaned like `rule-acks`. The wiring version bumps.
- **Audit.** Every outbound call allowed under a batch appends to `<agent>/.memory/outbound/<session>.jsonl` (time, surface, tool, args digest, summary hash). The work receipt and the session-end summary list them.
- **In-session peers.** A `-work` subagent is asked through the hub's permission prompt, same gate and batch, acks keyed by its own session. Assumption to verify in the prototype: Claude Code surfaces subagent permission prompts in the main window. A consult subagent is denied every outbound surface.
- **Headless.** When the hook sees `AGENT_DISPATCH_DEPTH` or `AGENT_MESH_DISTILLING`, outbound calls are denied "no human at the prompt". The peer returns `needs-confirm` carrying the summary and the planned writes; the human performs them from a gated session. No relayed approval.
- **No ask path** (opencode, unwired gate): deny "no confirmation path; run from a gated session". Doctor's wiring check reports the `outbound` config.
- **No exemptions.** Every listed mutator asks. There is no `exempt:` key; a pool scopes by what it lists.
- **No agent resolves.** Inside a pool with no matching workspace: deny "run from a peer workspace". Outside any pool: inert.
- **Doctrine.** The template CLAUDE.md gains "write `.memory/confirm/<session>.md` before any outbound write". agent-comms rule 7 gains "writes to outbound surfaces are gate-asked". Core text only; pools cannot loosen.

## Consequences

- Positive: no tracker item, wiki page or push leaves a peer without the human having seen intent, grounding and precedent. The incident's path is closed at the tool call, not by prose.
- Positive: pool prose about who may write which surface becomes a deny, and the consult-mode MCP gap closes.
- Negative: calls after the first in a batch are not seen individually; the audit log and the receipt are the mitigation, not live visibility.
- Negative: a new PostToolUse hook and a wiring bump mean every peer re-onboards, and `outbound:` plus correct `mutates` tags must be written per pool before the gate does anything.
- Negative: the subagent-prompt assumption is unverified until the prototype runs; if it fails, work-mode outbound writes fall back to the headless rule (deny, `needs-confirm`).
- Rollout: `outbound:` config in `pool/config.yaml` (after the ADR-0066 migration), `mutates` tags audited per peer, PostToolUse wiring, doctor checks.
