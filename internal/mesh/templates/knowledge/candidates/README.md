# Candidates

Holding area for proposed learning awaiting consolidation — the KB's inbox for
*knowledge* (not to be confused with the mesh `intake/`, which is cross-agent
messages). One file per candidate (`kebab-title.md`).

A candidate is a not-yet-vetted note: a possible playbook, a suspected failure
mode, a fact to confirm. Land new learnings here via `meshctl kb begin`; when
one is verified and durable, consolidate it into the right area
(`playbooks/`, `experience/`, a decision, `glossary.md`, …) and remove/supersede
the candidate — an ordinary `meshctl kb finish` commit.

`meshctl doctor` warns on any candidate older than 14 days; `meshctl kb consolidate`
lists them with the rest of the KB's debt.
