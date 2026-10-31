package mesh

import (
	"strings"
	"testing"
	"time"
)

// reconcile report grammar.

func TestReconcileReport_ValidatorAcceptsGrammarAndNamesFailingLines(t *testing.T) {
	good := `# Reconcile report — alpha (2026-10-18)

## Standing orders

- CLAUDE.md says "push on green", doctrine says "the human pushes" — fixed by hand.

## Entries

- state.md#now · consistent · "All services are Go." (services.yaml)
- reference/renewal.md · conflict → mesh://bravo/reference/renewal.md · "we renew daily" vs "bravo renews weekly"
- reference/window.md · conflict → doctrine · "window is ours" vs "the lending operator sets the window"
- reference/fees.md · conflict → owner bravo, pending · "fee is 2%" vs mesh://bravo/reference/fees.md "fee is 3%"
- playbooks/run.md · judgment · "run it when the queue is empty"
- experience/slip.md · ungrounded · "the slip cost a day"
- glossary.md#circulation-window · term-vs-fact · "the renewal period is daily"
`
	r := ParseReconcileReport(good)
	if err := r.Err(); err != nil {
		t.Fatalf("well-formed report rejected: %v", err)
	}
	if len(r.StandingOrders) != 1 || len(r.Lines) != 7 {
		t.Fatalf("standing orders %d, lines %d: %+v", len(r.StandingOrders), len(r.Lines), r.Lines)
	}
	if l, ok := r.Latest("state.md#now"); !ok || l.Verdict != ReconcileConsistent || l.Line != 9 {
		t.Fatalf("state.md#now: %+v ok=%v", l, ok)
	}
	if l, _ := r.Latest("reference/fees.md"); l.OwnerPending != "bravo" || l.Counterpart != "mesh://bravo/reference/fees.md" {
		t.Fatalf("owner-pending line: %+v", l)
	}
	if n := r.OpenConflicts(); n != 3 {
		t.Fatalf("open conflicts = %d, want 3", n)
	}

	bad := `- a.md · consistent
- b.md · conflict · "x" vs "y"
- c.md · consistent · "fine"
- d.md · conflict → mesh://bravo/d.md · "only one claim"
- e.md · maybe · "x"
`
	r = ParseReconcileReport(bad)
	err := r.Err()
	if err == nil {
		t.Fatal("malformed report accepted")
	}
	for _, want := range []string{"line 1", "no quoted grounding", "line 2", "names no counterpart", "line 4", "both claims", "line 5", "unknown verdict"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "line 3") {
		t.Errorf("line 3 is well formed: %v", err)
	}

	late := "- a.md · consistent · \"x\"\n\n## Standing orders\n\n- something\n"
	if err := ParseReconcileReport(late).Err(); err == nil || !strings.Contains(err.Error(), "Standing orders") {
		t.Fatalf("standing orders after entries must be rejected, got %v", err)
	}
}

func TestDoctor_ReconcileReportAgeAndOpenConflicts(t *testing.T) {
	root, p := reconcilePool(t)
	a := p["alpha"]
	fs, _ := Doctor(root)
	if got := agentFindings(fs, "alpha", "reconcile report"); len(got) != 0 {
		t.Fatalf("no report yet: no reconcile finding, got %+v", got)
	}

	now := time.Now()
	writeReconcileReport(t, a, now.AddDate(0, 0, -9).Format("2006-01-02"), "- reference/x.md · conflict → doctrine · \"a\" vs \"b\"\n")
	writeReconcileReport(t, a, now.AddDate(0, 0, -3).Format("2006-01-02"), `- reference/a.md · conflict → doctrine · "a" vs "b"
- reference/b.md · conflict → mesh://bravo/reference/b.md · "a" vs "b"
- reference/c.md · conflict → doctrine · "a" vs "b"
- reference/c.md · consistent · "fixed by hand"
- reference/d.md · consistent · "d"
`)
	fs, _ = Doctor(root)
	got := agentFindings(fs, "alpha", "reconcile report")
	if len(got) != 1 || !strings.Contains(got[0].Message, "3 day(s) ago") || !strings.Contains(got[0].Message, "2 open conflict(s)") {
		t.Fatalf("want the latest report's age and open conflicts, got %+v", got)
	}
	if len(agentFindings(fs, "bravo", "reconcile report")) != 0 {
		t.Fatal("doctor does not read across peers")
	}

	writeReconcileReport(t, a, now.Format("2006-01-02"), "- reference/a.md · consistent\n")
	fs, _ = Doctor(root)
	if got := agentFindings(fs, "alpha", "reconcile report"); len(got) != 1 || !strings.Contains(got[0].Message, "line 1") {
		t.Fatalf("a malformed latest report is named with its lines, got %+v", got)
	}
}
