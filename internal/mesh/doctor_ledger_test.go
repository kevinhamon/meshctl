package mesh

import (
	"os"
	"strings"
	"testing"
)

// Doctor and the ledger (ADR-0067, spec decision 33): a ledger line with no
// matching file or section, an accepted ADR that is unconfirmed, and the count
// of unconfirmed current-tier entries per peer.

func TestDoctor_FreshPeerHasNoLedgerFindings(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "grid-expert", true, nil)
	fs, _ := Doctor(dir)
	for _, f := range agentFindings(fs, "grid-expert", "") {
		if strings.Contains(f.Message, "unconfirmed") || strings.Contains(f.Message, LedgerFile) {
			t.Fatalf("a fresh peer (scaffolded READMEs, empty ledger) is clean: %+v", f)
		}
	}
}

func TestDoctor_LedgerFindings(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	writeKB(t, a, "playbooks/release.md", "# Release\n")
	writeKB(t, a, "playbooks/gone.md", "# Gone\n")
	writeKB(t, a, "architecture/settlement.md", "# Settlement\n")
	writeKB(t, a, "decisions/0001-confirmed.md", "---\nstatus: accepted\n---\n# 0001. Confirmed\n")
	writeKB(t, a, "decisions/0002-accepted.md", "---\nstatus: accepted\n---\n# 0002. Accepted only\n")
	writeKB(t, a, "decisions/0003-proposed.md", "---\nstatus: proposed\n---\n# 0003. Proposed\n")
	if err := StateSet(a, "## Now\n\nx\n\n## Old section\n\ny\n", ledgerNow); err != nil {
		t.Fatal(err)
	}
	confirmKB(t, a, "playbooks/release.md", "playbooks/gone.md", "decisions/0001-confirmed.md", StateFile)
	// Drift: a confirmed file deleted, a confirmed section removed.
	if err := os.Remove(writeKB(t, a, "playbooks/gone.md", "")); err != nil {
		t.Fatal(err)
	}
	if err := StateSet(a, "## Now\n\nx\n", ledgerNow); err != nil {
		t.Fatal(err)
	}

	fs := agentFindings(func() []DoctorFinding { f, _ := Doctor(dir); return f }(), "grid-expert", "")
	for _, want := range []string{
		"lists playbooks/gone.md",
		"lists state.md#old-section",
		"decisions/0002-accepted.md is accepted but unconfirmed",
		"1 unconfirmed current-tier entry: architecture/settlement.md",
	} {
		if !hasFinding(fs, want) {
			t.Fatalf("missing %q in %+v", want, fs)
		}
	}
	for _, not := range []string{"0001-confirmed.md is accepted", "0003-proposed.md is accepted", "lists state.md#now", "lists playbooks/release.md"} {
		if hasFinding(fs, not) {
			t.Fatalf("unexpected %q in %+v", not, fs)
		}
	}
	for _, f := range fs {
		if strings.Contains(f.Message, "unconfirmed") && f.Level != "warn" {
			t.Fatalf("ledger findings are warnings: %+v", f)
		}
	}

	// No ledger: every current-tier entry counts (state sections included),
	// with the absence named.
	if err := os.Remove(LedgerPath(a.Dir())); err != nil {
		t.Fatal(err)
	}
	fs = agentFindings(func() []DoctorFinding { f, _ := Doctor(dir); return f }(), "grid-expert", "")
	if !hasFinding(fs, "3 unconfirmed current-tier entries") || !hasFinding(fs, "no "+KnowledgeDir+"/"+LedgerFile) {
		t.Fatalf("no-ledger count: %+v", fs)
	}

	// An unparseable ledger is a warning and reads as empty.
	if err := os.WriteFile(LedgerPath(a.Dir()), []byte("a: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs = agentFindings(func() []DoctorFinding { f, _ := Doctor(dir); return f }(), "grid-expert", "")
	if !hasFinding(fs, "unparseable") || !hasFinding(fs, "3 unconfirmed current-tier entries") {
		t.Fatalf("unparseable ledger: %+v", fs)
	}
}
