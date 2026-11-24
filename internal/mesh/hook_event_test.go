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

// hook_event_test.go — the neutral bridge event protocol. Each RunHookEvent
// call stands for one meshctl process: state carries between calls only
// through the pool's session store.

// sendEvent runs one event as a bridge would and returns the reply.
func sendEvent(t *testing.T, dir string, ev HookEvent, now time.Time) string {
	t.Helper()
	if ev.Harness == "" {
		ev.Harness, ev.Variant = HarnessOpencode, 2
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	RunHookEvent(&out, dir, "", b, now)
	return out.String()
}

func eventEnv(t *testing.T) {
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv(distillGuardEnv, "")
	t.Setenv(idleWindowEnv, "60")
}

func presence(t *testing.T, a *Agent, sid string) *Session {
	t.Helper()
	s, err := readAgentSession(a.Dir(), sid)
	if err != nil {
		return nil
	}
	return s
}

// The session-start hook runs once per session, on its first context event;
// its block is cached and repeated on every later one (the host rebuilds the
// system prompt per request).
func TestHookEvent_ContextStartsOnceAndRepeatsTheBlock(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	t0 := time.Now().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		out := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "S1"}, t0.Add(time.Duration(i)*time.Second))
		if !strings.Contains(out, "# Mesh identity") {
			t.Fatalf("context %d lacks the session-start block:\n%s", i, out)
		}
		if strings.Contains(out, "hookSpecificOutput") {
			t.Fatal("opencode reply must be raw text, not Claude Code's envelope")
		}
	}
	s := presence(t, a, "S1")
	if s == nil || s.Started != t0.Unix() {
		t.Fatalf("session-start ran more than once (presence %+v)", s)
	}
	st, ok := loadBridgeSession(a, "S1")
	if !ok || !st.Started || !strings.Contains(st.StartBlock, "# Mesh identity") {
		t.Fatalf("bridge state not kept: %+v", st)
	}
}

// A prompt is recorded for the next context event and marks the session busy.
func TestHookEvent_PromptRecordedForContext(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	now := time.Now()
	if out := sendEvent(t, dir, HookEvent{Event: HookEventPrompt, Cwd: a.Dir(), SessionID: "S1", Prompt: "refactor the gate"}, now); out != "" {
		t.Fatalf("prompt must not reply: %q", out)
	}
	st, ok := loadBridgeSession(a, "S1")
	if !ok || st.Prompt != "refactor the gate" || !st.InTurn || st.Started {
		t.Fatalf("prompt state: %+v", st)
	}
	sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "S1"}, now)
	if st, _ := loadBridgeSession(a, "S1"); st.Prompt != "refactor the gate" || !st.Started {
		t.Fatalf("context lost the prompt: %+v", st)
	}
}

// The idle window holds across processes: a finished turn starts it, a tick
// after it closes the session (presence gone, state gone), and a session
// mid-turn is never closed as idle.
func TestHookEvent_IdleWindowClosesAcrossInvocations(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	t0 := time.Now().Truncate(time.Second)
	ev := func(kind, sid string) HookEvent {
		return HookEvent{Event: kind, Cwd: a.Dir(), SessionID: sid, Instance: "i1"}
	}

	// S1: a finished turn. S2: a prompt still running.
	sendEvent(t, dir, ev(HookEventPrompt, "S1"), t0)
	sendEvent(t, dir, ev(HookEventContext, "S1"), t0)
	sendEvent(t, dir, ev(HookEventTurnEnd, "S1"), t0.Add(10*time.Second))
	sendEvent(t, dir, ev(HookEventPrompt, "S2"), t0)
	sendEvent(t, dir, ev(HookEventContext, "S2"), t0)

	// Inside the window: nothing closes.
	sendEvent(t, dir, ev(HookEventTick, "S1"), t0.Add(50*time.Second))
	if _, ok := loadBridgeSession(a, "S1"); !ok || presence(t, a, "S1") == nil {
		t.Fatal("closed inside the idle window")
	}
	// Past it: S1 closes; S2, mid-turn, stays.
	sendEvent(t, dir, ev(HookEventTick, "S1"), t0.Add(75*time.Second))
	if _, ok := loadBridgeSession(a, "S1"); ok {
		t.Fatal("idle session's bridge state survived")
	}
	if presence(t, a, "S1") != nil {
		t.Fatal("idle session still registered: session-end did not run")
	}
	if _, ok := loadBridgeSession(a, "S2"); !ok || presence(t, a, "S2") == nil {
		t.Fatal("a session mid-turn was closed as idle")
	}
	// Another session's finished turn sweeps too.
	sendEvent(t, dir, ev(HookEventTurnEnd, "S2"), t0.Add(80*time.Second))
	sendEvent(t, dir, ev(HookEventTurnEnd, "S3"), t0.Add(150*time.Second))
	if _, ok := loadBridgeSession(a, "S2"); ok {
		t.Fatal("a turn end of another session did not sweep the idle one")
	}
}

// A context event after the idle window closes the session first, then opens
// it afresh: the session-start block is built again.
func TestHookEvent_ContextAfterIdleRestarts(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	t0 := time.Now().Truncate(time.Second)
	sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "S1"}, t0)
	sendEvent(t, dir, HookEvent{Event: HookEventTurnEnd, Cwd: a.Dir(), SessionID: "S1"}, t0)
	t1 := t0.Add(5 * time.Minute)
	out := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "S1"}, t1)
	if !strings.Contains(out, "# Mesh identity") {
		t.Fatalf("restarted session lacks its block:\n%s", out)
	}
	if s := presence(t, a, "S1"); s == nil || s.Started != t1.Unix() {
		t.Fatalf("session not restarted after the idle window: %+v", s)
	}
}

// session.end closes one session; shutdown closes the sessions its bridge
// instance saw and leaves another instance's alone.
func TestHookEvent_SessionEndAndShutdown(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	now := time.Now()
	for _, c := range []struct{ sid, inst string }{{"A", "i1"}, {"B", "i1"}, {"C", "i2"}} {
		sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: c.sid, Instance: c.inst}, now)
	}
	sendEvent(t, dir, HookEvent{Event: HookEventSessionEnd, Cwd: a.Dir(), SessionID: "A", Instance: "i1"}, now)
	if presence(t, a, "A") != nil {
		t.Fatal("session.end did not end the session")
	}
	sendEvent(t, dir, HookEvent{Event: HookEventShutdown, Cwd: a.Dir(), Instance: "i1"}, now)
	if presence(t, a, "B") != nil {
		t.Fatal("shutdown left its instance's session open")
	}
	if _, ok := loadBridgeSession(a, "C"); !ok || presence(t, a, "C") == nil {
		t.Fatal("shutdown closed another instance's session")
	}
}

// A host that leaves the session id out of an event falls back to the last
// session this bridge instance saw a prompt for.
func TestHookEvent_LastSessionFallback(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	now := time.Now()
	sendEvent(t, dir, HookEvent{Harness: HarnessOpencode, Variant: 1, Event: HookEventPrompt, Cwd: a.Dir(), SessionID: "V1S", Instance: "i1", Prompt: "hi"}, now)
	sendEvent(t, dir, HookEvent{Harness: HarnessOpencode, Variant: 1, Event: HookEventContext, Cwd: a.Dir(), Instance: "i1"}, now)
	if st, ok := loadBridgeSession(a, "V1S"); !ok || !st.Started {
		t.Fatalf("context without a session id did not use the last one: %+v", st)
	}
	sendEvent(t, dir, HookEvent{Harness: HarnessOpencode, Variant: 1, Event: HookEventContext, Cwd: a.Dir(), Instance: "other"}, now)
	if n := len(bridgeSessions(a)); n != 1 {
		t.Fatalf("another instance borrowed the pointer: %d sessions", n)
	}
}

// Doctrine is injected only when the bridge asks; a mesh peer subagent gets
// none and starts no session; any other subagent gets the caller's doctrine.
func TestHookEvent_DoctrineAndSubagents(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	_ = os.WriteFile(filepath.Join(a.Dir(), "CLAUDE.md"), []byte("ROLE-DOCTRINE"), 0o644)
	_ = os.WriteFile(filepath.Join(a.Dir(), ClaudeLocalFile), []byte("STANDING-ORDERS"), 0o644)
	now := time.Now()

	out := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "S1", Doctrine: true}, now)
	if !strings.HasPrefix(out, "ROLE-DOCTRINE\n\nSTANDING-ORDERS") || !strings.Contains(out, "# Mesh identity") {
		t.Fatalf("doctrine then block expected:\n%s", out)
	}
	if out := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "S2"}, now); strings.Contains(out, "ROLE-DOCTRINE") {
		t.Fatal("doctrine injected without being asked")
	}
	peer := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "C1", ParentSessionID: "S1", Agent: "mesh-arch", Doctrine: true}, now)
	if peer != "" {
		t.Fatalf("peer child got context: %q", peer)
	}
	gen := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "C2", ParentSessionID: "S1", Agent: "general", Doctrine: true}, now)
	if !strings.Contains(gen, "ROLE-DOCTRINE") || strings.Contains(gen, "# Mesh identity") {
		t.Fatalf("general child: doctrine only expected:\n%s", gen)
	}
	for _, sid := range []string{"C1", "C2"} {
		if presence(t, a, sid) != nil {
			t.Fatalf("child %s registered as a session", sid)
		}
	}

	// AGENTS.md pointing at CLAUDE.md: the host loads the role already.
	if err := os.Symlink("CLAUDE.md", filepath.Join(a.Dir(), "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if d := workspaceDoctrine(a.Dir()); d != "STANDING-ORDERS" {
		t.Fatalf("role loaded twice: %q", d)
	}
}

// A headless run (dispatch, distil) gets its doctrine and the gate only: no
// session starts, no state is kept.
func TestHookEvent_HeadlessIsDoctrineAndGateOnly(t *testing.T) {
	eventEnv(t)
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	_ = os.WriteFile(filepath.Join(a.Dir(), "CLAUDE.md"), []byte("ROLE-DOCTRINE"), 0o644)
	now := time.Now()
	sendEvent(t, dir, HookEvent{Event: HookEventPrompt, Cwd: a.Dir(), SessionID: "H1", Prompt: "x"}, now)
	out := sendEvent(t, dir, HookEvent{Event: HookEventContext, Cwd: a.Dir(), SessionID: "H1", Doctrine: true}, now)
	if !strings.HasPrefix(out, "ROLE-DOCTRINE") || strings.Contains(out, "# Mesh identity") {
		t.Fatalf("headless context: doctrine only expected:\n%s", out)
	}
	sendEvent(t, dir, HookEvent{Event: HookEventTurnEnd, Cwd: a.Dir(), SessionID: "H1"}, now)
	if presence(t, a, "H1") != nil || len(bridgeSessions(a)) != 0 {
		t.Fatal("headless run kept session state")
	}
	deny := sendEvent(t, dir, HookEvent{Event: HookEventToolBefore, Cwd: a.Dir(), SessionID: "H1", Agent: "build",
		Tool: &HookEventTool{Name: "write", Input: json.RawMessage(`{"path":"intake/x.md"}`)}}, now)
	if strings.TrimSpace(deny) == "" {
		t.Fatal("headless gate let an intake write through")
	}
}

// The gate is fail-closed: a malformed event, a tool.before with no tool, an
// unknown harness and a subagent whose agent cannot be told are all denied.
func TestHookEvent_GateFailsClosed(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	now := time.Now()
	run := func(stdin, flag string) string {
		var out bytes.Buffer
		RunHookEvent(&out, dir, flag, []byte(stdin), now)
		return out.String()
	}
	gate := func(ev HookEvent) string {
		ev.Event = HookEventToolBefore
		ev.Cwd = a.Dir()
		return sendEvent(t, dir, ev, now)
	}
	write := func(path string) *HookEventTool {
		return &HookEventTool{Name: "write", Input: json.RawMessage(`{"path":"` + path + `"}`)}
	}

	for name, out := range map[string]string{
		"not JSON":        run("{nope", "opencode"),
		"empty":           run("", "opencode"),
		"no kind":         run(`{"cwd":"`+a.Dir()+`"}`, "opencode"),
		"unknown harness": run(`{"event":"tool.before","cwd":"`+a.Dir()+`","tool":{"name":"read"}}`, "nosuch"),
		"no tool":         gate(HookEvent{SessionID: "S1", Agent: "build"}),
		"unknown session": gate(HookEvent{SessionID: "S1", SessionUnknown: true, Tool: write("src/a.go")}),
		"child, no agent": gate(HookEvent{SessionID: "C1", ParentSessionID: "S1", Tool: write("src/a.go")}),
		"unmapped peer":   gate(HookEvent{SessionID: "C1", ParentSessionID: "S1", Agent: "mesh-nobody", Tool: write("src/a.go")}),
		"intake write":    gate(HookEvent{SessionID: "S1", Agent: "build", Tool: write("intake/x.md")}),
	} {
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s: allowed, want a deny", name)
		}
		// An unknown harness has no form of its own: the agent's is used.
		if name != "unknown harness" && strings.Contains(out, "hookSpecificOutput") {
			t.Errorf("%s: opencode deny must be the raw reason, got %q", name, out)
		}
	}
	if out := gate(HookEvent{SessionID: "S1", Agent: "build", Tool: write("src/a.go")}); out != "" {
		t.Fatalf("an ordinary write was denied: %q", out)
	}
	if out := gate(HookEvent{SessionID: "C2", ParentSessionID: "S1", Agent: "general", Tool: write("src/a.go")}); out != "" {
		t.Fatalf("a general subagent's ordinary write was denied: %q", out)
	}
	// The same deny in Claude Code's envelope when the bridge names it.
	if out := run(`{"event":"tool.before","cwd":"`+a.Dir()+`","session_id":"S1","tool":{"name":"Write","input":{"file_path":"intake/x.md"}}}`, HarnessClaude); !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Fatalf("claude envelope expected: %q", out)
	}
}

// Every event kind the protocol names is handled; advisory kinds reply
// nothing, an unknown kind is ignored.
func TestHookEvent_AdvisoryKindsReplyNothing(t *testing.T) {
	eventEnv(t)
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "oc", false, nil)
	now := time.Now()
	for _, k := range []string{HookEventPrompt, HookEventTurnEnd, HookEventHeartbeat, HookEventTick, HookEventSessionEnd, HookEventShutdown, "no.such.kind"} {
		if out := sendEvent(t, dir, HookEvent{Event: k, Cwd: a.Dir(), SessionID: "S1"}, now); out != "" {
			t.Errorf("%s replied %q", k, out)
		}
	}
	if out := sendEvent(t, dir, HookEvent{Event: HookEventToolAfter, Cwd: a.Dir(), SessionID: "S1",
		Tool: &HookEventTool{Name: "read", Input: json.RawMessage(`{}`)}}, now); out != "" {
		t.Errorf("tool.after replied %q", out)
	}
	if out := sendEvent(t, dir, HookEvent{Event: HookEventSessionStart, Cwd: a.Dir(), SessionID: "S9"}, now); !strings.Contains(out, "# Mesh identity") {
		t.Errorf("session.start did not reply with the block: %q", out)
	}
}
