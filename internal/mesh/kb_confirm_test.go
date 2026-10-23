package mesh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kb confirm: bulk confirmation from a reconcile report (ADR-0070; spec
// decision 46): ledger lines and the Confirm: commit on main in one step, at
// most five paths per call, refusals by latest verdict.

const confirmGlossary = "# Glossary\n\n## Settlement window\n\n**Settlement window**: the netting period.\n\n## Fact owner\n\n**Fact owner**: the peer that owns a fact.\n"

// confirmFixture is a gated peer repo on main with a few committed entries.
func confirmFixture(t *testing.T) (dir string, a *Agent) {
	t.Helper()
	dir, a = poolGateFixture(t)
	gitIdentity(t)
	writeKB(t, a, "reference/a.md", "# A\n\nAll services are Go.\n")
	writeKB(t, a, "reference/c.md", "# C\n\nC.\n")
	writeKB(t, a, "reference/d.md", "# D\n\nD.\n")
	writeKB(t, a, "reference/e.md", "# E\n\nE.\n")
	writeKB(t, a, "reference/f.md", "# F\n\nF.\n")
	writeKB(t, a, PeerGlossaryFile, confirmGlossary)
	if err := StateSet(a, "## Now\n\nShipping.\n\n## Active\n\n- settlement\n", finishNow); err != nil {
		t.Fatal(err)
	}
	gitInit(t, a.Dir())
	return dir, a
}

func writeConfirmReport(t *testing.T, a *Agent, body string) string {
	t.Helper()
	p := ReconcileReportPath(a.Dir(), finishNow)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const confirmReport = `# Reconcile report — architect (2026-10-18)

## Intent
Confirm the reviewed reference entries after the first reconcile pass.

- reference/a.md · consistent · "All services are Go." (services.yaml)
- reference/c.md · judgment · "C."
- reference/d.md · consistent · "D."
- reference/e.md · consistent · "E."
- reference/f.md · consistent · "F."
- glossary.md#settlement-window · consistent · "the netting period"
- glossary.md#fact-owner · consistent · "the peer that owns a fact"
- state.md#now · consistent · "Shipping."
- state.md#active · consistent · "settlement"
`

func confirmPaths(dir string, a *Agent, report string, paths ...string) (KBConfirmResult, error) {
	return KBConfirm(dir, a, paths, KBConfirmOptions{Report: report, Session: "sess-1", Now: finishNow})
}

func TestKBConfirm_WritesLedgerAndConfirmCommitOnMain(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)

	res, err := confirmPaths(dir, a, rep, "reference/a.md", "glossary.md#settlement-window")
	if err != nil {
		t.Fatalf("kb confirm: %v", err)
	}
	if got := headSubject(t, a.Dir()); got != "Confirm: Confirm the reviewed reference entries after the first reconcile pass." {
		t.Fatalf("tip subject = %q", got)
	}
	if files := gitOut(t, a.Dir(), "show", "--name-only", "--format=", "main"); strings.TrimSpace(files) != "knowledge/"+LedgerFile {
		t.Fatalf("Confirm commit must hold only the ledger, got %q", files)
	}
	l := ledgerOnMain(t, a)
	ae := l.Entries["reference/a.md"]
	if want, _ := CurrentLedgerEntry(a.Dir(), "reference/a.md"); ae.Hash == "" || ae.Hash != want.Hash || ae.Session != "sess-1" || ae.Confirmed != "2026-10-18" {
		t.Fatalf("ledger line for reference/a.md: %+v", ae)
	}
	ge := l.Entries[PeerGlossaryFile]
	if len(ge.Sections) != 1 || ge.Sections["settlement-window"] == "" || ge.Hash != "" {
		t.Fatalf("want exactly one section hash for glossary.md, got %+v", ge)
	}
	if strings.Join(res.Ledgered, ",") != "reference/a.md,glossary.md#settlement-window" || res.Confirm == "" {
		t.Fatalf("result: %+v", res)
	}
	if !l.SectionConfirmed(a.Dir(), PeerGlossaryFile, "settlement-window") || l.SectionConfirmed(a.Dir(), PeerGlossaryFile, "fact-owner") {
		t.Fatal("only the addressed section is confirmed")
	}
	if strings.TrimSpace(gitOut(t, a.Dir(), "status", "--porcelain")) != "" {
		t.Fatal("kb confirm left the checkout dirty")
	}
}

func TestKBConfirm_BareSectionedPathIsOneUnitAndSixthPathRefuses(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)

	pre := mainRef(t, a)
	_, err := confirmPaths(dir, a, rep, "reference/a.md", "reference/c.md", "reference/d.md", "reference/e.md", "reference/f.md", "state.md")
	if !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "at most 5") {
		t.Fatalf("want a five-path refusal, got %v", err)
	}
	if mainRef(t, a) != pre || fileExists(LedgerPath(a.Dir())) && len(ledgerOnMain(t, a).Entries) > 0 {
		t.Fatal("a refused call wrote something")
	}

	res, err := confirmPaths(dir, a, rep, "reference/a.md", "reference/c.md", "reference/d.md", "reference/e.md", "state.md")
	if err != nil {
		t.Fatalf("five units incl. a bare sectioned path: %v", err)
	}
	st := ledgerOnMain(t, a).Entries[StateFile]
	if st.Sections["now"] == "" || st.Sections["active"] == "" {
		t.Fatalf("bare state.md must confirm every section: %+v", st)
	}
	if !strings.Contains(strings.Join(res.Ledgered, ","), "state.md#now") {
		t.Fatalf("ledgered: %v", res.Ledgered)
	}
}

func TestKBConfirm_RefusesByLatestVerdict(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, `## Intent
Second pass.

- reference/a.md · consistent · "first look"
- reference/a.md · conflict → doctrine · "all Go" vs "the doctrine says mixed"
- reference/c.md · term-vs-fact · "C."
- reference/d.md · ungrounded · "D."
- reference/e.md · consistent · "E."
`)
	cases := []struct {
		path, want string
	}{
		{"reference/a.md", "conflict"},
		{"reference/c.md", "term-vs-fact"},
		{"reference/d.md", "ungrounded"},
		{"reference/f.md", "no report line"},
		{"glossary.md#no-such", "no section"},
	}
	for _, c := range cases {
		pre := mainRef(t, a)
		_, err := confirmPaths(dir, a, rep, "reference/e.md", c.path)
		if !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), c.path) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want refusal naming %q, got %v", c.path, c.want, err)
		}
		if mainRef(t, a) != pre {
			t.Fatalf("%s: a refusal moved main", c.path)
		}
	}

	_, err := KBConfirm(dir, a, []string{"reference/d.md"}, KBConfirmOptions{Report: rep, Session: "sess-1", Now: finishNow,
		AcceptUngrounded: []string{"reference/d.md"}})
	if err != nil {
		t.Fatalf("--accept-ungrounded must let the path through: %v", err)
	}
	if !ledgerOnMain(t, a).Confirmed(a.Dir(), "reference/d.md") {
		t.Fatal("reference/d.md not confirmed")
	}
	// Accepting one path does not accept another.
	if _, err := KBConfirm(dir, a, []string{"reference/c.md"}, KBConfirmOptions{Report: rep, Now: finishNow,
		AcceptUngrounded: []string{"reference/c.md"}}); !errors.Is(err, ErrKBConfirmRefused) {
		t.Fatalf("term-vs-fact is not overridable: %v", err)
	}
}

func TestKBConfirm_ReportRequiredAndValid(t *testing.T) {
	dir, a := confirmFixture(t)
	if _, err := confirmPaths(dir, a, "", "reference/a.md"); !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "--report") {
		t.Fatalf("want a no-report refusal, got %v", err)
	}
	rep := writeConfirmReport(t, a, "- reference/a.md · consistent\n")
	if _, err := confirmPaths(dir, a, rep, "reference/a.md"); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("want a grammar refusal naming line 1, got %v", err)
	}
}

func TestKBConfirm_NonOwnerConflictWaitsForOwnerLedger(t *testing.T) {
	dir, a := confirmFixture(t)
	owner := newTestAgent(t, dir, "bravo", false, nil)
	writeKB(t, owner, "reference/fees.md", "# Fees\n\nThe fee is 3%.\n")
	rep := writeConfirmReport(t, a, `## Intent
Confirm after the owner settled the fee.

- reference/a.md · conflict → owner bravo, pending · "fee is 2%" vs mesh://bravo/reference/fees.md "fee is 3%"
`)
	_, err := confirmPaths(dir, a, rep, "reference/a.md")
	if !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "bravo") || !strings.Contains(err.Error(), "mesh://bravo/reference/fees.md") {
		t.Fatalf("want a refusal waiting on bravo's ledger, got %v", err)
	}
	confirmKB(t, owner, "reference/fees.md")
	if _, err := confirmPaths(dir, a, rep, "reference/a.md"); err != nil {
		t.Fatalf("owner confirmed the counterpart; want pass, got %v", err)
	}
}

func TestKBConfirm_TrustGuardAndHeadless(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)
	if err := os.WriteFile(filepath.Join(a.Dir(), ".claude", meshWiringStampFile), []byte(`{"version": 4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmPaths(dir, a, rep, "reference/a.md"); !errors.Is(err, ErrKBTrustGuard) {
		t.Fatalf("want the trust guard, got %v", err)
	}
	dir, a = confirmFixture(t)
	rep = writeConfirmReport(t, a, confirmReport)
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if _, err := confirmPaths(dir, a, rep, "reference/a.md"); !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "human") {
		t.Fatalf("want a headless refusal, got %v", err)
	}
}

func TestKBConfirm_RefusesUncommittedEntry(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)
	writeKB(t, a, "reference/a.md", "# A\n\nEdited, not committed.\n")
	if _, err := confirmPaths(dir, a, rep, "reference/a.md"); !errors.Is(err, ErrKBConfirmRefused) || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("want an uncommitted refusal, got %v", err)
	}
}

func TestGate_KBConfirmAskedWithReportLines(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)

	d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb confirm reference/a.md state.md --report "+rep))
	if !d.Ask || d.Check != GateCheckPromotion {
		t.Fatalf("want ask/promotion, got %+v", d)
	}
	for _, want := range []string{"Intent: Confirm the reviewed reference entries", `- reference/a.md · consistent · "All services are Go."`,
		`- state.md#now · consistent`, `- state.md#active · consistent`, PromotionAskFooter} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask lacks %q:\n%s", want, d.Reason)
		}
	}
	if strings.Contains(d.Reason, "reference/c.md") {
		t.Errorf("ask shows lines for paths not named:\n%s", d.Reason)
	}

	for name, cmd := range map[string]string{
		"no report":  "meshctl kb confirm reference/a.md",
		"no file":    "meshctl kb confirm reference/a.md --report /nonexistent/r.md",
		"six paths":  "meshctl kb confirm a.md b.md c.md d.md e.md f.md --report " + rep,
		"bad report": "meshctl kb confirm reference/a.md --report " + writeConfirmReport(t, a, "- reference/a.md · consistent\n"),
	} {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if !d.Deny || d.Check != GateCheckPromotion {
			t.Errorf("%s: want deny/promotion, got %+v", name, d)
		}
	}
	rep = writeConfirmReport(t, a, confirmReport)
	t.Setenv("AGENT_MESH_DISTILLING", "1")
	if d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb confirm reference/a.md --report "+rep)); !d.Deny {
		t.Fatalf("no human at the prompt: want deny, got %+v", d)
	}
}
