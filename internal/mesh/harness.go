package mesh

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Harness is the per-coding-agent adapter seam. Skills and hooks are
// defined once, harness-neutral; each Harness renders them into the form its
// target expects. Adding a harness = one implementation here — the mesh core,
// the recall/messaging contracts, and the `meshctl hook` subcommands never change.
type Harness interface {
	// Name is the harness id stored in agent.yaml (claude | opencode).
	Name() string
	// WireAgent installs the mesh integration (hooks + preapproval, or the
	// harness equivalent) into the workspace. Add-only + idempotent; returns
	// human-readable notes for what it added (empty ⇒ nothing).
	WireAgent(dest string) ([]string, error)
	// RenderSkill writes one shared skill into the harness's native location.
	// A same-content copy is a no-op; a differing mesh-owned copy is replaced;
	// files beside the skill are never touched.
	RenderSkill(name, body, dest string) (bool, error)
	// SkillDir is where the harness discovers skill name in workspace dest.
	SkillDir(dest, name string) string
	// EmitContext formats a hook's injected context for this harness and writes
	// it to w. Empty ctx ⇒ nothing written.
	EmitContext(w io.Writer, event, ctx string) error
	// DistillCmd returns the argv that runs one background distillation with
	// the given prompt, or nil if this harness has no background primitive of
	// its own and relies on the in-session boundary nudge instead. This is the
	// binding distiller.md leaves to each harness.
	DistillCmd(prompt string) []string
	// EmitGate writes a pre-tool-use gate decision in the form this harness's
	// hook bridge consumes. Allow ⇒ nothing written.
	EmitGate(w io.Writer, d GateDecision) error
	// Variant is the release line this adapter renders for (0 ⇒ the harness
	// has no variants).
	Variant() int
	// Caps is what this harness variant can do. Core code branches on caps,
	// never on Name().
	Caps() HarnessCaps
}

const (
	HarnessClaude   = "claude"
	HarnessOpencode = "opencode"
)

// HarnessByName returns the adapter for a harness id. Empty or unknown ⇒
// DefaultHarness; input boundaries validate with ResolveHarness first.
// A variant harness resolves its variant lazily from the installed binary.
func HarnessByName(name string) Harness { return HarnessByNameVersion(name, 0) }

// HarnessByNameVersion is HarnessByName with the variant pinned (opencode: the
// major, 1 or 2); 0 ⇒ detect from the installed binary.
func HarnessByNameVersion(name string, variant int) Harness {
	if h, err := ResolveHarness(name, variant); err == nil {
		return h
	}
	h, _ := ResolveHarness(DefaultHarness, 0)
	return h
}

// HarnessFor returns the adapter an agent runs under.
func HarnessFor(a *Agent) Harness { return HarnessByName(a.HarnessName()) }

// --- Claude Code ---

type claudeHarness struct{}

func (claudeHarness) Name() string { return HarnessClaude }

func (claudeHarness) Variant() int { return 0 }

// Caps: Claude Code has every surface the mesh uses.
func (claudeHarness) Caps() HarnessCaps {
	return HarnessCaps{
		PreToolGate: true, GateAsk: true, PromotionAsk: true, PostToolHook: true,
		SessionEndEvent: true, InSessionPeers: true, InSessionDistill: true, StatusLine: true,
	}
}

// WireAgent = the per-agent settings.json wiring (hooks + preapproval).
func (claudeHarness) WireAgent(dest string) ([]string, error) {
	notes, err := EnsureAgentSettings(dest)
	if err != nil {
		return notes, err
	}
	n, err := ensureDistillerAgent(dest)
	notes = append(notes, n...)
	if err != nil {
		return notes, err
	}
	// In-session peers: one subagent definition per dispatchable
	// peer, plus pool-root read access for the caller's session. The pool root
	// is the workspace's parent (agents are direct children of the pool).
	poolRoot := filepath.Dir(dest)
	n, err = ensurePeerAgents(poolRoot, dest)
	notes = append(notes, n...)
	if err != nil {
		return notes, err
	}
	if IsPool(poolRoot) {
		note, err := ensurePoolAccess(poolRoot, dest)
		if err != nil {
			return notes, err
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	return notes, nil
}

// distillerAgentMarker tags the meshctl-managed distiller subagent so upgrades
// replace only copies meshctl wrote; a hand-authored distiller is left alone.
const distillerAgentMarker = "<!-- meshctl-managed:"

// ensureDistillerAgent writes .claude/agents/distiller.md — the Claude Code
// binding of the harness-neutral distiller procedure that the boundary nudge
// asks the model to spawn. Without it the nudge names a subagent that does not
// exist. Managed: written when absent, refreshed when meshctl's copy changed,
// never overwrites a file without the marker.
func ensureDistillerAgent(dest string) ([]string, error) {
	tpl, err := templatesFS.ReadFile("templates/claude/distiller.md")
	if err != nil {
		return nil, err
	}
	body := renderPoolTokens(filepath.Dir(dest), strings.ReplaceAll(string(tpl), "__NAME__", filepath.Base(dest)))
	out := filepath.Join(dest, ".claude", "agents", "distiller.md")
	if b, err := os.ReadFile(out); err == nil {
		cur := string(b)
		if !strings.Contains(cur, distillerAgentMarker) {
			return nil, nil // hand-authored distiller — keep it
		}
		if cur == body {
			return nil, nil
		}
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			return nil, err
		}
		return []string{"updated .claude/agents/distiller.md"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return []string{"wrote .claude/agents/distiller.md (background distiller subagent)"}, nil
}

// RenderSkill writes .claude/skills/<name>/SKILL.md (add-only).
func (claudeHarness) SkillDir(dest, name string) string {
	return filepath.Join(dest, ".claude", "skills", name)
}

func (h claudeHarness) RenderSkill(name, body, dest string) (bool, error) {
	out := filepath.Join(h.SkillDir(dest, name), "SKILL.md")
	return writeSkillFile(out, body)
}

// EmitContext = Claude Code's hookSpecificOutput JSON envelope.
func (claudeHarness) EmitContext(w io.Writer, event, ctx string) error {
	if strings.TrimSpace(ctx) == "" {
		return nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": ctx,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// DistillCmd: nil. Claude Code's background primitive is an in-session
// subagent (Agent tool, run_in_background), which the boundary nudge triggers.
// Spawning a second `claude` process would duplicate that work and burn a
// second context for it.
func (claudeHarness) DistillCmd(string) []string { return nil }

// EmitGate = Claude Code's PreToolUse permission-decision envelope.
func (claudeHarness) EmitGate(w io.Writer, d GateDecision) error { return EmitGateDecision(w, d) }

// --- opencode ---

// opencodeHarness covers both opencode release lines (see opencode_v2.go).
// major pins the line; 0 ⇒ detect from the installed binary.
type opencodeHarness struct{ major int }

func (opencodeHarness) Name() string { return HarnessOpencode }

func (h opencodeHarness) Variant() int { return h.Major() }

// Caps: V2 has the pre-tool gate (plugin tool.execute.before), opencode's own
// permission ask for the promotion commands, subagents and a TUI status panel;
// it has no arbitrary gate ask, no after-call hook and no session-end event
// (an idle window closes sessions). V1 has none of the gate surfaces.
func (h opencodeHarness) Caps() HarnessCaps {
	if h.Major() >= 2 {
		return HarnessCaps{PreToolGate: true, NativeAsk: true, PromotionAsk: true, InSessionPeers: true, StatusLine: true}
	}
	return HarnessCaps{}
}

// Major is the opencode major this adapter renders for: the pinned value, else
// the installed binary's, else 2 (the current line) when opencode is absent.
func (h opencodeHarness) Major() int {
	if h.major > 0 {
		return h.major
	}
	if m := InstalledOpencodeMajor(); m > 0 {
		return m
	}
	return 2
}

// RenderSkill writes .opencode/skills/<name>/SKILL.md — the Anthropic Agent
// Skills spec form the opencode-skills plugin discovers (same body as Claude).
func (opencodeHarness) SkillDir(dest, name string) string {
	return filepath.Join(dest, ".opencode", "skills", name)
}

func (h opencodeHarness) RenderSkill(name, body, dest string) (bool, error) {
	out := filepath.Join(h.SkillDir(dest, name), "SKILL.md")
	return writeSkillFile(out, body)
}

// EmitContext = raw text; the opencode plugin injects it via
// client.session.prompt({noReply:true}). No JSON envelope.
func (opencodeHarness) EmitContext(w io.Writer, event, ctx string) error {
	if strings.TrimSpace(ctx) == "" {
		return nil
	}
	_, err := io.WriteString(w, ctx+"\n")
	return err
}

// DistillCmd: a detached headless run. opencode has no in-session background
// subagent primitive, so distiller.md's "map to that harness's background-task
// primitive" resolves here to a separate `opencode run` process. Without this
// the opencode side of the cognition loop was a nudge and nothing else.
//
// V2: --standalone, so the run's server inherits the distill guard env (a run
// on the shared background service would not, and its session-end would spawn
// another distill); --auto, because nobody is there to approve its KB writes.
func (h opencodeHarness) DistillCmd(prompt string) []string {
	if h.Major() >= 2 {
		return []string{"opencode", "run", "--standalone", "--auto", prompt}
	}
	return []string{"opencode", "run", prompt}
}

// EmitGate = the raw deny reason; the plugin throws it to block the call.
// The plugin can only throw, so an ask that reaches here is a deny; the asks
// opencode can put itself were already let through (opencode_ask.go,
// opencode_outbound_ask.go).
func (opencodeHarness) EmitGate(w io.Writer, d GateDecision) error {
	if !d.Deny && !d.Ask {
		return nil
	}
	_, err := io.WriteString(w, d.Reason+"\n")
	return err
}

// WireAgent for opencode:
//   - merges opencode.json: instructions ⊇ CLAUDE.md (loads the agent's doctrine
//   - cognition/comms stanzas on V1; inert on V2, where the plugin injects it).
//     Skills are NATIVE in opencode (discovered from .opencode/skills/) — no
//     plugin needed;
//   - writes the local mesh plugin for the installed major: V1 →
//     .opencode/plugin/meshctl.js (validated against 1.18), V2 →
//     .opencode/plugins/meshctl.js (validated against 2.0.28) — and removes the
//     other major's meshctl plugin, so an upgrade re-wires cleanly;
//   - V2: the in-session peer definitions in .opencode/agents/;
//     on V1 any managed ones are removed. No pool-access grant: V2 lets a
//     session read and write outside its project by default, and the peer
//     and work gates draw the line, as on Claude Code.
//
// Idempotent.
func (h opencodeHarness) WireAgent(dest string) ([]string, error) {
	var notes []string
	if n, err := ensureOpencodeConfig(dest, h.Major()); err != nil {
		return nil, err
	} else {
		notes = append(notes, n...)
	}
	want, wantRel, staleRel := opencodePluginJS, opencodePluginV1Rel, opencodePluginV2Rel
	if h.Major() >= 2 {
		want, wantRel, staleRel = opencodePluginV2JS, opencodePluginV2Rel, opencodePluginV1Rel
	}
	panel := func() ([]string, error) { return removeManagedPlugin(dest, opencodeTUIRel) } // V1: no TUI plugins
	if h.Major() >= 2 {
		panel = func() ([]string, error) { return writeManagedPlugin(dest, opencodeTUIRel, opencodeTUIPlugin) }
	}
	for _, step := range []func() ([]string, error){
		func() ([]string, error) { return writeManagedPlugin(dest, wantRel, want) },
		func() ([]string, error) { return removeManagedPlugin(dest, staleRel) },
		panel,
	} {
		n, err := step()
		if err != nil {
			return notes, err
		}
		notes = append(notes, n...)
	}
	n, err := ensurePeerAgents(filepath.Dir(dest), dest)
	notes = append(notes, n...)
	if err != nil || h.Major() < 2 {
		return notes, err
	}
	n, err = ensureOpencodeSubagentAsk(dest)
	return append(notes, n...), err
}

// ensureOpencodeConfig add-only merges opencode.json: instructions ⊇ CLAUDE.md
// and CLAUDE.local.md (the managed standing orders),
// and on V2 the ask rules for the gate-asked commands.
func ensureOpencodeConfig(dest string, major int) ([]string, error) {
	path := filepath.Join(dest, "opencode.json")
	root := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if json.Unmarshal(b, &root) != nil {
			return []string{"opencode.json is not valid JSON — left unchanged (wire manually)"}, nil
		}
	}
	var notes []string
	for _, f := range []string{"CLAUDE.md", ClaudeLocalFile} {
		if addStringToArray(root, "instructions", f) {
			notes = append(notes, "opencode.json: instructions += "+f)
		}
	}
	if major >= 2 {
		if added := ensureOpencodeAskRules(root); len(added) > 0 {
			notes = append(notes, "opencode.json: ask rules for "+strings.Join(added, ", "))
		}
		if added := ensureOpencodeGateAskRules(root, dest); len(added) > 0 {
			notes = append(notes, "opencode.json: pool and outbound rules: "+strings.Join(added, ", "))
		}
	}
	if len(notes) == 0 {
		return nil, nil
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return notes, nil
}

// opencodePluginJS is the V1 plugin: a thin bridge from opencode's real plugin
// hooks (opencode ≥1.18) to `meshctl --harness opencode hook event`
// (hook_event.go). It translates each hook into a normalized event and applies
// the reply; whether a session has started, its cached session-start block,
// its latest prompt and the idle window are all kept by meshctl. The system
// transform injects the context reply (the session-start block on every call,
// then intuition); session.idle is a finished turn; a wake-up timer per session
// sends a tick after the idle window so meshctl can close it.
const opencodePluginMarker = "meshctl mesh integration for opencode"
const opencodePluginJS = `// meshctl mesh integration for opencode (generated by ` + "`meshctl agent new/onboard`" + `).
// A thin bridge: each hook becomes one event for ` + "`meshctl hook event`" + `, which keeps
// the session state. Injection goes through experimental.chat.system.transform
// (output.system is a string[]) — NOT chat.message output.parts, which require
// id/sessionID/messageID and reject a synthetic text part ("invalid user part before
// save"). chat.message is read-only: it forwards the prompt.
export const MeshctlPlugin = async ({ $, directory }) => {
  const cwd = directory || process.cwd()
  const instance = "oc1-" + process.pid + "-" + Date.now().toString(36)
  const send = async (ev) => {
    const payload = JSON.stringify({ harness: "opencode", variant: 1, instance, cwd, ...ev })
    try { return (await $` + "`echo ${payload} | meshctl --harness opencode hook event`" + `.quiet()).stdout.toString().trim() }
    catch { return "" }
  }
  // Wake-up timers: after a finished turn, tell meshctl once the idle window
  // (AGENT_MESH_OPENCODE_IDLE_SEC, default 600s) has passed; meshctl decides
  // whether the session is still quiet and closes it.
  const idleMs = (Number(process.env.AGENT_MESH_OPENCODE_IDLE_SEC) || 600) * 1000
  const wake = new Map()
  const armWake = (sid) => {
    const t = wake.get(sid); if (t) clearTimeout(t)
    const h = setTimeout(() => { wake.delete(sid); void send({ event: "tick", session_id: sid || "" }) }, idleMs + 2000)
    if (h && typeof h.unref === "function") h.unref()
    wake.set(sid, h)
  }
  return {
    // Read-only: forward the latest user prompt (never mutate output).
    "chat.message": async (input, output) => {
      let prompt = ""
      try { prompt = (output.parts || []).filter(p => p.type === "text").map(p => p.text).join("\n") } catch {}
      await send({ event: "prompt", session_id: (input && input.sessionID) || "", prompt })
    },
    // Inject mesh context into the system prompt (safe string push).
    "experimental.chat.system.transform": async (input, output) => {
      const ctx = await send({ event: "context", session_id: (input && input.sessionID) || "" })
      if (ctx && output && Array.isArray(output.system)) output.system.push(ctx)
    },
    event: async ({ event }) => {
      const sid = event?.properties?.sessionID || event?.properties?.session?.id || ""
      if (event.type === "session.deleted") { await send({ event: "session.end", session_id: sid }); return }
      if (event.type === "session.idle") {
        await send({ event: "turn.end", session_id: sid })
        armWake(sid)
      }
    },
  }
}
`

// --- shared skill writer ---

func writeSkillFile(out, body string) (bool, error) {
	// Mesh-owned skills (mesh-cognition, mesh-steward) are regenerated from the
	// embedded template — overwrite when the on-disk copy differs so `onboard`
	// propagates skill updates to existing agents (not add-only). No-op if current.
	if b, err := os.ReadFile(out); err == nil {
		if string(b) == body {
			return false, nil
		}
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// addStringToArray appends v to root[key] ([]any of strings) if absent. Returns
// true if it added it. Creates the array when missing.
func addStringToArray(root map[string]any, key, v string) bool {
	arr, _ := root[key].([]any)
	for _, x := range arr {
		if s, ok := x.(string); ok && s == v {
			return false
		}
	}
	root[key] = append(arr, v)
	return true
}
