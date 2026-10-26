package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gate_log_test.go — step-aside for subagent calls (ADR-0068 amendment) and
// decision-log attribution: which check decided, logged per call.

// lastGateLogLine returns the last line of an agent's gate decision log.
func lastGateLogLine(t *testing.T, a *Agent) string {
	t.Helper()
	b, err := os.ReadFile(GateLogPath(a.Dir()))
	if err != nil {
		t.Fatalf("no gate log for %s: %v", a.Name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

func editAt(a *Agent, session, tool, path string) HookPayload {
	return HookPayload{Cwd: a.Dir(), SessionID: session, ToolName: tool,
		ToolInput: json.RawMessage(`{"file_path":` + strconv.Quote(path) + `}`)}
}

// A hub's own settings hooks fire for every tool call its subagents make. A
// payload carrying a subagent id with no --agent flag is the hub hook seeing a
// subagent's call: it keeps the machine-global rules and steps aside for the
// KB-rule gate (and the outbound gate, when it exists); the subagent's own
// frontmatter hook, invoked with the peer flag, owns those rules.
func TestGate_StepAsideForSubagentCalls(t *testing.T) {
	dir := meshRoot(t)
	hub := newTestAgent(t, dir, "architect", true, nil)
	writeKBNote(t, hub, "experience/tokens.md", tokenRuleNote)
	sub := func(p HookPayload) HookPayload { p.AgentID = "a7437b6161a9857dc"; return p }
	intake := filepath.Join(hub.Dir(), IntakeDir, "req.md")

	cases := []struct {
		name  string
		p     HookPayload
		deny  bool
		check string
		mode  string
	}{
		{"subagent id, no peer flag: KB rule is not the hub's to apply",
			sub(bash(hub, "s1", "git remote -v")), false, GateCheckNone, GateModeStepAside},
		{"subagent id, no peer flag: competing memory store still denied",
			sub(editAt(hub, "s1", "Write", "/Users/k/.claude/projects/p/memory/note.md")), true, GateCheckPath, GateModeStepAside},
		{"subagent id, no peer flag: hand-written intake still denied",
			sub(editAt(hub, "s1", "Write", intake)), true, GateCheckPath, GateModeStepAside},
		{"subagent id, no peer flag: shell write to intake still denied",
			sub(bash(hub, "s1", "echo hi > "+intake)), true, GateCheckShellWrite, GateModeStepAside},
		{"no subagent id: KB rule applies exactly as today",
			bash(hub, "s2", "git remote -v"), true, GateCheckKBRule, GateModeSession},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := GatePreToolUse(dir, c.p)
			if d.Deny != c.deny || d.Check != c.check {
				t.Fatalf("Deny=%v Check=%q, want Deny=%v Check=%q (%+v)", d.Deny, d.Check, c.deny, c.check, d)
			}
			line := lastGateLogLine(t, hub)
			for _, want := range []string{"check=" + c.check, "mode=" + c.mode, "tool=" + c.p.ToolName} {
				if !strings.Contains(line, want) {
					t.Fatalf("log line %q lacks %q", line, want)
				}
			}
		})
	}
}

// The same subagent call invoked with the peer flag (the subagent's own
// frontmatter hook: --agent <peer> --in-session) is evaluated in full for that
// peer: the peer's KB rule denies once, logged in the PEER's log, and the retry
// passes.
func TestGate_PeerFlagEvaluatesInFull(t *testing.T) {
	dir, caller, arch := inSessionPool(t)
	writeKBNote(t, arch, "experience/tokens.md", tokenRuleNote)
	p := bash(caller, "hub-session", "git remote -v")
	p.AgentID = "a7437b6161a9857dc"
	p.PeerAgent = arch.Name
	p.InSession = true

	d := GatePreToolUse(dir, p)
	if !d.Deny || d.Check != GateCheckKBRule || !strings.Contains(d.Reason, "never flag them") {
		t.Fatalf("peer-flagged call must get the peer's KB rule, got %+v", d)
	}
	line := lastGateLogLine(t, arch)
	for _, want := range []string{"decision=deny", "check=" + GateCheckKBRule, "mode=" + GateModeConsult, "agent_id=a7437b6161a9857dc"} {
		if !strings.Contains(line, want) {
			t.Fatalf("peer log line %q lacks %q", line, want)
		}
	}
	if d := GatePreToolUse(dir, p); d.Deny {
		t.Fatalf("retry must pass, got %+v", d)
	}
	// The consult contract is still the deciding check when it bites.
	p2 := p
	p2.ToolInput = json.RawMessage(`{"command":"git commit -m x"}`)
	if d := GatePreToolUse(dir, p2); !d.Deny || d.Check != GateCheckPeer {
		t.Fatalf("consult contract must decide, got %+v", d)
	}
}

// Every decision names the check that produced it; an allow no check touched
// says so. The KB-rule check keeps running before anything that follows it:
// a deny-once is the deciding check on the first call, and the retry is decided
// by whatever comes next (today: nothing, an untouched allow).
func TestGate_DecisionLogNamesTheDecidingCheck(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, []string{"tracker"})
	writeKBNote(t, a, "experience/tokens.md", tokenRuleNote)

	if d := GatePreToolUse(dir, bash(a, "s1", "git remote -v")); !d.Deny || d.Check != GateCheckKBRule {
		t.Fatalf("first call: want kb-rule deny, got %+v", d)
	}
	first := lastGateLogLine(t, a)
	if d := GatePreToolUse(dir, bash(a, "s1", "git remote -v")); d.Deny || d.Check != GateCheckNone {
		t.Fatalf("retry: want untouched allow, got %+v", d)
	}
	second := lastGateLogLine(t, a)
	if !strings.Contains(first, "decision=deny check="+GateCheckKBRule) {
		t.Fatalf("first line must attribute the deny to the KB rule: %q", first)
	}
	if !strings.Contains(second, "decision=allow check="+GateCheckNone) {
		t.Fatalf("retry line must show the next check deciding (none touched it): %q", second)
	}
	// Both lines are in the log, in order.
	b, _ := os.ReadFile(GateLogPath(a.Dir()))
	if i, j := strings.Index(string(b), first), strings.Index(string(b), second); i < 0 || j < 0 || i > j {
		t.Fatalf("log must hold the deny before the allow:\n%s", b)
	}

	cases := []struct {
		name     string
		p        HookPayload
		decision string
		check    string
	}{
		{"path rule", editAt(a, "s1", "Write", "/Users/k/.claude/projects/p/memory/note.md"), "deny", GateCheckPath},
		{"shell-write rule", bash(a, "s1", "sed -i s/a/b/ "+filepath.Join(a.Dir(), IntakeDir, "x.md")), "deny", GateCheckShellWrite},
		{"delegation to a mutating peer", HookPayload{Cwd: a.Dir(), SessionID: "s1", ToolName: "Agent",
			ToolInput: json.RawMessage(`{"subagent_type":"` + PeerAgentPrefix + a.Name + PeerWorkSuffix + `"}`)}, "ask", GateCheckDelegation},
		{"untouched allow", bash(a, "s1", "go test ./..."), "allow", GateCheckNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := GatePreToolUse(dir, c.p)
			if d.Check != c.check {
				t.Fatalf("Check=%q, want %q (%+v)", d.Check, c.check, d)
			}
			line := lastGateLogLine(t, a)
			if !strings.Contains(line, "decision="+c.decision+" check="+c.check) {
				t.Fatalf("log line %q, want decision=%s check=%s", line, c.decision, c.check)
			}
		})
	}
}

// Outside any pool there is no agent to log for: the gate still decides and
// writes nothing.
func TestGate_NoAgentNoLog(t *testing.T) {
	p := edit("Write", "/Users/k/.claude/projects/p/memory/note.md")
	p.AgentID = "sub"
	d := GatePreToolUse("", p)
	if !d.Deny || d.Check != GateCheckPath {
		t.Fatalf("got %+v", d)
	}
}
