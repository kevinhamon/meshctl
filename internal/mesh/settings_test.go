package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if !anyStr(allow, meshctlAllowRule) {
		t.Fatalf("missing %s in permissions.allow", meshctlAllowRule)
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
	if !anyStr(allow, "Bash(git status)") || !anyStr(allow, meshctlAllowRule) {
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
