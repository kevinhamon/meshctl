package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gate_promotion_test.go — the promotion ask at `kb finish`: candidates-only finishes pass,
// a promotion is asked with the summary as its signature, a missing summary
// or an undiffable branch is a deny, and the main-checkout bypass is closed.

// promotionFixture is a migrated pool whose architect peer is a git repo with
// a live KB session sess-1 (a real mesh/kb/sess-1 branch and worktree).
func promotionFixture(t *testing.T) (dir string, a *Agent, wt string) {
	t.Helper()
	dir, a = poolGateFixture(t)
	gitInit(t, a.Dir())
	wt, err := KBBegin(dir, a, "sess-1", time.Now())
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	return dir, a, wt
}

// commitKB writes kb-relative files into the worktree's knowledge/ and commits.
func commitKB(t *testing.T, wt string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(wt, KnowledgeDir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# "+f+"\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, wt, "kb edits")
}

const promotionSummary = `## Intent
Record the release playbook and the decision to cut releases on Tuesdays.

## Grounding
- release.yaml:4 runs on Tuesdays.

## Precedent
- *new*: searched the architect KB.

## Changes
### playbooks/release.md
+ releases are cut on Tuesdays by the on-call
- + the release checklist lives in this playbook
- − releases were cut on any weekday
- - the old checklist link
+ a third added bullet
+ a fourth added bullet that the ask must not show
`

func TestGate_PromotionAskAtKBFinish(t *testing.T) {
	dir, a, wt := promotionFixture(t)
	commitKB(t, wt, "candidates/idea.md", "playbooks/release.md", "decisions/0001-tuesday.md")
	sum := writeSummary(t, a, "sess-1", promotionSummary)

	d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb finish --summary-file "+sum))
	if !d.Ask || d.Deny || d.Check != GateCheckPromotion {
		t.Fatalf("want ask/promotion, got %+v", d)
	}
	if !strings.HasPrefix(d.Reason, "Intent: Record the release playbook") {
		t.Fatalf("reason must start with the Intent line:\n%s", d.Reason)
	}
	for _, want := range []string{
		"playbooks/release.md  +4 -2",
		"+ releases are cut on Tuesdays by the on-call",
		"+ the release checklist lives in this playbook",
		"- releases were cut on any weekday",
		"- the old checklist link",
		"decisions/0001-tuesday.md  !! not described in ## Changes",
	} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask lacks %q:\n%s", want, d.Reason)
		}
	}
	if strings.Contains(d.Reason, "must not show") {
		t.Errorf("at most three added bullets per file:\n%s", d.Reason)
	}
	if strings.Contains(d.Reason, "candidates/idea.md") {
		t.Errorf("a candidate is not a promotion and is not listed:\n%s", d.Reason)
	}
	if !strings.HasSuffix(d.Reason, "deny if this was not read to you as a confirmation round") {
		t.Errorf("ask must end with the deny-if-not-read line:\n%s", d.Reason)
	}
	// The rendered length is logged.
	if line := lastGateLogLine(t, a); !strings.Contains(line, "check="+GateCheckPromotion) {
		t.Errorf("last gate log line %q", line)
	}
	b, _ := os.ReadFile(GateLogPath(a.Dir()))
	if !strings.Contains(string(b), "ask-rendered check="+GateCheckPromotion+" chars=") {
		t.Errorf("gate log lacks the rendered length:\n%s", b)
	}
}

func TestGate_PromotionCases(t *testing.T) {
	cases := []struct {
		name    string
		files   []string
		summary string // "" ⇒ none written
		cmd     func(sum string) string
		want    string // "allow" | "ask" | "deny"
		reason  string // substring of the reason
	}{
		{"candidates only, no summary", []string{"candidates/a.md", "candidates/b.md"}, "",
			func(string) string { return "meshctl kb finish" }, "allow", ""},
		{"nothing committed", nil, "",
			func(string) string { return "meshctl kb finish" }, "allow", ""},
		{"abort is never asked", []string{"playbooks/x.md"}, "",
			func(string) string { return "meshctl kb finish --abort" }, "allow", ""},
		{"explicit session and agent", []string{"playbooks/release.md"}, promotionSummary,
			func(s string) string {
				return "meshctl kb finish --agent architect --session sess-1 --summary-file " + s
			}, "ask", "Intent: Record"},
		{"chained after cd", []string{"experience/x.md"}, promotionSummary,
			func(s string) string { return "cd . && meshctl kb finish --summary-file=" + s }, "ask", "experience/x.md  !! not described"},
		{"no --summary-file", []string{"playbooks/x.md"}, promotionSummary,
			func(string) string { return "meshctl kb finish" }, "deny", SummaryPath("/AGENT", "sess-1")},
		{"summary file missing", []string{"playbooks/x.md"}, "",
			func(s string) string { return "meshctl kb finish --summary-file " + s }, "deny", "no file at"},
		{"summary created in the same call", []string{"playbooks/x.md"}, "",
			func(s string) string { return "cp /tmp/x.md " + s + " && meshctl kb finish --summary-file " + s }, "deny",
			"write it in a separate call first"},
		{"summary file empty", []string{"playbooks/x.md"}, "  \n",
			func(s string) string { return "meshctl kb finish --summary-file " + s }, "deny", "empty at"},
		{"summary with no Intent", []string{"playbooks/x.md"}, "## Grounding\n- x\n",
			func(s string) string { return "meshctl kb finish --summary-file " + s }, "deny", "no Intent line at"},
		{"unknown session", []string{"playbooks/x.md"}, promotionSummary,
			func(s string) string { return "meshctl kb finish --session nope --summary-file " + s }, "deny", "cannot diff"},
		{"a mention is not an invocation", []string{"playbooks/x.md"}, "",
			func(string) string { return `grep -n "meshctl kb finish" notes.md` }, "allow", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, a, wt := promotionFixture(t)
			if len(c.files) > 0 {
				commitKB(t, wt, c.files...)
			}
			sum := SummaryPath(a.Dir(), "sess-1")
			if c.summary != "" {
				writeSummary(t, a, "sess-1", c.summary)
			}
			d := GatePreToolUse(dir, bash(a, "sess-1", c.cmd(sum)))
			got := "allow"
			switch {
			case d.Deny:
				got = "deny"
			case d.Ask:
				got = "ask"
			}
			if got != c.want {
				t.Fatalf("want %s, got %+v", c.want, d)
			}
			want := strings.ReplaceAll(c.reason, "/AGENT", a.Dir())
			if want != "" && !strings.Contains(d.Reason, want) {
				t.Fatalf("reason lacks %q:\n%s", want, d.Reason)
			}
			if got == "deny" && c.reason != "cannot diff" && !strings.Contains(d.Reason, sum) {
				t.Fatalf("deny must name the summary path %s:\n%s", sum, d.Reason)
			}
		})
	}
}

func TestGate_PromotionAmbiguousSessionDenied(t *testing.T) {
	dir, a, wt := promotionFixture(t)
	commitKB(t, wt, "playbooks/x.md")
	if _, err := KBBegin(dir, a, "sess-2", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Neither the harness session (sess-9) owns a worktree nor is there a sole one.
	d := GatePreToolUse(dir, bash(a, "sess-9", "meshctl kb finish"))
	if !d.Deny || !strings.Contains(d.Reason, "multiple KB worktrees") {
		t.Fatalf("want deny naming the ambiguity, got %+v", d)
	}
}

func TestPromotionAsk_ThreeFilesUnder1200(t *testing.T) {
	s := ParseSummary(`## Intent
Record the release playbook, the Tuesday decision and the on-call lesson.

## Changes
### playbooks/release.md
+ releases are cut on Tuesdays by the on-call engineer
+ the release checklist lives in this playbook now
+ a hotfix skips the Tuesday window with an approval
- releases were cut on any weekday
- the old checklist link in the wiki
- release notes were written by hand

### decisions/0001-tuesday.md
+ Tuesday is the release day because Monday is planning
+ the decision cites release.yaml as its grounding
- no release day was fixed before

### experience/oncall-release.md
+ an on-call release without the checklist missed a step
+ the checklist now names the migration step first
- nothing was recorded about on-call releases
`)
	text := RenderPromotionAsk("", PromotionAsk{Agent: "architect", Branch: KBBranch("sess-1"), Summary: s,
		Promoted: []string{"playbooks/release.md", "decisions/0001-tuesday.md", "experience/oncall-release.md"}})
	if n := len(text); n > 1200 || n < 400 {
		t.Fatalf("three-file ask is %d chars, want about 600-1200:\n%s", n, text)
	}
}

func TestSummary_BulletFormsParseAsMeant(t *testing.T) {
	cases := []struct {
		line  string
		added bool
		text  string
	}{
		{"+ text one", true, "text one"},
		{"- text two", false, "text two"},
		{"- + text three", true, "text three"},
		{"- − text four", false, "text four"},
		{"- - text five", false, "text five"},
	}
	for _, c := range cases {
		s := ParseSummary("## Intent\nx\n\n## Changes\n### a.md\n" + c.line + "\n")
		if len(s.Changes) != 1 {
			t.Fatalf("%q: no change block", c.line)
		}
		got := s.Changes[0].Removed
		if c.added {
			got = s.Changes[0].Added
		}
		if len(got) != 1 || got[0] != c.text || len(s.Changes[0].Added)+len(s.Changes[0].Removed) != 1 {
			t.Errorf("%q: want added=%v %q, got %+v", c.line, c.added, c.text, s.Changes[0])
		}
	}
}

func TestGate_KnowledgeWritesOnMainCheckoutDenied(t *testing.T) {
	dir, a, wt := promotionFixture(t)
	work := filepath.Join(WorktreesPath(dir), "architect-work-req1")
	os.MkdirAll(filepath.Join(work, KnowledgeDir), 0o755)
	kb := func(root, rel string) string { return filepath.Join(root, KnowledgeDir, rel) }

	deny := []HookPayload{
		editAt(a, "sess-1", "Write", kb(a.Dir(), "playbooks/x.md")),
		editAt(a, "sess-1", "Edit", kb(a.Dir(), "decisions/0001-x.md")),
		editAt(a, "sess-1", "Write", "knowledge/state.md"),
		bash(a, "sess-1", "echo x >> knowledge/experience/y.md"),
		bash(a, "sess-1", "sed -i '' s/a/b/ "+kb(a.Dir(), "glossary.md")),
		bash(a, "sess-1", "cp /tmp/x.md knowledge/playbooks/x.md"),
		bash(a, "sess-1", "rm knowledge/decisions/0001-x.md"),
	}
	for _, p := range deny {
		d := GatePreToolUse(dir, p)
		if !d.Deny || d.Check != GateCheckKBMainWrite || !strings.Contains(d.Reason, "meshctl kb begin") {
			t.Errorf("%s: want deny/%s, got %+v", p.ToolInput, GateCheckKBMainWrite, d)
		}
	}
	allow := []HookPayload{
		editAt(a, "sess-1", "Write", kb(a.Dir(), "candidates/x.md")),
		bash(a, "sess-1", "echo x > knowledge/candidates/y.md"),
		editAt(a, "sess-1", "Edit", kb(a.Dir(), PendingDecisionsFile)),
		bash(a, "sess-1", "echo x >> knowledge/pending-decisions.md"),
		editAt(a, "sess-1", "Write", kb(wt, "playbooks/x.md")),
		editAt(a, "sess-1", "Edit", kb(work, "decisions/0001-x.md")),
		bash(a, "sess-1", "echo x > "+kb(wt, "state.md")),
		bash(a, "sess-1", "cp knowledge/playbooks/x.md /tmp/x.md"),
		bash(a, "sess-1", "cat knowledge/playbooks/x.md 2>/dev/null"),
		editAt(a, "sess-1", "Write", filepath.Join(a.Dir(), ".memory", "notes.md")),
	}
	for _, p := range allow {
		if d := GatePreToolUse(dir, p); d.Deny {
			t.Errorf("%s: want allow, got %+v", p.ToolInput, d)
		}
	}
	// Outside any pool the rule is inert.
	if d := GatePreToolUse("", editAt(a, "sess-1", "Write", kb(a.Dir(), "playbooks/x.md"))); d.Deny {
		t.Errorf("no pool root: want allow, got %+v", d)
	}
}
