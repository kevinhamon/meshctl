package mesh

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// gate_flag_value_test.go — a flag's separate value before the anchored verb
// (`meshctl kb --session S finish`) is how cobra reads it, so every gate rule
// that matches a meshctl subcommand must see through it. Each rule is pinned
// with the flag-value-before-verb form, the `--flag=value` form and a boolean
// flag (which takes no value) before the verb.

func TestShellInvokes_FlagValueBeforeVerb(t *testing.T) {
	cases := []struct {
		cmd, sub string
		want     bool
	}{
		{"meshctl kb --session S finish", "meshctl kb finish", true},
		{"meshctl kb --agent x confirm a.md", "meshctl kb confirm", true},
		{"meshctl state --agent x set", "meshctl state set", true},
		{"meshctl inbox --agent x triage apply", "meshctl inbox triage apply", true},
		{"meshctl inbox --agent x triage --peer y", "meshctl inbox triage", true},
		{"meshctl inbox --agent x triage --report r apply 1", "meshctl inbox triage apply", true},
		{"meshctl --agent x kb --session S finish", "meshctl kb finish", true},
		{"meshctl -a x kb finish", "meshctl kb finish", true},
		{"meshctl kb --headless --session S finish", "meshctl kb finish", true},
		{"meshctl kb --abort finish", "meshctl kb finish", true},
		{"meshctl kb --session=S finish", "meshctl kb finish", true},
		{`bash -c "meshctl kb --session S finish"`, "meshctl kb finish", true},
		// Still never a mention, and the verb must still be there.
		{"meshctl kb --session finish", "meshctl kb finish", true}, // over-match is safe: the gate asks
		{"meshctl kb --session S begin", "meshctl kb finish", false},
		{"meshctl state --agent x show", "meshctl state set", false},
		{`echo meshctl kb --session S finish`, "meshctl kb finish", false},
	}
	for _, c := range cases {
		if got := ShellInvokes(c.cmd, c.sub); got != c.want {
			t.Errorf("ShellInvokes(%q, %q) = %v, want %v", c.cmd, c.sub, got, c.want)
		}
	}
}

// TestShellInvokes_ManyFlagsStaysLinear: the either-way flag reading must not
// blow up on a long run of flags.
func TestShellInvokes_ManyFlagsStaysLinear(t *testing.T) {
	cmd := "meshctl kb " + strings.Repeat("--x y ", 200) + "finish"
	if !ShellInvokes(cmd, "meshctl kb finish") {
		t.Fatal("long flag run not matched")
	}
	if ShellInvokes(strings.Repeat("--x ", 200), "meshctl kb finish") {
		t.Fatal("matched without meshctl")
	}
}

func gateVerdict(d GateDecision) string {
	switch {
	case d.Deny:
		return "deny"
	case d.Ask:
		return "ask"
	}
	return "allow"
}

func TestGate_KBFinishFlagValueBeforeVerb(t *testing.T) {
	cases := []struct {
		name string
		cmd  func(sum string) string
		want string
	}{
		{"session before verb", func(s string) string { return "meshctl kb --session sess-1 finish --summary-file " + s }, "ask"},
		{"agent before kb", func(s string) string { return "meshctl --agent architect kb finish --summary-file " + s }, "ask"},
		{"every flag before verb", func(s string) string {
			return "meshctl kb --agent architect --session sess-1 --summary-file " + s + " finish"
		}, "ask"},
		{"= forms", func(s string) string { return "meshctl kb --session=sess-1 finish --summary-file=" + s }, "ask"},
		{"no summary, session before verb", func(string) string { return "meshctl kb --session sess-1 finish" }, "deny"},
		{"no summary, agent before verb", func(string) string { return "meshctl kb --agent architect finish" }, "deny"},
		{"abort before verb is a bool", func(string) string { return "meshctl kb --abort --session sess-1 finish" }, "allow"},
		{"session value that looks like --abort", func(string) string { return "meshctl kb --session --abort finish" }, "deny"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, a, wt := promotionFixture(t)
			commitKB(t, wt, "playbooks/release.md")
			sum := writeSummary(t, a, "sess-1", promotionSummary)
			d := GatePreToolUse(dir, bash(a, "sess-1", c.cmd(sum)))
			if got := gateVerdict(d); got != c.want {
				t.Fatalf("want %s, got %+v", c.want, d)
			}
			if c.want != "allow" && d.Check != GateCheckPromotion {
				t.Fatalf("check %q, want %q", d.Check, GateCheckPromotion)
			}
		})
	}
}

func TestGate_KBConfirmFlagValueBeforeVerb(t *testing.T) {
	dir, a := confirmFixture(t)
	rep := writeConfirmReport(t, a, confirmReport)
	for _, cmd := range []string{
		"meshctl kb --agent architect confirm reference/a.md --report " + rep,
		"meshctl kb --session sess-1 confirm reference/a.md --report=" + rep,
		"meshctl --agent architect kb confirm --report " + rep + " reference/a.md",
		"meshctl kb --agent=architect confirm reference/a.md --report " + rep,
	} {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if !d.Ask || d.Check != GateCheckPromotion {
			t.Fatalf("%q: want ask/promotion, got %+v", cmd, d)
		}
		if !strings.Contains(d.Reason, "- reference/a.md · consistent") {
			t.Errorf("%q: ask lacks the named path:\n%s", cmd, d.Reason)
		}
		if strings.Contains(d.Reason, "architect · ") || strings.Contains(d.Reason, "sess-1 · ") {
			t.Errorf("%q: a flag value was read as a path:\n%s", cmd, d.Reason)
		}
	}
	if d := GatePreToolUse(dir, bash(a, "sess-1", "meshctl kb --agent architect confirm reference/a.md")); !d.Deny || d.Check != GateCheckPromotion {
		t.Fatalf("no report: want deny, got %+v", d)
	}
}

func TestGate_StateSetFlagValueBeforeVerb(t *testing.T) {
	dir, a := poolGateFixture(t)
	sum := writeSummary(t, a, "sess-1", stateSummary)
	for cmd, want := range map[string]string{
		"meshctl state --agent architect set --summary-file " + sum: "ask",
		"meshctl --agent architect state set --summary-file=" + sum: "ask",
		"meshctl state --agent=architect set --summary-file " + sum: "ask",
		"meshctl state --agent architect set":                       "deny",
		"meshctl state --agent architect set --force":               "deny",
	} {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if got := gateVerdict(d); got != want || d.Check != GateCheckPromotion {
			t.Errorf("%q: want %s/promotion, got %+v", cmd, want, d)
		}
	}
}

func TestGate_TriageFlagValueBeforeVerb(t *testing.T) {
	dir, arch, _, _ := triagePool(t)
	req := triageAsk(t, arch, "developer", "guidance", "first ask", nil, triageDaysAgo(10))
	report := triageReport(t, arch, TriageReportLine{ID: req.ID, Verdict: TriageWithdrawn, Ref: "architect:" + req.ID, Quote: "not needed", Source: TriageSourceHuman})
	payload := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{ToolName: "Bash", ToolInput: in, Cwd: arch.Dir(), SessionID: "s1"}
	}
	for _, cmd := range []string{
		"meshctl inbox --agent architect triage apply --report " + report + " " + req.ID,
		"meshctl --agent architect inbox triage apply --report " + report + " " + req.ID,
		"meshctl inbox --agent architect triage --report " + report + " apply " + req.ID,
		"meshctl inbox --agent=architect triage apply --report=" + report + " " + req.ID,
	} {
		d := gateEvaluate(dir, payload(cmd))
		if !d.Ask || d.Check != GateCheckTriage {
			t.Fatalf("%q: apply not asked: %+v", cmd, d)
		}
		if !strings.Contains(d.Reason, "- "+req.ID+" · withdrawn") {
			t.Errorf("%q: ask lacks the id's line:\n%s", cmd, d.Reason)
		}
	}
	if d := gateEvaluate(dir, payload("meshctl inbox --agent architect triage apply "+req.ID)); !d.Deny {
		t.Errorf("no report not denied: %+v", d)
	}

	// A consulted peer is denied any triage, flag value or not.
	for _, cmd := range []string{
		"meshctl inbox --agent architect triage --peer architect",
		"meshctl --agent architect inbox triage",
		"meshctl inbox --agent=architect triage",
	} {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		consult := HookPayload{ToolName: "Bash", ToolInput: in, InSession: true, Cwd: filepath.Join(dir, "architect")}
		if d := gateEvaluate(dir, consult); !d.Deny || d.Check != GateCheckTriage {
			t.Errorf("%q: consult triage not denied: %+v", cmd, d)
		}
	}
}

func TestGate_KBBranchFlagValueForms(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	payload := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: a.Dir(), ToolName: "Bash", ToolInput: in, SessionID: "sess-1"}
	}
	for cmd, deny := range map[string]bool{
		"meshctl kb --agent architect finish --session mesh/kb/abc": false,
		"meshctl --agent architect kb finish --abort":               false,
		"meshctl kb --session=abc finish":                           false,
		"git -C kb merge mesh/kb/abc":                               true,
		"git --git-dir kb merge mesh/kb/abc":                        true,
		"meshctl state --agent kb set; git merge mesh/kb/abc":       true,
	} {
		d := GatePreToolUse(dir, payload(cmd))
		if got := d.Deny && d.Check == GateCheckKBBranch; got != deny {
			t.Errorf("%q: KB-branch deny=%v, want %v (%+v)", cmd, got, deny, d)
		}
	}
}

// TestGate_PeerDenyListsSeeFlagsBeforeVerb: the in-session peer deny lists
// (consult and work) match meshctl verbs through the same matcher, so a flag
// before the verb does not slip a dispatch or a KB write past them.
func TestGate_PeerDenyListsSeeFlagsBeforeVerb(t *testing.T) {
	dir, caller, _ := inSessionPool(t)
	consult := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: caller.Dir(), ToolName: "Bash", ToolInput: in, PeerAgent: "architect", InSession: true, SessionID: "sess-1"}
	}
	for _, cmd := range []string{
		"meshctl --agent architect ask pm guidance x",
		"meshctl msg --agent architect send pm fyi x",
		"meshctl --agent=architect kb begin",
		"meshctl handoff --agent architect set",
		"meshctl --harness claude pool upgrade",
		"meshctl sent --agent architect ack x",
	} {
		if d := GatePreToolUse(dir, consult(cmd)); !d.Deny {
			t.Errorf("consulted peer should be denied: %s", cmd)
		}
	}
	for _, cmd := range []string{
		"meshctl inbox respond x --agent architect --status answered",
		"meshctl memory recall --agent architect billing",
		"meshctl agent --agent kb list",
	} {
		if d := GatePreToolUse(dir, consult(cmd)); d.Deny {
			t.Errorf("consulted peer should be allowed: %s (%s)", cmd, d.Reason)
		}
	}

	wdir, wcaller, _, _ := workPool(t)
	work := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: wcaller.Dir(), ToolName: "Bash", ToolInput: in, PeerAgent: "architect", InSession: true, Work: true, SessionID: "sess-1"}
	}
	for _, cmd := range []string{
		"meshctl --agent architect ask pm guidance x",
		"meshctl work --agent architect merge req1",
		"meshctl session --agent architect list",
	} {
		if d := GatePreToolUse(wdir, work(cmd)); !d.Deny {
			t.Errorf("working peer should be denied: %s", cmd)
		}
	}
}
