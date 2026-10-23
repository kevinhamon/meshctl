package mesh

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// kb_finish_test.go — the atomic finish (ADR-0067; spec decisions 21, 22,
// 23): a promotion lands on main together with its ledger lines and a
// `Confirm: <Intent line>` commit, or main does not move.

var finishNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

// finishFixture is a peer repo with a live KB session sess-1.
func finishFixture(t *testing.T) (dir string, a *Agent, wt string) {
	t.Helper()
	gitIdentity(t)
	dir = meshRoot(t)
	a = newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	wt, err := KBBegin(dir, a, "sess-1", time.Now())
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	return dir, a, wt
}

// writeWT writes repo-relative files into the worktree (content per pair)
// and commits them.
func writeWT(t *testing.T, wt string, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		p := filepath.Join(wt, filepath.FromSlash(kv[i]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(kv[i+1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, wt, "kb edits")
}

// writeFinishSummary writes a usable summary for the session and returns its path.
func writeFinishSummary(t *testing.T, a *Agent, session, intent string) string {
	t.Helper()
	p := SummaryPath(a.Dir(), session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "## Intent\n" + intent + "\n\n## Grounding\n- read it\n\n## Precedent\n- *new*\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func headSubject(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "log", "-1", "--format=%s", "main"))
}

func TestKBFinish_PromotionWritesLedgerAndConfirmCommit(t *testing.T) {
	dir, a, wt := finishFixture(t)
	writeWT(t, wt,
		"knowledge/playbooks/release.md", "# Release\n\nTag then push.\n",
		"knowledge/decisions/0001-tuesdays.md", "# Tuesdays\n\nStatus: accepted\n",
		"knowledge/candidates/idea.md", "maybe\n")
	sum := writeFinishSummary(t, a, "sess-1", "Record the release playbook.")

	res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
	if err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	if got := headSubject(t, a.Dir()); got != "Confirm: Record the release playbook." {
		t.Fatalf("tip subject = %q", got)
	}
	// The tip commit carries both the merged content and the ledger.
	files := gitOut(t, a.Dir(), "show", "--name-only", "--format=", "main")
	if !strings.Contains(files, "knowledge/"+LedgerFile) {
		t.Fatalf("Confirm commit does not touch the ledger: %q", files)
	}
	for _, f := range []string{"knowledge/playbooks/release.md", "knowledge/decisions/0001-tuesdays.md"} {
		if !strings.Contains(gitOut(t, a.Dir(), "ls-tree", "-r", "--name-only", "main"), f) {
			t.Fatalf("%s not on main", f)
		}
	}
	l, err := ReadLedger(a.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"playbooks/release.md", "decisions/0001-tuesdays.md"} {
		e, ok := l.Entries[rel]
		if !ok || e.Confirmed != "2026-10-18" || e.Session != "sess-1" || !l.Confirmed(a.Dir(), rel) {
			t.Fatalf("ledger line for %s: %+v (ok=%v)", rel, e, ok)
		}
	}
	if _, ok := l.Entries["candidates/idea.md"]; ok {
		t.Fatal("a candidate must never enter the ledger")
	}
	if res.Confirm == "" || !strings.HasPrefix(strings.TrimSpace(gitOut(t, a.Dir(), "rev-parse", "main")), res.Confirm) {
		t.Fatalf("result Confirm sha %q is not main's tip", res.Confirm)
	}
	if dirExists(wt) || gitBranchExists(t, a.Dir(), "mesh/kb/sess-1") {
		t.Fatal("finish left the worktree or branch behind")
	}
	if st := strings.TrimSpace(gitOut(t, a.Dir(), "status", "--porcelain", "--", "knowledge")); st != "" {
		t.Fatalf("main checkout left dirty: %q", st)
	}
}

// kbFinishConfirmed finishes a session that promotes, with a usable summary
// (a promotion needs one since the atomic finish, ADR-0067).
func kbFinishConfirmed(t *testing.T, dir string, a *Agent, session string) error {
	t.Helper()
	gitIdentity(t)
	_, err := KBFinish(dir, a, session, KBFinishOptions{SummaryFile: writeFinishSummary(t, a, session, "Land the test edits.")})
	return err
}

func kbFinishAbort(dir string, a *Agent, session string) error {
	_, err := KBFinish(dir, a, session, KBFinishOptions{Abort: true})
	return err
}

func mainRef(t *testing.T, a *Agent) string {
	t.Helper()
	return gitOut(t, a.Dir(), "rev-parse", "main")
}

func ledgerOnMain(t *testing.T, a *Agent) Ledger {
	t.Helper()
	l, err := ParseLedger([]byte(gitOut(t, a.Dir(), "show", "main:knowledge/"+LedgerFile)))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestKBFinish_CandidatesOnlyWritesNoLedgerWithOrWithoutSummary(t *testing.T) {
	for _, withSummary := range []bool{false, true} {
		dir, a, wt := finishFixture(t)
		before := gitOut(t, a.Dir(), "show", "main:knowledge/"+LedgerFile)
		writeWT(t, wt, "knowledge/candidates/idea.md", "maybe\n")
		o := KBFinishOptions{Now: finishNow}
		if withSummary {
			o.SummaryFile = writeFinishSummary(t, a, "sess-1", "Should be ignored.")
		}
		res, err := KBFinish(dir, a, "sess-1", o)
		if err != nil {
			t.Fatalf("summary=%v: kb finish: %v", withSummary, err)
		}
		if !fileExists(filepath.Join(a.Dir(), "knowledge/candidates/idea.md")) {
			t.Fatal("candidate did not land on main")
		}
		if s := headSubject(t, a.Dir()); strings.HasPrefix(s, "Confirm:") || res.Confirm != "" {
			t.Fatalf("summary=%v: candidates-only finish made a Confirm commit (%q)", withSummary, s)
		}
		if after := gitOut(t, a.Dir(), "show", "main:knowledge/"+LedgerFile); after != before {
			t.Fatalf("summary=%v: ledger changed: %q → %q", withSummary, before, after)
		}
	}
}

func TestKBFinish_PromotionWithoutUsableSummaryRefusesBeforeMain(t *testing.T) {
	cases := map[string]string{"no flag": "", "no file": "missing", "empty": "", "no intent": "## Grounding\n- x\n"}
	for name, body := range cases {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
		pre := mainRef(t, a)
		o := KBFinishOptions{}
		switch name {
		case "no file":
			o.SummaryFile = filepath.Join(t.TempDir(), "nope.md")
		case "empty", "no intent":
			o.SummaryFile = filepath.Join(t.TempDir(), "s.md")
			if err := os.WriteFile(o.SummaryFile, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := KBFinish(dir, a, "sess-1", o)
		if !errors.Is(err, ErrKBSummaryRequired) {
			t.Fatalf("%s: want ErrKBSummaryRequired, got %v", name, err)
		}
		if !strings.Contains(err.Error(), SummaryPath(a.Dir(), "sess-1")) {
			t.Fatalf("%s: refusal must name the summary path: %v", name, err)
		}
		if mainRef(t, a) != pre || !dirExists(wt) || !gitBranchExists(t, a.Dir(), "mesh/kb/sess-1") {
			t.Fatalf("%s: refusal touched main or removed the branch/worktree", name)
		}
	}
}

func TestKBFinish_InjectedFailureResetsMainAndKeepsBranch(t *testing.T) {
	for _, stage := range []string{"merged", "ledger-written"} {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
		sum := writeFinishSummary(t, a, "sess-1", "Record the release playbook.")
		pre := mainRef(t, a)
		ledgerBefore, _ := os.ReadFile(LedgerPath(a.Dir()))

		kbFinishFailpoint = func(s string) error {
			if s == stage {
				return errors.New("injected " + s)
			}
			return nil
		}
		_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
		kbFinishFailpoint = nil

		if !errors.Is(err, ErrKBConfirmFailed) {
			t.Fatalf("%s: want ErrKBConfirmFailed, got %v", stage, err)
		}
		for _, want := range []string{"pre-merge", "mesh/kb/sess-1", wt} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: error must name the state (%q): %v", stage, want, err)
			}
		}
		if got := mainRef(t, a); got != pre {
			t.Fatalf("%s: main at %s, want pre-merge %s", stage, got, pre)
		}
		if !dirExists(wt) || !gitBranchExists(t, a.Dir(), "mesh/kb/sess-1") {
			t.Fatalf("%s: branch or worktree removed", stage)
		}
		if fileExists(filepath.Join(a.Dir(), "knowledge/playbooks/release.md")) {
			t.Fatalf("%s: merged content left in the main checkout", stage)
		}
		if after, _ := os.ReadFile(LedgerPath(a.Dir())); string(after) != string(ledgerBefore) {
			t.Fatalf("%s: ledger file left modified: %q", stage, after)
		}
		if st := gitOut(t, a.Dir(), "status", "--porcelain", "--", "knowledge"); st != "" {
			t.Fatalf("%s: main checkout dirty after reset: %q", stage, st)
		}
		// The intact branch finishes cleanly on the retry.
		if _, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow}); err != nil {
			t.Fatalf("%s: retry: %v", stage, err)
		}
		if !strings.HasPrefix(headSubject(t, a.Dir()), "Confirm:") {
			t.Fatalf("%s: retry did not confirm", stage)
		}
	}
}

// promoteOnMain lands kv files on main through a confirmed finish of session
// s0 and begins sess-1 on top of it.
func promoteOnMain(t *testing.T, dir string, a *Agent, kv ...string) string {
	t.Helper()
	wt0, err := KBBegin(dir, a, "s0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeWT(t, wt0, kv...)
	if _, err := KBFinish(dir, a, "s0", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "s0", "Seed."), Now: finishNow}); err != nil {
		t.Fatalf("seed finish: %v", err)
	}
	wt, err := KBBegin(dir, a, "sess-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return wt
}

const stateTwoSections = "## Current focus\n\nShipping v2.\n\n## Open questions\n\n- none\n"

func TestKBFinish_DeletionDropsLedgerLineInConfirmCommit(t *testing.T) {
	gitIdentity(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	wt := promoteOnMain(t, dir, a,
		"knowledge/playbooks/old.md", "# Old\n",
		"knowledge/"+StateFile, stateTwoSections)
	l := ledgerOnMain(t, a)
	if _, ok := l.Entries["playbooks/old.md"]; !ok || len(l.Entries[StateFile].Sections) != 2 {
		t.Fatalf("seed ledger: %+v", l.Entries)
	}

	if err := os.Remove(filepath.Join(wt, "knowledge/playbooks/old.md")); err != nil {
		t.Fatal(err)
	}
	writeWT(t, wt, "knowledge/"+StateFile, "## Current focus\n\nShipping v2.\n")
	res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Retire the old playbook."), Now: finishNow})
	if err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	l = ledgerOnMain(t, a)
	if _, ok := l.Entries["playbooks/old.md"]; ok {
		t.Fatal("deleted file kept its ledger line")
	}
	if _, ok := l.Entries[StateFile].Sections["open-questions"]; ok {
		t.Fatal("deleted section kept its ledger line")
	}
	if !l.SectionConfirmed(a.Dir(), StateFile, "current-focus") {
		t.Fatal("untouched section lost its confirmation")
	}
	// Both drops ride the one Confirm: commit.
	if !strings.HasPrefix(headSubject(t, a.Dir()), "Confirm: Retire the old playbook.") ||
		!strings.Contains(gitOut(t, a.Dir(), "show", "--name-only", "--format=", "main"), LedgerFile) {
		t.Fatal("the drop is not in the tip Confirm: commit")
	}
	if strings.Join(res.Dropped, ",") != "playbooks/old.md,state.md#open-questions" {
		t.Fatalf("Dropped = %v", res.Dropped)
	}
}

func TestKBFinish_SectionedEditRehashesOnlyChangedSections(t *testing.T) {
	gitIdentity(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	gitInit(t, a.Dir())
	wt := promoteOnMain(t, dir, a, "knowledge/"+StateFile, stateTwoSections)

	// Unconfirm "open questions" on main by hand: its ledger hash is old.
	l, _ := ReadLedger(a.Dir())
	l.Entries[StateFile].Sections["open-questions"] = "sha256:old"
	if err := WriteLedger(a.Dir(), l); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, a.Dir(), "hand-edited ledger")
	if _, err := git(wt, "rebase", "main"); err != nil {
		t.Fatal(err)
	}

	writeWT(t, wt, "knowledge/"+StateFile, "## Current focus\n\nShipping v3.\n\n## Open questions\n\n- none\n")
	res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Move focus to v3."), Now: finishNow})
	if err != nil {
		t.Fatalf("kb finish: %v", err)
	}
	l = ledgerOnMain(t, a)
	if !l.SectionConfirmed(a.Dir(), StateFile, "current-focus") {
		t.Fatal("the edited section was not re-hashed")
	}
	if got := l.Entries[StateFile].Sections["open-questions"]; got != "sha256:old" {
		t.Fatalf("an unchanged section was re-hashed: %q", got)
	}
	if strings.Join(res.Ledgered, ",") != "state.md#current-focus" {
		t.Fatalf("Ledgered = %v", res.Ledgered)
	}
}

func TestKBFinish_TrustGuardRefusesPromotionOnly(t *testing.T) {
	cases := map[string]func(t *testing.T, a *Agent){
		"gate absent": func(t *testing.T, a *Agent) {
			if err := os.WriteFile(filepath.Join(a.Dir(), ".claude", "settings.json"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"wiring stale": func(t *testing.T, a *Agent) {
			if err := os.WriteFile(filepath.Join(a.Dir(), ".claude", meshWiringStampFile), []byte(`{"version": 4}`), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"opencode": func(t *testing.T, a *Agent) {
			b, _ := os.ReadFile(ManifestPath(filepath.Dir(a.Dir()), a.Name))
			b = regexp.MustCompile(`(?m)^harness:.*$`).ReplaceAll(b, []byte("harness: opencode"))
			if err := os.WriteFile(ManifestPath(filepath.Dir(a.Dir()), a.Name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, breakIt := range cases {
		dir, a, wt := finishFixture(t)
		breakIt(t, a)
		var err error
		if a, err = FindAgent(dir, a.Name); err != nil {
			t.Fatalf("%s: reload: %v", name, err)
		}
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
		pre := mainRef(t, a)
		_, err = KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Promote."), Now: finishNow})
		if !errors.Is(err, ErrKBTrustGuard) || !strings.Contains(err.Error(), "trust guard") {
			t.Fatalf("%s: want a trust-guard refusal, got %v", name, err)
		}
		if mainRef(t, a) != pre || !dirExists(wt) {
			t.Fatalf("%s: refusal touched main or the worktree", name)
		}

		// A candidates-only finish still passes.
		if _, err := KBFinish(dir, a, "sess-1", KBFinishOptions{Abort: true}); err != nil {
			t.Fatal(err)
		}
		wt, err = KBBegin(dir, a, "sess-2", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		writeWT(t, wt, "knowledge/candidates/idea.md", "maybe\n")
		if _, err := KBFinish(dir, a, "sess-2", KBFinishOptions{}); err != nil {
			t.Fatalf("%s: candidates-only finish refused: %v", name, err)
		}
	}
}

func TestKBFinish_SessionListingAndReceiptShowConfirmCommit(t *testing.T) {
	dir, a, wt := finishFixture(t)
	started := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
	res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Record the release playbook."), Now: finishNow})
	if err != nil {
		t.Fatal(err)
	}
	want := res.Confirm + " Confirm: Record the release playbook."
	if got := KBConfirmCommits(a.Dir(), "sess-1"); len(got) != 1 || got[0] != want {
		t.Fatalf("KBConfirmCommits = %v, want [%q]", got, want)
	}
	if got := KBConfirmCommits(a.Dir(), "sess-10"); len(got) != 0 {
		t.Fatalf("another session's listing shows %v", got)
	}
	if _, err := CaptureSession(a, "", "sess-1", started, time.Now()); err != nil {
		t.Fatal(err)
	}
	recs := ListSessionRecords(a)
	if len(recs) != 1 || len(recs[0].Confirms) != 1 || recs[0].Confirms[0] != want {
		t.Fatalf("session listing confirms = %+v", recs)
	}
}

// TestKBFinish_RefusalsAndParkLeaveTheBranchUnrebased: the refusals that do
// not depend on the rebase (summary, trust guard, ledger edit) and a park
// run before it, so main having moved on leaves the branch exactly as the
// peer committed it.
func TestKBFinish_RefusalsAndParkLeaveTheBranchUnrebased(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, a *Agent, wt string) KBFinishOptions
		want  error
	}{
		"no summary": {func(t *testing.T, a *Agent, wt string) KBFinishOptions {
			return KBFinishOptions{Now: finishNow}
		}, ErrKBSummaryRequired},
		"trust guard": {func(t *testing.T, a *Agent, wt string) KBFinishOptions {
			if err := os.WriteFile(filepath.Join(a.Dir(), ".claude", "settings.json"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Promote."), Now: finishNow}
		}, ErrKBTrustGuard},
		"headless park": {func(t *testing.T, a *Agent, wt string) KBFinishOptions {
			return KBFinishOptions{Headless: true, SummaryFile: writeFinishSummary(t, a, "sess-1", "Promote."), Now: finishNow}
		}, ErrKBNeedsConfirm},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, a, wt := finishFixture(t)
			writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
			// main moves on after the branch forked.
			if err := os.WriteFile(filepath.Join(a.Dir(), "unrelated.md"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCommitAll(t, a.Dir(), "main moves on")
			o := c.setup(t, a, wt)
			if a2, err := FindAgent(dir, a.Name); err == nil {
				a = a2
			}
			tip := gitOut(t, a.Dir(), "rev-parse", "mesh/kb/sess-1")
			_, err := KBFinish(dir, a, "sess-1", o)
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
			if got := gitOut(t, a.Dir(), "rev-parse", "mesh/kb/sess-1"); got != tip {
				t.Fatalf("branch rebased by a refused finish: %s → %s", tip, got)
			}
		})
	}
	t.Run("ledger edit", func(t *testing.T) {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n", "knowledge/"+LedgerFile, "tampered\n")
		if err := os.WriteFile(filepath.Join(a.Dir(), "unrelated.md"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCommitAll(t, a.Dir(), "main moves on")
		tip := gitOut(t, a.Dir(), "rev-parse", "mesh/kb/sess-1")
		_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Promote."), Now: finishNow})
		if err == nil || !strings.Contains(err.Error(), "ledger is written only by meshctl") {
			t.Fatalf("want ledger-edit refusal, got %v", err)
		}
		if got := gitOut(t, a.Dir(), "rev-parse", "mesh/kb/sess-1"); got != tip {
			t.Fatalf("branch rebased by a refused finish: %s → %s", tip, got)
		}
	})
}
