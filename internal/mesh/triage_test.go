package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// triage_test.go — the intake triage pass, worksheet half (ADR-0072; spec
// decisions 55, 56): the hub walk order, the per-peer worksheet and its
// signal groups, and the "no human at the prompt" refusals.

var triageNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func triageDaysAgo(n int) time.Time { return triageNow.AddDate(0, 0, -n) }

// triagePool is a multi-peer temporary pool: architect and developer are
// canonical peers with git repos, analyst a third peer.
func triagePool(t *testing.T) (dir string, arch, dev, analyst *Agent) {
	t.Helper()
	gitIdentity(t)
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv(distillGuardEnv, "")
	dir = meshRoot(t)
	arch = newTestAgent(t, dir, "architect", true, nil)
	dev = newTestAgent(t, dir, "developer", false, []string{"code"})
	analyst = newTestAgent(t, dir, "analyst", true, nil)
	for _, a := range []*Agent{arch, dev, analyst} {
		gitInit(t, a.Dir())
	}
	return dir, arch, dev, analyst
}

func triageAsk(t *testing.T, to *Agent, from, typ, prompt string, related []string, at time.Time) *Request {
	t.Helper()
	r, err := WriteRequest(to, typ, prompt, from, "P2", related, at)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func triageClose(t *testing.T, a *Agent, id, status string) {
	t.Helper()
	if _, err := RespondRequest(a, id, RespondOptions{Status: status, Response: "closed in test", Now: triageNow}); err != nil {
		t.Fatal(err)
	}
}

func TestTriageWalkOrder_OldestStaleFirstAndWritesNothing(t *testing.T) {
	dir, arch, dev, analyst := triagePool(t)
	triageAsk(t, arch, "developer", "guidance", "pick a queue", nil, triageDaysAgo(3))
	triageAsk(t, arch, "user", "fyi", "note the outage", nil, triageDaysAgo(1))
	triageAsk(t, dev, "architect", "guidance", "review the split", nil, triageDaysAgo(20))
	closed := triageAsk(t, analyst, "user", "guidance", "old closed question", nil, triageDaysAgo(40))
	triageClose(t, analyst, closed.ID, StatusAnswered)

	walk, err := TriageWalkOrder(dir, triageNow)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range walk {
		names = append(names, w.Peer)
	}
	if got := strings.Join(names, ","); got != "developer,architect,analyst" {
		t.Fatalf("walk order = %s, want developer (20d) before architect (3d), empty analyst last", got)
	}
	if walk[0].Open != 1 || walk[0].OldestDays != 20 || walk[1].Open != 2 || walk[1].OldestDays != 3 || walk[2].Open != 0 {
		t.Fatalf("counts/ages wrong: %+v", walk)
	}
	md := TriageWalkMarkdown(walk)
	for _, want := range []string{"developer", "1 open", "oldest 20d", "⚠ stale", "architect", "2 open", "meshctl inbox triage --peer"} {
		if !strings.Contains(md, want) {
			t.Errorf("walk markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "architect · 2 open · oldest 3d ⚠") {
		t.Errorf("3-day-old request marked stale:\n%s", md)
	}
	for _, a := range []*Agent{arch, dev, analyst} {
		if dirExists(filepath.Join(a.Dir(), MemoryDir, TriageDir)) {
			t.Fatalf("walk order wrote a triage dir for %s", a.Name)
		}
	}
}

func TestTriageRefusal_NoHumanAtThePrompt(t *testing.T) {
	triagePool(t)
	if r := TriageRefusal(TriageMode{}); r != "" {
		t.Fatalf("own interactive session refused: %s", r)
	}
	if r := TriageRefusal(TriageMode{InSession: true, Work: true}); r != "" {
		t.Fatalf("hub doing the peer's work refused: %s", r)
	}
	if r := TriageRefusal(TriageMode{InSession: true}); !strings.Contains(r, "consult") {
		t.Fatalf("consult mode not refused with reason: %q", r)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if r := TriageRefusal(TriageMode{}); !strings.Contains(r, "dispatch depth 1") {
		t.Fatalf("dispatch depth not refused with reason: %q", r)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv(distillGuardEnv, "1")
	if r := TriageRefusal(TriageMode{}); !strings.Contains(r, "distil") {
		t.Fatalf("distilling not refused with reason: %q", r)
	}
}

func TestTriageWorksheet_RefusedHeadlessWritesNothing(t *testing.T) {
	dir, arch, _, _ := triagePool(t)
	triageAsk(t, arch, "developer", "guidance", "pick a queue", nil, triageDaysAgo(3))
	t.Setenv("AGENT_DISPATCH_DEPTH", "2")
	if _, err := WriteTriageWorksheet(dir, arch, TriageMode{}, triageNow); err == nil || !strings.Contains(err.Error(), "dispatch depth") {
		t.Fatalf("worksheet under dispatch depth: err=%v", err)
	}
	if _, err := InboxTriage(dir, nil, TriageMode{}, triageNow); err == nil {
		t.Fatal("walk order under dispatch depth not refused")
	}
	if dirExists(filepath.Join(arch.Dir(), MemoryDir, TriageDir)) {
		t.Fatal("refused worksheet wrote files")
	}
}

func TestTriageGate_ConsultDeniedWorkAllowed(t *testing.T) {
	dir, _, _, _ := triagePool(t)
	cmd, _ := json.Marshal(map[string]string{"command": "meshctl inbox triage --peer architect"})
	consult := HookPayload{ToolName: "Bash", ToolInput: cmd, InSession: true, Cwd: filepath.Join(dir, "architect")}
	d := gateEvaluate(dir, consult)
	if !d.Deny || d.Check != GateCheckTriage || !strings.Contains(d.Reason, "consult") {
		t.Fatalf("consult triage: %+v", d)
	}
	work := consult
	work.Work = true
	if d := gateEvaluate(dir, work); d.Deny {
		t.Fatalf("work-mode triage denied: %+v", d)
	}
	mention, _ := json.Marshal(map[string]string{"command": `grep "meshctl inbox triage" README.md`})
	if d := gateEvaluate(dir, HookPayload{ToolName: "Bash", ToolInput: mention, InSession: true, Cwd: filepath.Join(dir, "architect")}); d.Deny {
		t.Fatalf("a mention is not an invocation: %+v", d)
	}
}

// worksheetFor builds + writes architect's worksheet and returns it with its text.
func worksheetFor(t *testing.T, dir string, a *Agent) (*TriageWorksheet, string) {
	t.Helper()
	ws, err := WriteTriageWorksheet(dir, a, TriageMode{}, triageNow)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ws.Path)
	if err != nil {
		t.Fatal(err)
	}
	return ws, string(b)
}

// section returns the worksheet section for one request id.
func triageSection(t *testing.T, text, id string) string {
	t.Helper()
	i := strings.Index(text, "## "+id)
	if i < 0 {
		t.Fatalf("no section for %s:\n%s", id, text)
	}
	rest := text[i+3:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestTriageWorksheet_DatedPathOpenRequestsAndEmptyGroups(t *testing.T) {
	dir, arch, _, _ := triagePool(t)
	open := triageAsk(t, arch, "user", "guidance", "pick a queue for billing", nil, triageDaysAgo(5))
	inprog := triageAsk(t, arch, "user", "fyi", "note the outage window", nil, triageDaysAgo(2))
	if _, err := RespondRequest(arch, inprog.ID, RespondOptions{Status: StatusInProgress, Now: triageNow}); err != nil {
		t.Fatal(err)
	}
	needsInfo := triageAsk(t, arch, "user", "guidance", "needs info one", nil, triageDaysAgo(2))
	triageClose(t, arch, needsInfo.ID, StatusNeedsInfo)
	answered := triageAsk(t, arch, "user", "guidance", "already answered", nil, triageDaysAgo(9))
	triageClose(t, arch, answered.ID, StatusAnswered)

	ws, text := worksheetFor(t, dir, arch)
	want := filepath.Join(arch.Dir(), MemoryDir, TriageDir, "2026-10-18.worksheet.md")
	if ws.Path != want {
		t.Fatalf("path = %s, want %s", ws.Path, want)
	}
	if len(ws.Items) != 2 {
		t.Fatalf("items = %d, want pending + in-progress only", len(ws.Items))
	}
	if strings.Contains(text, needsInfo.ID) || strings.Contains(text, answered.ID) {
		t.Fatalf("needs-info / closed requests are not triaged here:\n%s", text)
	}
	s := triageSection(t, text, open.ID)
	for _, want := range []string{"age 5d", "priority P2", "type guidance", "status pending", "requester user",
		"Work branches: (none)", "Knowledge: (none)", "Siblings: (none)", "Redirects: (none)", "Requester mentions: (none",
		"pick a queue for billing"} {
		if !strings.Contains(s, want) {
			t.Errorf("section missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(triageSection(t, text, inprog.ID), "status in-progress") {
		t.Errorf("in-progress request missing:\n%s", text)
	}
}

func TestTriageWorksheet_WorkBranchSignal(t *testing.T) {
	dir, arch, dev, _ := triagePool(t)
	r := triageAsk(t, arch, "developer", "guidance", "split the billing service", nil, triageDaysAgo(4))
	viaRelated := triageAsk(t, arch, "developer", "guidance", "follow-up on the split", []string{"developer:2026-10-01-user-orig"}, triageDaysAgo(3))
	// Live: a branch named for the request id in another peer's repo.
	gitOut(t, dev.Dir(), "branch", WorkBranchPrefix+r.ID)
	// Merged: a branch named for a related id, landed and removed the way
	// `work merge` does it (ff-only merge, then delete).
	gitOut(t, dev.Dir(), "checkout", "-q", "-b", WorkBranchPrefix+"2026-10-01-user-orig")
	if err := os.WriteFile(filepath.Join(dev.Dir(), "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, dev.Dir(), "work")
	gitOut(t, dev.Dir(), "checkout", "-q", "main")
	gitOut(t, dev.Dir(), "merge", "--ff-only", WorkBranchPrefix+"2026-10-01-user-orig")
	gitOut(t, dev.Dir(), "branch", "-D", WorkBranchPrefix+"2026-10-01-user-orig")

	_, text := worksheetFor(t, dir, arch)
	if s := triageSection(t, text, r.ID); !strings.Contains(s, "developer@"+WorkBranchPrefix+r.ID+" (live)") {
		t.Errorf("live branch not listed:\n%s", s)
	}
	if s := triageSection(t, text, viaRelated.ID); !strings.Contains(s, "developer@"+WorkBranchPrefix+"2026-10-01-user-orig (merged)") {
		t.Errorf("merged branch for a related id not listed:\n%s", s)
	}
}

func TestTriageWorksheet_KnowledgeSignal(t *testing.T) {
	dir, arch, dev, analyst := triagePool(t)
	r := triageAsk(t, arch, "developer", "guidance", "decide the retry policy", nil, triageDaysAgo(4))
	other := triageAsk(t, arch, "analyst", "guidance", "pick the metrics store", nil, triageDaysAgo(4))
	// A Confirm: commit on the peer's own main naming the id.
	gitOut(t, arch.Dir(), "commit", "-q", "--allow-empty", "-m", ConfirmSubjectPrefix+"retry policy circulated", "-m", "Closes "+r.ID)
	sha := strings.TrimSpace(gitOut(t, arch.Dir(), "rev-parse", "--short", "HEAD"))
	// A canonical peer's knowledge entry body naming the other id.
	entry := filepath.Join(dev.Dir(), KnowledgeDir, "decisions", "metrics.md")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("# Metrics\n\n## Store\n\nPrometheus, per "+other.ID+".\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A candidate in a peer's inbox tier is a proposal, never read across peers.
	cand := filepath.Join(analyst.Dir(), KnowledgeDir, "candidates", "x.md")
	if err := os.MkdirAll(filepath.Dir(cand), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cand, []byte("mentions "+other.ID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, text := worksheetFor(t, dir, arch)
	if s := triageSection(t, text, r.ID); !strings.Contains(s, "architect@"+sha) || !strings.Contains(s, "retry policy circulated") {
		t.Errorf("Confirm: commit not listed with pointer:\n%s", s)
	}
	s := triageSection(t, text, other.ID)
	if !strings.Contains(s, "mesh://developer/decisions/metrics.md#store") {
		t.Errorf("canonical entry not listed with pointer:\n%s", s)
	}
	if strings.Contains(s, "mesh://analyst/candidates") {
		t.Errorf("a peer's candidate was read:\n%s", s)
	}
}

func TestTriageWorksheet_SiblingCandidates(t *testing.T) {
	dir, arch, dev, _ := triagePool(t)
	r := triageAsk(t, arch, "analyst", "guidance", "choose the billing queue backend", []string{"INFRA-12"}, triageDaysAgo(2))
	byRelated := triageAsk(t, dev, "analyst", "fyi", "unrelated words entirely", []string{"INFRA-12"}, triageDaysAgo(10))
	triageClose(t, dev, byRelated.ID, StatusAnswered)
	byType := triageAsk(t, arch, "analyst", "guidance", "which billing queue backend", nil, triageDaysAgo(12))
	triageClose(t, arch, byType.ID, StatusFiled)
	otherRequester := triageAsk(t, arch, "developer", "guidance", "choose the billing queue backend too", []string{"INFRA-12"}, triageDaysAgo(12))
	triageClose(t, arch, otherRequester.ID, StatusAnswered)
	stillOpen := triageAsk(t, arch, "analyst", "guidance", "billing queue backend again", []string{"INFRA-12"}, triageDaysAgo(1))

	_, text := worksheetFor(t, dir, arch)
	s := triageSection(t, text, r.ID)
	if !strings.Contains(s, "developer:"+byRelated.ID) || !strings.Contains(s, "related INFRA-12") {
		t.Errorf("sibling sharing a related id not listed:\n%s", s)
	}
	if !strings.Contains(s, "architect:"+byType.ID) || !strings.Contains(s, "same type") {
		t.Errorf("sibling of the same type not listed:\n%s", s)
	}
	if strings.Contains(s, otherRequester.ID) {
		t.Errorf("another requester's request listed as a sibling:\n%s", s)
	}
	if i := strings.Index(s, "Siblings"); i >= 0 && strings.Contains(s[i:], stillOpen.ID+" (") {
		t.Errorf("an open request listed as a closed sibling:\n%s", s)
	}
}

func TestTriageWorksheet_RedirectChainEndingClosed(t *testing.T) {
	dir, arch, dev, analyst := triagePool(t)
	orig := triageAsk(t, dev, "analyst", "guidance", "own the export format", nil, triageDaysAgo(8))
	_, hop, err := DeclineRequest(dir, dev, orig.ID, DeclineOptions{Reason: "not mine", RedirectTo: "architect", Now: triageNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DeclineRequest(dir, arch, hop.ID, DeclineOptions{Reason: "analyst owns it", RedirectTo: "analyst", Now: triageNow}); err != nil {
		t.Fatal(err)
	}
	reqs, _ := ListRequests(analyst.InboxPath())
	if len(reqs) != 1 {
		t.Fatalf("redirect target missing: %d", len(reqs))
	}
	triageClose(t, analyst, reqs[0].ID, StatusAnswered)
	// The requester asks architect again, naming the original.
	again := triageAsk(t, arch, "analyst", "guidance", "export format still open?", []string{"developer:" + orig.ID}, triageDaysAgo(1))

	_, text := worksheetFor(t, dir, arch)
	s := triageSection(t, text, again.ID)
	chain := "developer:" + orig.ID + " → architect:" + hop.ID + " → analyst:" + reqs[0].ID + " (answered)"
	if !strings.Contains(s, chain) {
		t.Errorf("closed redirect chain not listed; want %q in:\n%s", chain, s)
	}
}

func TestTriageWorksheet_RequesterMentions(t *testing.T) {
	dir, arch, dev, _ := triagePool(t)
	r := triageAsk(t, arch, "developer", "guidance", "circulate the billing retry policy", nil, triageDaysAgo(4))
	if err := HandoffSet(dev, "## Now\n\n- waiting on "+r.ID+" from architect\n- unrelated line\n", triageNow); err != nil {
		t.Fatal(err)
	}
	if err := StateSet(dev, "## State\n\n- billing retry policy now handled by the gateway\n", triageNow); err != nil {
		t.Fatal(err)
	}
	_, text := worksheetFor(t, dir, arch)
	s := triageSection(t, text, r.ID)
	for _, want := range []string{"handoff: - waiting on " + r.ID, "state: - billing retry policy now handled"} {
		if !strings.Contains(s, want) {
			t.Errorf("mention %q missing:\n%s", want, s)
		}
	}
	if strings.Contains(s, "unrelated line") {
		t.Errorf("unrelated handoff line listed:\n%s", s)
	}
}
