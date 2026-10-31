package mesh

import (
	"os"
	"strings"
	"testing"
)

// triage_report_test.go — the triage report grammar: the validator, its
// line-numbered failures, and the writer helper.

func TestTriageReport_ValidatorAcceptsGrammar(t *testing.T) {
	text := strings.Join([]string{
		"# Triage report — architect, 2026-10-18",
		"",
		"Consulted:",
		`- developer: "the queue question is circulated, see the ADR"`,
		"",
		`- 2026-10-01-developer-a · stands · "no supersession signal; checked work branches, knowledge, siblings" · worksheet`,
		`- 2026-10-02-developer-b · superseded → architect:2026-10-01-developer-a · "same ask, older request" · worksheet siblings`,
		`- 2026-10-03-user-c · done → mesh://architect/decisions/queue.md#queue-choice · "We use SQS." · knowledge`,
		`- 2026-10-04-user-d · withdrawn → developer@abc1234 · "dropped it, not needed" · human, confirmation round`,
		`- 2026-10-05-user-e · unclear · "checked: work branches, knowledge, siblings, redirects — none" · worksheet`,
		`- 2026-10-06-user-f · done → developer@mesh/work/2026-10-06-user-f · "merged the fix" · worksheet · branch live`,
	}, "\n")
	r, errs := ParseTriageReport(text)
	if len(errs) != 0 {
		t.Fatalf("valid report rejected: %v", errs)
	}
	if len(r.Consulted) != 1 || !strings.Contains(r.Consulted[0], "developer") {
		t.Fatalf("consulted section = %q", r.Consulted)
	}
	if len(r.Lines) != 6 {
		t.Fatalf("lines = %d, want 6", len(r.Lines))
	}
	l, ok := r.Line("2026-10-04-user-d")
	if !ok || l.Verdict != TriageWithdrawn || l.Ref != "developer@abc1234" || l.Source != "human, confirmation round" || l.Quote != "dropped it, not needed" {
		t.Fatalf("withdrawn line = %+v", l)
	}
	if l, _ := r.Line("2026-10-06-user-f"); !l.BranchLive || l.Verdict != TriageDone {
		t.Fatalf("branch live line = %+v", l)
	}
}

func TestTriageReport_ValidatorRejectsWithLineNumbers(t *testing.T) {
	text := strings.Join([]string{
		"# Triage report", // 1
		`- a1 · superseded · "quote" · worksheet`,                // 2 no ref
		`- a2 · done → architect:x · "" · worksheet`,             // 3 empty quote
		`- a3 · done → architect:x · no quotes here · worksheet`, // 4 malformed
		`- a4 · in-progress · "still working" · worksheet`,       // 5 state is never a verdict
		`- a5 · stands · "live need" · worksheet`,                // 6 ok
		`- a6 · withdrawn · "gone" · worksheet`,                  // 7 no ref
		`- a7 · done → not a ref · "x" · worksheet`,              // 8 bad ref form
		"Consulted:", // 9 not first
	}, "\n")
	_, errs := ParseTriageReport(text)
	got := map[int]string{}
	for _, e := range errs {
		got[e.Line] = e.Msg
	}
	for _, n := range []int{2, 3, 4, 5, 7, 8, 9} {
		if got[n] == "" {
			t.Errorf("line %d not rejected; errors: %v", n, errs)
		}
	}
	if got[6] != "" {
		t.Errorf("valid stands line rejected: %s", got[6])
	}
	if !strings.Contains(got[2], "ref") || !strings.Contains(got[3], "quote") {
		t.Errorf("reasons do not name ref/quote: %q / %q", got[2], got[3])
	}
	for _, e := range errs {
		if !strings.Contains(e.Error(), "line ") {
			t.Errorf("error does not name its line: %s", e.Error())
		}
	}
}

func TestTriageReport_DuplicatesNewerSupersededOlderStands(t *testing.T) {
	_, arch, _, _ := triagePool(t)
	older := triageAsk(t, arch, "developer", "guidance", "pick a message queue for ingest", nil, triageDaysAgo(9))
	newer := triageAsk(t, arch, "analyst", "guidance", "pick a message queue for ingest", nil, triageDaysAgo(2))
	path := TriageReportPath(arch.Dir(), triageNow)
	if err := WriteTriageReport(path, nil, []TriageReportLine{
		{ID: older.ID, Verdict: TriageStands, Quote: "pick a message queue for ingest", Source: "request"},
		{ID: newer.ID, Verdict: TriageSuperseded, Ref: "architect:" + older.ID, Quote: "pick a message queue for ingest", Source: "worksheet siblings"},
	}); err != nil {
		t.Fatal(err)
	}
	r, err := ReadTriageReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := r.Line(older.ID); l.Verdict != TriageStands {
		t.Fatalf("older = %+v", l)
	}
	if l, _ := r.Line(newer.ID); l.Verdict != TriageSuperseded || l.Ref != "architect:"+older.ID {
		t.Fatalf("newer = %+v", l)
	}
}

func TestTriageWorksheet_SkipsStandsWithUnchangedSignals(t *testing.T) {
	dir, arch, dev, _ := triagePool(t)
	kept := triageAsk(t, arch, "developer", "guidance", "pick a message queue", nil, triageDaysAgo(10))
	unsure := triageAsk(t, arch, "developer", "guidance", "audit the billing tables", nil, triageDaysAgo(11))
	if _, err := WriteTriageWorksheet(dir, arch, TriageMode{}, triageNow); err != nil {
		t.Fatal(err)
	}
	if err := WriteTriageReport(TriageReportPath(arch.Dir(), triageNow), nil, []TriageReportLine{
		{ID: kept.ID, Verdict: TriageStands, Quote: "no supersession signal; checked all groups", Source: "worksheet"},
		{ID: unsure.ID, Verdict: TriageUnclear, Quote: "checked all groups, none", Source: "worksheet"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, day := range []int{0, 1, 2} { // same-day re-run, then later days
		ws, err := WriteTriageWorksheet(dir, arch, TriageMode{}, triageNow.AddDate(0, 0, day))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(ws.Path)
		text := string(b)
		if strings.Contains(text, "## "+kept.ID) {
			t.Fatalf("day +%d: stands with unchanged signals not skipped:\n%s", day, text)
		}
		if !strings.Contains(text, "## "+unsure.ID) {
			t.Fatalf("day +%d: unclear request skipped:\n%s", day, text)
		}
		if !strings.Contains(text, kept.ID) {
			t.Fatalf("day +%d: skipped request not listed as skipped:\n%s", day, text)
		}
	}

	// A signal changes: the request is back.
	gitOut(t, dev.Dir(), "branch", WorkBranchPrefix+kept.ID)
	ws, err := WriteTriageWorksheet(dir, arch, TriageMode{}, triageNow.AddDate(0, 0, 3))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ws.Path)
	if !strings.Contains(string(b), "## "+kept.ID) {
		t.Fatalf("changed signal still skipped:\n%s", b)
	}
}
