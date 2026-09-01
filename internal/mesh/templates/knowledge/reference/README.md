# Reference

Distilled, **cited** knowledge from external source-of-record documents (ADR-0019) — standards,
protocols, vendor/ISO specs, tariffs, manuals. One note per topic (`kebab-title.md`).

This is the deliberate tier over a **disposable, gitignored corpus**: raw fetched docs stay out of
git (big, versioned, re-fetchable); only distilled, cited notes are committed and indexed by
`memory recall`. Optional area — a fresh or non-doc-heavy agent legitimately has none.

A reference note is **not** an `experience/` lesson and **not** an internal system fact — it is an
external authoritative rule you operate against, so it carries a citation and, critically, an
**effective/version date**.

## Mandatory frontmatter (provenance)

```yaml
---
source: "<document + section>"     # e.g. "Vendor API Reference §4.4.9"
effective: YYYY-MM-DD              # version / effective date — REQUIRED
url: <public origin>               # re-fetch key
tags: [...]
---
```

External sources are versioned; without `effective:` a note silently goes stale, so
`meshctl doctor` warns on any reference note missing it. Populate via a priming workflow
(read the corpus → distill cited notes → `meshctl kb finish`).
