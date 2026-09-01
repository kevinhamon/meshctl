package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarness_ByNameAndDefault(t *testing.T) {
	if HarnessByName("").Name() != HarnessClaude {
		t.Fatal("empty ⇒ claude")
	}
	if HarnessByName("opencode").Name() != HarnessOpencode {
		t.Fatal("opencode not resolved")
	}
	if HarnessByName("CLAUDE").Name() != HarnessClaude {
		t.Fatal("case-insensitive claude")
	}
}

func TestHarness_EmitContextFormats(t *testing.T) {
	// Claude → JSON envelope.
	var c strings.Builder
	claudeHarness{}.EmitContext(&c, "SessionStart", "hi")
	if !strings.Contains(c.String(), "hookSpecificOutput") || !strings.Contains(c.String(), `"additionalContext":"hi"`) {
		t.Fatalf("claude envelope wrong: %s", c.String())
	}
	// opencode → raw text, no JSON.
	var o strings.Builder
	opencodeHarness{}.EmitContext(&o, "SessionStart", "hi")
	if strings.Contains(o.String(), "hookSpecificOutput") || strings.TrimSpace(o.String()) != "hi" {
		t.Fatalf("opencode should emit raw text, got: %q", o.String())
	}
	// Both silent on empty.
	var e strings.Builder
	opencodeHarness{}.EmitContext(&e, "x", "  ")
	if e.Len() != 0 {
		t.Fatal("empty ctx must be silent")
	}
}

func TestHarness_ClaudeWiring(t *testing.T) {
	dest := t.TempDir()
	var ch Harness = claudeHarness{}
	if _, err := ch.WireAgent(dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "meshctl hook session-start") {
		t.Fatalf("claude wiring missing hook: %s", b)
	}
	// Skill → .claude/skills.
	if ok, _ := ch.RenderSkill("mesh-cognition", "BODY", dest); !ok {
		t.Fatal("expected skill written")
	}
	if !fileExists(filepath.Join(dest, ".claude", "skills", "mesh-cognition", "SKILL.md")) {
		t.Fatal("claude skill path wrong")
	}
}

func TestHarness_OpencodeWiring(t *testing.T) {
	dest := t.TempDir()
	notes, err := opencodeHarness{}.WireAgent(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("expected wiring notes")
	}
	// opencode.json: instructions ⊇ CLAUDE.md, plugin ⊇ skills plugin.
	b, err := os.ReadFile(filepath.Join(dest, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("opencode.json invalid: %v", err)
	}
	if !anyStr(cfg["instructions"].([]any), "CLAUDE.md") {
		t.Fatalf("instructions missing CLAUDE.md: %v", cfg["instructions"])
	}
	// Skills are native in opencode ≥1.18 — no plugin should be added.
	if _, hasPlugin := cfg["plugin"]; hasPlugin {
		t.Fatalf("opencode.json should not add a plugin array (native skills): %v", cfg["plugin"])
	}
	// Local plugin written, using the real hooks (chat.message + session.idle).
	pb, err := os.ReadFile(filepath.Join(dest, ".opencode", "plugin", "meshctl.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"meshctl", "chat.message", "session.idle", "output.parts"} {
		if !strings.Contains(string(pb), want) {
			t.Fatalf("opencode plugin missing %q", want)
		}
	}
	// Skill → .opencode/skills.
	opencodeHarness{}.RenderSkill("mesh-cognition", "BODY", dest)
	if !fileExists(filepath.Join(dest, ".opencode", "skills", "mesh-cognition", "SKILL.md")) {
		t.Fatal("opencode skill path wrong")
	}
	// Idempotent: second wiring adds nothing.
	notes2, _ := opencodeHarness{}.WireAgent(dest)
	if len(notes2) != 0 {
		t.Fatalf("opencode wiring not idempotent: %v", notes2)
	}
}

func TestHarness_Detect(t *testing.T) {
	dest := t.TempDir()
	if DetectHarness(dest) != HarnessClaude {
		t.Fatal("bare dir ⇒ claude")
	}
	os.WriteFile(filepath.Join(dest, "opencode.json"), []byte("{}"), 0o644)
	if DetectHarness(dest) != HarnessOpencode {
		t.Fatal("opencode.json ⇒ opencode")
	}
}

func TestNew_OpencodeHarnessEndToEnd(t *testing.T) {
	dir := meshRoot(t)
	spec := AgentSpec{
		Name: "ocagent", Title: "OC", Role: "r",
		Accepts: []Accept{{Type: "fyi", Desc: "x"}},
		Badge:   Badge{Label: "OC"}, Harness: HarnessOpencode, Bare: true,
	}
	if _, err := New(dir, spec, BgOptions{}); err != nil {
		t.Fatal(err)
	}
	a, err := FindAgent(dir, "ocagent")
	if err != nil {
		t.Fatal(err)
	}
	if a.HarnessName() != HarnessOpencode {
		t.Fatalf("manifest harness not persisted: %q", a.HarnessName())
	}
	// opencode wiring present, NOT claude settings.json hooks.
	if !fileExists(filepath.Join(a.Dir(), "opencode.json")) {
		t.Fatal("opencode agent missing opencode.json")
	}
	if !fileExists(filepath.Join(a.Dir(), ".opencode", "skills", "mesh-cognition", "SKILL.md")) {
		t.Fatal("opencode agent missing skill in .opencode/skills")
	}
}
