package mesh

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// askWork runs an in-session work delegation developer → architect.
func askWork(t *testing.T, dir string, caller, peer *Agent, prompt string) (*Request, string) {
	t.Helper()
	var out bytes.Buffer
	req, rc, err := AskInSession(dir, peer, InSessionOptions{Type: "guidance", Prompt: prompt, From: caller.Name, Work: true, Now: time.Now(), Stdout: &out})
	if err != nil || rc != 0 {
		t.Fatalf("ask --work: rc=%d err=%v", rc, err)
	}
	return req, out.String()
}

// goLive registers a live session for a, as its own SessionStart hook would.
func goLive(t *testing.T, a *Agent) {
	t.Helper()
	if _, err := SessionStart(a, "live-1", os.Getpid(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAskInSessionWork_LogsFrontmatterAndPrintsWorkDefinition(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	req, out := askWork(t, dir, caller, arch, "Write the ADR for the split")

	r, err := ReadRequest(req.Path())
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "work" || r.Worktree != "" || r.Status != StatusInProgress {
		t.Fatalf("frontmatter: mode=%q worktree=%q status=%q", r.Mode, r.Worktree, r.Status)
	}
	logb, _ := os.ReadFile(filepath.Join(dir, DispatchLog))
	if !strings.Contains(string(logb), "DISPATCH mode=in-session-work id="+req.ID) {
		t.Fatalf("log: %s", logb)
	}
	if !strings.Contains(out, "subagent_type: mesh-architect-work\n") {
		t.Fatalf("must name the work definition:\n%s", out)
	}
	if strings.Contains(out, "worktree") {
		t.Fatalf("no live peer session ⇒ no worktree:\n%s", out)
	}
	if ws := workWorktrees(dir, ""); len(ws) != 0 {
		t.Fatalf("no worktree expected, got %+v", ws)
	}
}

func TestAskInSessionWork_Eligibility(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	_ = arch
	ops := newTestAgent(t, dir, "ops", false, []string{"infra"})
	scribe := newTestAgent(t, dir, "scribe", false, nil)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	// Neither dispatchable nor mutating: refused.
	if _, _, err := AskInSession(dir, scribe, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Work: true, Now: time.Now(), Stdout: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "neither dispatchable") {
		t.Fatalf("non-delegable peer must be refused, got %v", err)
	}
	// Mutating: delegable, and the output warns that the human approves it.
	var out bytes.Buffer
	if _, rc, err := AskInSession(dir, ops, InSessionOptions{Type: "guidance", Prompt: "rotate the key", From: caller.Name, Work: true, Now: time.Now(), Stdout: &out}); err != nil || rc != 0 {
		t.Fatalf("mutating peer must be work-delegable: rc=%d err=%v", rc, err)
	}
	if !strings.Contains(out.String(), "mesh-ops-work") || !strings.Contains(out.String(), "approve this delegation") {
		t.Fatalf("output:\n%s", out.String())
	}
	// ...but still not consultable in read-only mode.
	if _, _, err := AskInSession(dir, ops, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Now: time.Now(), Stdout: &bytes.Buffer{}}); err == nil {
		t.Fatal("consult of a mutating peer must stay refused")
	}
}

func TestAskInSessionWork_LivePeerGetsWorktreeAndBrief(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	gitInit(t, arch.Dir())
	goLive(t, arch)
	req, out := askWork(t, dir, caller, arch, "Write the ADR for the split")

	wt := workWorktreePath(dir, "architect", req.ID)
	if !dirExists(wt) {
		t.Fatalf("live peer ⇒ worktree %s expected", wt)
	}
	if b, err := git(wt, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || strings.TrimSpace(b) != "mesh/work/"+req.ID {
		t.Fatalf("worktree branch: %q %v", b, err)
	}
	r, _ := ReadRequest(req.Path())
	if r.Worktree != wt {
		t.Fatalf("request worktree = %q, want %q", r.Worktree, wt)
	}
	if !strings.Contains(out, "meshctl work merge "+req.ID) {
		t.Fatalf("output must name the merge:\n%s", out)
	}
	// The work worktree is neither a KB session nor a hygiene orphan.
	if s := ListKBSessions(dir, arch); len(s) != 0 {
		t.Fatalf("work worktree listed as KB session: %v", s)
	}
	for _, it := range poolLevelHygiene(dir, time.Now()) {
		if strings.Contains(it.Message, "architect-work-") {
			t.Fatalf("hygiene must not reap a work worktree: %s", it.Message)
		}
	}
	if nag := WorkNag(dir, arch); !strings.Contains(nag, "meshctl work merge "+req.ID) {
		t.Fatalf("session-start nag: %q", nag)
	}

	brief, err := Brief(dir, arch, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mode: delegated work",
		"## Work contract",
		"Do every workspace edit in `" + wt + "`",
		"meshctl kb begin|finish --agent architect",
		"No dispatch",
		"End with a receipt",
		"meshctl inbox respond " + req.ID + " --agent architect",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("work brief missing %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "No file edits, commits") {
		t.Fatal("work brief must not carry the read-only contract")
	}
}

func TestWorkMerge_HappyPathAndLiveGuard(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	gitInit(t, arch.Dir())
	goLive(t, arch)
	req, _ := askWork(t, dir, caller, arch, "Write the ADR")
	wt := workWorktreePath(dir, "architect", req.ID)
	if err := os.WriteFile(filepath.Join(wt, "adr-split.md"), []byte("split\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "add split ADR")

	if ws := ListWork(dir, "architect"); len(ws) != 1 || ws[0].Ahead != 1 || ws[0].Dirty {
		t.Fatalf("list: %+v", ws)
	}
	// The hub may not move the live peer's checkout.
	if _, err := WorkMerge(dir, req.ID, WorkMergeOptions{Caller: "developer"}); err == nil || !strings.Contains(err.Error(), "live session") {
		t.Fatalf("merge from the hub while the peer is live must be refused, got %v", err)
	}
	if _, err := WorkMerge(dir, req.ID, WorkMergeOptions{Caller: "architect"}); err != nil {
		t.Fatalf("merge from the peer's own session: %v", err)
	}
	if !fileExists(filepath.Join(arch.Dir(), "adr-split.md")) {
		t.Fatal("work did not land on the peer's main")
	}
	if dirExists(wt) {
		t.Fatal("worktree not removed")
	}
	if _, err := git(arch.Dir(), "rev-parse", "--verify", "--quiet", "refs/heads/mesh/work/"+req.ID); err == nil {
		t.Fatal("work branch not deleted")
	}
}

func TestWorkMerge_ConflictLeavesBranchThenAbortDiscards(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	if err := os.WriteFile(filepath.Join(arch.Dir(), "notes.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, arch.Dir())
	goLive(t, arch)
	req, _ := askWork(t, dir, caller, arch, "Rewrite notes")
	wt := workWorktreePath(dir, "architect", req.ID)

	if err := os.WriteFile(filepath.Join(wt, "notes.md"), []byte("from work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, wt, "work edit")
	if err := os.WriteFile(filepath.Join(arch.Dir(), "notes.md"), []byte("from main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, arch.Dir(), "main edit")

	_, err := WorkMerge(dir, req.ID, WorkMergeOptions{Caller: "architect"})
	if !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("want ErrWorkConflict, got %v", err)
	}
	if !dirExists(wt) {
		t.Fatal("conflict must leave the worktree intact")
	}
	if st, _ := git(wt, "status"); strings.Contains(st, "rebase in progress") {
		t.Fatal("conflicted rebase must be aborted")
	}
	// Abort is allowed from the hub even while the peer is live.
	if _, err := WorkMerge(dir, req.ID, WorkMergeOptions{Abort: true, Caller: "developer"}); err != nil {
		t.Fatal(err)
	}
	if dirExists(wt) {
		t.Fatal("abort must remove the worktree")
	}
	if b, _ := os.ReadFile(filepath.Join(arch.Dir(), "notes.md")); string(b) != "from main\n" {
		t.Fatalf("abort must not touch the peer's checkout: %q", b)
	}
}

func TestGateWork_RootsIncludeOwnWorktreesOnly(t *testing.T) {
	dir, _, arch := inSessionPool(t)
	other := newTestAgent(t, dir, "architect-ops", true, nil)
	for _, n := range []string{"architect-work-r1", "architect-s-123", "architect-ops-work-r2"} {
		if err := os.MkdirAll(filepath.Join(dir, WorktreesDir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := peerWorkRoots(dir, arch)
	has := func(n string) bool { return underAny(filepath.Join(dir, WorktreesDir, n, "x"), roots) }
	if !has("architect-work-r1") || !has("architect-s-123") {
		t.Fatalf("own work + KB worktrees must be writable: %v", roots)
	}
	if has("architect-ops-work-r2") {
		t.Fatalf("another agent's worktree (%s) must not be writable: %v", other.Name, roots)
	}
}
