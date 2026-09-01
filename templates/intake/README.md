# Intake — inbound requests to __NAME__

Inbox for requests from the user and sibling agents. Canonical protocol: `.agentmesh/doctrine/agent-comms.md`.

## Lifecycle

`pending` → `in-progress` → `answered`/`done` | `needs-info` | `declined`. The owner updates `status` in place and writes the outcome back. Don't delete — audit trail.

## Request file format

Filename `YYYY-MM-DD-<from>-<slug>.md`:

```markdown
---
id: 2026-01-01-<from>-<slug>
from: <agent|user>
to: __NAME__
type: <request-type>       # define the types this inbox accepts below
created: 2026-01-01
status: pending
priority: P1|P2|P3
related: []
response:
---

## Ask
What is needed and why.

## Context
Links, constraints, prior art.
```

## Accepted request types

- _(list the `type` values this agent handles, e.g. guidance / review / implement)_

Requests are proposals — the owner may `decline` (with reason) or ask `needs-info`.
