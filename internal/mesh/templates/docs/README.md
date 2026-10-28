# Documents

What this agent writes for the person to read: designs written before a piece of work,
reports, briefs, mockups. They live here, in the agent's own workspace, never in a repo
the agent works on.

- Name each `YYYY-MM-DD-<slug>.<ext>`.
- Write designs in markdown; draw diagrams in `mermaid` code fences.
- `local/` is ignored by git: put documents there that must never leave this machine.
- A document is not knowledge. What it teaches that should last goes through
  `knowledge/candidates/`.

`meshctl console` lists and renders these (markdown with mermaid, HTML, PDF, images),
read-only and sandboxed.
