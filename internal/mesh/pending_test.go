package mesh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pending_test.go — the session-start pending-work block and `meshctl
// pending`.

// pendingLine returns the block's line that opens with the given label, "".
func pendingLine(block, label string) string {
	for _, ln := range strings.Split(block, "\n") {
		if strings.HasPrefix(ln, "- **"+label+"**") {
			return ln
		}
	}
	return ""
}

func writeCandidate(t *testing.T, a *Agent, name string) string {
	t.Helper()
	p := filepath.Join(a.Dir(), KnowledgeDir, "candidates", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# "+name+"\n\nA proposal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ageFile(t *testing.T, p string, days int) {
	t.Helper()
	at := time.Now().AddDate(0, 0, -days).Add(-time.Hour)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// pendingPool: architect with one parked KB branch, two pending requests, one
// unread response to a request it sent, one delegated work branch and three
// candidates.
func pendingPool(t *testing.T) (dir string, dev, arch *Agent, reqs []*Request) {
	t.Helper()
	gitIdentity(t)
	dir, dev, arch = inSessionPool(t)
	gitInit(t, arch.Dir())
	now := time.Now()

	// Parked needs-confirm branch: a headless promotion.
	wt, err := KBBegin(dir, arch, "sess-1", now)
	if err != nil {
		t.Fatal(err)
	}
	writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n\nTag then push.\n")
	sum := writeFinishSummary(t, arch, "sess-1", "Record the release playbook.")
	if _, err := KBFinish(dir, arch, "sess-1", KBFinishOptions{Headless: true, SummaryFile: sum, Now: now}); err == nil {
		t.Fatal("headless promotion must park")
	}

	// Delegated work (live peer ⇒ worktree).
	goLive(t, arch)
	askWork(t, dir, dev, arch, "Write the ADR for the split")

	// Two pending requests.
	r1, _ := WriteRequest(arch, "guidance", "Should we split the service?", "developer", "P2", nil, now.AddDate(0, 0, -3))
	r2, _ := WriteRequest(arch, "guidance", "Which queue do we use?", "user", "P2", nil, now)

	// One answer awaiting read-back on a request architect sent.
	sent, _ := WriteRequest(dev, "guidance", "Is the build green?", "architect", "P2", nil, now)
	if _, err := RespondRequest(dev, sent.ID, RespondOptions{Status: "answered", Response: "yes", Now: now}); err != nil {
		t.Fatal(err)
	}

	for _, n := range []string{"a.md", "b.md", "c.md"} {
		writeCandidate(t, arch, n)
	}
	return dir, dev, arch, []*Request{r1, r2, sent}
}

func TestHook_SessionStart_PendingBlockFiveLinesInOrder(t *testing.T) {
	dir, _, arch, reqs := pendingPool(t)
	now := time.Now()
	ctx := HookSessionStart(dir, HookPayload{Cwd: arch.Dir(), SessionID: "s"}, 1, now)

	if !strings.Contains(ctx, "# Pending work (") {
		t.Fatalf("no pending block:\n%s", ctx)
	}
	labels := []string{"KB branches needs-confirm", "Intake", "Read-back", "Delegated work", "KB health"}
	last := -1
	for _, l := range labels {
		i := strings.Index(ctx, "- **"+l+"**")
		if i < 0 {
			t.Fatalf("missing %q line:\n%s", l, ctx)
		}
		if i < last {
			t.Fatalf("%q out of order:\n%s", l, ctx)
		}
		last = i
	}
	if pendingLine(ctx, "Open conflicts") != "" {
		t.Errorf("no reconcile report ⇒ no conflicts line:\n%s", ctx)
	}

	kb := pendingLine(ctx, "KB branches needs-confirm")
	for _, want := range []string{"1,", "oldest 0d", "mesh/kb/sess-1", SummaryPath(arch.Dir(), "sess-1"), "meshctl kb finish --agent architect --session sess-1"} {
		if !strings.Contains(kb, want) {
			t.Errorf("KB line missing %q: %s", want, kb)
		}
	}
	in := pendingLine(ctx, "Intake")
	for _, want := range []string{"2 pending", "oldest 3d", reqs[0].ID, "from developer", reqs[1].ID, "from user", "`meshctl inbox list`", "propose an order and wait", "`meshctl inbox claim <id>`"} {
		if !strings.Contains(in, want) {
			t.Errorf("intake line missing %q: %s", want, in)
		}
	}
	if strings.Contains(in, "inbox next") {
		t.Errorf("the intake line must not claim the oldest request: %s", in)
	}
	if strings.Contains(in, "triage signals") {
		t.Errorf("no signals ⇒ no triage clause: %s", in)
	}
	rb := pendingLine(ctx, "Read-back")
	for _, want := range []string{"1,", reqs[2].ID, "developer", "answered", "meshctl sent ack"} {
		if !strings.Contains(rb, want) {
			t.Errorf("read-back line missing %q: %s", want, rb)
		}
	}
	dw := pendingLine(ctx, "Delegated work")
	for _, want := range []string{"1,", "mesh/work/", "meshctl work merge"} {
		if !strings.Contains(dw, want) {
			t.Errorf("work line missing %q: %s", want, dw)
		}
	}
	if h := pendingLine(ctx, "KB health"); !strings.Contains(h, "3 candidates") || !strings.Contains(h, "meshctl kb consolidate") {
		t.Errorf("health line: %s", h)
	}
	if strings.Contains(ctx, "⚠ stale") {
		t.Errorf("nothing is past 14 days:\n%s", ctx)
	}

	// The old separate sections are gone; the repo warning stays above.
	for _, gone := range []string{"delegated work branch pending merge", "# Responses to your requests"} {
		if strings.Contains(ctx, gone) {
			t.Errorf("old section %q still rendered:\n%s", gone, ctx)
		}
	}
	if i, j := strings.Index(ctx, "no git remote"), strings.Index(ctx, "# Pending work"); i < 0 || j < i {
		t.Errorf("repo warning must render above the block:\n%s", ctx)
	}

	// `meshctl pending` prints the same text the hook injects.
	block := PendingMarkdown(dir, arch, now)
	if block == "" || !strings.Contains(ctx, block) {
		t.Fatalf("pending output is not the injected block:\n%s\n---\n%s", block, ctx)
	}
}

func TestPending_AboveThreeShowsCountAndListCommand(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	var ids []string
	for i, p := range []string{"one", "two", "three", "four"} {
		r, err := WriteRequest(a, "guidance", "ask "+p, "user", "P2", nil, now.AddDate(0, 0, -i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	in := pendingLine(PendingMarkdown(dir, a, now), "Intake")
	for _, want := range []string{"4 pending", "oldest 3d", "meshctl inbox list"} {
		if !strings.Contains(in, want) {
			t.Errorf("intake line missing %q: %s", want, in)
		}
	}
	for _, id := range ids {
		if strings.Contains(in, id) {
			t.Errorf("above three, items are not listed inline: %s", in)
		}
	}
	if !strings.Contains(PendingMarkdown(dir, a, now), "# Pending work (4 items)") {
		t.Errorf("header: %s", PendingMarkdown(dir, a, now))
	}
}

func TestPending_StaleThresholdIs14Days(t *testing.T) {
	gitIdentity(t)
	dir, dev, arch := inSessionPool(t)
	gitInit(t, arch.Dir())
	now := time.Now()

	old, _ := WriteRequest(arch, "guidance", "old ask", "user", "P2", nil, now.AddDate(0, 0, -20))
	WriteRequest(arch, "guidance", "week-old ask", "user", "P2", nil, now.AddDate(0, 0, -7))
	goLive(t, arch)
	req, _ := askWork(t, dir, dev, arch, "Old work")
	ageFile(t, workWorktreePath(dir, "architect", req.ID), 20)
	wt, err := KBBegin(dir, arch, "sess-old", now)
	if err != nil {
		t.Fatal(err)
	}
	writeWT(t, wt, "knowledge/playbooks/x.md", "# X\n\nx\n")
	if _, err := KBFinish(dir, arch, "sess-old", KBFinishOptions{Headless: true, Now: now}); !errors.Is(err, ErrKBNeedsConfirm) {
		t.Fatalf("want park, got %v", err)
	}
	ageFile(t, ParkMarkerPath(arch.Dir(), "sess-old"), 20) // the park's age, not the worktree's
	ageFile(t, writeCandidate(t, arch, "aged.md"), 20)

	block := PendingMarkdown(dir, arch, now)
	in := pendingLine(block, "Intake")
	if !strings.Contains(in, "`"+old.ID+"` from user · 20d ⚠ stale") {
		t.Errorf("20-day request must render stale: %s", in)
	}
	if strings.Contains(in, "7d ⚠ stale") {
		t.Errorf("7-day request is not stale: %s", in)
	}
	for _, l := range []string{"Delegated work", "KB branches needs-confirm"} {
		if ln := pendingLine(block, l); !strings.Contains(ln, "20d ⚠ stale") {
			t.Errorf("%s line must render stale: %s", l, ln)
		}
	}
	if h := pendingLine(block, "KB health"); !strings.Contains(h, "1 past 14 days") {
		t.Errorf("health line must count aged candidates: %s", h)
	}
}

func TestPending_NothingPendingNoBlock(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	if got := PendingMarkdown(dir, a, time.Now()); got != "" {
		t.Fatalf("nothing pending ⇒ no block, got %q", got)
	}
}

func TestPending_HealthLineOnlyWithCandidatesOrReport(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	p := filepath.Join(a.Dir(), KnowledgeDir, "reference", "renewal.md")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("# Renewal\n\nWe net daily.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(UnconfirmedCurrentEntries(a.Dir())) == 0 {
		t.Fatal("fixture: want an unconfirmed entry")
	}
	if got := PendingMarkdown(dir, a, now); got != "" {
		t.Fatalf("unconfirmed entries alone (no candidates, no report) ⇒ no block, got %q", got)
	}
	writeReconcileReport(t, a, now.AddDate(0, 0, -3).Format("2006-01-02"), "- reference/renewal.md · consistent · \"we renew daily\"\n")
	h := pendingLine(PendingMarkdown(dir, a, now), "KB health")
	if !strings.Contains(h, "unconfirmed") || !strings.Contains(h, "reconcile report 3d ago") {
		t.Fatalf("report present ⇒ health line, got %q", h)
	}
	if strings.Contains(PendingMarkdown(dir, a, now), "Open conflicts") {
		t.Error("no open conflicts ⇒ no conflicts line")
	}

	// An open conflict is an action kind.
	writeReconcileReport(t, a, now.AddDate(0, 0, -2).Format("2006-01-02"), "- reference/renewal.md · conflict → mesh://bravo/reference/renewal.md · \"we renew daily\" vs \"bravo renews weekly\"\n")
	c := pendingLine(PendingMarkdown(dir, a, now), "Open conflicts")
	for _, want := range []string{"1,", "reference/renewal.md", "2d", "meshctl kb confirm"} {
		if !strings.Contains(c, want) {
			t.Errorf("conflicts line missing %q: %s", want, c)
		}
	}
}

func TestPending_TriageSignalClause(t *testing.T) {
	gitIdentity(t)
	dir := meshRoot(t)
	arch := newTestAgent(t, dir, "architect", true, nil)
	dev := newTestAgent(t, dir, "developer", true, nil)
	qa := newTestAgent(t, dir, "qa", true, nil)
	now := time.Now()

	plain, _ := WriteRequest(arch, "guidance", "plain ask", "user", "P2", nil, now)
	if in := pendingLine(PendingMarkdown(dir, arch, now), "Intake"); strings.Contains(in, "triage signals") {
		t.Fatalf("no signal ⇒ no clause: %s", in)
	}

	// Signal 1: the request's id names a mesh/work/* branch in another peer.
	worked, _ := WriteRequest(arch, "guidance", "worked ask", "user", "P2", nil, now)
	gitInit(t, dev.Dir())
	gitOut(t, dev.Dir(), "branch", WorkBranchPrefix+worked.ID)

	// Signal 2: a related request was redirected and its target closed.
	orig, _ := WriteRequest(dev, "guidance", "redirected ask", "user", "P2", nil, now)
	_, fwd, err := DeclineRequest(dir, dev, orig.ID, DeclineOptions{Reason: "qa owns it", RedirectTo: "qa", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RespondRequest(qa, fwd.ID, RespondOptions{Status: "answered", Response: "done", Now: now}); err != nil {
		t.Fatal(err)
	}
	WriteRequest(arch, "guidance", "same ask here", "user", "P2", []string{"developer:" + orig.ID}, now)

	in := pendingLine(PendingMarkdown(dir, arch, now), "Intake")
	if !strings.Contains(in, "3 pending") || !strings.Contains(in, "2 have triage signals, run meshctl inbox triage") {
		t.Fatalf("want 2 of 3 with triage signals: %s", in)
	}
	_ = plain
}

func TestHook_SessionStart_PoolWarningAbovePendingBlock(t *testing.T) {
	root := migratedRoot(t)
	a := newTestAgent(t, root, "architect", true, nil)
	gitOut(t, a.Dir(), "remote", "add", "origin", "https://example.test/x.git")
	WriteRequest(a, "guidance", "an ask", "user", "P2", nil, time.Now())
	ctx := HookSessionStart(root, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now())
	i, j := strings.Index(ctx, "pool directory pool/"), strings.Index(ctx, "# Pending work (1 items)")
	if i < 0 || j < 0 || j < i {
		t.Fatalf("pool warning must render above the block:\n%s", ctx)
	}
}

func TestPending_OmittedHeadlessAndInBrief(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	r, _ := WriteRequest(a, "guidance", "an ask", "user", "P2", nil, time.Now())
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now()); !strings.Contains(ctx, "# Pending work") {
		t.Fatalf("interactive ⇒ block:\n%s", ctx)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s2"}, 1, time.Now()); strings.Contains(ctx, "# Pending work") {
		t.Fatalf("dispatch depth ⇒ no block:\n%s", ctx)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv(distillGuardEnv, "1")
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s3"}, 1, time.Now()); strings.Contains(ctx, "# Pending work") {
		t.Fatalf("distilling ⇒ no block:\n%s", ctx)
	}
	t.Setenv(distillGuardEnv, "")
	brief, err := Brief(dir, a, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(brief, "# Pending work") {
		t.Fatalf("agent brief carries no pending block:\n%s", brief)
	}
}

func TestPending_AllPrintsPerPeerSkippingEmpty(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	newTestAgent(t, dir, "developer", true, nil)
	q := newTestAgent(t, dir, "qa", true, nil)
	now := time.Now()
	WriteRequest(a, "guidance", "an ask", "user", "P2", nil, now)
	WriteRequest(q, "guidance", "another ask", "user", "P2", nil, now)
	out, err := PendingAllMarkdown(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## architect\n\n"+PendingMarkdown(dir, a, now)) || !strings.Contains(out, "## qa\n\n"+PendingMarkdown(dir, q, now)) {
		t.Fatalf("want one block per peer under a heading:\n%s", out)
	}
	if strings.Contains(out, "## developer") {
		t.Fatalf("peer with nothing pending must be skipped:\n%s", out)
	}
	if strings.Index(out, "## architect") > strings.Index(out, "## qa") {
		t.Fatalf("peers in roster order:\n%s", out)
	}
}

// Doctor's branch-age warning: 14 days, not 7, for
// delegated-work and KB branches; an unmerged KB branch is never reaped.
func TestPoolHygiene_KBBranchAgeIs14Days(t *testing.T) {
	gitIdentity(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	wt, err := KBBegin(dir, a, "sess-old", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeWT(t, wt, "knowledge/playbooks/x.md", "# X\n\nx\n")
	find := func(now time.Time) []HygieneItem {
		var out []HygieneItem
		for _, it := range PoolHygiene(dir, now) {
			if strings.Contains(it.Message, "sess-old") {
				out = append(out, it)
			}
		}
		return out
	}
	if got := find(time.Now().Add(8 * 24 * time.Hour)); len(got) != 0 {
		t.Fatalf("an 8-day unmerged KB branch is not warned: %s", hygieneMessages(got))
	}
	got := find(time.Now().Add(15 * 24 * time.Hour))
	if len(got) != 1 || got[0].Fixable() || !strings.Contains(got[0].Message, "mesh/kb/sess-old") || !strings.Contains(got[0].Message, "15 days") {
		t.Fatalf("a 15-day unmerged KB branch is a manual warning: %s", hygieneMessages(got))
	}
}
