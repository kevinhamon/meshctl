package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ocOutboundConfig = `outbound:
  tracker:
    tools: ["createIssue"]
    commands: ['^ssh ', 'git push', '^curl .*(-X ?POST|--data)']
    ask:
      opencode:
        tools: ['tracker_create*']
`

func ocShell(cwd, c string) HookPayload {
	p := outboundCall(cwd, "shell", map[string]string{"command": c})
	p.Harness = HarnessOpencode
	return p
}

func wireOpencode(t *testing.T, a *Agent) string {
	t.Helper()
	if _, err := ensureOpencodeConfig(a.Dir(), 2); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(a.Dir(), "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLiteralCommandGlob(t *testing.T) {
	for pattern, want := range map[string]string{
		"^ssh ":                "ssh *",
		"git push":             "git push*",
		`^curl .*(-X ?POST)`:   "",
		`catl\s+story`:         "",
		"^":                    "",
		"gh issue create":      "gh issue create*",
		`^curl .*api\.x\.test`: "",
	} {
		got, _ := literalCommandGlob(pattern)
		if got != want {
			t.Errorf("%q: got %q, want %q", pattern, got, want)
		}
	}
}

func TestOpencodeOutbound_WiredAndAsked(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	dir, pm, dev := outboundPool(t, ocOutboundConfig)
	pm = setHarness(t, dir, pm, HarnessOpencode)
	cfg := wireOpencode(t, pm)
	pool := `"../` + PoolDirName(dir)
	for _, want := range []string{`"ssh *"`, `"git push*"`, `"tracker_create*"`, pool + `/*"`, pool + `/worktrees/*"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("opencode.json lacks %s:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "curl") {
		t.Errorf("a non-literal regex gets no rule: %s", cfg)
	}
	if !opencodeGateAskWired(pm.Dir()) {
		t.Fatal("wired config reads as not wired")
	}
	if _, undeclared := opencodeGateAskRules(pm.Dir()); len(undeclared) != 1 || !strings.Contains(undeclared[0], "curl") {
		t.Fatalf("the curl regex is the one undeclared matcher: %v", undeclared)
	}
	// A peer that does not declare the surface gets no outbound rules.
	if c := wireOpencode(t, setHarness(t, dir, dev, HarnessOpencode)); strings.Contains(c, "ssh") {
		t.Errorf("developer does not mutate tracker: %s", c)
	}

	// No summary: still the summary deny, as under any harness.
	if d := GatePreToolUse(dir, ocShell(pm.Dir(), "ssh host uptime")); !d.Deny || !strings.Contains(d.Reason, "summary required") {
		t.Fatalf("no summary: %+v", d)
	}
	writeSummary(t, pm, "sess-1", outboundSummary)

	for _, c := range []string{"ssh host uptime", "cd /tmp && ssh host uptime", "git push origin main",
		`ssh host 'ls -l "a b"; date'`, `ssh -q host "ls | wc -l" 2>&1`} {
		if d := GatePreToolUse(dir, ocShell(pm.Dir(), c)); d.Deny || d.Ask {
			t.Errorf("%q: opencode asks on it, so the gate lets it through; got %+v", c, d)
		}
	}
	if b, err := os.ReadFile(OutboundAuditPath(pm.Dir(), "sess-1")); err != nil || !strings.Contains(string(b), "tracker") {
		t.Errorf("a call handed to opencode's ask is audited: %v %s", err, b)
	}
	tool := outboundCall(pm.Dir(), "tracker_createIssue", nil)
	tool.Harness = HarnessOpencode
	if d := GatePreToolUse(dir, tool); d.Deny || d.Ask {
		t.Errorf("declared tool pattern: want through, got %+v", d)
	}

	// Forms opencode would not ask on are refused, and no refusal names another harness.
	for _, c := range []string{"sudo git push origin main", "sh -c 'ssh host uptime'", "curl -X POST https://x.test/y"} {
		d := GatePreToolUse(dir, ocShell(pm.Dir(), c))
		if !d.Deny || !strings.Contains(d.Reason, "opencode will not ask") {
			t.Errorf("%q: want the opencode refusal, got %+v", c, d)
		}
		if strings.Contains(d.Reason, "Claude") {
			t.Errorf("%q: refusal names another harness: %s", c, d.Reason)
		}
	}

	// An override that stops opencode asking closes the path again.
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permissions":[{"action":"shell","resource":"*","effect":"allow"}]}`)
	if d := GatePreToolUse(dir, ocShell(pm.Dir(), "ssh host uptime")); !d.Deny {
		t.Errorf("overridden ask rule: want deny, got %+v", d)
	}
}

func TestOpencodePoolEdit_AskedOnFileEdits(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	dir, pm, _ := outboundPool(t, ocOutboundConfig)
	pm = setHarness(t, dir, pm, HarnessOpencode)
	writeSummary(t, pm, "sess-1", poolSummary)
	target := filepath.Join(PoolDir(dir), "glossary.md")
	edit := func(path string) HookPayload {
		b, _ := json.Marshal(map[string]string{"path": path, "content": "x"})
		return HookPayload{Cwd: pm.Dir(), ToolName: "write", ToolInput: b, SessionID: "sess-1", Harness: HarnessOpencode}
	}

	d := GatePreToolUse(dir, edit(target))
	if !d.Deny || !strings.Contains(d.Reason, "meshctl agent onboard") || strings.Contains(d.Reason, "Claude") {
		t.Fatalf("not wired: want a refusal naming onboard and no other harness, got %+v", d)
	}
	wireOpencode(t, pm)
	for _, path := range []string{target, "../" + PoolDirName(dir) + "/glossary.md"} {
		if d := GatePreToolUse(dir, edit(path)); d.Deny || d.Ask {
			t.Errorf("%s: opencode asks on the edit, so the gate lets it through; got %+v", path, d)
		}
	}
	d = GatePreToolUse(dir, ocShell(pm.Dir(), "echo x > ../"+PoolDirName(dir)+"/glossary.md"))
	if !d.Deny || !strings.Contains(d.Reason, "file-edit tool") || strings.Contains(d.Reason, "Claude") {
		t.Errorf("a shell write to the pool cannot be asked: got %+v", d)
	}
}

// Quoted arguments are the normal case for an outbound command: opencode
// matches the raw text, so the gate must too.
func TestOpencodeOutbound_QuotedArguments(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	dir, pm, _ := outboundPool(t, `outbound:
  tracker:
    commands: ['^ssh ', 'curl .*(-X ?(POST|PUT|PATCH|DELETE)|-d |--data)']
    ask:
      opencode:
        shell: ['ssh *', 'curl *-X POST*', 'curl *-d *', 'curl *--data*']
`)
	pm = setHarness(t, dir, pm, HarnessOpencode)
	wireOpencode(t, pm)
	writeSummary(t, pm, "sess-1", outboundSummary)
	for _, c := range []string{
		`curl -s https://h.test/graphql -H 'Content-Type: application/json' -d '{"query":"{status{state uptime}}"}'`,
		`curl -s https://h.test/graphql -H "Content-Type: application/json" -d "{\"query\":\"{a}\"}" | jq .`,
		`curl -sk -X POST https://h.test/api --data-binary @body.json`,
		`ssh host 'cd /x && ls; echo "a|b"'`,
	} {
		if d := GatePreToolUse(dir, ocShell(pm.Dir(), c)); d.Deny || d.Ask {
			t.Errorf("%s\n  opencode asks on it, so the gate lets it through; got %s", c, d.Reason)
		}
	}
	// A read is not outbound and passes without any ask.
	if d := GatePreToolUse(dir, ocShell(pm.Dir(), `curl -s https://h.test/status`)); d.Deny || d.Ask {
		t.Errorf("a GET is not outbound: %+v", d)
	}
}
