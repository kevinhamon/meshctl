# Pool glossary

The one vocabulary every peer in this pool uses. Each entry is a `**Term**:` line
with a plain-words definition and an optional `_Avoid_:` line; `##` groups are
fine; no implementation detail inside an entry. Every term is a canonical fact
owned by `pool`, referenced as `mesh://pool/glossary.md#<term-slug>`. Agents
propose changes here through the gate ask; the human confirms, meshctl commits.
