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
// not into a machine-global settings file. Three things are wired:
//   - the five hooks, each invoking `meshctl hook …` (resolved on $PATH, so
//     the wiring carries no machine-absolute path),
//   - a scoped set of Bash(meshctl <verb>:*) permissions so mesh messaging,
//     inbox and cognition commands are preapproved — but not the destructive
//     ones (pool teardown/forget, update), and
//   - the status line, `meshctl hook status-line` (ADR-0074), only when the
//     workspace has none: a hand-set status line is never replaced.
//
// EnsureAgentSettings is idempotent and near add-only: it inserts what is
// missing and never removes a key. The one rewrite is the matcher on the mesh's
// OWN hook entry, upgraded in place when hookWiring widens; hand-added entries
// are never touched. Safe over a hand-tuned settings.json; a second run is a
// no-op.

// legacyMeshctlAllowRule pre-approved every meshctl verb — including
// `pool teardown --purge`, which deletes agent workspaces. Replaced by the
// scoped set below; an existing legacy rule the mesh itself wrote is upgraded
// in place (hand-added rules are never touched).
const legacyMeshctlAllowRule = "Bash(meshctl:*)"

// meshctlAllowRules pre-approves the verbs an agent uses in a session: the
// steward's scaffolding, messaging, inbox work, cognition, and read-only
// diagnostics. Deliberately absent: `pool teardown`, `pool forget`, `update`.
var meshctlAllowRules = []string{
	"Bash(meshctl ask:*)", "Bash(meshctl send:*)", "Bash(meshctl msg:*)",
	"Bash(meshctl inbox:*)", "Bash(meshctl sent:*)",
	"Bash(meshctl kb:*)", "Bash(meshctl memory:*)", "Bash(meshctl intuition:*)",
	"Bash(meshctl handoff:*)", "Bash(meshctl session:*)", "Bash(meshctl pending:*)",
	"Bash(meshctl agent:*)",
	"Bash(meshctl pool init:*)", "Bash(meshctl pool upgrade:*)", "Bash(meshctl pool list:*)",
	"Bash(meshctl pool register:*)", "Bash(meshctl pool migrate:*)",
	"Bash(meshctl doctor:*)", "Bash(meshctl version:*)", "Bash(meshctl help:*)", "Bash(meshctl --help:*)",
}

// MeshWiringVersion is the version of the per-agent harness wiring (hook set +
// gate policy). It is stamped into each agent's .claude/.mesh-wiring.json so
// doctor can detect an agent left on older wiring after a meshctl upgrade and
// tell you to re-onboard. Bump it whenever hookWiring OR the gate's deny policy
// (gate.go) changes materially. v3: scoped allow rules, shell-write gate.
// v4: the gate also sees Agent/Task delegations (ask before delegating work to
// a state-mutating peer, ADR-0063). v5: the gate matcher actually matches MCP
// tool names (`mcp__.*`, ADR-0062/0068 amendments), the PostToolUse hook is
// wired on the same matcher, and the gate policy changes of the
// shared-understanding spec ride the same bump — one re-onboard per peer.
// v6: a Stop hook (session-beat) records the session's heartbeat and its
// scheduled jobs at the end of every turn (mesh console).
const MeshWiringVersion = 6

const meshWiringStampFile = ".mesh-wiring.json"

// GateHookMatcher is the tool-name matcher the gate hooks (PreToolUse and
// PostToolUse) are wired with, in the per-agent settings and in the rendered
// in-session peer definitions. Claude Code applies a matcher as a FULL regex
// match on the tool name, so an MCP tool (`mcp__<server>__<tool>`) needs the
// `mcp__.*` alternative: the bare `mcp__` shipped in v1..v4 never matched one
// and the gate had never seen an MCP call (ADR-0062 and ADR-0068 amendments).
// Doctor flags the bare form; see HasBareMCPMatcher.
const GateHookMatcher = "Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__.*|Agent|Task"

// staleMCPAlternative is the matcher alternative that never matches an MCP
// tool name under a full match.
const staleMCPAlternative = "mcp__"

// HasBareMCPMatcher reports whether a hook matcher carries the bare `mcp__`
// alternative (one that full-matches nothing an MCP server names) rather than
// `mcp__.*` or another prefix form.
func HasBareMCPMatcher(matcher string) bool {
	for _, alt := range strings.Split(matcher, "|") {
		if strings.TrimSpace(alt) == staleMCPAlternative {
			return true
		}
	}
	return false
}

// hookWiring is the (event, subcommand) set the mesh installs. PreToolUse is the
// fail-closed enforcement gate (gate.go); PostToolUse is its after-the-fact
// companion on the same matcher (the seam for pool commits and outbound acks:
// ADR-0066, ADR-0068); the others are advisory (Stop, v6, is the end-of-turn heartbeat that also records the session's scheduled jobs). The gate matcher fires on
// file-edit tools, Bash, every MCP tool and delegations — the gate denies
// competing-store and intake paths, then checks the call against the
// agent's KB rules (kbrules.go). v2 widened the matcher for the KB-rule gate;
// v4 added Agent/Task for delegations; v5 fixed the MCP alternative and added
// PostToolUse.
var hookWiring = []struct{ event, sub, matcher string }{
	{"SessionStart", "session-start", "startup|resume|clear"},
	{"UserPromptSubmit", "user-prompt", ""},
	{"SessionEnd", "session-end", ""},
	{"Stop", "session-beat", ""},
	{"PreToolUse", "pre-tool-use", GateHookMatcher},
	{"PostToolUse", "post-tool-use", GateHookMatcher},
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

	if n := ensureMeshctlPermission(root); n != "" {
		notes = append(notes, n)
		changed = true
	}
	if added := ensureHooks(root); len(added) > 0 {
		notes = append(notes, "wired hooks: "+strings.Join(added, ", "))
		changed = true
	}
	if ensureStatusLine(root) {
		notes = append(notes, "wired status line")
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

// ensureMeshctlPermission adds any missing scoped meshctl allow-rule to
// permissions.allow, and drops the legacy wildcard the mesh itself used to
// write (it covered destructive verbs). Returns a note, or "" if nothing changed.
func ensureMeshctlPermission(root map[string]any) string {
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		root["permissions"] = perms
	}
	allow, _ := perms["allow"].([]any)
	have := map[string]bool{}
	var kept []any
	droppedLegacy := false
	for _, r := range allow {
		if s, ok := r.(string); ok {
			if s == legacyMeshctlAllowRule {
				droppedLegacy = true
				continue
			}
			have[s] = true
		}
		kept = append(kept, r)
	}
	added := 0
	for _, rule := range meshctlAllowRules {
		if !have[rule] {
			kept = append(kept, rule)
			added++
		}
	}
	if added == 0 && !droppedLegacy {
		return ""
	}
	perms["allow"] = kept
	switch {
	case droppedLegacy && added > 0:
		return fmt.Sprintf("replaced %s with %d scoped meshctl permissions", legacyMeshctlAllowRule, len(meshctlAllowRules))
	case droppedLegacy:
		return "removed legacy " + legacyMeshctlAllowRule + " permission"
	default:
		return fmt.Sprintf("added %d scoped meshctl permissions", added)
	}
}

// StatusLineCommand is the status-line command the mesh wires (ADR-0074).
const StatusLineCommand = "meshctl hook status-line"

// ensureStatusLine sets statusLine when the workspace has none. Any existing
// statusLine, the mesh's own or hand-set, is left as it is. Returns true if it
// wrote.
func ensureStatusLine(root map[string]any) bool {
	if _, ok := root["statusLine"]; ok {
		return false
	}
	root["statusLine"] = map[string]any{
		"type":            "command",
		"command":         StatusLineCommand,
		"refreshInterval": StatusLineRefreshSeconds,
	}
	return true
}

// hasStatusLine reports whether the workspace's settings.json sets statusLine
// (the mesh's or a hand-set one). An unreadable file counts as set: the
// wiring would leave it alone anyway.
func hasStatusLine(dest string) bool {
	b, err := os.ReadFile(filepath.Join(dest, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return true
	}
	_, ok := root["statusLine"]
	return ok
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
			// The mesh owns its own entry's matcher: upgrade it in place when
			// hookWiring widens (v2: KB-rule gate needs Bash + all MCP tools;
			// v5: `mcp__.*` so MCP tool names actually match).
			if w.matcher != "" && upgradeHookMatcher(entries, cmdStr, w.matcher) {
				added = append(added, w.event+" (matcher)")
			}
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

// upgradeHookMatcher sets the matcher on the entry whose hooks invoke cmdStr, if
// it differs. Only the mesh's own entry is touched. Returns true if it changed.
func upgradeHookMatcher(entries []any, cmdStr, matcher string) bool {
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok || !hookCommandPresent([]any{em}, cmdStr) {
			continue
		}
		if cur, _ := em["matcher"].(string); cur != matcher {
			em["matcher"] = matcher
			return true
		}
		return false
	}
	return false
}
