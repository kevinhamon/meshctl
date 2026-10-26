---
status: accepted
date: 2026-10-26
---
# 0075. opencode V2 promotions use opencode's own permission ask; harness parity is a tested table

- **Status:** accepted
- **Date:** 2026-10-26
- **Deciders:** maintainer (accepted 2026-10-26)
- **Related:** ADR-0032 (multi-harness adapter seam), ADR-0041 (harness-aware dispatch), ADR-0064 (agents propose, humans confirm), ADR-0067 (ledger and trust guard; amended), ADR-0070 (`kb confirm`), ADR-0072 (`inbox triage apply`), `internal/mesh/opencode_ask.go`, `internal/mesh/parity.go`

## Context

The confirmation flow (ADR-0064, ADR-0067) runs through gate asks: `kb finish` and `state set` promotions, `kb confirm`, `inbox triage apply`, writes to `pool/` and outbound writes are each asked of the human. On opencode every ask became a refusal, and the trust guard refused promotion for every opencode peer, so since the confirmation flow shipped an opencode peer could not promote knowledge at all, even when the human ran the command from a terminal. ADR-0067 had accepted that for opencode V1 only. Nothing tested the opencode path, so the gap went unnoticed for a release.

The guard also read only the workspace's wiring. opencode run inside a workspace wired for Claude Code has no mesh gate, yet the guard passed, so a promotion could be recorded as confirmed with no one asked.

Verified against opencode 2.0.28 with a scripted model and the real plugin and gate:
- A V2 plugin's pre-tool hook can let a call through or throw; it has no ask.
- opencode's permission rules (`permissions`, entries `{action, resource, effect}`) support `effect: "ask"`. An interactive session asks the human; a non-interactive run refuses ("cannot ask the user for permission").
- The plugin's `execute.before` runs before the permission ask, so a gate refusal means the human is never asked.
- The last matching rule wins. The project `opencode.json` beats the user's global config; `OPENCODE_CONFIG_CONTENT` beats the project. With no matching rule a shell call is allowed.
- A compound command is split and each simple command matched; `*` matches across spaces and flags.
- `--auto` approves an ask without a human.
- opencode exports `OPENCODE=1` and `OPENCODE_SESSION_ID` (equal to the hooks' session id) to tool commands; Claude Code exports `CLAUDECODE` and `CLAUDE_CODE_SESSION_ID`.
- Headless dispatch's permission config is honored on V2: shell and edits are refused, as on V1.

## Options considered

1. **Keep opencode unable to promote.** Rejected: an opencode pool cannot keep a confirmed KB, and the human cannot finish by hand.
2. **Let the human finish from a terminal** (guard accepts when no harness is running). Rejected as the only path: workable, but every promotion leaves the session.
3. **Use opencode's own ask for the gate-asked commands.** Chosen.

## Decision

- **Ask rules.** On opencode V2, `agent new` / `agent onboard` append to the agent's `opencode.json` one `ask` rule per gate-asked command: `meshctl kb finish *--summary-file*`, `meshctl state set *--summary-file*`, `meshctl kb confirm*`, `meshctl inbox triage apply*`. Appended last, since the last match wins. A candidates-only `kb finish` has no `--summary-file` and is not asked, as on Claude Code.
- **Gate.** Under the opencode harness a gate ask on those commands becomes an allow when opencode will ask on every invocation in the call: each must be a top-level command, written verbatim (no quoting, no `env`/`sudo` wrapper, no `bash -c`), with `meshctl` by name, and the project rules plus `OPENCODE_CONFIG_CONTENT` must leave its effect at `ask`. Otherwise the gate refuses and names the plain form and `meshctl agent onboard`. The gate's own checks (summary present and well-formed, headless refused) run first, unchanged. Asks with no opencode equivalent (writes to `pool/`, outbound writes) are refused with a reason, not the human-facing ask text.
- **Trust guard.** Refuses when the harness running the command (`CLAUDECODE` / `OPENCODE`) is not the workspace's harness. For opencode peers it requires the current V2 plugin and the ask rules in force; opencode V1 stays refused. Claude Code peers keep the existing checks. A human's own terminal sets neither marker and is checked against the workspace's wiring, as before.
- **Session id.** `OPENCODE_SESSION_ID` joins the session-id environment list, so summary paths and claims resolve to the opencode session.
- **Parity is a tested table.** `internal/mesh/parity.go` lists each feature's support per harness and classifies every meshctl command, either in a feature row or as harness-neutral. A test walks the command tree and fails on an unclassified or stale command, and another fails when the README block differs from the generated table. The release workflow runs both before building.

## Consequences

- Positive: an opencode V2 peer promotes through the same flow as a Claude Code peer, the human answering opencode's own prompt.
- Positive: a promotion run under a harness whose gate never saw it is refused, closing a path where the ledger recorded a confirmation nobody gave.
- Positive: a new command or harness-specific behaviour cannot ship without stating where it works.
- Negative: opencode's prompt shows the command, not the gate's rendered confirmation text. The confirmation round read in chat (ADR-0067 amendment) stays the reading surface; the prompt is the signature.
- Negative: `--auto`, or answering "always" in opencode's prompt, approves later promotions without asking. That is the human choosing to stop being asked; meshctl cannot see it.
- Negative: in-session peers, the outbound gate, writes to `pool/` and the after-call record remain Claude Code only, now stated in the table.
- Follow-ups: opencode V2 has a `subagent` tool, so in-session peers are a candidate for a later decision.
