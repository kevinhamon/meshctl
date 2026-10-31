package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func readSettings(t *testing.T, dest string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dest, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings.json not valid JSON: %v", err)
	}
	return m
}

func TestSettings_WiresHooksAndPermission(t *testing.T) {
	dest := t.TempDir()
	notes, err := EnsureAgentSettings(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("expected notes on first wiring")
	}
	m := readSettings(t, dest)

	// Permission present.
	perms := m["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if !anyStr(allow, "Bash(meshctl inbox:*)") {
		t.Fatalf("missing %s in permissions.allow", "Bash(meshctl inbox:*)")
	}
	// All three hook events wired.
	hooks := m["hooks"].(map[string]any)
	for _, w := range hookWiring {
		entries, ok := hooks[w.event].([]any)
		if !ok || !hookCommandPresent(entries, "meshctl hook "+w.sub) {
			t.Fatalf("event %s not wired to `meshctl hook %s`", w.event, w.sub)
		}
	}
}

func TestSettings_Idempotent(t *testing.T) {
	dest := t.TempDir()
	if _, err := EnsureAgentSettings(dest); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dest, ".claude", "settings.json"))
	notes, err := EnsureAgentSettings(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("second run should add nothing, got notes: %v", notes)
	}
	second, _ := os.ReadFile(filepath.Join(dest, ".claude", "settings.json"))
	if string(first) != string(second) {
		t.Fatal("second run mutated settings.json (not idempotent)")
	}
}

func TestSettings_PreservesExisting(t *testing.T) {
	dest := t.TempDir()
	claude := filepath.Join(dest, ".claude")
	os.MkdirAll(claude, 0o755)
	existing := `{
  "model": "claude-sonnet-5",
  "permissions": { "allow": ["Bash(git status)"] },
  "hooks": { "SessionStart": [ { "hooks": [ {"type":"command","command":"echo hi"} ] } ] }
}`
	os.WriteFile(filepath.Join(claude, "settings.json"), []byte(existing), 0o644)

	if _, err := EnsureAgentSettings(dest); err != nil {
		t.Fatal(err)
	}
	m := readSettings(t, dest)

	if m["model"] != "claude-sonnet-5" {
		t.Fatal("model key not preserved")
	}
	perms := m["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if !anyStr(allow, "Bash(git status)") || !anyStr(allow, "Bash(meshctl inbox:*)") {
		t.Fatalf("permissions not merged additively: %v", allow)
	}
	// Existing SessionStart hook must survive alongside the new one.
	ss := m["hooks"].(map[string]any)["SessionStart"].([]any)
	if !hookCommandPresent(ss, "echo hi") {
		t.Fatal("existing SessionStart hook was clobbered")
	}
	if !hookCommandPresent(ss, "meshctl hook session-start") {
		t.Fatal("mesh SessionStart hook not added alongside existing")
	}
}

func TestSettings_WiresStatusLine(t *testing.T) {
	dest := t.TempDir()
	if _, err := EnsureAgentSettings(dest); err != nil {
		t.Fatal(err)
	}
	sl, ok := readSettings(t, dest)["statusLine"].(map[string]any)
	if !ok {
		t.Fatal("statusLine not wired")
	}
	if sl["type"] != "command" || sl["command"] != StatusLineCommand || sl["refreshInterval"] != float64(StatusLineRefreshSeconds) {
		t.Fatalf("statusLine = %v", sl)
	}
}

func TestSettings_HandSetStatusLineIsKept(t *testing.T) {
	dest := t.TempDir()
	claude := filepath.Join(dest, ".claude")
	os.MkdirAll(claude, 0o755)
	os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"my-line"}}`), 0o644)
	notes, err := EnsureAgentSettings(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if n == "wired status line" {
			t.Fatal("reported wiring over a hand-set status line")
		}
	}
	sl := readSettings(t, dest)["statusLine"].(map[string]any)
	if sl["command"] != "my-line" || sl["refreshInterval"] != nil {
		t.Fatalf("hand-set status line changed: %v", sl)
	}
}

func TestSettings_UnparseableLeftAlone(t *testing.T) {
	dest := t.TempDir()
	claude := filepath.Join(dest, ".claude")
	os.MkdirAll(claude, 0o755)
	bad := filepath.Join(claude, "settings.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	notes, err := EnsureAgentSettings(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("expected a note that settings.json was left unchanged")
	}
	b, _ := os.ReadFile(bad)
	if string(b) != "{not json" {
		t.Fatal("unparseable settings.json must be left untouched")
	}
}

func anyStr(xs []any, want string) bool {
	for _, x := range xs {
		if s, ok := x.(string); ok && s == want {
			return true
		}
	}
	return false
}

// TestSettings_GateMatcherFullMatchesMCPToolName reproduces how Claude Code
// applies a hook matcher: as a full regex match on the tool name. The bare
// `mcp__` alternative shipped in v1..v4 never matched an MCP tool, so the gate
// had never seen an MCP call.
func TestSettings_GateMatcherFullMatchesMCPToolName(t *testing.T) {
	full := regexp.MustCompile("^(?:" + GateHookMatcher + ")$")
	for _, tool := range []string{"mcp__tracker__createTicket", "mcp__tracker__search", "Bash", "Write", "Edit", "MultiEdit", "NotebookEdit", "Agent", "Task"} {
		if !full.MatchString(tool) {
			t.Errorf("matcher %q must full-match %q", GateHookMatcher, tool)
		}
	}
	for _, tool := range []string{"Read", "Grep", "BashOutput", "WebFetch"} {
		if full.MatchString(tool) {
			t.Errorf("matcher %q must not match %q", GateHookMatcher, tool)
		}
	}
	old := regexp.MustCompile("^(?:Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__|Agent|Task)$")
	if old.MatchString("mcp__tracker__createTicket") {
		t.Fatal("the bare mcp__ alternative is supposed to be the defect; it must not full-match an MCP tool name")
	}
}

// TestSettings_ReonboardV4AddsPostToolUse: a peer wired at v4 (bare mcp__
// matcher, no PostToolUse) re-onboards to v5 in one pass: the matcher is
// rewritten in place and a PostToolUse entry on the same matcher runs
// `meshctl hook post-tool-use`.
func TestSettings_ReonboardV4AddsPostToolUse(t *testing.T) {
	dest := t.TempDir()
	v4 := `{"hooks":{"PreToolUse":[
	  {"matcher":"Write|Edit|MultiEdit|NotebookEdit|Bash|mcp__|Agent|Task","hooks":[{"type":"command","command":"meshctl hook pre-tool-use","timeout":10}]}]}}`
	if err := os.MkdirAll(filepath.Join(dest, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".claude", "settings.json"), []byte(v4), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".claude", meshWiringStampFile), []byte(`{"version":4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAgentSettings(dest); err != nil {
		t.Fatal(err)
	}
	m := readSettings(t, dest)
	hooks := m["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 || pre[0].(map[string]any)["matcher"] != GateHookMatcher {
		t.Fatalf("PreToolUse matcher not rewritten in place: %v", pre)
	}
	post, _ := hooks["PostToolUse"].([]any)
	if !hookCommandPresent(post, "meshctl hook post-tool-use") {
		t.Fatalf("PostToolUse hook not wired: %v", hooks)
	}
	if len(post) != 1 || post[0].(map[string]any)["matcher"] != GateHookMatcher {
		t.Fatalf("PostToolUse must use the gate matcher: %v", post)
	}
	if v := WiringVersion(dest); v != MeshWiringVersion {
		t.Fatalf("stamp = v%d, want v%d", v, MeshWiringVersion)
	}
	if stop, _ := hooks["Stop"].([]any); !hookCommandPresent(stop, "meshctl hook session-beat") {
		t.Fatalf("Stop hook (session-beat, wiring v6) not wired: %v", hooks)
	}
}

// TestSettings_PendingIsPreApprovedAndAddedOnReWire: `meshctl pending` is a
// read-only listing the session-start block points at, so it is pre-approved;
// an agent wired before it gains the rule on its next re-onboard (the rules
// are merged add-only, delivered by the same wiring v5).
func TestSettings_PendingIsPreApprovedAndAddedOnReWire(t *testing.T) {
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".claude", "settings.json"), []byte(`{"permissions": {"allow": ["Bash(meshctl inbox:*)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAgentSettings(dest); err != nil {
		t.Fatal(err)
	}
	allow := readSettings(t, dest)["permissions"].(map[string]any)["allow"].([]any)
	if !anyStr(allow, "Bash(meshctl pending:*)") {
		t.Fatalf("missing Bash(meshctl pending:*) in permissions.allow: %v", allow)
	}
}
