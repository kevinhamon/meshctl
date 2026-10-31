package mesh

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// state_confirm_test.go — `state set` is a promotion: the digest and its
// section hashes land on main together as one `Confirm: <Intent line>` commit,
// asked by the gate with the summary.

const stateDigest = "## Now\nAll services are Go.\n\n## Active\n- v2 migration: 5 of 7\n\n## Recently decided\n- ADR-0012 one repo per service\n\n## Open questions\n- none\n"

const stateSummary = `## Intent
Refresh the state digest after the v2 migration progress.

## Grounding
- services.yaml lists 5 of 7 on v2.

## Precedent
- none

## Changes
### state.md
+ v2 migration: 5 of 7
- v2 migration: 3 of 7
`

// stateFixture is a gated peer repo on main with no digest yet.
func stateFixture(t *testing.T) *Agent {
	t.Helper()
	_, a := poolGateFixture(t)
	gitIdentity(t)
	gitInit(t, a.Dir())
	return a
}

func TestStateSetConfirmed_WritesDigestLedgerAndConfirmCommit(t *testing.T) {
	a := stateFixture(t)
	sum := writeSummary(t, a, "sess-1", stateSummary)

	res, err := StateSetConfirmed(a, stateDigest, StateSetOptions{SummaryFile: sum, Session: "sess-1", Now: finishNow})
	if err != nil {
		t.Fatalf("state set: %v", err)
	}
	if got := headSubject(t, a.Dir()); got != "Confirm: Refresh the state digest after the v2 migration progress." {
		t.Fatalf("tip subject = %q", got)
	}
	files := gitOut(t, a.Dir(), "show", "--name-only", "--format=", "main")
	for _, f := range []string{"knowledge/" + StateFile, "knowledge/" + LedgerFile} {
		if !strings.Contains(files, f) {
			t.Fatalf("Confirm commit lacks %s: %q", f, files)
		}
	}
	shown := gitOut(t, a.Dir(), "show", "main:knowledge/"+StateFile)
	if !strings.Contains(shown, "<!-- meshctl state") || !strings.Contains(shown, "All services are Go.") {
		t.Fatalf("digest on main:\n%s", shown)
	}
	e, ok := ledgerOnMain(t, a).Entries[StateFile]
	if !ok || e.Hash != "" || e.Session != "sess-1" || e.Confirmed != "2026-10-18" {
		t.Fatalf("ledger line for state.md: %+v (ok=%v)", e, ok)
	}
	want := []string{"now", "active", "recently-decided", "open-questions"}
	if len(e.Sections) != len(want) {
		t.Fatalf("want one hash per section %v, got %v", want, e.Sections)
	}
	for _, s := range want {
		if e.Sections[s] == "" {
			t.Errorf("no hash for section %s: %v", s, e.Sections)
		}
	}
	if res.Confirm == "" || !strings.HasPrefix(strings.TrimSpace(gitOut(t, a.Dir(), "rev-parse", "main")), res.Confirm) {
		t.Fatalf("result Confirm %q is not main's tip", res.Confirm)
	}
	if st := strings.TrimSpace(gitOut(t, a.Dir(), "status", "--porcelain", "--", "knowledge")); st != "" {
		t.Fatalf("checkout left dirty: %q", st)
	}
}

func TestStateSetConfirmed_EditedSectionRecallsUnconfirmed(t *testing.T) {
	a := stateFixture(t)
	sum := writeSummary(t, a, "sess-1", stateSummary)
	body := "## Circulation cadence\n\nDaily.\n\n## Circulation owners\n\nOps.\n"
	if _, err := StateSetConfirmed(a, body, StateSetOptions{SummaryFile: sum, Session: "sess-1", Now: finishNow}); err != nil {
		t.Fatalf("state set: %v", err)
	}
	hits, _ := Recall(a, "circulation", 20)
	if cad, own := kbHit(hits, "circulation-cadence"), kbHit(hits, "circulation-owners"); cad == nil || own == nil || !cad.Confirmed || !own.Confirmed {
		t.Fatalf("both sections confirmed after state set: cadence=%+v owners=%+v", cad, own)
	}
	// A later edit outside state set (a raw rewrite) unconfirms only its section.
	if err := StateSet(a, "## Circulation cadence\n\nWeekly.\n\n## Circulation owners\n\nOps.\n", finishNow); err != nil {
		t.Fatal(err)
	}
	hits, _ = Recall(a, "circulation", 20)
	cad, own := kbHit(hits, "circulation-cadence"), kbHit(hits, "circulation-owners")
	if cad == nil || own == nil || cad.Confirmed || !own.Confirmed {
		t.Fatalf("edited section unconfirmed, untouched confirmed: cadence=%+v owners=%+v", cad, own)
	}
	if md := RecallMarkdown(hits); !strings.Contains(md, "·unconfirmed] circulation-cadence") {
		t.Fatalf("edited section must render unconfirmed:\n%s", md)
	}
}

func TestStateSetConfirmed_RefusedHeadless(t *testing.T) {
	for _, env := range [][2]string{{"AGENT_DISPATCH_DEPTH", "1"}, {"AGENT_MESH_DISTILLING", "1"}} {
		t.Run(env[0], func(t *testing.T) {
			a := stateFixture(t)
			sum := writeSummary(t, a, "sess-1", stateSummary)
			before := mainRef(t, a)
			t.Setenv(env[0], env[1])
			_, err := StateSetConfirmed(a, stateDigest, StateSetOptions{SummaryFile: sum, Session: "sess-1", Now: finishNow})
			if !errors.Is(err, ErrStateHeadless) || !strings.Contains(err.Error(), "a human must be at the prompt") {
				t.Fatalf("want headless refusal, got %v", err)
			}
			if fileExists(StatePath(a)) || mainRef(t, a) != before {
				t.Fatal("a refused state set must not write the digest or move main")
			}
		})
	}
}

func TestStateSetConfirmed_RefusedWithoutUsableSummary(t *testing.T) {
	for name, summary := range map[string]string{"none": "", "empty": " \n", "no-intent": "## Grounding\n- x\n"} {
		t.Run(name, func(t *testing.T) {
			a := stateFixture(t)
			file := ""
			if summary != "" {
				file = writeSummary(t, a, "sess-1", summary)
			}
			before := mainRef(t, a)
			_, err := StateSetConfirmed(a, stateDigest, StateSetOptions{SummaryFile: file, Session: "sess-1", Now: finishNow})
			if !errors.Is(err, ErrStateSummaryRequired) || !strings.Contains(err.Error(), SummaryPath(a.Dir(), "sess-1")) {
				t.Fatalf("want summary refusal naming the path, got %v", err)
			}
			if fileExists(StatePath(a)) || mainRef(t, a) != before {
				t.Fatal("a refused state set must not write the digest or move main")
			}
		})
	}
}

func TestGate_StateSetAskedWithSummary(t *testing.T) {
	dir, a := poolGateFixture(t)
	sum := writeSummary(t, a, "sess-1", stateSummary)

	d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl state set --summary-file "+sum+" <<'EOF'\n## Now\nx\nEOF"))
	if !d.Ask || d.Deny || d.Check != GateCheckPromotion {
		t.Fatalf("want ask/promotion, got %+v", d)
	}
	if !strings.HasPrefix(d.Reason, "Intent: Refresh the state digest") {
		t.Fatalf("reason must start with the Intent line:\n%s", d.Reason)
	}
	for _, want := range []string{"state.md  +1 -1", "+ v2 migration: 5 of 7", "- v2 migration: 3 of 7", PromotionAskFooter} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask lacks %q:\n%s", want, d.Reason)
		}
	}
}

func TestGate_StateSetDenied(t *testing.T) {
	cases := []struct {
		name, summary, cmd, env string
		want                    []string
	}{
		{name: "no flag", cmd: "meshctl state set", want: []string{"needs your confirmation summary", "confirm/sess-1.md"}},
		{name: "no file", cmd: "meshctl state set --summary-file /nonexistent/s.md", want: []string{"no file", "confirm/sess-1.md"}},
		{name: "no intent", summary: "## Grounding\n- x\n", want: []string{"no Intent line", "confirm/sess-1.md"}},
		{name: "dispatch depth", summary: stateSummary, env: "AGENT_DISPATCH_DEPTH", want: []string{"a human must be at the prompt", "dispatch depth"}},
		{name: "distilling", summary: stateSummary, env: "AGENT_MESH_DISTILLING", want: []string{"a human must be at the prompt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, a := poolGateFixture(t)
			cmd := c.cmd
			if c.summary != "" {
				cmd = "meshctl state set --summary-file=" + writeSummary(t, a, "sess-1", c.summary)
			}
			if c.env != "" {
				t.Setenv(c.env, "1")
			}
			d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
			if !d.Deny || d.Check != GateCheckPromotion {
				t.Fatalf("want deny/promotion, got %+v", d)
			}
			for _, w := range c.want {
				if !strings.Contains(d.Reason, w) {
					t.Errorf("reason lacks %q: %s", w, d.Reason)
				}
			}
		})
	}
}

func TestGate_StateSetMentionIsNotInvocation(t *testing.T) {
	dir, a := poolGateFixture(t)
	for _, cmd := range []string{"meshctl state show", "grep 'meshctl state set' notes.md", "meshctl state template"} {
		if d := GatePreToolUse(dir, bash(a, "sess-1", cmd)); d.Deny || d.Ask {
			t.Errorf("%q: want allow, got %+v", cmd, d)
		}
	}
}

// TestStateSet_HeaderShowsSummaryFile: the digest header names the form the
// gate accepts (`state set` without --summary-file is denied).
func TestStateSet_HeaderShowsSummaryFile(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	if err := StateSet(a, stateDigest, finishNow); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(StatePath(a))
	header := strings.SplitN(string(b), "\n", 2)[0]
	if !strings.Contains(header, "meshctl state set --summary-file") {
		t.Fatalf("header does not show --summary-file: %s", header)
	}
}
