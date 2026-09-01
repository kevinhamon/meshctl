package mesh

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Harness is the per-coding-agent adapter seam (ADR-0032). Skills and hooks are
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
	// Add-only: never clobbers an existing skill file.
	RenderSkill(name, body, dest string) (bool, error)
	// EmitContext formats a hook's injected context for this harness and writes
	// it to w. Empty ctx ⇒ nothing written.
	EmitContext(w io.Writer, event, ctx string) error
	// DistillCmd returns the argv that runs one background distillation with
	// the given prompt, or nil if this harness has no background primitive of
	// its own and relies on the in-session boundary nudge instead. This is the
	// binding distiller.md leaves to each harness.
	DistillCmd(prompt string) []string
}

const (
	HarnessClaude   = "claude"
	HarnessOpencode = "opencode"
)

// HarnessByName returns the adapter for a harness id (default: claude).
func HarnessByName(name string) Harness {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case HarnessOpencode:
		return opencodeHarness{}
	default:
		return claudeHarness{}
	}
}

// HarnessFor returns the adapter an agent runs under.
func HarnessFor(a *Agent) Harness { return HarnessByName(a.HarnessName()) }

// DetectHarness infers the harness from a pre-existing workspace: an opencode.json
// or .opencode/ ⇒ opencode; else claude (the default). Used by onboard when no
// --harness is given.
func DetectHarness(dest string) string {
	if fileExists(filepath.Join(dest, "opencode.json")) || dirExists(filepath.Join(dest, ".opencode")) {
		return HarnessOpencode
	}
	return HarnessClaude
}

// --- Claude Code ---

type claudeHarness struct{}

func (claudeHarness) Name() string { return HarnessClaude }

// WireAgent = the ADR-0030 per-agent settings.json wiring (hooks + preapproval).
func (claudeHarness) WireAgent(dest string) ([]string, error) { return EnsureAgentSettings(dest) }

// RenderSkill writes .claude/skills/<name>/SKILL.md (add-only).
func (claudeHarness) RenderSkill(name, body, dest string) (bool, error) {
	out := filepath.Join(dest, ".claude", "skills", name, "SKILL.md")
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

// --- opencode ---

type opencodeHarness struct{}

func (opencodeHarness) Name() string { return HarnessOpencode }

// RenderSkill writes .opencode/skills/<name>/SKILL.md — the Anthropic Agent
// Skills spec form the opencode-skills plugin discovers (same body as Claude).
func (opencodeHarness) RenderSkill(name, body, dest string) (bool, error) {
	out := filepath.Join(dest, ".opencode", "skills", name, "SKILL.md")
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
func (opencodeHarness) DistillCmd(prompt string) []string {
	return []string{"opencode", "run", prompt}
}

// WireAgent for opencode (validated against opencode 1.18):
//   - merges opencode.json: instructions ⊇ CLAUDE.md (loads the agent's doctrine
//     + cognition/comms stanzas). Skills are NATIVE in opencode (discovered from
//     .opencode/skills/) — no plugin needed;
//   - writes a local .opencode/plugin/meshctl.js that, via the real plugin hooks,
//     injects the handoff (first message) + intuition (each message) through
//     chat.message output.parts, and captures a session-record on session.idle.
//
// Add-only + idempotent.
func (opencodeHarness) WireAgent(dest string) ([]string, error) {
	var notes []string
	if n, err := ensureOpencodeConfig(dest); err != nil {
		return nil, err
	} else {
		notes = append(notes, n...)
	}
	if n, err := ensureOpencodePlugin(dest); err != nil {
		return notes, err
	} else {
		notes = append(notes, n...)
	}
	return notes, nil
}

// ensureOpencodeConfig add-only merges opencode.json: instructions ⊇ CLAUDE.md.
func ensureOpencodeConfig(dest string) ([]string, error) {
	path := filepath.Join(dest, "opencode.json")
	root := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if json.Unmarshal(b, &root) != nil {
			return []string{"opencode.json is not valid JSON — left unchanged (wire manually)"}, nil
		}
	}
	var notes []string
	if addStringToArray(root, "instructions", "CLAUDE.md") {
		notes = append(notes, "opencode.json: instructions += CLAUDE.md")
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

// ensureOpencodePlugin writes/updates the local mesh plugin. It writes when
// absent, and OVERWRITES an existing meshctl-managed plugin whose content differs
// (so onboard upgrades agents past an older/broken plugin, ADR-0039 fix). A
// non-meshctl file at that path is left untouched.
func ensureOpencodePlugin(dest string) ([]string, error) {
	out := filepath.Join(dest, ".opencode", "plugin", "meshctl.js")
	if b, err := os.ReadFile(out); err == nil {
		cur := string(b)
		if !strings.Contains(cur, opencodePluginMarker) {
			return nil, nil // someone else's plugin at this path — don't clobber
		}
		if cur == opencodePluginJS {
			return nil, nil // already current
		}
		if err := os.WriteFile(out, []byte(opencodePluginJS), 0o644); err != nil {
			return nil, err
		}
		return []string{"updated .opencode/plugin/meshctl.js (plugin upgraded)"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out, []byte(opencodePluginJS), 0o644); err != nil {
		return nil, err
	}
	return []string{"wrote .opencode/plugin/meshctl.js"}, nil
}

// opencodePluginJS bridges opencode's real plugin hooks (opencode ≥1.18) to the
// harness-neutral `meshctl hook` subcommands (on $PATH), which emit raw text under
// --harness opencode. chat.message injects the handoff (first message of a session)
// + intuition (each message) as a text part; the session.idle event captures a
// session-record (overwrite-by-id, so repeated idles converge on one record).
const opencodePluginMarker = "meshctl mesh integration for opencode"
const opencodePluginJS = `// meshctl mesh integration for opencode (generated by ` + "`meshctl agent new/onboard`" + `; ADR-0032).
// Injection goes through experimental.chat.system.transform (output.system is a
// string[]) — NOT chat.message output.parts, which require id/sessionID/messageID
// and reject a synthetic text part ("invalid user part before save"). chat.message
// is used read-only to capture the latest prompt for intuition.
export const MeshctlPlugin = async ({ $, directory }) => {
  const cwd = directory || process.cwd()
  const started = new Set()
  let lastPrompt = ""
  let lastSession = ""
  const run = async (sub, sessionID, promptText) => {
    const payload = JSON.stringify({ cwd, session_id: sessionID || "", prompt: promptText || "" })
    try { return (await $` + "`echo ${payload} | meshctl --harness opencode hook ${sub}`" + `.quiet()).stdout.toString().trim() }
    catch { return "" }
  }
  return {
    // Read-only: capture the latest user prompt + session (never mutate output).
    "chat.message": async (input, output) => {
      if (input && input.sessionID) lastSession = input.sessionID
      try { lastPrompt = (output.parts || []).filter(p => p.type === "text").map(p => p.text).join("\n") } catch {}
    },
    // Inject mesh context into the system prompt (safe string push).
    "experimental.chat.system.transform": async (input, output) => {
      const sid = (input && input.sessionID) || lastSession || ""
      let ctx = ""
      if (sid && !started.has(sid)) { started.add(sid); ctx += await run("session-start", sid, "") }
      const intu = await run("user-prompt", sid, lastPrompt)
      if (intu) ctx += (ctx ? "\n\n" : "") + intu
      if (ctx && output && Array.isArray(output.system)) output.system.push(ctx)
    },
    event: async ({ event }) => {
      if (event.type === "session.idle") {
        const sid = event?.properties?.sessionID || event?.properties?.session?.id || lastSession
        await run("session-end", sid, "")
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
