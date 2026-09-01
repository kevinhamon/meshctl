package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScaffoldSteward(t *testing.T) {
	dir := meshRoot(t)
	sdir, notes, err := ScaffoldSteward(dir, HarnessClaude, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("expected scaffold notes")
	}
	// Manifest: not dispatchable, harness persisted.
	a, err := FindAgent(dir, StewardName)
	if err != nil {
		t.Fatal(err)
	}
	if a.IsDispatchable() {
		t.Fatal("steward must not be dispatchable")
	}
	// CLAUDE.md = steward role + the standard stanzas.
	cb, _ := os.ReadFile(filepath.Join(sdir, "CLAUDE.md"))
	for _, want := range []string{"Mesh Steward", "mesh-steward", "Cognition", "Agent Communication"} {
		if !strings.Contains(string(cb), want) {
			t.Fatalf("steward CLAUDE.md missing %q", want)
		}
	}
	// Both skills present in the claude location.
	for _, s := range []string{"mesh-cognition", "mesh-steward"} {
		if !fileExists(filepath.Join(sdir, ".claude", "skills", s, "SKILL.md")) {
			t.Fatalf("steward missing skill %s", s)
		}
	}
	// Idempotent: second call leaves it unchanged.
	_, notes2, err := ScaffoldSteward(dir, HarnessClaude, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes2) != 1 || !strings.Contains(notes2[0], "already present") {
		t.Fatalf("second scaffold should no-op, got %v", notes2)
	}
}

func TestScaffoldSteward_Opencode(t *testing.T) {
	dir := meshRoot(t)
	sdir, _, err := ScaffoldSteward(dir, HarnessOpencode, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(sdir, ".opencode", "skills", "mesh-steward", "SKILL.md")) {
		t.Fatal("opencode steward missing mesh-steward skill in .opencode/skills")
	}
	if !fileExists(filepath.Join(sdir, "opencode.json")) {
		t.Fatal("opencode steward missing opencode.json")
	}
}
