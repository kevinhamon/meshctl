---
status: accepted
date: 2026-07-24
---
# 0040. Tact on external human-facing comms; mesh doctrine is a default an agent's role overrides

- **Status:** accepted — **implemented** (agent-comms.md Core rules 8 + 9; template CLAUDE.md + architect CLAUDE.md tone sections; doctrine embed synced)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** companion to Core rule 7 / the no-internal-refs guardrail (rule 7 governs *content* that crosses out; this governs *tone*). Introduces a general layering precedence rule.

## Context

When agents author content on external, human-facing surfaces — issue-tracker tickets/comments,
PRs, code review, email — **especially speaking as the user**, blunt phrasing ("this is
wrong") lands badly with human readers and reflects on the user. The mesh had a content
rule (rule 7: no internal refs, product-plain) but no *tone* rule.

Separately: some meshes will exist where **communication style is the agent's primary
purpose** (e.g. academic tutors/assistants, brand-voice writers, support personas). A
one-size mesh tone default must not override such an agent's own guidance — the mesh
provides defaults, the agent's role is authoritative for its own voice.

## Decision

Two doctrine rules in `agent-comms.md`:

- **Core rule 8 — tact on external, human-facing surfaces (default).** For content a human
  will read externally, especially as the user: critique the work not the person; don't
  flatly call a solution "wrong" — frame as trade-off / risk / observation /
  recommendation; lead with what's sound. Direct on substance, diplomatic on tone. Blunt
  is fine only in internal mesh artifacts (KB/ADRs/intake) and in the user chat.
- **Core rule 9 — mesh doctrine is a set of defaults; an agent's own role guidance
  overrides it where they conflict.** An agent whose role defines a different
  communication style/standard follows *its* guidance; stated explicitly in that agent's
  `CLAUDE.md`. Layering: mesh = shared defaults + coordination protocol; the agent's
  `CLAUDE.md` is authoritative for its own domain and voice.

Surfaced in: the template `CLAUDE.md` (new agents inherit a "Communication tone" section
noting the rule-9 override), and architect's own `CLAUDE.md` (architect follows the
default). Existing agents pick it up via the canonical `agent-comms.md` they all cite.

## Consequences

- Positive: agents speaking as the user on tickets/PRs read as diplomatic, not curt —
  fewer bruised egos, better reflection on the user.
- Positive: rule 9 makes the mesh reusable for communication-first domains (academic
  assistants, writing agents) without the general-mesh defaults fighting them — a stated
  precedence, not an implicit one.
- Trade-off: tone is judgment, not mechanically enforced (unlike rule 7's ref-scrubbing);
  it relies on the model following doctrine. Acceptable — it's guidance, and rule 9 means
  it's overridable by design.
- Note: rule 9 generalizes beyond tone (mesh defaults vs agent authority) but is scoped
  here to communication; broader precedence conflicts can cite it.
