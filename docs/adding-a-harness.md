# Adding a harness

A harness is a coding agent the mesh runs inside (Claude Code, opencode). meshctl
supports a new one with three pieces of code and no edits anywhere else:

1. a **registration** (`RegisterHarness`) that says how to find, detect and wire it and
   what it can do;
2. an **adapter** (the `Harness` interface) that renders the mesh into the harness's
   native form;
3. a **bridge**, the small piece that runs inside the harness and forwards its hook
   events to `meshctl hook …`.

Core code asks a harness what it **can do** (its capabilities), never which harness it
**is**. If adding yours seems to need an edit to the gate, doctor, dispatch or upgrade
code, that is a gap in the registry, not a step: open an issue rather than adding an
`if harness == "yours"` branch.

The built-in registrations at the bottom of `internal/mesh/harness_registry.go` and the
adapters in `internal/mesh/harness.go` are the working examples.

## 1. Register it

Add one `RegisterHarness(HarnessSpec{…})` call (in an `init` function, after the
built-ins, so the existing columns keep their order):

| Field | What to put there |
|---|---|
| `ID` | The stable id stored in `agent.yaml` and pool config, lowercase (`claude`, `opencode`). Never rename it. |
| `Label` | The human name, used as the parity-table column header. |
| `Binary` | The executable that must be on `PATH` for the harness to run. |
| `RunningEnv` | Environment variables the harness exports to the tool subprocesses it starts. Any one set means "this process runs under it". |
| `WorkspaceMarkers` | Workspace-relative files or directories your wiring creates. Their presence is how meshctl knows a workspace is wired for you. They must not overlap another harness's markers. |
| `Variants` | Only when release lines differ in wiring or capabilities (opencode 1.x vs 2.x): one entry per line, newest first, each with its column label. Leave nil otherwise. |
| `InstalledVariant` | Probes the installed binary: `(variant, true)` when installed, variant `0` without variants. It may spawn a process; meshctl calls it only at wire, upgrade and doctor time. |
| `New` | Builds the adapter for a variant (`0` means detect from the installed binary). |
| `Tools` | Your tool names mapped to neutral kinds: `ToolWrite`, `ToolEdit`, `ToolPatch`, `ToolShell`, `ToolRead`, `ToolFetch`, `ToolSubagent`. |
| `PathKeys` | The tool-input keys that carry a write or edit target path, in the order to try them. |

`Tools` and `PathKeys` are what the pre-tool gate reads. A tool missing from `Tools` is
not a write, edit, patch or shell call as far as the gate is concerned, so map every tool
that can change a file or run a command. A name may appear in more than one harness only
if it means the same kind in each; the conformance suite fails a registration that maps
one name to two kinds.

## 2. Write the adapter

Implement the `Harness` interface (`internal/mesh/harness.go`):

| Method | Contract |
|---|---|
| `Name()` | Returns the registered `ID`. |
| `Variant()` | The release line this adapter renders for (`0` without variants). |
| `Caps()` | What this harness (variant) can do; see below. |
| `WireAgent(dest)` | Installs the mesh integration into the workspace: hook wiring, the bridge, preapproval rules. Add-only and idempotent: a second run returns no notes. It must create at least one of your `WorkspaceMarkers` and none of another harness's. |
| `RenderSkill(name, body, dest)` | Writes one shared skill where your harness discovers skills. Re-rendering the same body is a no-op, and other files are left alone. |
| `SkillDir(dest, name)` | Where that skill lives, inside the workspace. |
| `EmitContext(w, event, ctx)` | Writes injected context in the form your bridge consumes. Empty context writes nothing. |
| `EmitGate(w, decision)` | Writes a gate decision in the form your bridge consumes. Allow writes nothing; deny and ask write something. A harness that cannot put an ask to the human writes an ask as a deny. |
| `DistillCmd(prompt)` | The argv of one background distillation run, or nil when distillation runs inside the session. |

### Capabilities

`HarnessCaps` lists every surface the mesh uses. Declare only what your harness really
has; the mesh degrades each missing one explicitly.

| Capability | Meaning | Without it |
|---|---|---|
| `PreToolGate` | A pre-tool hook the gate runs in; a deny blocks the call. | No enforcement; doctrine is advisory. |
| `GateAsk` | The gate can put any question to the human. | Outbound writes and agent writes to `pool/` are refused. |
| `PromotionAsk` | The promotion commands are asked before they run. | Promotions are refused. |
| `PostToolHook` | An after-call hook. | No pool commits or outbound acks after a call. |
| `SessionEndEvent` | The harness signals the end of a session. | meshctl closes a quiet session after an idle window. |
| `InSessionPeers` | A subagent tool that runs peer definitions under the peer gate. | `ask --in-session` is refused. |
| `InSessionDistill` | A background in-session subagent runs the distiller. | Distillation runs as a separate process (`DistillCmd`). |
| `StatusLine` | A status surface meshctl feeds. | No status line. |

The conformance suite checks that `GateAsk` and `PromotionAsk` come with `PreToolGate`,
and that `InSessionDistill` holds exactly when `DistillCmd` is nil.

## 3. Write the bridge

The bridge runs inside the harness (a hook entry in its settings, or a plugin) and only
translates: it turns each harness event into a JSON payload on the stdin of
`meshctl hook <event> --harness <id>` and hands the output back to the harness. Keep logic
out of it; session bookkeeping and decisions belong in meshctl, where every harness shares
them.

- Always pass `--harness <id>`. The environment marker is only a fallback, because a
  harness started from inside another inherits its parent's marker.
- Send `cwd`, `session_id` and, for tool calls, `tool_name` and `tool_input` as the
  harness gives them. The gate matches `tool_name` through your `Tools` map and reads the
  target path through your `PathKeys`.
- The pre-tool hook is fail-closed: anything `EmitGate` writes means "block". If the
  bridge cannot reach meshctl, it should block too.

## 4. Fill in the parity table

`internal/mesh/parity.go` holds the feature × harness table rendered into the README.
Its columns come from the registry, so your registration adds a column (one per variant),
and a test fails until every row has a cell for it, keyed by the column key (`<id>`, or
`<id>@<variant>`). Start each cell with ✅, ✗ or a plain word such as `untested`, and say
how it works on your harness. Rows tied to a capability must agree with your `Caps()`:
✅ needs the capability, ✗ must lack it. Then regenerate the README block between the
parity markers; the test prints the expected block when it is out of date.

## 5. Run the conformance suite

`internal/mesh/harness_conformance_test.go` runs the same contract against every
registered harness and every variant: registration fields, wiring (a marker created, only
yours, idempotent), skills, context and gate output, and capability consistency. It also
registers a fake harness that exists only in the test and checks that detection,
resolution, the running-harness marker and the gate handle it with no core change. If
your harness needs a core change the fake does not, add the case to the fake first.

```sh
go test ./internal/mesh -run 'TestHarnessConformance|TestGate_'
go test ./cmd/meshctl -run TestHarnessParity
```

## 6. Smoke-test it live

Unit tests do not start the harness. Before a release, check the real thing in a
throwaway sandbox, never against your own pools or installed meshctl:

1. Build meshctl into a temp directory and put it first on `PATH`.
2. Point `HOME` and the `XDG_*` directories at a temp directory, so the pool registry
   and settings are throwaway.
3. `meshctl pool init <tmp>/pool --no-steward`, then
   `meshctl agent new smoke --bare --harness <id> --accepts "question:a question"`.
4. Check that your workspace markers exist and `meshctl doctor` reports no errors.
5. From the agent workspace, pipe synthetic payloads into
   `meshctl hook session-start --harness <id>` (context comes back) and
   `meshctl hook pre-tool-use --harness <id>` (a write to `intake/` is blocked, an
   ordinary write passes, a payload that is not JSON is blocked).

Do not run `hook session-end` there: it starts a distillation run. The maintainers' tree
has a script that does all of this for a harness id; add your harness's profile to it.
Then, if you can, run one real session with a scripted model and watch a gated write get
blocked.
