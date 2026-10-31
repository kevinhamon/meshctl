package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// triage_apply_test.go — `inbox triage apply`:
// the ref resolver, the status mapping and stamps, the refusals (before any
// file changes), the cap, the live-branch refusal and read-back.

// triageReport writes architect's report for today and returns its path.
func triageReport(t *testing.T, a *Agent, lines ...TriageReportLine) string {
	t.Helper()
	path := TriageReportPath(a.Dir(), triageNow)
	if err := WriteTriageReport(path, nil, lines); err != nil {
		t.Fatal(err)
	}
	return path
}

func applyTriage(dir string, a *Agent, report string, ids ...string) (*TriageApplyResult, error) {
	return TriageApply(dir, a, TriageApplyOptions{Report: report, IDs: ids, Now: triageNow})
}

func readReq(t *testing.T, a *Agent, id string) (*Request, string) {
	t.Helper()
	p := filepath.Join(a.InboxPath(), id+".md")
	r, err := ReadRequest(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	return r, string(b)
}

func TestTriageRef_ResolvesTheFourForms(t *testing.T) {
	dir, arch, dev, _ := triagePool(t)
	req := triageAsk(t, dev, "architect", "guidance", "old ask", nil, triageDaysAgo(5))
	if err := os.WriteFile(filepath.Join(arch.Dir(), KnowledgeDir, "queue.md"), []byte("# Queue choice\n\nWe use SQS.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dev.Dir(), "branch", WorkBranchPrefix+"2026-10-01-x")
	sha := strings.TrimSpace(gitOut(t, dev.Dir(), "rev-parse", "--short", "HEAD"))

	for _, ref := range []string{
		"developer:" + req.ID,
		"mesh://architect/queue.md#queue-choice",
		"developer@" + WorkBranchPrefix + "2026-10-01-x",
		"developer@" + sha,
	} {
		if _, err := ResolveTriageRef(dir, ref); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	for _, ref := range []string{
		"developer:2026-01-01-nobody-none",
		"nobody:" + req.ID,
		"mesh://architect/missing.md#x",
		"developer@" + WorkBranchPrefix + "2026-10-01-none",
		"developer@deadbeef",
		"free text",
	} {
		if _, err := ResolveTriageRef(dir, ref); err == nil || !strings.Contains(err.Error(), strings.Fields(ref)[0]) {
			t.Errorf("%s resolved or error does not name it: %v", ref, err)
		}
	}
}

func TestTriageApply_ClosesThroughRespondPath(t *testing.T) {
	dir, arch, dev, analyst := triagePool(t)
	older := triageAsk(t, arch, "developer", "guidance", "pick a message queue for ingest", nil, triageDaysAgo(9))
	newer := triageAsk(t, arch, "analyst", "guidance", "pick a message queue for ingest", nil, triageDaysAgo(2))
	doneReq := triageAsk(t, arch, "developer", "fyi", "review the split", nil, triageDaysAgo(20))
	gone := triageAsk(t, arch, "analyst", "fyi", "the old dashboard", nil, triageDaysAgo(30))
	sha := strings.TrimSpace(gitOut(t, dev.Dir(), "rev-parse", "--short", "HEAD"))
	report := triageReport(t, arch,
		TriageReportLine{ID: older.ID, Verdict: TriageStands, Quote: "pick a message queue for ingest", Source: "request"},
		TriageReportLine{ID: newer.ID, Verdict: TriageSuperseded, Ref: "architect:" + older.ID, Quote: "pick a message queue for ingest", Source: "worksheet siblings"},
		TriageReportLine{ID: doneReq.ID, Verdict: TriageDone, Ref: "developer@" + sha, Quote: "seed", Source: "worksheet knowledge"},
		TriageReportLine{ID: gone.ID, Verdict: TriageWithdrawn, Ref: "architect:" + gone.ID, Quote: "drop the dashboard ask", Source: TriageSourceHuman},
	)

	res, err := applyTriage(dir, arch, report, newer.ID, doneReq.ID, gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Closed) != 3 {
		t.Fatalf("closed = %d", len(res.Closed))
	}
	cases := []struct {
		id, status, stamp, body string
	}{
		{newer.ID, StatusFiled, "superseded → architect:" + older.ID,
			`Closed by triage as superseded → architect:` + older.ID + `. Grounding: "pick a message queue for ingest". Dispute: re-ask with related: ` + newer.ID + `.`},
		{doneReq.ID, StatusFiled, "done → developer@" + sha,
			`Closed by triage as done → developer@` + sha + `. Grounding: "seed". Dispute: re-ask with related: ` + doneReq.ID + `.`},
		{gone.ID, StatusDeclined, "withdrawn → architect:" + gone.ID,
			`Closed by triage as withdrawn → architect:` + gone.ID + `. Grounding: "drop the dashboard ask". Dispute: re-ask with related: ` + gone.ID + `.`},
	}
	for _, c := range cases {
		r, text := readReq(t, arch, c.id)
		if r.Status != c.status || r.Triage != c.stamp || r.Responded != "2026-10-18" {
			t.Errorf("%s: status=%q triage=%q responded=%q", c.id, r.Status, r.Triage, r.Responded)
		}
		if !strings.Contains(r.Body, c.body) {
			t.Errorf("%s: body missing %q:\n%s", c.id, c.body, r.Body)
		}
		if !strings.Contains(text, "triage: ") {
			t.Errorf("%s: no triage stamp in frontmatter:\n%s", c.id, text)
		}
	}
	// Different requesters: the newer requester's body points at the older request.
	if r, _ := readReq(t, arch, newer.ID); !strings.Contains(r.Body, "architect:"+older.ID) || !strings.Contains(r.Body, "read back on") {
		t.Errorf("newer requester not pointed at the older request:\n%s", r.Body)
	}
	if r, _ := readReq(t, arch, older.ID); r.Status != StatusPending {
		t.Errorf("older (stands) changed: %s", r.Status)
	}

	// Read-back: the requester sees the close; archive owes no distillation,
	// only the normal read-back rule.
	sent, _ := ListSent(dir, "analyst", true)
	if len(sent) != 2 {
		t.Fatalf("analyst read-back = %d, want 2 (superseded + withdrawn)", len(sent))
	}
	if md := SentMarkdown(sent); !strings.Contains(md, newer.ID) || !strings.Contains(md, "filed") {
		t.Errorf("read-back list:\n%s", md)
	}
	for _, id := range []string{newer.ID, gone.ID} {
		if _, err := AckSent(dir, "analyst", id, triageNow); err != nil {
			t.Fatal(err)
		}
	}
	archived, skipped, err := ArchiveClosed(arch, false)
	if err != nil || len(archived) != 2 || len(skipped) != 1 {
		t.Fatalf("archive: archived=%v skipped=%v err=%v", archived, skipped, err)
	}
	_ = analyst
}

func TestTriageApply_RefusesBeforeAnyFileChanges(t *testing.T) {
	dir, arch, _, _ := triagePool(t)
	var ids []string
	var lines []TriageReportLine
	for i, ask := range []string{"one ask", "two ask", "three ask", "four ask", "five ask", "six ask"} {
		r := triageAsk(t, arch, "developer", "guidance", ask, nil, triageDaysAgo(10+i))
		ids = append(ids, r.ID)
		lines = append(lines, TriageReportLine{ID: r.ID, Verdict: TriageWithdrawn, Ref: "architect:" + r.ID, Quote: "not needed", Source: TriageSourceHuman})
	}
	stands := triageAsk(t, arch, "developer", "guidance", "stands ask", nil, triageDaysAgo(3))
	unclear := triageAsk(t, arch, "developer", "guidance", "unclear ask", nil, triageDaysAgo(4))
	badRef := triageAsk(t, arch, "developer", "guidance", "bad ref ask", nil, triageDaysAgo(5))
	missing := triageAsk(t, arch, "developer", "guidance", "missing ask", nil, triageDaysAgo(6))
	lines = append(lines,
		TriageReportLine{ID: stands.ID, Verdict: TriageStands, Quote: "still needed", Source: "request"},
		TriageReportLine{ID: unclear.ID, Verdict: TriageUnclear, Quote: "checked: all groups empty", Source: "worksheet"},
		TriageReportLine{ID: badRef.ID, Verdict: TriageDone, Ref: "developer:2026-01-01-nobody-none", Quote: "x", Source: "worksheet"},
	)
	report := triageReport(t, arch, lines...)

	snapshot := func() string {
		var b strings.Builder
		entries, _ := os.ReadDir(arch.InboxPath())
		for _, e := range entries {
			c, _ := os.ReadFile(filepath.Join(arch.InboxPath(), e.Name()))
			b.WriteString(e.Name() + string(c))
		}
		return b.String()
	}
	before := snapshot()
	cases := []struct {
		name string
		ids  []string
		want string
	}{
		{"stands", []string{ids[0], stands.ID}, "stands"},
		{"unclear", []string{unclear.ID, ids[0]}, "unclear"},
		{"missing line", []string{ids[0], missing.ID}, "no report line"},
		{"unresolvable ref", []string{ids[0], badRef.ID}, "developer:2026-01-01-nobody-none"},
		{"sixth id", ids, "at most 5"},
		{"none", nil, "at least one"},
	}
	for _, c := range cases {
		if _, err := applyTriage(dir, arch, report, c.ids...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want %q", c.name, err, c.want)
		}
		if snapshot() != before {
			t.Fatalf("%s: refusal changed the inbox", c.name)
		}
	}
	if _, err := applyTriage(dir, arch, "", ids[0]); err == nil || !strings.Contains(err.Error(), "--report") {
		t.Errorf("no report: %v", err)
	}
	// Headless: refused with the triage refusal.
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if _, err := applyTriage(dir, arch, report, ids[0]); err == nil || !strings.Contains(err.Error(), "dispatch depth") {
		t.Errorf("headless apply: %v", err)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	// The five that are fine do close.
	if _, err := applyTriage(dir, arch, report, ids[:5]...); err != nil {
		t.Fatalf("five ids: %v", err)
	}
}

func TestTriageApply_LiveWorkBranchRefusesAndMarksLine(t *testing.T) {
	dir, arch, _, _ := triagePool(t)
	viaBranch := triageAsk(t, arch, "developer", "guidance", "branch ask", nil, triageDaysAgo(10))
	viaTree := triageAsk(t, arch, "developer", "guidance", "worktree ask", nil, triageDaysAgo(11))
	gitOut(t, arch.Dir(), "branch", WorkBranchPrefix+viaBranch.ID)
	if _, err := WorkBegin(dir, arch, viaTree.ID); err != nil {
		t.Fatal(err)
	}
	report := triageReport(t, arch,
		TriageReportLine{ID: viaBranch.ID, Verdict: TriageWithdrawn, Ref: "architect:" + viaBranch.ID, Quote: "gone", Source: TriageSourceHuman},
		TriageReportLine{ID: viaTree.ID, Verdict: TriageWithdrawn, Ref: "architect:" + viaTree.ID, Quote: "gone", Source: TriageSourceHuman},
	)
	_, err := applyTriage(dir, arch, report, viaBranch.ID, viaTree.ID)
	if err == nil || !strings.Contains(err.Error(), "branch live") || !strings.Contains(err.Error(), "work merge") {
		t.Fatalf("live branch: %v", err)
	}
	r, rerr := ReadTriageReport(report)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, id := range []string{viaBranch.ID, viaTree.ID} {
		if l, _ := r.Line(id); !l.BranchLive {
			t.Errorf("%s line not marked branch live: %q", id, l.Raw)
		}
		if req, _ := readReq(t, arch, id); req.Status != StatusPending {
			t.Errorf("%s closed despite live branch", id)
		}
	}
	if out := gitOut(t, arch.Dir(), "branch", "--list", WorkBranchPrefix+"*"); !strings.Contains(out, viaBranch.ID) || !strings.Contains(out, viaTree.ID) {
		t.Fatalf("a work branch was deleted: %q", out)
	}
	// Re-applying keeps a single suffix.
	_, _ = applyTriage(dir, arch, report, viaBranch.ID)
	b, _ := os.ReadFile(report)
	if strings.Count(string(b), TriageBranchLive) != 2 {
		t.Fatalf("suffix duplicated:\n%s", b)
	}
}

func TestTriageApplyGate_AskedWithReportLinesDeniedWithoutReport(t *testing.T) {
	dir, arch, _, _ := triagePool(t)
	a := triageAsk(t, arch, "developer", "guidance", "first ask", nil, triageDaysAgo(10))
	b := triageAsk(t, arch, "developer", "guidance", "second ask", nil, triageDaysAgo(11))
	other := triageAsk(t, arch, "developer", "guidance", "other ask", nil, triageDaysAgo(12))
	report := triageReport(t, arch,
		TriageReportLine{ID: a.ID, Verdict: TriageWithdrawn, Ref: "architect:" + a.ID, Quote: "not needed any more", Source: TriageSourceHuman},
		TriageReportLine{ID: b.ID, Verdict: TriageDone, Ref: "architect:" + a.ID, Quote: "covered by the first", Source: "worksheet"},
		TriageReportLine{ID: other.ID, Verdict: TriageStands, Quote: "still wanted", Source: "request"},
	)
	bash := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{ToolName: "Bash", ToolInput: in, Cwd: arch.Dir(), SessionID: "s1"}
	}

	d := gateEvaluate(dir, bash("meshctl inbox triage apply --report "+report+" "+a.ID+" "+b.ID))
	if !d.Ask || d.Check != GateCheckTriage {
		t.Fatalf("apply not asked: %+v", d)
	}
	for _, want := range []string{"- " + a.ID + " · withdrawn → architect:" + a.ID, "- " + b.ID + " · done → architect:" + a.ID, "not needed any more"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask text missing %q:\n%s", want, d.Reason)
		}
	}
	if strings.Contains(d.Reason, other.ID) {
		t.Errorf("ask text carries a line for an id not applied:\n%s", d.Reason)
	}
	// Relative report path, --agent and --report= forms.
	rel, _ := filepath.Rel(arch.Dir(), report)
	if d := gateEvaluate(dir, bash("meshctl inbox --agent=architect triage apply --report="+rel+" "+a.ID)); !d.Ask {
		t.Errorf("relative --report= form not asked: %+v", d)
	}

	if d := gateEvaluate(dir, bash("meshctl inbox triage apply "+a.ID)); !d.Deny || !strings.Contains(d.Reason, "--report") {
		t.Errorf("no report not denied: %+v", d)
	}
	if d := gateEvaluate(dir, bash("meshctl inbox triage apply --report /nope/report.md "+a.ID)); !d.Deny {
		t.Errorf("unreadable report not denied: %+v", d)
	}
	// A mention is not an invocation; the worksheet command is not the apply.
	if d := gateEvaluate(dir, bash(`echo "meshctl inbox triage apply --report x"`)); d.Ask || d.Deny {
		t.Errorf("mention gated: %+v", d)
	}
	if d := gateEvaluate(dir, bash("meshctl inbox triage --peer architect")); d.Ask || d.Deny {
		t.Errorf("worksheet command gated: %+v", d)
	}
	// No human at the prompt: denied.
	consult := bash("meshctl inbox triage apply --report " + report + " " + a.ID)
	consult.InSession = true
	if d := gateEvaluate(dir, consult); !d.Deny || !strings.Contains(d.Reason, "consult") {
		t.Errorf("consult apply not denied: %+v", d)
	}
	work := consult
	work.Work = true
	if d := gateEvaluate(dir, work); !d.Ask {
		t.Errorf("hub doing the peer's work not asked: %+v", d)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if d := gateEvaluate(dir, bash("meshctl inbox triage apply --report "+report+" "+a.ID)); !d.Deny || !strings.Contains(d.Reason, "dispatch depth") {
		t.Errorf("headless apply not denied: %+v", d)
	}
}

func TestDoctor_TriageAgeAndStaleCountClosesNothing(t *testing.T) {
	dir, arch, dev, _ := triagePool(t)
	now := time.Now()
	old1 := triageAsk(t, arch, "developer", "guidance", "old one", nil, now.AddDate(0, 0, -20))
	triageAsk(t, arch, "developer", "guidance", "old two", nil, now.AddDate(0, 0, -30))
	triageAsk(t, arch, "developer", "guidance", "fresh", nil, now.AddDate(0, 0, -2))
	triageAsk(t, dev, "architect", "guidance", "old for dev", nil, now.AddDate(0, 0, -40))
	if err := WriteTriageReport(TriageReportPath(arch.Dir(), now.AddDate(0, 0, -3)), nil, []TriageReportLine{
		{ID: old1.ID, Verdict: TriageStands, Quote: "still wanted", Source: "request"},
	}); err != nil {
		t.Fatal(err)
	}
	findings, _ := Doctor(dir)
	var archMsg, devMsg string
	for _, f := range findings {
		if strings.Contains(f.Message, "older than 14d") {
			switch f.Agent {
			case "architect":
				archMsg = f.Message
			case "developer":
				devMsg = f.Message
			}
		}
	}
	if !strings.Contains(archMsg, "2 open requests") || !strings.Contains(archMsg, "last triage report 3d ago") {
		t.Errorf("architect finding = %q", archMsg)
	}
	if !strings.Contains(devMsg, "1 open request") || !strings.Contains(devMsg, "never triaged") || !strings.Contains(devMsg, "meshctl inbox triage --peer developer") {
		t.Errorf("developer finding = %q", devMsg)
	}
	if r, _ := readReq(t, arch, old1.ID); r.Status != StatusPending {
		t.Errorf("doctor closed a request: %s", r.Status)
	}
}
