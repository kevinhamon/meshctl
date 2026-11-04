# Grants: letting a headless run act

By default a peer with no human at its prompt can read, think and answer, but it cannot
take any action that needs a human's confirmation. It parks the work as `needs-confirm`
and waits for its own interactive session. A **grant** is a line in the pool config that
lets one peer, or the pool as a whole, take one of those actions without a human. Grants
are opt-in. With no `grants:` block, nothing changes.

## When a grant applies

A grant applies only when **no human is at the prompt** and meshctl can see that:

- a dispatched run (`meshctl ask`, or any run where `AGENT_DISPATCH_DEPTH` is set and non-zero);
- a distil run;
- `pool upgrade --headless` (and `kb finish --headless`).

Whether the action can actually run also depends on the tools that headless process has:

- **`pool upgrade --headless`** runs entirely in meshctl, so the `pool-upgrade` grant
  works on its own.
- **A peer dispatched by `meshctl ask`** currently gets a fixed tool set: reads plus the
  meshctl commands it needs to answer. That covers none of the granted actions, so in an
  `ask` run a grant only lets a peer that `mutates` something be dispatched at all.
- **A distil run** can write candidates only.
- **A headless run you start yourself** (with `AGENT_DISPATCH_DEPTH` set) can take a
  granted action when its harness permissions allow the command or tool involved.

A grant never applies in these cases:

- **An interactive session.** The gate asks exactly as it always has.
- **In-session consult and delegated work** (`ask --in-session`, `--work`). The command
  running inside them cannot tell that no human is present, so it could not record the
  action as a policy action. These keep the old rule.

## Setting grants

Grants live in `pool/config.yaml`, never in a peer's `agent.yaml`. Every write to the pool
config is asked of a human, so a peer cannot grant itself anything.

```yaml
grants:
  paused: false                 # true switches every grant off
  pool:
    pool-upgrade: {}            # pool-wide
  peers:
    <peer>:
      kb-promote: { max_per_day: 10 }
      state-set: {}
      kb-confirm: {}
      triage-apply: {}
      "outbound:<surface>": {}  # one per surface; the surface must be declared under `outbound:`
```

`{}` means no daily cap. `meshctl doctor` checks the block and reports:

- an unknown grant name;
- a grant for a peer that isn't in the pool;
- an `outbound:` grant for a surface that isn't declared;
- a negative cap;
- a dispatchable peer that `mutates` a surface it has no grant for.

## What each grant lets through

| Grant | Lets through, with no human | Still required |
|---|---|---|
| `kb-promote` | `kb finish` promoting knowledge out of `candidates/` | the summary file; the entry is marked `by: policy` |
| `state-set` | `state set`, the current-state digest | the summary file |
| `kb-confirm` | `kb confirm` of up to five entries a reconcile report reviewed | the report; `--accept-ungrounded` is refused, since overriding an ungrounded verdict stays a human call |
| `triage-apply` | `inbox triage apply` closing requests the triage pass judged closed | the triage report |
| `outbound:<surface>` | a write to that outbound surface; also lets a dispatchable peer that `mutates` that surface be dispatched at all | a grant for **every** surface the peer mutates, or it is not dispatched |
| `pool-upgrade` (pool-wide) | `meshctl pool upgrade --headless` | a clean `pool upgrade --check`: any doctrine conflict or warning sends it back to a human, and nothing is changed |

Knowledge confirmed under a grant is kept apart from human confirmations. The ledger
entry says `by: policy` and names the grant, the `Confirm:` commit carries a
`Confirmed-by: policy (grant …)` line, and recall shows the entry as `confirmed·policy`,
ranked below human-confirmed knowledge. A later `kb confirm` in an interactive session
makes it a human confirmation.

## Seeing what grants did

- **The policy log.** Every action a grant lets through writes one JSON line to
  `pool/.memory/policy.log`: time, peer (`pool` for pool-wide grants), grant, what was
  done, and the session. The log is machine-local and is not committed. An outbound
  write that can't be logged is refused, so no write happens without a record.
- **Session start.** A peer's next interactive session opens with a `# Grant audit`
  section listing every action grants let through for that peer, plus pool-wide ones,
  since its last interactive session. The agent is told to read it out to you. Up to 20
  rows are shown; for older ones it points to the log.
- **The deny message.** When a peer holds a grant that doesn't apply right now, because
  grants are paused or the cap is reached, the usual deny gains a line saying why.

## Limits and switching off

- **Daily cap.** `max_per_day` counts that peer's actions under that grant on the
  current **UTC** day. Once it is reached, the action waits for a human until the next
  UTC day.
- **Pause.** `paused: true` switches every grant off. Headless runs then wait for a
  human, peers that need an outbound grant are not dispatched, the status line ends in
  red `[grants: paused]`, and every session start says grants are paused.
- **Revoke.** Remove the grant from `pool/config.yaml`. The config is read on every
  check, so the change applies from the next action. Runs already in progress are not
  stopped.
- **One action per grant, no chaining.** A grant covers one action. A dispatched peer
  still may not dispatch another peer.
- **The real boundary is the environment.** meshctl decides from the grant name and the
  pool config. It does not judge content, and `from` and the request type are
  self-declared. What a headless process can actually reach is set by where and how you
  run it, such as its filesystem mounts and credentials. Grant accordingly.
