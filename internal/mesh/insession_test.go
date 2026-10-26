package mesh

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// inSessionPool: caller `developer` (claude) with the `architect` peer rendered.
func inSessionPool(t *testing.T) (dir string, caller, arch *Agent) {
	t.Helper()
	dir = meshRoot(t)
	caller = newTestAgent(t, dir, "developer", false, []string{"code"})
	arch = newTestAgent(t, dir, "architect", true, nil)
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_DISPATCH_HOURLY_MAX", "12")
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	t.Setenv("CLAUDE_SESSION_ID", "")
	return dir, caller, arch
}

func TestAskInSession_WritesClaimsLogsAndPrintsDelegation(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	var out bytes.Buffer
	now := time.Now()
	req, rc, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "Should we split the service?", From: caller.Name, Now: now, Stdout: &out})
	if err != nil || rc != 0 {
		t.Fatalf("rc=%d err=%v", rc, err)
	}
	r, err := ReadRequest(req.Path())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusInProgress || r.From != "developer" || r.To != "architect" {
		t.Fatalf("request: %+v", r)
	}
	c, err := readClaim(claimPath(req.Path()))
	if err != nil || c.OwnerSession != "sess-1" {
		t.Fatalf("claim must be held by the harness session: %+v %v", c, err)
	}
	if n, _ := hourlyCount(dir, now); n != 1 {
		t.Fatalf("in-session dispatch must count against the cap, got %d", n)
	}
	logb, _ := os.ReadFile(DispatchLogPath(dir))
	if !strings.Contains(string(logb), "DISPATCH mode=in-session id="+req.ID) {
		t.Fatalf("log: %s", logb)
	}
	s := out.String()
	for _, want := range []string{"subagent_type: mesh-architect", "meshctl agent brief --agent architect --request " + req.ID, "meshctl sent"} {
		if !strings.Contains(s, want) {
			t.Fatalf("delegation output missing %q:\n%s", want, s)
		}
	}
	// `inbox next` must skip it (in-progress), and the same session can respond.
	if next, _ := InboxNext(arch, "other", 1, false, now); next != nil {
		t.Fatalf("inbox next must skip an in-progress request, got %s", next.ID)
	}
	if _, err := RespondRequest(arch, req.ID, RespondOptions{Status: "answered", Response: "split it", Session: "sess-1", Now: now}); err != nil {
		t.Fatalf("respond from the same harness session: %v", err)
	}
	if fileExists(claimPath(req.Path())) {
		t.Fatal("claim must be released on answer")
	}
	sent, _ := ListSent(dir, "developer", true)
	if len(sent) != 1 || sent[0].ID != req.ID {
		t.Fatalf("sent view: %+v", sent)
	}
}

func TestAskInSession_NoHarnessSessionMeansNoClaim(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	req, rc, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Stdout: discard{}})
	if err != nil || rc != 0 {
		t.Fatalf("rc=%d err=%v", rc, err)
	}
	if fileExists(claimPath(req.Path())) {
		t.Fatal("a claim nobody could release must not be written")
	}
	// Any session may respond (status in-progress only hides it from inbox next).
	if _, err := RespondRequest(arch, req.ID, RespondOptions{Status: "answered", Response: "ok", Session: "s-minted", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestAskInSession_Guards(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	pm := newTestAgent(t, dir, "pm", false, []string{"tracker"})

	if _, _, err := AskInSession(dir, pm, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Stdout: discard{}}); err == nil || !strings.Contains(err.Error(), "mutates") {
		t.Fatalf("state-mutating peer must be refused: %v", err)
	}
	// Missing definition in the caller (e.g. onboard never re-run).
	_ = os.Remove(PeerAgentFile(caller.Dir(), "architect"))
	if _, _, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Stdout: discard{}}); err == nil || !strings.Contains(err.Error(), "meshctl agent onboard developer") {
		t.Fatalf("missing definition must point at onboard: %v", err)
	}
	if _, err := Onboard(dir, caller.Dir(), AgentSpec{}, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	// Depth: a headless-dispatched peer may not go in-session either.
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if _, rc, _ := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: caller.Name, Stdout: discard{}}); rc != ExitDepth {
		t.Fatalf("want depth block %d, got %d", ExitDepth, rc)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	if reqs, _ := ListRequests(arch.InboxPath()); len(reqs) != 0 {
		t.Fatal("a blocked dispatch must not write intake")
	}
	// Hourly cap.
	t.Setenv("AGENT_DISPATCH_HOURLY_MAX", "1")
	if _, rc, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "one", From: caller.Name, Stdout: discard{}}); rc != 0 || err != nil {
		t.Fatalf("first: rc=%d %v", rc, err)
	}
	if _, rc, _ := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "two", From: caller.Name, Stdout: discard{}}); rc != ExitHourlyCap {
		t.Fatalf("want cap block %d, got %d", ExitHourlyCap, rc)
	}
	// An opencode caller has no Agent tool.
	newTestAgent(t, dir, "oc", false, []string{"code"})
	mp := ManifestPath(dir, "oc")
	b, _ := os.ReadFile(mp)
	_ = os.WriteFile(mp, []byte(strings.Replace(string(b), "harness: claude", "harness: opencode", 1)), 0o644)
	t.Setenv("AGENT_DISPATCH_HOURLY_MAX", "12")
	if _, _, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "x", From: "oc", Stdout: discard{}}); err == nil || !strings.Contains(err.Error(), "opencode") {
		t.Fatalf("opencode caller must be refused with the headless alternative: %v", err)
	}
}

func TestBrief_CarriesRequestRecallHandoffAndContract(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	_ = os.MkdirAll(filepath.Join(arch.Dir(), KnowledgeDir, "playbooks"), 0o755)
	_ = os.WriteFile(filepath.Join(arch.Dir(), KnowledgeDir, "playbooks", "service-split.md"), []byte("# Service split playbook\n\nHow to split a service.\n"), 0o644)
	if err := HandoffSet(arch, "## Resume\n\nwas reviewing the split", time.Now()); err != nil {
		t.Fatal(err)
	}
	req, _, err := AskInSession(dir, arch, InSessionOptions{Type: "guidance", Prompt: "Should we split the billing service?", From: caller.Name, Stdout: discard{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Brief(dir, arch, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"You are `architect`", "**Role:**", "`guidance`",
		"## Request `" + req.ID + "`", "Should we split the billing service?",
		"## Recall for this request", "service-split",
		"## Handoff", "was reviewing the split",
		"meshctl inbox respond " + req.ID + " --agent architect", "needs-human",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("brief missing %q:\n%s", want, out)
		}
	}
	if _, err := Brief(dir, arch, "no-such-request"); err == nil {
		t.Fatal("unknown request must error")
	}
	// Without a request: identity + contract, no request block.
	out, _ = Brief(dir, arch, "")
	if strings.Contains(out, "## Request") || !strings.Contains(out, "Write-back contract") {
		t.Fatalf("bare brief: %s", out)
	}
}

func TestGate_InSessionPeerContract(t *testing.T) {
	dir, caller, _ := inSessionPool(t)
	bash := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: caller.Dir(), ToolName: "Bash", ToolInput: in, PeerAgent: "architect", InSession: true, SessionID: "sess-1"}
	}
	deny := []string{
		"meshctl ask pm guidance 'x'",
		"meshctl msg send pm fyi 'x'",
		"cd ../architect && meshctl ask grid guidance x",
		"meshctl kb begin --agent architect",
		"meshctl handoff set --agent architect <<'EOF'\nx\nEOF",
		"git -C ../architect commit -am x",
		"git push",
		"echo hi > ../architect/knowledge/notes.md",
		"cat a >> b.md",
		"sed -i '' s/a/b/ file.go",
		"rm -rf ../architect/knowledge",
	}
	allow := []string{
		"meshctl agent brief --agent architect --request 2026-01-01-developer-x",
		"meshctl inbox respond 2026-01-01-developer-x --agent architect --status answered --response-file - <<'EOF'\n# Answer\nsplit it\nEOF",
		"meshctl inbox decline 2026-01-01-developer-x --agent architect --reason x --redirect pm",
		"meshctl memory recall --agent architect 'billing' 2>/dev/null",
		"meshctl agent list --json",
		"git -C ../architect log --oneline -5",
		"grep -rn split ../architect/knowledge/ | head",
		"ls ../architect/knowledge >/dev/null 2>&1; echo $?",
	}
	for _, c := range deny {
		if d := GatePreToolUse(dir, bash(c)); !d.Deny {
			t.Errorf("peer should be denied: %s", c)
		}
	}
	for _, c := range allow {
		if d := GatePreToolUse(dir, bash(c)); d.Deny {
			t.Errorf("peer should be allowed: %s (%s)", c, d.Reason)
		}
	}
	// Edit tools by name, regardless of path.
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "NotebookEdit"} {
		p := HookPayload{Cwd: caller.Dir(), ToolName: tool, PeerAgent: "architect", InSession: true}
		if !GatePreToolUse(dir, p).Deny {
			t.Errorf("peer should be denied %s", tool)
		}
	}
	// Reads and MCP lookups pass.
	for _, tool := range []string{"Read", "Grep", "Glob", "mcp__tracker__search"} {
		p := HookPayload{Cwd: caller.Dir(), ToolName: tool, PeerAgent: "architect", InSession: true}
		if GatePreToolUse(dir, p).Deny {
			t.Errorf("peer should be allowed %s", tool)
		}
	}
	// The same commands from an ordinary session are not peer-gated.
	if d := GatePreToolUse(dir, HookPayload{Cwd: caller.Dir(), ToolName: "Bash", ToolInput: bash("git push").ToolInput}); d.Deny {
		t.Fatalf("non-peer session must not get the peer gate: %s", d.Reason)
	}
}

func TestGate_InSessionPeerKBRulesAreThePeers(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	// A standing rule in the PEER's KB (not the caller's) must apply to the
	// peer subagent, which runs with the caller's cwd.
	rule := "---\nrule: \"never query prod directly\"\ntriggers: ['warehouse.*prod']\n---\n# rule\n"
	_ = os.MkdirAll(filepath.Join(arch.Dir(), KnowledgeDir, "experience"), 0o755)
	_ = os.WriteFile(filepath.Join(arch.Dir(), KnowledgeDir, "experience", "prod.md"), []byte(rule), 0o644)
	in, _ := json.Marshal(map[string]string{"command": "warehouse --account prod -q 'select 1'"})
	p := HookPayload{Cwd: caller.Dir(), ToolName: "Bash", ToolInput: in, SessionID: "sess-1", PeerAgent: "architect", InSession: true}
	d := GatePreToolUse(dir, p)
	if !d.Deny || !strings.Contains(d.Reason, "never query prod directly") {
		t.Fatalf("peer's KB rule must gate the peer subagent: %+v", d)
	}
	// The caller's own session (no override) is not subject to the peer's rule.
	p.PeerAgent, p.InSession = "", false
	if d := GatePreToolUse(dir, p); d.Deny {
		t.Fatalf("caller must not inherit the peer's rule: %s", d.Reason)
	}
}

func TestHookSessionEnd_InSessionCapturesPeerRecordAndClosesLog(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	t.Setenv("PATH", t.TempDir()) // no claude: the peer distill must not really spawn
	p := HookPayload{Cwd: caller.Dir(), SessionID: "sess-1", AgentID: "agent-abc", PeerAgent: "architect", InSession: true}
	HookSessionEnd(dir, p, 1, time.Now())
	recs := ListSessionRecords(arch)
	if len(recs) != 1 || !strings.Contains(recs[0].ID, "peer-agent-abc") {
		t.Fatalf("peer record: %+v", recs)
	}
	if ListSessionRecords(caller) != nil {
		t.Fatal("the caller must not get the peer's record")
	}
	logb, _ := os.ReadFile(DispatchLogPath(dir))
	if !strings.Contains(string(logb), "caller=developer target=architect depth=0 DONE mode=in-session agent_id=agent-abc") {
		t.Fatalf("log: %s", logb)
	}
}

// fakeClaude puts a stub `claude` first on PATH that records its argv.
func fakeClaude(t *testing.T) (argvFile string) {
	t.Helper()
	bin := t.TempDir()
	argvFile = filepath.Join(bin, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\necho distilled\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return argvFile
}

func waitFor(t *testing.T, path string) []byte {
	t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
	return nil
}

func TestHookSessionEnd_InSessionStopDistillsIntoPeerKB(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	argvFile := fakeClaude(t)
	t.Setenv(distillGuardEnv, "")
	p := HookPayload{Cwd: caller.Dir(), SessionID: "sess-1", AgentID: "agent-abc", PeerAgent: "architect", InSession: true}
	HookSessionEnd(dir, p, 1, time.Now())

	id := "sess-1-peer-agent-abc"
	if _, err := os.Stat(distillMarkerPath(arch.Dir(), id)); err != nil {
		t.Fatalf("peer distill marker: %v", err)
	}
	if _, err := os.Stat(distillMarkerPath(caller.Dir(), id)); err == nil {
		t.Fatal("the peer's window must not distil into the caller")
	}
	argv := string(waitFor(t, argvFile))
	for _, want := range []string{"-p", "agent: architect", "Edit(./knowledge/candidates/**)", "Bash(meshctl kb consolidate:*)"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "Bash(meshctl ask") || strings.Contains(argv, "\nBash\n") {
		t.Errorf("peer distill must not get ask or unscoped Bash:\n%s", argv)
	}
	if b := waitFor(t, DistillLogPath(arch.Dir(), id)); !strings.Contains(string(b), "distilled") {
		t.Fatalf("log: %s", b)
	}

	// Once per window: a repeated Stop for the same subagent does not re-run.
	if SpawnPeerDistill(dir, arch, id) {
		t.Fatal("second distill of the same window must be refused")
	}
}

func TestHooks_DistillRunRegistersNoPresenceOrRecord(t *testing.T) {
	dir, _, arch := inSessionPool(t)
	t.Setenv(distillGuardEnv, "1")
	p := HookPayload{Cwd: arch.Dir(), SessionID: "distill-sess"}
	if out := HookSessionStart(dir, p, 1, time.Now()); out != "" {
		t.Fatalf("distill run got a session-start block: %q", out)
	}
	HookUserPrompt(dir, p, time.Now())
	if s := SessionList(arch, time.Now()); len(s) != 0 {
		t.Fatalf("distill run registered presence: %+v", s)
	}
	HookSessionEnd(dir, p, 1, time.Now())
	if recs := ListSessionRecords(arch); recs != nil {
		t.Fatalf("distill run captured a record: %+v", recs)
	}
}
