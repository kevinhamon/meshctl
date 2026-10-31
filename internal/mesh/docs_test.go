package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs_test.go — the documents folder convention.

func TestDocFolders_DefaultCleanAndRefused(t *testing.T) {
	a := &Agent{}
	if got := a.DocFolders(); len(got) != 1 || got[0] != DocsDir {
		t.Fatalf("default: %v", got)
	}
	a.Docs = []string{"reports", "./mockups/", "reports", "/abs", "../out", "~/x", "a/../../b", ""}
	if got := strings.Join(a.DocFolders(), ","); got != "reports,mockups" {
		t.Fatalf("cleaned: %q", got)
	}
	probs := strings.Join(a.docFolderProblems(), "\n")
	for _, bad := range []string{`"/abs"`, `"../out"`, `"~/x"`, `"a/../../b"`, `""`} {
		if !strings.Contains(probs, bad) {
			t.Errorf("Validate must refuse %s:\n%s", bad, probs)
		}
	}
	if strings.Contains(probs, `"reports"`) {
		t.Errorf("a good entry is not a problem:\n%s", probs)
	}
}

func TestScaffoldDocs_NewAndAddOnly(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	readme := filepath.Join(a.Dir(), DocsDir, "README.md")
	b, err := os.ReadFile(readme)
	if err != nil || !strings.Contains(string(b), "never in a repo") {
		t.Fatalf("new agent has docs/README.md: %v %s", err, b)
	}
	_ = os.WriteFile(readme, []byte("mine"), 0o644)
	if added, err := scaffoldDocs(a.Dir()); added || err != nil {
		t.Fatalf("second run must be a no-op: %v %v", added, err)
	}
	if b, _ := os.ReadFile(readme); string(b) != "mine" {
		t.Errorf("an existing README is never touched")
	}
	gi, _ := os.ReadFile(filepath.Join(a.Dir(), ".gitignore"))
	if !strings.Contains(string(gi), "\n"+DocsLocalDir+"/\n") {
		t.Errorf("new agents ignore docs/local/:\n%s", gi)
	}
}

func TestEnsureMemoryIgnored_AddsNewLinesToAnOldBlock(t *testing.T) {
	dest := t.TempDir()
	gi := filepath.Join(dest, ".gitignore")
	old := "node_modules/\n\n" + gitignoreMarker + " — do not edit\n.memory/\nintake/*.md\n" + gitignoreEnd + "\nmine/\n"
	_ = os.WriteFile(gi, []byte(old), 0o644)
	if err := ensureMemoryIgnored(dest); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(gi)
	got := string(b)
	if strings.Count(got, gitignoreMarker) != 1 || strings.Count(got, DocsLocalDir+"/") != 1 ||
		!strings.Contains(got, "intake/*.claim\n") || !strings.HasSuffix(got, gitignoreEnd+"\nmine/\n") ||
		!strings.HasPrefix(got, "node_modules/\n") {
		t.Fatalf("migrated block:\n%s", got)
	}
	if strings.Index(got, DocsLocalDir) > strings.Index(got, gitignoreEnd) {
		t.Errorf("new lines go inside the block:\n%s", got)
	}
	_ = ensureMemoryIgnored(dest)
	if b2, _ := os.ReadFile(gi); string(b2) != got {
		t.Errorf("second run must not change the file:\n%s", b2)
	}
	// A block whose end marker was edited away is left alone.
	hand := gitignoreMarker + "\n.memory/\n"
	_ = os.WriteFile(gi, []byte(hand), 0o644)
	_ = ensureMemoryIgnored(dest)
	if b3, _ := os.ReadFile(gi); string(b3) != hand {
		t.Errorf("hand-edited block changed:\n%s", b3)
	}
}

func TestDocFolderFindings_MissingAndEscaping(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	if f := docFolderFindings(a); len(f) != 0 {
		t.Fatalf("nothing declared, nothing to report: %+v", f)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(a.Dir(), "linked")); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(a.Dir(), "reports"), 0o755)
	a.Docs = []string{"reports", "missing", "linked"}
	var msgs []string
	for _, f := range docFolderFindings(a) {
		msgs = append(msgs, f.Message)
	}
	all := strings.Join(msgs, "\n")
	if len(msgs) != 2 || !strings.Contains(all, "missing/, which does not exist") || !strings.Contains(all, "linked/ leads outside") {
		t.Fatalf("findings:\n%s", all)
	}
}

// Every surface that tells a peer where to write says the same rule.
func TestDocsRule_InEveryStandingOrder(t *testing.T) {
	texts := templateTexts(t)
	for _, name := range []string{"cognitionSection", "templates/CLAUDE.md", "templates/skills/mesh-cognition/SKILL.md", "templates/doctrine/agent-comms.md"} {
		text := strings.Join(strings.Fields(texts[name]), " ")
		for _, want := range []string{"`docs/`", "never in a repo", "YYYY-MM-DD-<slug>", "mermaid", "`docs/local/`"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
}

// pool upgrade re-onboards every peer: an older workspace gains docs/ and
// the docs/local/ ignore, once.
func TestOnboard_BringsAnOlderWorkspaceToTheDocsConvention(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	_ = os.RemoveAll(filepath.Join(a.Dir(), DocsDir))
	gi := filepath.Join(a.Dir(), ".gitignore")
	b, _ := os.ReadFile(gi)
	_ = os.WriteFile(gi, []byte(strings.Replace(string(b), DocsLocalDir+"/\n", "", 1)), 0o644)

	res, err := Onboard(dir, a.Dir(), AgentSpec{}, ScaffoldOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "scaffolded the documents folder") || !fileExists(filepath.Join(a.Dir(), DocsDir, "README.md")) {
		t.Errorf("onboard must scaffold docs/: %v", res.Notes)
	}
	b, _ = os.ReadFile(gi)
	if strings.Count(string(b), DocsLocalDir+"/") != 1 {
		t.Errorf("onboard must add docs/local/ once:\n%s", b)
	}
	claude, _ := os.ReadFile(filepath.Join(a.Dir(), "CLAUDE.md"))
	if !strings.Contains(string(claude), "Documents for the person go in") {
		t.Errorf("the managed stanza carries the docs rule")
	}
}
