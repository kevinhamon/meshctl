package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// statusline_test.go — the Claude Code status line.

var reANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plainStatusLine(dir string, p StatusLinePayload) string {
	return reANSI.ReplaceAllString(StatusLine(dir, p, time.Now()), "")
}

func cwdPayload(cwd string) StatusLinePayload {
	var p StatusLinePayload
	p.Cwd = cwd
	return p
}

func TestStatusLine_ShowsWhatWaitsOnTheHuman(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	got := plainStatusLine(dir, cwdPayload(arch.Dir()))
	want := `[agent: architect] * [needs-confirm: 1, say "confirm kb"] * [intake: 2, oldest: 3d, say "check intake"] * [dispatched: 1, oldest: 0d, say "review work"]`
	if got != want {
		t.Fatalf("status line:\n got %q\nwant %q", got, want)
	}
}

func TestStatusLine_NeedsConfirmIsRed(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	got := StatusLine(dir, cwdPayload(arch.Dir()), time.Now())
	if !regexp.MustCompile(`needs-confirm:\x1b\[0m \x1b\[31m1`).MatchString(got) {
		t.Fatalf("needs-confirm count not red: %q", got)
	}
}

func TestStatusLine_OverdueCandidatesCueConsolidate(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	for _, n := range []string{"a.md", "b.md"} {
		ageFile(t, filepath.Join(arch.Dir(), KnowledgeDir, "candidates", n), PendingStaleDays+1)
	}
	got := plainStatusLine(dir, cwdPayload(arch.Dir()))
	if want := `[kb: 2 overdue, say "consolidate"]`; !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(got) {
		t.Fatalf("missing %q in %q", want, got)
	}
}

// Unconfirmed entries show whenever the KB health line counts any, with the
// reconcile phrase; fresh candidates alone stay off the line.
func TestStatusLine_UnconfirmedEntriesCueReconcile(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	for rel, body := range map[string]string{
		"reference/renewal.md": "# Renewal\n\nWe net daily.\n",
		"candidates/fresh.md":  "# Fresh\n",
	} {
		p := filepath.Join(a.Dir(), KnowledgeDir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := len(UnconfirmedCurrentEntries(a.Dir()))
	if n == 0 {
		t.Fatal("fixture: want unconfirmed entries")
	}
	got := plainStatusLine(dir, cwdPayload(a.Dir()))
	if want := fmt.Sprintf(`[agent: architect] * [unconfirmed: %d, say "reconcile"]`, n); got != want {
		t.Fatalf("status line:\n got %q\nwant %q", got, want)
	}
}

func TestStatusLine_NothingPending(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	if got, want := plainStatusLine(dir, cwdPayload(a.Dir())), "[agent: architect] * [pending: none]"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStatusLine_ResolvesFromSubdirectoryAndProjectDir(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	sub := filepath.Join(a.Dir(), "repos", "service")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := plainStatusLine(dir, cwdPayload(sub)); got != "[agent: architect] * [pending: none]" {
		t.Fatalf("subdirectory: got %q", got)
	}
	var p StatusLinePayload
	p.Workspace.ProjectDir = a.Dir()
	p.Workspace.CurrentDir = t.TempDir()
	if got := plainStatusLine(dir, p); got != "[agent: architect] * [pending: none]" {
		t.Fatalf("project dir: got %q", got)
	}
}

func TestStatusLine_OutsideAPoolShowsTheDirectory(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	// A directory named like an agent but outside the pool is not that agent.
	other := filepath.Join(t.TempDir(), "architect")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := plainStatusLine(dir, cwdPayload(other)); got != "architect" {
		t.Fatalf("got %q, want the bare directory name", got)
	}
	if got := plainStatusLine("", cwdPayload(other)); got != "architect" {
		t.Fatalf("no pool: got %q", got)
	}
}

func TestStatusLine_InvalidPayloadIsTolerated(t *testing.T) {
	if p := ParseStatusLinePayload([]byte("not json")); p.Cwd != "" {
		t.Fatalf("got %+v", p)
	}
	p := ParseStatusLinePayload([]byte(`{"cwd":"/a","workspace":{"current_dir":"/b","project_dir":"/c"}}`))
	if p.Cwd != "/a" || p.Workspace.CurrentDir != "/b" || p.Workspace.ProjectDir != "/c" {
		t.Fatalf("got %+v", p)
	}
}

// Every phrase the line tells the human to say is defined where the agent
// reads its standing orders, or the hint would rely on the model guessing.
func TestStatusLine_EveryPhraseIsDefinedInTheStandingOrders(t *testing.T) {
	texts := templateTexts(t)
	for _, name := range []string{"cognitionSection", "templates/CLAUDE.md", "templates/skills/mesh-cognition/SKILL.md"} {
		text := texts[name]
		if !strings.Contains(text, "`meshctl pending` first") {
			t.Errorf("%s: phrases must start from a fresh `meshctl pending`", name)
		}
		for _, phrase := range StatusLinePhrases {
			if !strings.Contains(text, `"`+phrase+`":`) {
				t.Errorf("%s does not define the status-line phrase %q", name, phrase)
			}
		}
	}
}
