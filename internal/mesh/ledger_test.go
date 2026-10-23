package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ledgerNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

// The confirmation ledger (ADR-0067, spec decision 21): a tracked content-hash
// ledger at knowledge/.confirmed.yaml, one scheme for every entry type.

const ledgerShape = `playbooks/release.md:
  hash: sha256:9f2c
  confirmed: 2026-10-16
  session: f93b9f92
state.md:
  sections:
    current-focus: sha256:ab12
    open-questions: sha256:cd34
`

func TestLedger_RoundTripsTheShape(t *testing.T) {
	l, err := ParseLedger([]byte(ledgerShape))
	if err != nil {
		t.Fatal(err)
	}
	pb := l.Entries["playbooks/release.md"]
	if pb.Hash != "sha256:9f2c" || pb.Confirmed != "2026-10-16" || pb.Session != "f93b9f92" || len(pb.Sections) != 0 {
		t.Fatalf("file entry: %+v", pb)
	}
	st := l.Entries["state.md"]
	if st.Hash != "" || st.Sections["current-focus"] != "sha256:ab12" || st.Sections["open-questions"] != "sha256:cd34" {
		t.Fatalf("sectioned entry: %+v", st)
	}
	if got := string(RenderLedger(l)); got != ledgerShape {
		t.Fatalf("render must reproduce the shape:\n%s", got)
	}
}

func TestLedger_RenderSortsKeys(t *testing.T) {
	l := Ledger{Entries: map[string]LedgerEntry{
		"state.md":            {Sections: map[string]string{"open-questions": "sha256:2", "now": "sha256:1"}},
		"architecture/x.md":   {Hash: "sha256:3"},
		"decisions/0001-a.md": {Hash: "sha256:4", Confirmed: "2026-10-18", Session: "s1"},
		"reference/a file.md": {Hash: "sha256:5"},
	}}
	want := `architecture/x.md:
  hash: sha256:3
decisions/0001-a.md:
  hash: sha256:4
  confirmed: 2026-10-18
  session: s1
"reference/a file.md":
  hash: sha256:5
state.md:
  sections:
    now: sha256:1
    open-questions: sha256:2
`
	got := string(RenderLedger(l))
	if got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
	back, err := ParseLedger([]byte(got))
	if err != nil || back.Entries["reference/a file.md"].Hash != "sha256:5" {
		t.Fatalf("quoted key must parse back: %+v %v", back, err)
	}
	if string(RenderLedger(Ledger{})) != "{}\n" {
		t.Fatalf("empty ledger: %q", RenderLedger(Ledger{}))
	}
}

func TestLedger_HashesFilesAndSections(t *testing.T) {
	if HashBytes([]byte("abc")) != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("file hash is sha256 of the bytes: %s", HashBytes([]byte("abc")))
	}
	body := "<!-- meshctl state — a updated 2026-10-16T00:00:00Z. Current-state digest. -->\n\n## Now\n\nShipping.\n\n## Open questions\n\n- none\n"
	s := SectionHashes(body)
	if len(s) != 2 || s["now"] != HashBytes([]byte("## Now\n\nShipping.")) || s["open-questions"] != HashBytes([]byte("## Open questions\n\n- none")) {
		t.Fatalf("section hashes are of the trimmed section text: %+v", s)
	}
	// The state header comment is excluded: a rewrite that only moves the
	// timestamp leaves every section hash unchanged.
	again := strings.Replace(body, "2026-10-16", "2026-10-18", 1)
	if s2 := SectionHashes(again); s2["now"] != s["now"] || len(s2) != 2 {
		t.Fatalf("header must not affect sections: %+v", s2)
	}
	// One edited section changes only its own hash; text before the first
	// heading is its own anchor so an edit there is not invisible.
	edited := SectionHashes(strings.Replace(body, "Shipping.", "Paused.", 1) + "")
	if edited["now"] == s["now"] || edited["open-questions"] != s["open-questions"] {
		t.Fatalf("per-section granularity: %+v", edited)
	}
	if p := SectionHashes("Intro line.\n\n## Now\nx\n"); p[LedgerPreamble] == "" || p["now"] == "" {
		t.Fatalf("preamble anchor: %+v", p)
	}
	if !LedgerSectioned("state.md") || !LedgerSectioned("glossary.md") || LedgerSectioned("playbooks/state.md") || LedgerSectioned("playbooks/release.md") {
		t.Fatal("sectioned files are the state digest and a legacy peer glossary at the KB root")
	}
}

// confirmKB records rels (knowledge-relative) as confirmed at their current
// content, the way the write side (kb finish, state set) will.
func confirmKB(t *testing.T, a *Agent, rels ...string) {
	t.Helper()
	l, err := ReadLedger(a.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	for _, rel := range rels {
		e, err := CurrentLedgerEntry(a.Dir(), rel)
		if err != nil {
			t.Fatal(err)
		}
		e.Confirmed, e.Session = "2026-10-18", "test"
		l.Entries[rel] = e
	}
	if err := WriteLedger(a.Dir(), l); err != nil {
		t.Fatal(err)
	}
}

func TestLedger_ConfirmedIffHashMatches(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	writeKB(t, a, "playbooks/release.md", "# Release\n\nTag then push.\n")
	StateSet(a, "## Now\n\nShipping.\n\n## Open questions\n\n- none\n", ledgerNow)

	l, _ := ReadLedger(a.Dir())
	if l.Confirmed(a.Dir(), "playbooks/release.md") {
		t.Fatal("not in the ledger ⇒ unconfirmed")
	}
	confirmKB(t, a, "playbooks/release.md", StateFile)
	l, err := ReadLedger(a.Dir())
	if err != nil || !l.Present {
		t.Fatalf("ledger: %+v %v", l, err)
	}
	if !l.Confirmed(a.Dir(), "playbooks/release.md") || !l.Confirmed(a.Dir(), StateFile) {
		t.Fatalf("matching hashes ⇒ confirmed: %+v", l)
	}
	b, _ := os.ReadFile(LedgerPath(a.Dir()))
	if !strings.Contains(string(b), "  sections:\n    now: sha256:") || strings.Contains(string(b), "state.md:\n  hash:") {
		t.Fatalf("state.md is ledgered per section:\n%s", b)
	}

	// An edit after confirmation unconfirms by construction.
	writeKB(t, a, "playbooks/release.md", "# Release\n\nPush then tag.\n")
	if l.Confirmed(a.Dir(), "playbooks/release.md") {
		t.Fatal("edited file must read unconfirmed")
	}
	// One edited section unconfirms only that section.
	StateSet(a, "## Now\n\nPaused.\n\n## Open questions\n\n- none\n", ledgerNow)
	if l.SectionConfirmed(a.Dir(), StateFile, "now") || !l.SectionConfirmed(a.Dir(), StateFile, "open-questions") || l.Confirmed(a.Dir(), StateFile) {
		t.Fatal("per-section confirmation")
	}
	if got := LedgerPath(a.Dir()); got != filepath.Join(a.Dir(), KnowledgeDir, ".confirmed.yaml") {
		t.Fatalf("ledger path: %s", got)
	}
}

func TestLedger_OnboardScaffoldsEmptyTrackedLedger(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	b, err := os.ReadFile(LedgerPath(a.Dir()))
	if err != nil || string(b) != "{}\n" {
		t.Fatalf("new peer must carry an empty ledger: %q %v", b, err)
	}
	if out := gitOut(t, a.Dir(), "ls-files", KnowledgeDir+"/"+LedgerFile); strings.TrimSpace(out) != KnowledgeDir+"/"+LedgerFile {
		t.Fatalf("ledger must be tracked: %q", out)
	}

	// Onboarding an existing workspace adds it once and never clobbers one.
	ws := filepath.Join(dir, "legacy-peer")
	if err := os.MkdirAll(filepath.Join(ws, KnowledgeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Onboard(dir, ws, AgentSpec{Title: "Legacy", Role: "r"}, ScaffoldOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(LedgerPath(ws)); string(b) != "{}\n" {
		t.Fatalf("onboard must scaffold the ledger: %q", b)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), LedgerFile) {
		t.Fatalf("onboard reports the scaffold: %+v", res.Notes)
	}
	if err := os.WriteFile(LedgerPath(ws), []byte(ledgerShape), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Onboard(dir, ws, AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(LedgerPath(ws)); string(b) != ledgerShape {
		t.Fatalf("re-onboard must not clobber the ledger: %q", b)
	}
}
