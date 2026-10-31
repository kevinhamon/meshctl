package mesh

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// kb_needs_confirm_test.go — headless and delegated promotion parks as
// `needs-confirm`.

// assertParked checks a finish refused with needs-confirm and left the branch,
// the worktree and main alone.
func assertParked(t *testing.T, a *Agent, wt, before string, err error) *KBNeedsConfirmError {
	t.Helper()
	if !errors.Is(err, ErrKBNeedsConfirm) {
		t.Fatalf("want ErrKBNeedsConfirm, got %v", err)
	}
	var nc *KBNeedsConfirmError
	if !errors.As(err, &nc) {
		t.Fatalf("want *KBNeedsConfirmError, got %T", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, NeedsConfirmPrefix) {
		t.Errorf("message does not open %q: %q", NeedsConfirmPrefix, msg)
	}
	sum := SummaryPath(a.Dir(), "sess-1")
	for _, want := range []string{"mesh/kb/sess-1", sum} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not name %q: %q", want, msg)
		}
	}
	if !dirExists(wt) {
		t.Error("worktree removed")
	}
	if strings.TrimSpace(gitOut(t, a.Dir(), "branch", "--list", "mesh/kb/sess-1")) == "" {
		t.Error("branch removed")
	}
	if got := mainRef(t, a); got != before {
		t.Errorf("main moved: %s → %s", before, got)
	}
	return nc
}

func TestKBFinish_HeadlessPromotionParksNeedsConfirm(t *testing.T) {
	dir, a, wt := finishFixture(t)
	writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n\nTag then push.\n")
	sum := writeFinishSummary(t, a, "sess-1", "Record the release playbook.")
	before := mainRef(t, a)

	_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{Headless: true, SummaryFile: sum, Now: finishNow})
	nc := assertParked(t, a, wt, before, err)
	if nc.Branch != "mesh/kb/sess-1" || nc.SummaryPath != sum || len(nc.Promoted) != 1 || nc.Promoted[0] != "playbooks/release.md" {
		t.Errorf("parked record wrong: %+v", nc.NeedsConfirm)
	}
	if !strings.Contains(nc.Round, "Record the release playbook.") {
		t.Errorf("round does not carry the summary: %q", nc.Round)
	}

	// The peer's own session confirms it later: the same branch finishes.
	if err := kbFinishConfirmed(t, dir, a, "sess-1"); err != nil {
		t.Fatalf("interactive finish after parking: %v", err)
	}
	if got := headSubject(t, a.Dir()); !strings.HasPrefix(got, ConfirmSubjectPrefix) {
		t.Errorf("head = %q", got)
	}
}

func TestKBFinish_LogTierHeadlessParksWithoutSummary(t *testing.T) {
	dir, a, wt := finishFixture(t)
	writeWT(t, wt, "knowledge/decisions/0001-tuesdays.md", "# Tuesdays\n\nStatus: accepted\n")
	before := mainRef(t, a)

	_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{Headless: true, Now: finishNow})
	nc := assertParked(t, a, wt, before, err)
	if nc.Round != "" {
		t.Errorf("no summary written, yet round = %q", nc.Round)
	}
}

func TestKBFinish_NoHumanEnvironmentParksNeedsConfirm(t *testing.T) {
	for _, tc := range []struct{ name, key, val string }{
		{"dispatch depth", "AGENT_DISPATCH_DEPTH", "1"},
		{"distilling", "AGENT_MESH_DISTILLING", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, a, wt := finishFixture(t)
			writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
			sum := writeFinishSummary(t, a, "sess-1", "Record it.")
			before := mainRef(t, a)
			t.Setenv(tc.key, tc.val)
			_, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: sum, Now: finishNow})
			assertParked(t, a, wt, before, err)
		})
	}
}

func TestKBFinish_DispatchDepthZeroIsInteractive(t *testing.T) {
	dir, a, wt := finishFixture(t)
	writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
	t.Setenv("AGENT_DISPATCH_DEPTH", "0")
	if err := kbFinishConfirmed(t, dir, a, "sess-1"); err != nil {
		t.Fatalf("depth 0 finish: %v", err)
	}
}

func TestKBFinish_HeadlessCandidatesOnlyMerges(t *testing.T) {
	for _, env := range []string{"", "AGENT_DISPATCH_DEPTH"} {
		t.Run("env="+env, func(t *testing.T) {
			dir, a, wt := finishFixture(t)
			writeWT(t, wt, "knowledge/candidates/idea.md", "maybe\n")
			if env != "" {
				t.Setenv(env, "2")
			}
			res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{Headless: true, Now: finishNow})
			if err != nil {
				t.Fatalf("headless candidates-only finish: %v", err)
			}
			if res.Confirm != "" || len(res.Promoted) != 0 {
				t.Errorf("candidates-only finish promoted: %+v", res)
			}
			if dirExists(wt) {
				t.Error("worktree left behind")
			}
			if !fileExists(a.Dir() + "/knowledge/candidates/idea.md") {
				t.Error("candidate not on main")
			}
		})
	}
}

// The delegated peer's receipt reports needs-confirm, and its intake
// response uses the existing needs-human status with a body that opens
// `needs-confirm:` and carries the round and the summary path.
func TestNeedsConfirm_ReceiptAndResponseShape(t *testing.T) {
	dir, a, wt := finishFixture(t)
	writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
	sum := writeFinishSummary(t, a, "sess-1", "Record the release playbook.")
	req, err := WriteRequest(a, "guidance", "write the release playbook", "developer", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, ferr := KBFinish(dir, a, "sess-1", KBFinishOptions{Headless: true, SummaryFile: sum, Now: finishNow})
	var nc *KBNeedsConfirmError
	if !errors.As(ferr, &nc) {
		t.Fatalf("want needs-confirm, got %v", ferr)
	}

	receipt := nc.Receipt()
	if !strings.HasPrefix(receipt, "- outcome: needs-confirm") {
		t.Errorf("receipt outcome line: %q", receipt)
	}
	for _, want := range []string{"mesh/kb/sess-1", sum, "meshctl kb finish --agent architect --session sess-1 --summary-file " + sum} {
		if !strings.Contains(receipt, want) {
			t.Errorf("receipt lacks %q:\n%s", want, receipt)
		}
	}

	r, err := RespondNeedsConfirm(a, req.ID, nc.NeedsConfirm, RespondOptions{Now: finishNow})
	if err != nil {
		t.Fatalf("respond needs-confirm: %v", err)
	}
	if r.Status != StatusNeedsHuman {
		t.Errorf("status = %q, want %q (needs-confirm is not a status)", r.Status, StatusNeedsHuman)
	}
	_, section, ok := strings.Cut(r.Body, "## Response — architect")
	if !ok {
		t.Fatalf("no response section:\n%s", r.Body)
	}
	_, text, _ := strings.Cut(section, "\n\n")
	if !strings.HasPrefix(text, NeedsConfirmPrefix) {
		t.Errorf("response does not open %q:\n%s", NeedsConfirmPrefix, text)
	}
	round := strings.Index(text, "Record the release playbook.")
	path := strings.LastIndex(text, sum)
	if round < 0 || path < round {
		t.Errorf("response wants the round then the summary path:\n%s", text)
	}
	if _, ok := CanonicalStatus("needs-confirm"); ok {
		t.Error("needs-confirm became an intake status")
	}
}

// With no human at the prompt the gate does not ask a promotion it cannot put
// to anyone: it denies with the same needs-confirm record kb finish parks.
func TestGate_NoHumanPromotionDeniedNeedsConfirm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      string
		in, work bool
	}{
		{"delegated work in-session", "", true, true},
		{"dispatch depth", "AGENT_DISPATCH_DEPTH", false, false},
		{"distilling", "AGENT_MESH_DISTILLING", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, a, wt := promotionFixture(t)
			commitKB(t, wt, "playbooks/release.md")
			sum := writeSummary(t, a, "sess-1", promotionSummary)
			if tc.env != "" {
				t.Setenv(tc.env, "1")
			}
			p := bash(a, "sess-1", "meshctl kb finish --agent architect --session sess-1 --summary-file "+sum)
			p.InSession, p.Work = tc.in, tc.work
			d := GatePreToolUse(dir, p)
			if !d.Deny || d.Ask || d.Check != GateCheckPromotion {
				t.Fatalf("want deny/promotion, got %+v", d)
			}
			for _, want := range []string{NeedsConfirmPrefix, "no human at the prompt", "mesh/kb/sess-1", sum, "needs-human"} {
				if !strings.Contains(d.Reason, want) {
					t.Errorf("deny lacks %q:\n%s", want, d.Reason)
				}
			}
		})
	}
	// Candidates only: still no ask and no deny.
	dir, a, wt := promotionFixture(t)
	commitKB(t, wt, "candidates/idea.md")
	p := bash(a, "sess-1", "meshctl kb finish --agent architect --session sess-1")
	p.InSession, p.Work = true, true
	if d := GatePreToolUse(dir, p); d.Deny || d.Ask {
		t.Fatalf("candidates-only finish in work mode: %+v", d)
	}
}

func TestWorkBrief_PromotionEndsInNeedsConfirm(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	req, _ := askWork(t, dir, caller, arch, "Record the release playbook")
	brief, err := Brief(dir, arch, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A promotion ends in `needs-confirm`", "`needs-human`", NeedsConfirmPrefix, "summary path"} {
		if !strings.Contains(brief, want) {
			t.Errorf("work contract lacks %q", want)
		}
	}
}

// A parked promotion carries the related-entries lines the ask would have
// shown, with the summary, in its response body and in the gate's deny.
func TestNeedsConfirm_CarriesRelatedEntries(t *testing.T) {
	p := newRelatedPool(t, nil)
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	sum := writeSummary(t, p.arch, "sess-1", circulationSummary)
	const related = "mesh://lending/architecture/circulation-rules.md — Circulation window rules"

	_, err := KBFinish(p.root, p.arch, "sess-1", KBFinishOptions{Headless: true, SummaryFile: sum, Now: finishNow})
	var nc *KBNeedsConfirmError
	if !errors.As(err, &nc) {
		t.Fatalf("want needs-confirm, got %v", err)
	}
	if !strings.Contains(strings.Join(nc.Related, "\n"), related) {
		t.Errorf("parked record lacks the related entry: %q", nc.Related)
	}
	body := nc.ResponseBody()
	if r, s := strings.Index(body, related), strings.LastIndex(body, sum); r < 0 || s < r {
		t.Errorf("response wants the related lines before the summary path:\n%s", body)
	}

	pl := bash(p.arch, "sess-1", "meshctl kb finish --summary-file "+sum)
	pl.InSession, pl.Work = true, true
	if d := GatePreToolUse(p.root, pl); !d.Deny || !strings.Contains(d.Reason, related) {
		t.Errorf("gate deny lacks the related entry: %+v", d)
	}
}

func parkedSessions(dir string, a *Agent) []string {
	var out []string
	for _, p := range ParkedKBBranches(dir, a, time.Now()) {
		out = append(out, p.Session)
	}
	return out
}

// TestParkedKBBranches_ReadsParkMarkers: a park is recorded when it happens
// (KBFinish's needs-confirm, the gate's no-human deny) and cleared by a
// successful finish or an abort; an unheld KB branch that was never parked
// is not "needs-confirm".
func TestParkedKBBranches_ReadsParkMarkers(t *testing.T) {
	t.Run("headless finish parks, confirmed finish clears", func(t *testing.T) {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
		sum := writeFinishSummary(t, a, "sess-1", "Record it.")
		if _, err := KBFinish(dir, a, "sess-1", KBFinishOptions{Headless: true, SummaryFile: sum, Now: finishNow}); !errors.Is(err, ErrKBNeedsConfirm) {
			t.Fatalf("want park, got %v", err)
		}
		got := ParkedKBBranches(dir, a, time.Now())
		if len(got) != 1 || got[0].Session != "sess-1" || got[0].Branch != "mesh/kb/sess-1" || got[0].SummaryPath != sum ||
			len(got[0].Promoted) != 1 || got[0].Promoted[0] != "playbooks/release.md" {
			t.Fatalf("parked = %+v", got)
		}
		if err := kbFinishConfirmed(t, dir, a, "sess-1"); err != nil {
			t.Fatal(err)
		}
		if s := parkedSessions(dir, a); len(s) != 0 {
			t.Fatalf("finished branch still parked: %v", s)
		}
	})
	t.Run("abort clears", func(t *testing.T) {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
		_, _ = KBFinish(dir, a, "sess-1", KBFinishOptions{Headless: true, Now: finishNow})
		if s := parkedSessions(dir, a); len(s) != 1 {
			t.Fatalf("not parked: %v", s)
		}
		if err := kbFinishAbort(dir, a, "sess-1"); err != nil {
			t.Fatal(err)
		}
		if s := parkedSessions(dir, a); len(s) != 0 {
			t.Fatalf("aborted branch still parked: %v", s)
		}
	})
	t.Run("never parked is not listed", func(t *testing.T) {
		dir, a, wt := finishFixture(t)
		writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n")
		if s := parkedSessions(dir, a); len(s) != 0 {
			t.Fatalf("an unparked branch is listed: %v", s)
		}
	})
	t.Run("gate no-human deny parks", func(t *testing.T) {
		dir, a, wt := promotionFixture(t)
		commitKB(t, wt, "playbooks/release.md")
		sum := writeSummary(t, a, "sess-1", promotionSummary)
		t.Setenv("AGENT_DISPATCH_DEPTH", "1")
		if d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb finish --summary-file "+sum)); !d.Deny {
			t.Fatalf("want deny, got %+v", d)
		}
		got := ParkedKBBranches(dir, a, time.Now())
		if len(got) != 1 || got[0].Session != "sess-1" || got[0].SummaryPath != sum {
			t.Fatalf("parked = %+v", got)
		}
	})
}
