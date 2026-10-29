package mesh

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err := kbFinishConfirmed(t, dir, a, "sess1"); err != nil {
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
	if err := kbFinishConfirmed(t, dir, a, sid); err != nil {
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
	newTestAgent(t, dir, "web-ui-architect", true, nil) // name contains '-'

	cases := []struct{ cwd, want string }{
		{filepath.Join(WorktreesPath(dir), "architect-s-123-456"), "architect"},
		{filepath.Join(WorktreesPath(dir), "architect-s-123-456", "knowledge"), "architect"},
		{filepath.Join(WorktreesPath(dir), "web-ui-architect-s-9-9"), "web-ui-architect"},
		{filepath.Join(dir, "architect"), ""},                // not under .worktrees
		{filepath.Join(WorktreesPath(dir), "ghost-s-1"), ""}, // unknown agent
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
	// Seed a shared file on main. A candidate, so the finish is
	// candidates-only and reaches the rebase (a headless promotion parks
	// before it, kb_needs_confirm_test.go).
	shared := filepath.Join(KnowledgeDir, "candidates", "index.md")
	if err := os.MkdirAll(filepath.Join(a.Dir(), filepath.Dir(shared)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Dir(), shared), []byte("line: original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, a.Dir())

	now := time.Now()
	wt, err := KBBegin(dir, a, "sess1", now)
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	// Edit the same line in the worktree branch.
	if err := os.WriteFile(filepath.Join(wt, shared), []byte("line: branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "branch edit")

	// Concurrently advance main on the same line.
	if err := os.WriteFile(filepath.Join(a.Dir(), shared), []byte("line: mainline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, a.Dir(), "main edit")

	// Headless finish must NOT auto-resolve.
	_, err = KBFinish(dir, a, "sess1", KBFinishOptions{Headless: true})
	if !errors.Is(err, ErrKBConflict) {
		t.Fatalf("expected ErrKBConflict, got %v", err)
	}
	// The branch is left intact for the human.
	if !dirExists(wt) {
		t.Fatal("headless conflict should leave the worktree for the human")
	}
}

// gitBranchExists reports whether refs/heads/<branch> exists in dir.
func gitBranchExists(t *testing.T, dir, branch string) bool {
	t.Helper()
	_, err := git(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// TestKB_BeginCreatesMeshKBBranch: a KB session branch is mesh/kb/<session>
// (spec decision 17), alongside mesh/work/<id>; finish removes it.
func TestKB_BeginCreatesMeshKBBranch(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())

	wt, err := KBBegin(dir, a, "sess1", time.Now())
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	if got := KBBranch("sess1"); got != "mesh/kb/sess1" {
		t.Fatalf("KBBranch = %q, want mesh/kb/sess1", got)
	}
	if !gitBranchExists(t, a.Dir(), "mesh/kb/sess1") {
		t.Fatal("kb begin did not create mesh/kb/sess1")
	}
	if gitBranchExists(t, a.Dir(), "session/sess1") {
		t.Fatal("kb begin must not create the legacy session/ branch")
	}
	if err := os.WriteFile(filepath.Join(wt, "adr-a.md"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "add adr-a")
	if err := kbFinishConfirmed(t, dir, a, "sess1"); err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	if gitBranchExists(t, a.Dir(), "mesh/kb/sess1") {
		t.Fatal("finish left mesh/kb/sess1 behind")
	}
}

// legacyKBWorktree builds a worktree on a legacy session/<id> branch by hand,
// as a pool begun under an older meshctl holds them.
func legacyKBWorktree(t *testing.T, dir string, a *Agent, session string) string {
	t.Helper()
	wt := worktreePath(dir, a.Name, session)
	if err := os.MkdirAll(WorktreesPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git(a.Dir(), "worktree", "add", "-b", "session/"+session, wt); err != nil {
		t.Fatal(err)
	}
	return wt
}

// TestKB_LegacySessionBranchAccepted: finish, finish --abort and the session
// listing operate on a legacy session/<id> branch when that is what exists.
func TestKB_LegacySessionBranchAccepted(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())

	wtOld := legacyKBWorktree(t, dir, a, "old")
	wtGone := legacyKBWorktree(t, dir, a, "gone")

	got := ListKBSessions(dir, a)
	if len(got) != 2 || got[0] != "gone" || got[1] != "old" {
		t.Fatalf("ListKBSessions = %v, want [gone old]", got)
	}

	// finish merges the legacy branch and removes it.
	if err := os.WriteFile(filepath.Join(wtOld, "adr-old.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wtOld, "add adr-old")
	if err := kbFinishConfirmed(t, dir, a, "old"); err != nil {
		t.Fatalf("kb finish on legacy branch: %v", err)
	}
	if !fileExists(filepath.Join(a.Dir(), "adr-old.md")) {
		t.Fatal("legacy branch did not land on main")
	}
	if gitBranchExists(t, a.Dir(), "session/old") || dirExists(wtOld) {
		t.Fatal("finish left the legacy branch or worktree behind")
	}

	// --abort discards the legacy branch and its worktree.
	if err := kbFinishAbort(dir, a, "gone"); err != nil {
		t.Fatalf("kb finish --abort on legacy branch: %v", err)
	}
	if gitBranchExists(t, a.Dir(), "session/gone") || dirExists(wtGone) {
		t.Fatal("abort left the legacy branch or worktree behind")
	}
	if got := ListKBSessions(dir, a); len(got) != 0 {
		t.Fatalf("ListKBSessions after finish+abort = %v, want none", got)
	}
}

// TestKB_BeginRefusesWorktreeHeldByAnotherLiveSession: one worktree, one
// session (spec decision 6). Another live session's KB worktree cannot be
// begun into; the holder re-running kb begin is idempotent as before, and a
// dead holder frees it.
func TestKB_BeginRefusesWorktreeHeldByAnotherLiveSession(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	now := time.Now()

	// Session sX is live and begins its KB worktree.
	if _, err := SessionStart(a, "sX", os.Getpid(), now); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_SESSION_ID", "sX")
	wt, err := KBBegin(dir, a, "sX", now)
	if err != nil {
		t.Fatalf("kb begin as holder: %v", err)
	}

	// Another live session asks for the same KB session id: refused, naming sX.
	if _, err := SessionStart(a, "sZ", os.Getpid(), now); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_SESSION_ID", "sZ")
	_, err = KBBegin(dir, a, "sX", now)
	if err == nil {
		t.Fatal("kb begin must refuse a worktree held by another live session")
	}
	if !strings.Contains(err.Error(), "sX") {
		t.Fatalf("refusal must name the holding session: %v", err)
	}
	if !dirExists(wt) || !gitBranchExists(t, a.Dir(), "mesh/kb/sX") {
		t.Fatal("refusal must leave the holder's worktree and branch intact")
	}

	// The holder itself re-runs kb begin: idempotent, same worktree.
	t.Setenv("CLAUDE_SESSION_ID", "sX")
	if again, err := KBBegin(dir, a, "sX", now); err != nil || again != wt {
		t.Fatalf("holder re-begin: %q %v, want %q", again, err, wt)
	}

	// The holder ends; the worktree is no longer held.
	if err := SessionEnd(a, "sX"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_SESSION_ID", "sZ")
	if again, err := KBBegin(dir, a, "sX", now); err != nil || again != wt {
		t.Fatalf("begin after holder ended: %q %v, want %q", again, err, wt)
	}
}

// TestKB_BeginRefusesFolderThatIsNotAWorktree: a folder at the worktree path
// that git does not list as a worktree is not a begun session (#5).
func TestKB_BeginRefusesFolderThatIsNotAWorktree(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	wt := worktreePath(dir, a.Name, "sY")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := KBBegin(dir, a, "sY", time.Now())
	if err == nil || !strings.Contains(err.Error(), wt) {
		t.Fatalf("kb begin over a non-worktree folder must fail naming it, got %v", err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if got, err := KBBegin(dir, a, "sY", time.Now()); err != nil || got != wt {
		t.Fatalf("kb begin after removing the folder: %q %v", got, err)
	}
}
