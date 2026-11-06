package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skills_link_test.go — mesh skills are rendered once per pool and linked
// into each agent (skills_link.go, issue #12).

func TestScaffold_SkillIsLinkToPoolCopy(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	link := filepath.Join(a.Dir(), ".claude", "skills", "mesh-cognition")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mesh-cognition is not a symlink: %v", err)
	}
	want, _ := renderedSkill(dir, "mesh-cognition")
	if got := readFile(t, filepath.Join(link, "SKILL.md")); got != want {
		t.Fatal("the link does not resolve to the rendered pool copy")
	}
	if got := readFile(t, filepath.Join(PoolSkillPath(dir, "mesh-cognition"), "SKILL.md")); got != want {
		t.Fatal("pool copy missing or stale")
	}
	if gi := readFile(t, filepath.Join(a.Dir(), ".gitignore")); !strings.Contains(gi, ".claude/skills/mesh-*") {
		t.Fatalf(".gitignore does not ignore the skill links:\n%s", gi)
	}
	if acts := actions(planAgent(a, finishNow)); strings.Contains(acts, "skill") {
		t.Fatalf("a fresh workspace plans a skill change: %s", acts)
	}
}

// An agent with a rendered copy (an earlier meshctl) gets the link.
func TestOnboard_ReplacesRenderedSkillCopyWithLink(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	skill := filepath.Join(a.Dir(), ".claude", "skills", "mesh-cognition")
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("OLD COPY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if acts := actions(planAgent(a, finishNow)); !strings.Contains(acts, "link skill mesh-cognition") {
		t.Fatalf("plan does not name the link: %s", acts)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	if !skillLinked(dir, a.Dir(), "mesh-cognition", HarnessByName(HarnessClaude)) {
		t.Fatal("rendered copy not replaced by a link")
	}
}

// A stale pool copy is refreshed once, for every agent linking to it.
func TestOnboard_RefreshesStalePoolCopy(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	pool := filepath.Join(PoolSkillPath(dir, "mesh-cognition"), "SKILL.md")
	if err := os.WriteFile(pool, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if acts := actions(planAgent(a, finishNow)); !strings.Contains(acts, "refresh skill mesh-cognition") {
		t.Fatalf("plan does not name the stale pool copy: %s", acts)
	}
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	want, _ := renderedSkill(dir, "mesh-cognition")
	if readFile(t, filepath.Join(a.Dir(), ".claude", "skills", "mesh-cognition", "SKILL.md")) != want {
		t.Fatal("stale pool copy not refreshed")
	}
}

// A fresh clone of the steward has no (gitignored) skill links; onboard still
// gives it mesh-steward.
func TestOnboard_StewardCloneGetsStewardSkill(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, StewardName, false, nil)
	steward := filepath.Join(a.Dir(), ".claude", "skills", "mesh-steward")
	_ = os.RemoveAll(steward)
	if _, err := Onboard(dir, a.Dir(), AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	if !skillLinked(dir, a.Dir(), "mesh-steward", HarnessByName(HarnessClaude)) {
		t.Fatal("steward clone did not get the mesh-steward link")
	}
}

func TestPoolIgnore_SkillsLineAddedOnUpgrade(t *testing.T) {
	if !strings.Contains(poolIgnoreBlock, "\n"+PoolSkillsDirName+"/\n") {
		t.Fatal("pool ignore block lacks skills/")
	}
}
