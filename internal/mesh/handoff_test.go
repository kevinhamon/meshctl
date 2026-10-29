package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHandoffCommit_CommitsOnlyTheHandoff: `handoff set` commits the handoff
// it writes and nothing else staged or dirty beside it (#6).
func TestHandoffCommit_CommitsOnlyTheHandoff(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	other := filepath.Join(a.Dir(), "notes.md")
	if err := os.WriteFile(other, []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := HandoffSet(a, "## Current State\nx\n", time.Now()); err != nil {
		t.Fatal(err)
	}
	sha, err := HandoffCommit(a)
	if err != nil || sha == "" {
		t.Fatalf("HandoffCommit = %q, %v", sha, err)
	}
	if out, _ := git(a.Dir(), "status", "--porcelain"); !strings.Contains(out, "notes.md") || strings.Contains(out, HandoffFile) {
		t.Fatalf("want only notes.md left dirty, got %q", out)
	}
	if again, err := HandoffCommit(a); err != nil || again != "" {
		t.Fatalf("nothing to commit: got %q, %v", again, err)
	}
}
