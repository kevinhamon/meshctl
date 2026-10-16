---
status: accepted
date: 2026-10-01
---
# 0062. Standing rules are enforced by a KB-rule gate, not by prose

- **Status:** accepted
- **Date:** 2026-10-01
- **Deciders:** maintainer (directive: "check your actions against the internal KB else it blocks your action")
- **Related:** meshctl 2fb5a17 (`internal/mesh/kbrules.go`), the mesh gate (meshctl v1.2.0, internal/mesh/gate.go) lineage, experience/local-remote-tokens-not-a-finding.md

## Context

Standing instructions ("do not do X again") were stored as KB notes and prescribed by prose (CLAUDE.md, mesh-cognition: recall before acting). The model skips prescribed steps under task pressure. Verified 2026-10-01: the token note existed since 2026-09-23, yet a git failure led me to flag the intentional `~/.gitconfig` insteadOf rule, print a token unredacted, and offer to remove it — the recall that would have surfaced the note was never run. The recurrence was weekly. Contrast: the harness-enforced PR-review gate has not been skipped since it was built. Prose is advisory; only gates hold.

## Options considered

1. **More prose (CLAUDE.md restatement)** — zero build; proven not to hold; bloats every session.
2. **Inject all standing rules at session start** — reliable delivery, but constant context cost for rules that are rarely relevant. Rejected by the maintainer (context bloat).
3. **Gate that requires a recall receipt** — cheap, but gamed by an irrelevant recall.
4. **Gate that matches the action itself against rule triggers** — deterministic, zero cost unless a rule fires, model out of the loop.

## Decision

Option 4. A KB note with `rule:` + `triggers:` frontmatter is a standing rule. `meshctl hook pre-tool-use` (matcher `Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__`) flattens the tool input, matches triggers case-insensitively, and on the first match per session DENIES with the rule text + note path; the retry is allowed (ack in `.memory/rule-acks/<session>.json`). Edits under `knowledge/` and notes under `candidates/` are exempt. Fail-open on any error.

## Consequences

- Positive: rules surface at the moment of action in every agent; no session-start context cost; ~14 ms per gated call.
- Negative: catches tool calls only, not chat-only claims; recall depends on trigger quality (false negatives if a trigger is too narrow — e.g. `git\s+remote` missed `git -C <path> remote`, caught in test); a blind retry is still possible, but the rule is then in context.
- Implementation (done 2026-10-01): meshctl wiring v2; all 15 agents re-onboarded; token note converted with triggers; CLAUDE.md restatement removed as redundant.
- Update 2026-10-01 (steward rollout): the gate first matched only string values of tool_input, so no rule could target an MCP tool by name. Fixed in meshctl 36c1ee3: the tool name is prepended to the matched text. Still outside the matcher: the Artifact tool. Rollout converted rules across 11 agents; the original pool's doctrine (`.agentmesh/doctrine/agent-comms.md`) got the gated-rules section by hand-merge, because `pool upgrade` would replace it with the generic template.
- Follow-ups: convert other "standing instruction / do not repeat" notes to rules with triggers; every new standing rule is written with `rule:` + `triggers:`.

## Amendment (2026-10-16): the hook matcher never matched MCP tools

The matcher quoted in Option 4 (`…|Bash|mcp__`) is applied by Claude Code as a full regex match on the tool name, so the bare `mcp__` alternative never matched an `mcp__<server>__<tool>` name: rules were only ever checked on file-edit and shell calls. Found by the outbound-gate prototype (ADR-0068 amendment, same date). The matcher becomes `mcp__.*`; a doctor check flags the bare form; a test full-matches a sample MCP tool name.
