# Design decisions

Architecture decision records (ADRs) for meshctl and the agent-mesh design.
Each record captures the context, the options considered, the decision, and its
consequences at the time it was made. Later records update or supersede earlier
ones rather than rewriting them, so read a record together with the ones it links.

**Numbering is historical and has gaps.** These records were originally kept in a
larger decision log alongside unrelated product decisions; the mesh-related ones
were extracted here with their original numbers so existing cross-references stay
valid. Missing numbers are not lost meshctl decisions. Older records also use the
CLI's earlier name, `agentmesh` (renamed `meshctl` in [0034](0034-rename-meshctl-noun-verb-cli.md)).

**New ADRs continue from 0069.** Copy [template.md](template.md) to
`NNNN-short-slug.md`, fill it in, and add a row to the index below.

## Index

| ADR | Title | Status |
|---|---|---|
| [0010](0010-agent-mesh-cli-and-manifest.md) | Agent-mesh: `agentmesh` Go CLI, self-describing manifests, batch dispatch | accepted |
| [0011](0011-agent-mesh-concurrency-and-claims.md) | Agent-mesh concurrency: intake claims, session presence, KB branch-merge | accepted |
| [0014](0014-agent-knowledge-and-memory-layers.md) | Agent knowledge base structure + disposable memory layer | accepted |
| [0015](0015-agent-memory-index-schema-and-cli.md) | Agent memory: index schema, topic extraction, and `memory` CLI | accepted |
| [0016](0016-claude-mem-to-kb-distillation-bridge.md) | Agent memory as engine / contract / surface — two tiers, portable across harnesses | accepted, partially superseded by 0033 |
| [0017](0017-session-handoff-resumable-state.md) | Session handoff — an authoritative, resumable "where we are + next step" | accepted |
| [0018](0018-cross-agent-learning-capture.md) | Cross-agent learning capture — dispatched exchanges land in the owner's candidates/ | accepted |
| [0019](0019-external-reference-corpus.md) | External reference corpus → distilled `reference/` KB (expert priming) | accepted |
| [0026](0026-mesh-cognition-skill-plus-standing-order.md) | Mesh cognition delivered as a skill + always-present CLAUDE.md standing order | accepted |
| [0027](0027-background-distiller.md) | Background distiller — off-thread learning, surface only decisions | accepted |
| [0029](0029-agentmesh-multi-pool.md) | Multiple isolated mesh pools; `agentmesh init --pool` | accepted |
| [0030](0030-self-contained-hooks-in-binary.md) | Claude Code hooks fold into the `agentmesh` binary; wired per-agent | accepted |
| [0031](0031-binary-native-pool-init.md) | Pools are created by the binary, not by cloning a doctrine repo | accepted |
| [0032](0032-multi-harness-adapter-seam.md) | Multi-harness adapter seam — skills, hooks, and injection rendered per harness | accepted |
| [0033](0033-owned-vertical-memory-system.md) | Owned, vertically-integrated agent memory — KB + episodic + recall as one system; retire the claude-mem dependency | accepted (claude-mem retired 2026-10-26) |
| [0034](0034-rename-meshctl-noun-verb-cli.md) | CLI renamed `meshctl`; noun-verb command groups; repo home on gitea | accepted |
| [0035](0035-mesh-steward-onboarding-hub.md) | Mesh steward — an interview-and-build onboarding hub agent | accepted |
| [0036](0036-installable-module-path-release-workflow.md) | Installable module path + Gitea release workflow (cross-platform binaries) | accepted |
| [0037](0037-pool-registry-and-init-cwd.md) | Machine-level pool registry (discovery-only) + `pool init` targets cwd | accepted |
| [0038](0038-polyrepo-native-layout.md) | Poly-repo-native layout: infra under `.agentmesh/`, agents as `*-agent` repos | accepted |
| [0039](0039-agent-repo-hygiene.md) | Agent repo hygiene: `agent new` inits a repo; persistent nag until a remote exists | accepted |
| [0040](0040-external-communication-tone-and-doctrine-precedence.md) | Tact on external human-facing comms; mesh doctrine is a default an agent's role overrides | accepted |
| [0041](0041-harness-aware-dispatch.md) | Headless dispatch (`meshctl ask`) picks the runner by the target's harness | accepted |
| [0042](0042-tool-reference-layer.md) | Tool reference — a capability-awareness layer (pool + agent `tools:`) | accepted |
| [0043](0043-intake-lifecycle.md) | Intake is an ephemeral discussion: git-ignored, distilled to the KB, then archived | accepted |
| [0062](0062-kb-rule-gate.md) | Standing rules are enforced by a KB-rule gate, not by prose | accepted |
| [0063](0063-hub-session-delegated-peer-work.md) | Hub session: peers do delegated work in-session, in their own repos | accepted |
| [0064](0064-agents-propose-humans-confirm.md) | Agents propose, humans confirm: KB truth needs human confirmation, enforced at `kb finish` | accepted |
| [0065](0065-one-fact-owner-with-references.md) | One fact owner per canonical fact; other peers hold references, and recall follows them one hop | accepted |
| [0066](0066-pool-knowledge-lives-in-a-pool-repo.md) | Pool knowledge lives in a visible `pool/` git repo; meshctl commits, humans push | accepted |
| [0067](0067-confirmed-knowledge-is-a-content-hash-ledger.md) | Confirmed knowledge is a content-hash ledger written at promotion; the gate asks on `kb finish` and `state set` | accepted |
| [0068](0068-outbound-writes-are-gate-asked-per-batch.md) | Outbound writes are gate-asked once per summary; surfaces are pool config keyed by `mutates` | accepted |
| [0069](0069-knowledge-kind-per-peer-and-cross-peer-recall.md) | Knowledge kind is declared per peer; canonical peers recall each other and classify conflicts at promotion | accepted |
| [0073](0073-intake-order-and-decision-rounds.md) | "check intake" shows the queue and waits for the human's order; decisions are asked as decision rounds | accepted |
| [0074](0074-status-line-is-a-hook-subcommand.md) | The status line is a meshctl hook subcommand, wired per agent and refreshed on a timer | accepted |
| [0075](0075-opencode-v2-promotions-use-opencodes-own-ask.md) | opencode V2 promotions use opencode's own permission ask; harness parity is a tested table | accepted |
| [0076](0076-opencode-in-session-peers.md) | opencode V2 in-session peers: rendered subagents, one plugin maps child sessions to the peer gate | accepted |
| [0077](0077-mesh-console.md) | The mesh console: a live, read-only view of the pool served by meshctl | accepted |
