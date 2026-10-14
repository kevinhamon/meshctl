---
status: accepted
date: 2026-07-24
---
# 0032. Multi-harness adapter seam — skills, hooks, and injection rendered per harness

- **Status:** accepted — **implemented** (Harness seam + claude + opencode adapters; `--harness` flag + `agent.yaml` field + autodetect + doctor). opencode push-injection at session start is best-effort (see Consequences).
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** generalizes ADR-0030 (self-contained hooks, currently Claude-Code-only wiring); realizes the "surface is the only per-harness code" principle of ADR-0016; feeds ADR-0033 (memory injection is one of the rendered surfaces). Targets Claude Code + opencode first.

## Context

The mesh must run under multiple harnesses (Claude Code, opencode, Codex, bare shell) — a standing requirement since ADR-0016. Today the *core* is already harness-neutral: the `agentmesh hook …` subcommands, the KB (plain files), and the `memory`/`intuition` recall contracts. But two things are still hard-coded to Claude Code:

- **Wiring** — `.claude/settings.json` (ADR-0030's `EnsureAgentSettings`) is Claude Code's format.
- **Hook output envelope** — `EmitHookContext` emits Claude Code's `hookSpecificOutput` JSON; opencode and others expect a different shape.

Skills are also stored in Claude Code's `.claude/skills/<name>/SKILL.md` layout; another harness wants them injected in its own native form. The user's intent: **the mesh defines a skill/hook once, neutrally; the CLI injects it in whatever form the target harness wants.**

## Options considered

1. **Per-harness code sprinkled through scaffold/hook paths** — what exists for Claude Code; unmaintainable as harnesses multiply.
2. **A `Harness` adapter interface: one neutral definition, per-harness renderers (chosen).** Skills/hooks/injection stored once in neutral form; each harness adapter renders wiring, skill files, and the hook output envelope. Adding a harness = one adapter, no core change (the ADR-0016 portability rule).

## Decision

Introduce a **`Harness` adapter seam**. Everything harness-specific lives behind it; the core stays neutral.

```
type Harness interface {
    Name() string                                   // "claude" | "opencode" | …
    WireAgent(dest string) ([]string, error)        // install hooks + preapproval in the harness's config
    RenderSkill(name, body string, dest string) error   // write the skill in native form
    EmitContext(w io.Writer, event, ctx string) error   // format the hook output envelope
}
```

- **Skills** are stored **once, neutral** (name + markdown body, in embed/templates). `RenderSkill` writes the native artifact: Claude Code → `.claude/skills/<name>/SKILL.md`; opencode → its rules/skill location and format. Skills are thus **injected natively per harness**, as intended.
- **Hooks** stay the same neutral `agentmesh hook …` subcommands (ADR-0030). Only the *wiring* and the *output envelope* are per-harness: `WireAgent` writes Claude Code's `settings.json` (today's `EnsureAgentSettings`, refactored behind the seam) or opencode's plugin/hook config, each pointing at the same `agentmesh hook` subcommands; `EmitContext` formats the envelope that harness expects.
- **Selection:** `agentmesh new|onboard --harness claude|opencode` (default: autodetect from the workspace — presence of `.claude/` vs opencode config — else `claude`). The chosen harness is recorded in `agent.yaml` (`harness:` field) so later runs are consistent. The hook subcommands read it (or `--harness`) to pick `EmitContext`.
- **Delivery:** refactor the existing Claude Code behavior behind the seam (no behavior change), then implement the opencode adapter. `doctor` validates that an agent's wiring matches its declared harness.

## Consequences

- Positive: adding a harness = one adapter; the core, skills, hooks, and recall contracts never change (fulfills the ADR-0016 portability rule concretely).
- Positive: skills authored once, rendered natively everywhere — no per-harness skill duplication to drift.
- Positive: opencode pools become first-class, unlocking the mesh beyond Claude Code.
- Trade-off: `agent.yaml` gains a `harness` field and the hook path gains an envelope switch; small surface, but a migration touchpoint for existing agents (default to `claude`).
- Trade-off: neutral skill storage means the current `.claude/skills/*` templates must move to a harness-agnostic form + a Claude renderer — a refactor of ADR-0030/scaffold, not new capability.
- Migration: (1) define `Harness` + move Claude Code code behind it; (2) neutral skill store + `RenderSkill(claude)`; (3) `--harness` flag + `agent.yaml` field + autodetect; (4) opencode adapter (wiring + skill form + envelope); (5) `doctor` harness-consistency check; (6) docs. **All done.**

## Implementation notes (opencode — validated against opencode 1.18 type defs + a live envelope check)

Corrected after inspecting the installed `@opencode-ai/plugin` `.d.ts` and the real
config (earlier context7-only guesses were wrong on two points, now fixed):

- **Skills are NATIVE** in opencode ≥1.18 (discovered from `.opencode/skills/<name>/SKILL.md`,
  mirroring global `~/.config/opencode/skills/`). **No `opencode-skills` plugin** — the adapter
  no longer adds a `plugin` array. `RenderSkill` writes the same `SKILL.md` body.
- **Rules:** `opencode.json` `instructions:["CLAUDE.md"]` loads the agent's full doctrine
  (Cognition + comms stanzas + the pull commands). Reliable cognition floor.
- **Injection uses the REAL typed hooks** (not `client.session.prompt`): the local
  `.opencode/plugin/meshctl.js` implements `chat.message(input,output)` — it reads the
  user prompt from `output.parts`, shells `meshctl --harness opencode hook session-start`
  (first message of a session) + `… user-prompt` (each message), and **pushes the returned
  text as a `{type:"text"}` part into `output.parts`**. This is a stable (non-experimental)
  hook, so push injection is first-class, not best-effort.
- **Capture:** the same plugin's `event` handler fires `… hook session-end` on the
  documented `session.idle` event (session id from `event.properties.sessionID`, falling
  back to the last-seen chat.message session). Overwrite-by-id → repeated idles converge
  on one record.
- **EmitContext** is the per-harness envelope: claude → `hookSpecificOutput` JSON; opencode
  → raw text (the plugin injects it). `meshctl hook` takes `--harness` (the plugin passes
  `--harness opencode`); default resolves from the agent manifest.
- **BUGFIX (v0.4.1):** the first live opencode chat exposed that injecting via
  `chat.message`'s `output.parts` is invalid — opencode `Part`s require `id`/`sessionID`/
  `messageID`, so a synthetic `{type:"text",text}` triggers `invalid user part before
  save` → `EventV2.InvalidDurableEvent: Expected string aggregate field sessionID` and
  opencode refuses the message ("Failed to send prompt"). Fix: inject through
  **`experimental.chat.system.transform`** (`output.system` is a plain `string[]` — push a
  string, no Part schema); `chat.message` is now **read-only**, used only to capture the
  latest prompt for intuition. Also: `onboard` now **overwrites** a meshctl-managed
  `.opencode/plugin/meshctl.js` (was add-only, so it couldn't ship this fix to existing
  agents) — re-onboard upgrades them.
- **Validated:** generated plugin passes `node --check`; `meshctl --harness opencode hook
  session-start` emits raw markdown vs claude's JSON envelope (confirmed side-by-side). Live
  end-to-end under a running opencode session (plugin auto-load + `chat.message` part
  injection) is type-correct but not yet exercised with a real LLM turn — worth a smoke run
  when convenient.
