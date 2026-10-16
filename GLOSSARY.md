# meshctl

A coordinator for a pool of specialist agents, each holding one role's judgment and curated knowledge, so one person can run the work of a small team.

## Language

**Pool**:
The set of peers that coordinate with each other, plus the rules they share.
_Avoid_: mesh (as a noun for the set), workspace

**Peer**:
One specialist agent in a pool, owning a role and the knowledge base that role curates.
_Avoid_: agent (when the pool member is meant), bot, worker

**Knowledge base** (KB):
A peer's curated, role-scoped knowledge of the ecosystem: its glossary, decisions, playbooks, and experience.
_Avoid_: memory, notes, docs

**Ecosystem**:
The whole body of separate, reusable building blocks (many repos) that a pool's knowledge spans, as opposed to any single codebase.
_Avoid_: monorepo, the codebase, the project

**Pool glossary**:
The one shared vocabulary every peer in a pool uses, owned by the pool rather than any single peer's knowledge base.
_Avoid_: per-agent glossary, dictionary

**Pool knowledge**:
What the pool itself owns rather than any peer: the pool glossary, the pool's local doctrine, and pool-wide settings. Its fact owner is the pool.
_Avoid_: pool config, shared files, mesh-level knowledge

**Confirmed knowledge**:
A knowledge base entry whose specific content the human has agreed to; anything else in a knowledge base is a hypothesis, not ground truth.
_Avoid_: approved (approving a session is not confirming its content), verified

**Promotion**:
A knowledge base write that lands content where recall treats it as truth (the current or log tier), rather than in the candidates inbox; the moment human confirmation is required.
_Avoid_: beyond candidates, merge, landing

**Canonical knowledge**:
Knowledge a peer holds as part of the pool's one shared truth: it must not contradict any other peer's canonical knowledge.
_Avoid_: shared knowledge, global knowledge

**Perspective knowledge**:
Knowledge a peer holds as its own viewpoint (a character's beliefs, a role's opinion of the world), free to contradict other peers and never checked against them.
_Avoid_: private knowledge, opinion

**Fact conflict**:
Two canonical entries asserting incompatible states of the same referent, which the ecosystem or the record can settle; the only kind of disagreement the pool flags.
_Avoid_: contradiction (too broad), inconsistency

**Judgment difference**:
Two peers evaluating, ranking, or recommending differently; nothing settles it, so it is never flagged.
_Avoid_: opinion conflict, disagreement

**Fact owner**:
The one peer whose knowledge base holds a given canonical fact; other peers reference its entry instead of keeping a copy.
_Avoid_: source of truth (too vague), master

**Knowledge reference**:
A pointer from one peer's knowledge base entry to the fact owner's entry, in place of a copy; any gist it carries is a convenience, never confirmed knowledge.
_Avoid_: copy, cross-link, mirror

**Shared understanding**:
The state in which the human has seen and agreed to a peer's intent, the grounding of its claims, and the precedent for its proposed pattern, before the peer acts.
_Avoid_: alignment, sign-off, approval

**Precedent**:
Where a proposed pattern already exists in the ecosystem, in confirmed knowledge, or in pool doctrine; a pattern with none is new and must say so.
_Avoid_: prior art, example

**Outbound write**:
A change to a surface outside the pool that other people treat as true, such as a tracker item, a wiki page, or a product repository's remote.
_Avoid_: external write, push, side effect

**Outbound surface**:
One system outside the pool that the pool names so writes to it can be recognised and confirmed before they happen.
_Avoid_: integration, tool, target system

**Skill pack**:
An optional, externally maintained set of skills that a pool may adopt and assign to peers; meshctl works with any pack or none.
_Avoid_: plugin (the distribution mechanism, not the concept), toolkit
