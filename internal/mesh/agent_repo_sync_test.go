package mesh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushedAgentRepo is an agent-like repo with one commit, pushed to a bare remote.
func pushedAgentRepo(t *testing.T) string {
	t.Helper()
	gitIdentity(t)
	dir := filepath.Join(t.TempDir(), "architect")
	os.MkdirAll(dir, 0o755)
	gitOut(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("x\n"), 0o644)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "scaffold")
	bare := filepath.Join(t.TempDir(), "architect.git")
	gitOut(t, ".", "init", "-q", "--bare", bare)
	gitOut(t, dir, "remote", "add", "origin", bare)
	gitOut(t, dir, "push", "-q", "-u", "origin", "main")
	return dir
}

func TestAgentRepo_CommitThenPush(t *testing.T) {
	dir := pushedAgentRepo(t)
	if msg, action := AgentRepoFinding(dir); msg != "" || action != "" {
		t.Fatalf("a pushed clean repo has nothing to do, got %q %q", msg, action)
	}

	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("y\n"), 0o644)
	if st := AgentRepoSync(dir); len(st.Dirty) != 1 || st.Dirty[0] != " M CLAUDE.md" {
		t.Fatalf("status lines keep their leading column: %q", st.Dirty)
	}
	gitOut(t, dir, "checkout", "--", "CLAUDE.md")
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("n\n"), 0o644)
	st := AgentRepoSync(dir)
	if st.Action != ActionCommit || len(st.Dirty) != 1 || st.Dirty[0] != "?? notes.md" {
		t.Fatalf("dirty: got %+v", st)
	}
	if msg, action := AgentRepoFinding(dir); action != ActionCommit || !strings.HasPrefix(msg, "1 uncommitted path(s)") {
		t.Fatalf("dirty finding: %q %q", msg, action)
	}
	if _, _, err := AgentRepoPush(context.Background(), dir); err == nil {
		t.Fatalf("push with uncommitted changes must refuse")
	}
	if _, err := AgentRepoCommit(context.Background(), dir, "  ", st.Dirty); err == nil {
		t.Fatalf("an empty message must refuse")
	}

	// A path the person did not see appears: nothing is committed.
	os.WriteFile(filepath.Join(dir, "late.md"), []byte("l\n"), 0o644)
	if _, err := AgentRepoCommit(context.Background(), dir, "notes", st.Dirty); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("a changed tree must refuse, got %v", err)
	}
	st = AgentRepoSync(dir)
	if _, err := AgentRepoCommit(context.Background(), dir, "notes", st.Dirty); err != nil {
		t.Fatalf("commit: %v", err)
	}

	st = AgentRepoSync(dir)
	if st.Action != ActionPush || st.Upstream != "origin/main" || len(st.Commits) != 1 || !strings.HasSuffix(st.Commits[0], " notes") {
		t.Fatalf("ahead by one: got %+v", st)
	}
	if msg, action := AgentRepoFinding(dir); action != ActionPush || !strings.HasPrefix(msg, "1 commit(s) ahead of origin/main") {
		t.Fatalf("ahead finding: %q %q", msg, action)
	}
	outcome, out, err := AgentRepoPush(context.Background(), dir)
	if err != nil || outcome != PushPushed {
		t.Fatalf("push: %v %s %s", err, outcome, out)
	}
	if msg, _ := AgentRepoFinding(dir); msg != "" {
		t.Fatalf("pushed repo must be silent, got %q", msg)
	}
}

func TestAgentRepo_NoRemoteHasNoButton(t *testing.T) {
	gitIdentity(t)
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "x.md"), []byte("x\n"), 0o644)
	if st := AgentRepoSync(dir); st.Action != "" {
		t.Fatalf("no remote is the repo-hygiene finding, not a commit button: %+v", st)
	}
}

func TestGate_ConsoleAgentRepoButtonsDenied(t *testing.T) {
	dir, a := poolGateFixture(t)
	for _, cmd := range []string{
		"curl -X POST 'http://127.0.0.1:7717" + ConsoleAgentCommitPath + "?agent=architect'",
		"curl -s -XPOST http://localhost:7717" + ConsoleAgentPushPath + "/check?agent=x",
	} {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if !d.Deny || !strings.Contains(d.Reason, "those are the human's") {
			t.Fatalf("%s: want deny, got %+v", cmd, d)
		}
	}
	if d := GatePreToolUse(dir, bash(a, "sess-1", "curl -s 'http://127.0.0.1:7717/api/agent?agent=x'")); d.Deny {
		t.Fatalf("the agent view stays allowed, got %+v", d)
	}
}
