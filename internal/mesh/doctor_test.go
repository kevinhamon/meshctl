package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceMissingEffective(t *testing.T) {
	kb := t.TempDir()
	ref := filepath.Join(kb, "reference")
	if err := os.MkdirAll(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(n, s string) {
		if err := os.WriteFile(filepath.Join(ref, n), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Reference\n")                                  // ignored
	write("dated.md", "---\nsource: X\neffective: 2026-04-01\n---\nbody") // ok
	write("undated.md", "---\nsource: Y\nurl: z\n---\nbody")              // flagged

	got := referenceMissingEffective(kb)
	if len(got) != 1 || !strings.Contains(got[0], "undated.md") {
		t.Fatalf("want exactly the undated note flagged, got: %v", got)
	}
	// No reference/ dir → no findings.
	if r := referenceMissingEffective(t.TempDir()); r != nil {
		t.Fatalf("no reference dir should yield nil, got: %v", r)
	}
}

func TestKBConsistencyFindings(t *testing.T) {
	kb := t.TempDir()
	dec := filepath.Join(kb, "decisions")
	if err := os.MkdirAll(dec, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 0007 accepted, in README as accepted (clean).
	write(filepath.Join(dec, "0007-arck.md"), "# 0007. Arck\n- **Status:** accepted\n")
	// 0018 accepted in file, but README says proposed (status mismatch).
	write(filepath.Join(dec, "0018-capture.md"), "# 0018. Capture\n- **Status:** accepted\n")
	// 0020 file exists but NO README row (missing-row warn).
	write(filepath.Join(dec, "0020-boundary.md"), "# 0020. Boundary\n- **Status:** accepted\nrefs ADR-0099 which does not exist\n")
	write(filepath.Join(dec, "README.md"),
		"| ADR | Summary | Status | Date |\n"+
			"| [0007](0007-arck.md) | arck | accepted | 2026 |\n"+
			"| [0018](0018-capture.md) | capture | proposed | 2026 |\n"+
			"| [0099](0099-ghost.md) | ghost | accepted | 2026 |\n") // row for a missing file
	// direction.md with a stale pending-ADR marker + a broken ADR ref.
	write(filepath.Join(kb, "direction.md"), "FaaS platform: pending platform-choice ADR.\nSee ADR-0007.\n")

	got := kbConsistencyFindings("architect", kb)
	joined := ""
	for _, f := range got {
		if f.Level != "warn" {
			t.Errorf("consistency findings must be warn, got %q: %s", f.Level, f.Message)
		}
		joined += f.Message + "\n"
	}

	wants := []string{
		`lists ADR 18 as "proposed" but the file says "accepted"`, // status mismatch
		"decisions/0020-boundary.md has no row",                    // missing README row
		"README.md row references ADR 99 but no such file",         // orphan README row
		"0020-boundary.md references ADR-99 but no such",           // broken cross-ref in body (num is zero-stripped)
		"'pending … ADR' marker",                                   // stale pending marker
	}
	must := []string{wants[0], wants[1], wants[2], wants[3], wants[4]}
	for _, w := range must {
		if !strings.Contains(joined, w) {
			t.Errorf("missing expected finding %q\n--- got ---\n%s", w, joined)
		}
	}
	// 0007 is clean — no finding should mention ADR 7 status.
	if strings.Contains(joined, "ADR 7 as") {
		t.Errorf("0007 is consistent but was flagged:\n%s", joined)
	}
}
