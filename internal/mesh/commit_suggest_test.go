package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// suggestRepo is a committed repo with a mesh skill, a managed .gitignore
// block and CLAUDE.md.
func suggestRepo(t *testing.T) string {
	t.Helper()
	gitIdentity(t)
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	write := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
	}
	write(".claude/skills/mesh-cognition/SKILL.md", "# Mesh cognition\n\n## Recall\n\ntext\n")
	write(".gitignore", "own/\n# meshctl-managed ignores — do not edit\n.memory/\n# end meshctl-managed ignores\n")
	write("CLAUDE.md", "orders\n")
	write("knowledge/handoff.md", "h\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "scaffold")
	return dir
}

func appendTo(t *testing.T, dir, rel, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
	f, err := os.OpenFile(filepath.Join(dir, rel), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func TestSuggestCommitMessage_WiringNamedByNewHeadings(t *testing.T) {
	dir := suggestRepo(t)
	appendTo(t, dir, ".claude/skills/mesh-cognition/SKILL.md", "\n## Documents for the person\n\nmore\n")
	appendTo(t, dir, "CLAUDE.md", "- docs bullet\n")
	os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("own/\n# meshctl-managed ignores — do not edit\n.memory/\ndocs/local/\n# end meshctl-managed ignores\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs/README.md"), []byte("docs\n"), 0o644)

	if got, want := SuggestCommitMessage(dir, "v1.10.0"), "meshctl wiring: Documents for the person"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSuggestCommitMessage_WiringWithoutHeadingNamesVersion(t *testing.T) {
	dir := suggestRepo(t)
	appendTo(t, dir, ".claude/skills/mesh-cognition/SKILL.md", "a line\n")
	if got := SuggestCommitMessage(dir, "1.10.0"); got != "meshctl wiring: v1.10.0" {
		t.Fatalf("got %q", got)
	}
	if got := SuggestCommitMessage(dir, "dev"); got != "meshctl wiring: refresh" {
		t.Fatalf("dev build: got %q", got)
	}
}

func TestSuggestCommitMessage_CompanionsAloneAreNotWiring(t *testing.T) {
	dir := suggestRepo(t)
	appendTo(t, dir, "CLAUDE.md", "my own note\n")
	appendTo(t, dir, ".gitignore", "mine/\n") // after the managed block
	got := SuggestCommitMessage(dir, "v1.10.0")
	if !strings.HasPrefix(got, "Update: ") || strings.Contains(got, "wiring") ||
		!strings.Contains(got, "CLAUDE.md") || !strings.Contains(got, ".gitignore") {
		t.Fatalf("person's edits must not read as wiring: %q", got)
	}
}

func TestSuggestCommitMessage_KnowledgeBySlugAndMixedBody(t *testing.T) {
	dir := suggestRepo(t)
	appendTo(t, dir, "knowledge/candidates/watch-business-hours.md", "c\n")
	appendTo(t, dir, "knowledge/candidates/pr-session-gap.md", "c\n")
	appendTo(t, dir, "knowledge/handoff.md", "more\n")
	appendTo(t, dir, "scripts/run.sh", "x\n")

	got := SuggestCommitMessage(dir, "")
	want := "Add candidates: pr-session-gap, watch-business-hours\n\nAlso:\n- Update knowledge: handoff\n- Update: scripts"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestSuggestCommitMessage_LongSubjectCutWithFullListInBody(t *testing.T) {
	dir := suggestRepo(t)
	for _, s := range []string{"a-rather-long-candidate-slug-one", "a-rather-long-candidate-slug-two", "a-rather-long-candidate-slug-three"} {
		appendTo(t, dir, "knowledge/candidates/"+s+".md", "c\n")
	}
	got := SuggestCommitMessage(dir, "")
	subject, body, _ := strings.Cut(got, "\n\n")
	if len(subject) > suggestSubjectMax+len(" and 2 more") || !strings.HasSuffix(subject, " more") {
		t.Fatalf("subject must be cut: %q", subject)
	}
	if !strings.Contains(body, "a-rather-long-candidate-slug-three") {
		t.Fatalf("body must carry the full list: %q", body)
	}
}

func TestSuggestCommitMessage_CleanTree(t *testing.T) {
	if got := SuggestCommitMessage(suggestRepo(t), "v1"); got != "" {
		t.Fatalf("clean tree: got %q", got)
	}
}
