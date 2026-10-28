---
status: accepted
date: 2026-10-27
---
# 0078. Agent documents: a `docs/` folder in the workspace, shown sandboxed in the console

- **Status:** accepted
- **Date:** 2026-10-27
- **Deciders:** maintainer (accepted 2026-10-27)
- **Related:** ADR-0014 (knowledge base areas), ADR-0039 (repo hygiene), ADR-0063 (`workdirs`), ADR-0077 (mesh console), `internal/mesh/docs.go`, `internal/console/docs.go`, `internal/console/agentview.go`

## Context

Agents write documents meant for a person to read: designs written before a piece of work, often markdown with mermaid diagrams; reports; briefs; mockups. meshctl scaffolded only `knowledge/` and `intake/`, so these landed wherever the agent chose. Surveyed across several pools on one machine (28 agents, 2026-10-27): nine different folders (`reports/`, `mockups/`, `screenshots/`, `docs/`, `deliverables/`, `plans/`, `briefs/`, `files/`), in md, html, pdf, png and xlsx, some tracked by git and some ignored. Nothing kept a design or report out of a repo the agent works on, where it can be committed and pushed by accident. The person could read them only from a terminal or file browser, and mermaid rendered only where some viewer happened to support it.

Verified while building (2026-10-27, Chrome): a document framed with `sandbox` and no `allow-same-origin` loads with the console cookie, but every request it makes (its images, scripts, the viewer's libraries) is cross-site and carries no `SameSite=Strict` cookie; inside it, reading `document.cookie` throws and `fetch("/api/...")` fails. Chrome will not render a PDF in a sandboxed frame. Markdown source embedded as HTML-escaped text breaks mermaid (it sees `&gt;`); embedded as a JSON string literal it renders.

## Options considered

1. **Leave folders to each agent; the console browses the whole workspace.** Rejected: the workspace holds runtime state and the KB, and nothing would steer documents out of work repos.
2. **A fixed folder and nothing else.** Rejected: agents with existing folders would have to move files.
3. **One default folder, plus folders an agent declares, stated in doctrine; the console lists and renders them sandboxed.** Chosen.

## Decision

- **`docs/` in each workspace.** Documents written for the person go in `docs/` (scaffolded add-only with a README), never in a repo the agent works on. Names are `YYYY-MM-DD-<slug>.<ext>`; designs are markdown with diagrams as mermaid fences. `docs/` is committed with the workspace; `docs/local/` is in the meshctl-managed ignore block, for documents that must never leave the machine (onboard adds the line to existing blocks). A document is not knowledge: what should last goes through `candidates/`.
- **Declared folders.** `agent.yaml` `docs:` lists workspace-relative folders (default `[docs]`) so existing folders count without moving files. Validate refuses absolute or escaping entries; doctor warns when a declared folder is missing or a symlink leads it out of the workspace.
- **Stated where agents read.** The managed CLAUDE.md stanza, the `mesh-cognition` skill and the doctrine carry the rule; `pool upgrade` re-onboards every peer, so all receive it. No new command or phrase.
- **Listed and rendered in the console.** The agent page lists documents newest first (at most 200; over 10 MiB listed, not rendered) and renders markdown with mermaid, HTML, PDF, images and plain text. It also shows the knowledge base by area, `state.md` first, recent commits and recently closed requests. The list follows changes live.
- **Sandboxed, by capability.** A document is untrusted code. It is served from `/c/<cap>/<pool>/<agent>/<path>`, where the capability is an HMAC over pool, agent and folder under a key that lives only as long as the console process; it admits that one folder, so relative assets load, and needs no token. Every response except a real PDF carries `Content-Security-Policy: sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox` (no `allow-same-origin`), `nosniff` and `Referrer-Policy: no-referrer`. A file is served as a PDF only when its name ends `.pdf` and it starts with `%PDF-`. Paths are cleaned and symlinks resolved; nothing outside the folder is served. Markdown renders inside the sandbox from a small page that embeds the source as a JSON string literal.
- **Viewer libraries in the binary.** marked 18.1.1 and mermaid 12.2.0 (MIT, licenses in `NOTICE`) are embedded and served token-free from `/vendor/`; mermaid is stored gzip-compressed (about 1.6 MB). Rendering needs no network.

## Consequences

- Positive: the person reads every agent's designs and reports in one place, diagrams rendered, without opening a terminal; documents stop landing in work repos.
- Positive: the agent page shows the agent's knowledge and recent history, not only what waits.
- Negative: the binary grows by about 1.7 MB.
- Negative: a capability path works for anyone who learns it, until the console restarts. It reaches the page only through the guarded API and is never sent as a referrer.
- Negative: a PDF renders unsandboxed in the browser's own viewer, trusted to keep its scripts from the page.
- Negative: existing workspaces show `docs/` and the ignore line as uncommitted changes after `pool upgrade`, like any onboard change.
- Follow-ups: searching document contents; a gate rule if agents keep writing documents into work repos despite the doctrine.
