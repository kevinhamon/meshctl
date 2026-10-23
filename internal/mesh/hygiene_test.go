package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hygieneMessages(items []HygieneItem) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.Agent + ": " + it.Message + "\n")
	}
	return b.String()
}

func TestPoolHygiene_FindsAndFixesDrift(t *testing.T) {
	t.Setenv("AGENT_DISPATCH_STALE_SEC", "60")
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)

	// Pool: an old doctrine backup, an .upstream copy, a dead-holder lock.
	doc := DoctrinePath(dir)
	_ = os.MkdirAll(doc, 0o755)
	bak := filepath.Join(doc, "agent-comms.md.bak-20250101")
	_ = os.WriteFile(bak, []byte("x"), 0o644)
	_ = os.Chtimes(bak, old, old)
	up := filepath.Join(doc, "agent-comms.md"+upstreamSuffix)
	_ = os.WriteFile(up, []byte("x"), 0o644)
	lock := DispatchLockPath(dir)
	_ = os.MkdirAll(lock, 0o755)
	_ = os.WriteFile(filepath.Join(lock, "info"), []byte("pid=999999999 caller=x target=y\n"), 0o644)
	_ = os.Chtimes(lock, old, old)

	// Agent: stale claim, stale session, retired index, old nudge marker, legacy status.
	req, _ := WriteRequest(a, "guidance", "x", "u", "P2", nil, now)
	_ = os.WriteFile(claimPath(req.Path()), []byte(`{"owner_session":"dead","pid":1,"heartbeat":1}`), 0o644)
	_ = os.MkdirAll(sessionsDir(a.Dir()), 0o755)
	_ = os.WriteFile(sessionPath(a.Dir(), "dead"), []byte(`{"id":"dead","pid":1,"started":1,"last_beat":1}`), 0o644)
	_ = os.MkdirAll(filepath.Join(a.Dir(), MemoryDir, ruleAckDir), 0o755)
	_ = os.WriteFile(filepath.Join(a.Dir(), MemoryDir, MemoryIndexFile), []byte("{}"), 0o644)
	nudge := filepath.Join(a.Dir(), MemoryDir, ".distill-nudge-s9.json")
	_ = os.WriteFile(nudge, []byte("{}"), 0o644)
	_ = os.Chtimes(nudge, old, old)
	ack := filepath.Join(a.Dir(), MemoryDir, ruleAckDir, "s9.json")
	_ = os.WriteFile(ack, []byte("{}"), 0o644)
	_ = os.Chtimes(ack, old, old)
	b, _ := os.ReadFile(req.Path())
	_ = os.WriteFile(req.Path(), []byte(strings.Replace(string(b), "status: pending", "status: done   # pending | done", 1)), 0o644)
	// Hand-written file referencing the retired CLI: manual item.
	_ = os.WriteFile(filepath.Join(a.Dir(), "CLAUDE.md"), []byte("# a\n\nrun `agentmesh memory recall --project architect`\n"), 0o644)

	items := PoolHygiene(dir, now)
	msgs := hygieneMessages(items)
	for _, want := range []string{
		"doctrine backup", "differs from this meshctl's copy", "dispatch lock held by a dead process",
		"stale claim", "stale session record", "retired .memory/index.json",
		"distillation nudge marker", "KB-rule ack", `status "done" → "answered"`,
		"CLAUDE.md:3 references the retired",
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing hygiene item %q in:\n%s", want, msgs)
		}
	}
	manual := ManualItems(items)
	if len(manual) != 2 {
		t.Fatalf("want 2 manual items (upstream doctrine, retired CLI ref), got %d:\n%s", len(manual), hygieneMessages(manual))
	}

	fixed, errs := ApplyHygiene(items)
	if len(errs) != 0 {
		t.Fatalf("fix errors: %v", errs)
	}
	if len(fixed) != len(items)-len(manual) {
		t.Fatalf("fixed %d of %d fixable", len(fixed), len(items)-len(manual))
	}
	for _, gone := range []string{bak, lock, claimPath(req.Path()), sessionPath(a.Dir(), "dead"),
		filepath.Join(a.Dir(), MemoryDir, MemoryIndexFile), nudge, ack} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s should have been removed", gone)
		}
	}
	if _, err := os.Stat(up); err != nil {
		t.Error("upstream doctrine copy is a manual item and must be kept")
	}
	r, _ := ReadRequest(req.Path())
	if r.Status != "answered" {
		t.Fatalf("legacy status not normalised: %q", r.Status)
	}

	// Second pass: only the manual items remain.
	again := PoolHygiene(dir, now)
	if len(again) != 2 {
		t.Fatalf("hygiene should be idempotent; second pass found:\n%s", hygieneMessages(again))
	}
}

func TestPoolHygiene_CleanPoolIsEmpty(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	if items := PoolHygiene(dir, time.Now()); len(items) != 0 {
		t.Fatalf("fresh pool should be clean, got:\n%s", hygieneMessages(items))
	}
}

func TestPoolHygiene_OrphanWorktreeRemoved(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	_ = os.WriteFile(filepath.Join(a.Dir(), "knowledge", "README.md"), []byte("# kb\n"), 0o644)
	gitCommitAll(t, a.Dir(), "init")
	now := time.Now()
	if _, err := KBBegin(dir, a, "s-orphan", now); err != nil {
		t.Fatal(err)
	}
	wt := worktreePath(dir, a.Name, "s-orphan")
	if !dirExists(wt) {
		t.Fatal("worktree not created")
	}
	// No live session owns it ⇒ orphan.
	items := PoolHygiene(dir, now)
	if !strings.Contains(hygieneMessages(items), "orphan KB worktree") {
		t.Fatalf("orphan worktree not detected:\n%s", hygieneMessages(items))
	}
	if _, errs := ApplyHygiene(items); len(errs) != 0 {
		t.Fatal(errs)
	}
	if dirExists(wt) {
		t.Fatal("orphan worktree not removed")
	}
}

// TestPoolHygiene_LegacyKBBranchRenamed: a legacy session/<id> branch that no
// worktree references is a fixable item; the fix renames it to mesh/kb/<id>
// (spec decision 17). One still checked out in a worktree is left to kb finish.
func TestPoolHygiene_LegacyKBBranchRenamed(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	now := time.Now()

	if _, err := git(a.Dir(), "branch", "session/old"); err != nil {
		t.Fatal(err)
	}
	wtHeld := legacyKBWorktree(t, dir, a, "held")

	items := PoolHygiene(dir, now)
	msgs := hygieneMessages(items)
	if !strings.Contains(msgs, "legacy KB branch session/old") {
		t.Fatalf("legacy branch without a worktree not listed:\n%s", msgs)
	}
	if strings.Contains(msgs, "legacy KB branch session/held") {
		t.Fatalf("legacy branch with a worktree must not be renamed by hygiene:\n%s", msgs)
	}
	for _, it := range items {
		if strings.Contains(it.Message, "legacy KB branch") && !it.Fixable() {
			t.Fatal("legacy branch rename must be fixable")
		}
	}
	// Only the rename: the held worktree stays (its orphan item is a separate concern).
	var rename []HygieneItem
	for _, it := range items {
		if strings.Contains(it.Message, "legacy KB branch") {
			rename = append(rename, it)
		}
	}
	if _, errs := ApplyHygiene(rename); len(errs) != 0 {
		t.Fatal(errs)
	}
	if gitBranchExists(t, a.Dir(), "session/old") || !gitBranchExists(t, a.Dir(), "mesh/kb/old") {
		t.Fatal("fix did not rename session/old to mesh/kb/old")
	}
	if !gitBranchExists(t, a.Dir(), "session/held") || !dirExists(wtHeld) {
		t.Fatal("fix must not touch the branch a worktree holds")
	}
	if strings.Contains(hygieneMessages(PoolHygiene(dir, now)), "legacy KB branch session/old") {
		t.Fatal("rename is not idempotent: session/old still listed")
	}
}
