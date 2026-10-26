package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// report_json_test.go — `pending --json` and `doctor --json` (mesh-console/01).

func TestPendingReport_ShapeAndPhrases(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	now := time.Now()
	blocks, err := PendingAll(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(NewPendingReport(blocks, now))
	if err != nil {
		t.Fatal(err)
	}
	var r PendingReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Schema != ReportSchema || r.StaleDays != PendingStaleDays || r.Generated == "" {
		t.Fatalf("envelope: %+v", r)
	}
	agents, _, _ := ScanAgents(dir)
	if len(r.Agents) != len(agents) {
		t.Fatalf("--all must list every peer, got %d of %d", len(r.Agents), len(agents))
	}
	var a PendingAgentReport
	for _, x := range r.Agents {
		if x.Agent == arch.Name {
			a = x
		}
	}
	kinds := map[string]PendingLineReport{}
	for _, l := range a.Lines {
		kinds[l.Kind] = l
	}
	// The same phrases the status line shows for the same block.
	for kind, say := range map[string]string{"kb-branches": "confirm kb", "intake": "check intake", "delegated": "review work"} {
		l, ok := kinds[kind]
		if !ok || len(l.Say) != 1 || l.Say[0] != say || l.Count == 0 {
			t.Errorf("line %s: %+v", kind, l)
		}
	}
	line := plainStatusLine(dir, cwdPayload(arch.Dir()))
	for _, l := range a.Lines {
		for _, s := range l.Say {
			if !strings.Contains(line, `say "`+s+`"`) {
				t.Errorf("phrase %q in the report but not on the status line %q", s, line)
			}
		}
	}
	if a.Total == 0 {
		t.Error("total must be the block's item count")
	}
}

func TestPendingReport_NothingPendingHasEmptyArrays(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	b, _ := json.Marshal(NewPendingReport([]PendingBlock{Pending(dir, a, now)}, now))
	if !strings.Contains(string(b), `"lines":[]`) || strings.Contains(string(b), "null") {
		t.Fatalf("empty block must be [] not null: %s", b)
	}
}

func TestDoctorReport_FixableHygieneAndDedup(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	nudge := filepath.Join(a.Dir(), MemoryDir, ".distill-nudge-s9.json")
	_ = os.MkdirAll(filepath.Dir(nudge), 0o755)
	_ = os.WriteFile(nudge, []byte("{}"), 0o644)
	_ = os.Chtimes(nudge, old, old)

	findings, ok, _, _ := DoctorWithHygiene(dir, false, now)
	r := NewDoctorReport(findings, ok, []string{"note"}, now)
	b, _ := json.Marshal(r)
	var got DoctorReportJSON
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != ReportSchema || len(got.Info) != 1 || got.OK != ok {
		t.Fatalf("envelope: %+v", got)
	}
	fixable := 0
	seen := map[string]bool{}
	for _, f := range got.Findings {
		if f.Fixable {
			fixable++
			if strings.Contains(f.Message, "fixable:") {
				t.Errorf("the JSON message must not carry the text suffix: %q", f.Message)
			}
		}
		k := f.Agent + "\x00" + f.Message
		if seen[k] {
			t.Errorf("duplicate finding %q", f.Message)
		}
		seen[k] = true
	}
	if fixable == 0 {
		t.Fatalf("stale nudge marker must be a fixable finding: %+v", got.Findings)
	}
	// --fix repairs it, and the report no longer lists it.
	if _, _, fixed, errs := DoctorWithHygiene(dir, true, now); len(fixed) == 0 || len(errs) != 0 {
		t.Fatalf("fix: %v %v", fixed, errs)
	}
	if fileExists(nudge) {
		t.Error("nudge marker must be removed by --fix")
	}
}
