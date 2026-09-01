package mesh

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("add", "-A")
	// --allow-empty: New() may have already committed the scaffold (ADR-0039), so
	// there can be nothing new to seed.
	run("commit", "--allow-empty", "-m", "seed")
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestKB_DifferentFilesMergeToMain(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())

	now := time.Now()
	wt, err := KBBegin(dir, a, "sess1", now)
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "adr-a.md"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "add adr-a")
	if err := KBFinish(dir, a, "sess1", false, false); err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	if !fileExists(filepath.Join(a.Dir(), "adr-a.md")) {
		t.Fatal("adr-a.md did not land on main")
	}
	if dirExists(wt) {
		t.Fatal("worktree not removed after finish")
	}
}

func TestKB_ResolveSessionAcrossShells(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())

	// begin with one (minted) id, as one shell would.
	wt, err := KBBegin(dir, a, "s-111-222", time.Now())
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "adr-a.md"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "add adr-a")

	// finish computes a DIFFERENT minted id (different pid/nano); resolution
	// must still find the sole worktree.
	sid, err := ResolveKBSession(dir, a, "s-999-888", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sid != "s-111-222" {
		t.Fatalf("resolved %q, want s-111-222", sid)
	}
	if err := KBFinish(dir, a, sid, false, false); err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	if !fileExists(filepath.Join(a.Dir(), "adr-a.md")) {
		t.Fatal("adr-a.md did not land on main")
	}
}

func TestKB_ResolveSessionAmbiguousAndEmpty(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())

	// none yet → error pointing at `kb begin`.
	if _, err := ResolveKBSession(dir, a, "s-1-1", ""); err == nil {
		t.Fatal("expected error when no worktree exists")
	}

	if _, err := KBBegin(dir, a, "s-1-1", time.Now()); err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	if _, err := KBBegin(dir, a, "s-2-2", time.Now()); err != nil {
		t.Fatalf("begin 2: %v", err)
	}

	// two worktrees, no explicit id → ambiguous error.
	if _, err := ResolveKBSession(dir, a, "s-9-9", ""); err == nil {
		t.Fatal("expected ambiguity error with two worktrees")
	}
	// explicit id wins.
	if sid, err := ResolveKBSession(dir, a, "s-9-9", "s-2-2"); err != nil || sid != "s-2-2" {
		t.Fatalf("explicit resolve: %q %v", sid, err)
	}
}

func TestAgentFromWorktreePath(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	newTestAgent(t, dir, "beyond-mfe-architect", true, nil) // name contains '-'

	cases := []struct{ cwd, want string }{
		{filepath.Join(dir, WorktreesDir, "architect-s-123-456"), "architect"},
		{filepath.Join(dir, WorktreesDir, "architect-s-123-456", "knowledge"), "architect"},
		{filepath.Join(dir, WorktreesDir, "beyond-mfe-architect-s-9-9"), "beyond-mfe-architect"},
		{filepath.Join(dir, "architect"), ""},        // not under .worktrees
		{filepath.Join(dir, WorktreesDir, "ghost-s-1"), ""}, // unknown agent
	}
	for _, c := range cases {
		if got := AgentFromWorktreePath(dir, c.cwd); got != c.want {
			t.Errorf("AgentFromWorktreePath(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

func TestKB_SameLineConflictHeadlessNeedsHuman(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	// Seed a shared file on main.
	if err := os.WriteFile(filepath.Join(a.Dir(), "index.md"), []byte("line: original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, a.Dir())

	now := time.Now()
	wt, err := KBBegin(dir, a, "sess1", now)
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	// Edit the same line in the worktree branch.
	if err := os.WriteFile(filepath.Join(wt, "index.md"), []byte("line: branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "branch edit")

	// Concurrently advance main on the same line.
	if err := os.WriteFile(filepath.Join(a.Dir(), "index.md"), []byte("line: mainline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, a.Dir(), "main edit")

	// Headless finish must NOT auto-resolve.
	err = KBFinish(dir, a, "sess1", false, true)
	if !errors.Is(err, ErrKBConflict) {
		t.Fatalf("expected ErrKBConflict, got %v", err)
	}
	// The branch is left intact for the human.
	if !dirExists(wt) {
		t.Fatal("headless conflict should leave the worktree for the human")
	}
}
