package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The mesh's Claude Code integration is wired per-agent into
// <agent>/.claude/settings.json (committed, so it travels with the workspace) —
// not into a machine-global settings file. Two things are wired:
//   - the three hooks, each invoking `meshctl hook …` (resolved on $PATH, so
//     the wiring carries no machine-absolute path), and
//   - a Bash(meshctl:*) permission so mesh messaging/cognition commands are
//     preapproved.
//
// EnsureAgentSettings is ADD-ONLY and idempotent: it inserts only what is
// missing and never removes or rewrites an existing key, so it is safe to run
// over an agent that already has a hand-tuned settings.json. A second run is a
// no-op.

const meshctlAllowRule = "Bash(meshctl:*)"

// MeshWiringVersion is the version of the per-agent harness wiring (hook set +
// gate policy). It is stamped into each agent's .claude/.mesh-wiring.json so
// doctor can detect an agent left on older wiring after a meshctl upgrade and
// tell you to re-onboard. Bump it whenever hookWiring OR the gate's deny policy
// (gate.go) changes materially.
const MeshWiringVersion = 1

const meshWiringStampFile = ".mesh-wiring.json"

// hookWiring is the (event, subcommand) set the mesh installs. PreToolUse is the
// fail-closed enforcement gate (gate.go); the other three are advisory. The gate
// matcher fires on file-edit tools and every claude-mem tool — the gate itself
// then allows claude-mem reads and denies only the writes.
var hookWiring = []struct{ event, sub, matcher string }{
	{"SessionStart", "session-start", "startup|resume|clear"},
	{"UserPromptSubmit", "user-prompt", ""},
	{"SessionEnd", "session-end", ""},
	{"PreToolUse", "pre-tool-use", "Write|Edit|MultiEdit|NotebookEdit|mcp__plugin_claude-mem__"},
}

// EnsureAgentSettings merges the mesh integration into <dest>/.claude/settings.json.
// Returns a list of human-readable notes describing what it added (empty ⇒ nothing).
func EnsureAgentSettings(dest string) ([]string, error) {
	path := filepath.Join(dest, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	root := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if json.Unmarshal(b, &root) != nil {
			// Unparseable existing settings: do not clobber. Leave it for a human.
			return []string{"settings.json is not valid JSON — left unchanged (wire hooks manually)"}, nil
		}
	}

	var notes []string
	changed := false

	if ensureMeshctlPermission(root) {
		notes = append(notes, "added "+meshctlAllowRule+" permission")
		changed = true
	}
	if added := ensureHooks(root); len(added) > 0 {
		notes = append(notes, "wired hooks: "+strings.Join(added, ", "))
		changed = true
	}

	if changed {
		b, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			return nil, err
		}
	}

	// Wiring-version stamp (upgrade signal for doctor). A separate file so it
	// never risks Claude Code's settings.json schema. Written whenever the stamp
	// is absent or behind, independent of whether settings.json changed.
	if stamped, err := ensureWiringStamp(dest); err != nil {
		return notes, err
	} else if stamped {
		notes = append(notes, fmt.Sprintf("stamped mesh wiring v%d", MeshWiringVersion))
	}
	return notes, nil
}

// ensureWiringStamp writes .claude/.mesh-wiring.json to the current version when
// it is absent or behind. Idempotent: a stamp already at the current version is
// a no-op. Returns true if it wrote.
func ensureWiringStamp(dest string) (bool, error) {
	if WiringVersion(dest) == MeshWiringVersion {
		return false, nil
	}
	path := filepath.Join(dest, ".claude", meshWiringStampFile)
	b, err := json.MarshalIndent(map[string]any{"version": MeshWiringVersion}, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// WiringVersion reads the stamped wiring version for an agent workspace, or 0 if
// unstamped/unreadable (an agent wired before the stamp existed).
func WiringVersion(dest string) int {
	var cur struct {
		Version int `json:"version"`
	}
	if b, err := os.ReadFile(filepath.Join(dest, ".claude", meshWiringStampFile)); err == nil {
		_ = json.Unmarshal(b, &cur)
	}
	return cur.Version
}

// ensureMeshctlPermission appends the meshctl allow-rule to
// permissions.allow if absent. Returns true if it added it.
func ensureMeshctlPermission(root map[string]any) bool {
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		root["permissions"] = perms
	}
	allow, _ := perms["allow"].([]any)
	for _, r := range allow {
		if s, ok := r.(string); ok && s == meshctlAllowRule {
			return false
		}
	}
	perms["allow"] = append(allow, meshctlAllowRule)
	return true
}

// ensureHooks inserts any missing mesh hook entry. A hook is considered present
// for an event if some existing command under that event mentions its
// `meshctl hook <sub>` invocation. Returns the events it wired.
func ensureHooks(root map[string]any) []string {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	var added []string
	for _, w := range hookWiring {
		cmdStr := "meshctl hook " + w.sub
		entries, _ := hooks[w.event].([]any)
		if hookCommandPresent(entries, cmdStr) {
			continue
		}
		hookDef := map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": cmdStr, "timeout": 10},
			},
		}
		if w.matcher != "" {
			hookDef["matcher"] = w.matcher
		}
		hooks[w.event] = append(entries, hookDef)
		added = append(added, w.event)
	}
	return added
}

// hookCommandPresent reports whether cmdStr already appears in any hook command
// under the given event entries.
func hookCommandPresent(entries []any, cmdStr string) bool {
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := em["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := hm["command"].(string); ok && strings.Contains(c, cmdStr) {
				return true
			}
		}
	}
	return false
}
