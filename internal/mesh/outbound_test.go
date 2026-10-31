package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const outboundTestConfig = `outbound:
  tracker:
    tools: ["createIssue", "addCommentTo"]
    commands: ['^curl .*api\.tracker\.example', '^gh issue create']
`

// outboundPool is a pool with two tracker mutators (pm, ops), one code
// mutator (developer) and the given config file.
func outboundPool(t *testing.T, config string) (dir string, pm, dev *Agent) {
	t.Helper()
	dir = meshRoot(t)
	pm = newTestAgent(t, dir, "pm", false, []string{"tracker"})
	newTestAgent(t, dir, "ops", false, []string{"tracker"})
	dev = newTestAgent(t, dir, "developer", false, []string{"code"})
	if config != "" {
		if err := os.MkdirAll(filepath.Dir(PoolConfigPath(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(PoolConfigPath(dir), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv("AGENT_MESH_DISTILLING", "")
	return dir, pm, dev
}

func outboundCall(cwd, tool string, in map[string]string) HookPayload {
	b, _ := json.Marshal(in)
	return HookPayload{Cwd: cwd, ToolName: tool, ToolInput: b, SessionID: "sess-1"}
}

func outboundBash(cwd, c string) HookPayload {
	return outboundCall(cwd, "Bash", map[string]string{"command": c})
}

func TestGate_OutboundSurfaces(t *testing.T) {
	dir, pm, dev := outboundPool(t, outboundTestConfig)
	mcp := "mcp__tracker__createIssue"
	// A tagged peer is asked only with a usable summary.
	writeSummary(t, pm, "sess-1", outboundSummary)

	cases := []struct {
		name   string
		p      HookPayload
		want   string // "allow", "ask", "deny"
		reason []string
	}{
		{"tagged peer, MCP tool", outboundCall(pm.Dir(), mcp, map[string]string{"summary": "x"}), "ask", []string{"tracker"}},
		{"tagged peer, opencode-style tool name, any case", outboundCall(pm.Dir(), "tkt_CREATEISSUE", nil), "ask", nil},
		{"untagged peer names the mutators", outboundCall(dev.Dir(), mcp, nil), "deny", []string{"`tracker`", "ops", "pm"}},
		{"tagged peer, shell command", outboundBash(pm.Dir(), "curl -X POST https://api.tracker.example/issue -d @x.json"), "ask", nil},
		{"shell command inside bash -c", outboundBash(pm.Dir(), `bash -c "gh issue create --title x"`), "ask", nil},
		{"untagged peer, shell command", outboundBash(dev.Dir(), "gh issue create --title x"), "deny", []string{"ops", "pm"}},
		{"heredoc body is not matched", outboundBash(pm.Dir(), "cat > notes.md <<'EOF'\ngh issue create --title x\nEOF"), "allow", nil},
		{"cat of a script is not matched", outboundBash(pm.Dir(), "cat scripts/file-issue.sh"), "allow", nil},
		{"non-matching tool", outboundCall(pm.Dir(), "mcp__tracker__getIssue", nil), "allow", nil},
		{"Write never outbound", outboundCall(pm.Dir(), "Write", map[string]string{"file_path": filepath.Join(pm.Dir(), "createIssue.md")}), "allow", nil},
		{"Edit never outbound", outboundCall(pm.Dir(), "Edit", map[string]string{"file_path": filepath.Join(pm.Dir(), "createIssue.md")}), "allow", nil},
		{"MultiEdit never outbound", outboundCall(pm.Dir(), "MultiEdit", map[string]string{"file_path": filepath.Join(pm.Dir(), "x.md")}), "allow", nil},
		{"NotebookEdit never outbound", outboundCall(pm.Dir(), "NotebookEdit", map[string]string{"notebook_path": filepath.Join(pm.Dir(), "x.ipynb")}), "allow", nil},
		{"pool dir that is no peer", outboundCall(dir, mcp, nil), "deny", []string{"run from a peer workspace"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := GatePreToolUse(dir, c.p)
			got := "allow"
			switch {
			case d.Deny:
				got = "deny"
			case d.Ask:
				got = "ask"
			}
			if got != c.want {
				t.Fatalf("want %s, got %s (%s)", c.want, got, d.Reason)
			}
			if got != "allow" && d.Check != GateCheckOutbound {
				t.Errorf("check = %q, want %q", d.Check, GateCheckOutbound)
			}
			for _, r := range c.reason {
				if !strings.Contains(d.Reason, r) {
					t.Errorf("reason lacks %q: %s", r, d.Reason)
				}
			}
		})
	}
}

func TestGate_OutboundInertOutsidePool(t *testing.T) {
	_, pm, _ := outboundPool(t, outboundTestConfig)
	if d := GatePreToolUse("", outboundCall(pm.Dir(), "mcp__x__createIssue", nil)); d.Deny || d.Ask {
		t.Fatalf("outside any pool the outbound gate is inert: %+v", d)
	}
}

func TestGate_OutboundNoHumanAtThePrompt(t *testing.T) {
	dir, pm, dev := outboundPool(t, outboundTestConfig)
	mcp := "mcp__tracker__createIssue"
	peer := func(work bool) HookPayload {
		p := outboundCall(dev.Dir(), mcp, nil)
		p.PeerAgent, p.InSession, p.Work = "pm", true, work
		return p
	}
	cases := []struct {
		name   string
		env    map[string]string
		p      HookPayload
		reason []string
	}{
		{"consult mode", nil, peer(false), []string{"no human at the prompt"}},
		{"work mode", nil, peer(true), []string{"no human at the prompt", "needs-confirm", SummaryPath(pm.Dir(), "sess-1")}},
		{"dispatch depth", map[string]string{"AGENT_DISPATCH_DEPTH": "1"}, outboundCall(pm.Dir(), mcp, nil), []string{"no human at the prompt", "needs-confirm"}},
		{"distilling", map[string]string{"AGENT_MESH_DISTILLING": "1"}, outboundCall(pm.Dir(), mcp, nil), []string{"no human at the prompt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			d := GatePreToolUse(dir, c.p)
			if !d.Deny || d.Check != GateCheckOutbound {
				t.Fatalf("want outbound deny, got %+v", d)
			}
			for _, r := range c.reason {
				if !strings.Contains(d.Reason, r) {
					t.Errorf("reason lacks %q: %s", r, d.Reason)
				}
			}
		})
	}
}

func TestGate_OutboundNoAskForm(t *testing.T) {
	dir, pm, _ := outboundPool(t, outboundTestConfig)
	p := outboundCall(pm.Dir(), "tracker_createIssue", nil)
	p.Harness = HarnessOpencode
	if d := GatePreToolUse(dir, p); !d.Deny || !strings.Contains(d.Reason, "run from a gated session") {
		t.Fatalf("opencode has no ask form: want deny, got %+v", d)
	}
}

func TestGate_OutboundFailsClosed(t *testing.T) {
	for name, config := range map[string]string{
		"unparseable config": "outbound: [this is: not a map\n",
		"bad tool regex":     "outbound:\n  tracker:\n    tools: ['create(']\n    commands: ['^gh issue']\n",
		"bad command regex":  "outbound:\n  tracker:\n    tools: ['createIssue']\n    commands: ['gh [issue']\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir, pm, _ := outboundPool(t, config)
			for _, p := range []HookPayload{
				outboundCall(pm.Dir(), "mcp__tracker__getIssue", nil),
				outboundBash(pm.Dir(), "ls -la"),
			} {
				d := GatePreToolUse(dir, p)
				if !d.Deny || !strings.Contains(d.Reason, "outbound gate misconfigured") {
					t.Errorf("%s: want misconfigured deny, got %+v", p.ToolName, d)
				}
			}
			edit := outboundCall(pm.Dir(), "Edit", map[string]string{"file_path": filepath.Join(pm.Dir(), "x.md")})
			if d := GatePreToolUse(dir, edit); d.Deny || d.Ask {
				t.Errorf("non-outbound call must be unaffected: %+v", d)
			}
		})
	}
}

// TestGate_OutboundFailsClosedLetsDiagnosticsRun: the misconfigured deny
// tells the agent to run `meshctl doctor`, so doctor and the read-only
// upgrade preview still run; anything chained to them is still refused.
func TestGate_OutboundFailsClosedLetsDiagnosticsRun(t *testing.T) {
	dir, pm, _ := outboundPool(t, "outbound:\n  tracker:\n    tools: ['create(']\n    commands: ['^gh issue']\n")
	for _, cmd := range []string{
		"meshctl doctor",
		"meshctl doctor --agent pm",
		"cd .. && meshctl doctor",
		"meshctl pool upgrade --check",
		"meshctl --agent pm pool upgrade --check",
		"meshctl version",
	} {
		if d := GatePreToolUse(dir, outboundBash(pm.Dir(), cmd)); d.Deny {
			t.Errorf("%q: diagnostic refused: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{
		"meshctl doctor && gh issue create -t x",
		"meshctl doctor; ls",
		"meshctl pool upgrade",
		"meshctl doctor --fix",
		"echo meshctl doctor",
	} {
		if d := GatePreToolUse(dir, outboundBash(pm.Dir(), cmd)); !d.Deny || !strings.Contains(d.Reason, "outbound gate misconfigured") {
			t.Errorf("%q: want misconfigured deny, got %+v", cmd, d)
		}
	}
}

func TestDoctor_OutboundFindings(t *testing.T) {
	t.Run("misconfigured", func(t *testing.T) {
		dir, _, _ := outboundPool(t, "outbound:\n  tracker:\n    tools: ['create(']\n    commands: ['x']\n")
		fs, ok := Doctor(dir)
		if ok || !hasFinding(fs, "outbound gate misconfigured") {
			t.Fatalf("want an error for the bad regex: %+v", fs)
		}
	})
	t.Run("cross-checks", func(t *testing.T) {
		dir, _, _ := outboundPool(t, "outbound:\n  tracker:\n    tools: ['createIssue']\n  wiki:\n    tools: ['createPage']\n    commands: ['^curl .*wiki']\n")
		fs, _ := Doctor(dir)
		for _, want := range []string{
			"mutates `code` but no outbound surface", // developer's tag
			"surface `wiki` is mutated by no peer",   // nobody tags wiki
			"surface `tracker` lists `tools` but no `commands`",
		} {
			if !hasFinding(fs, want) {
				t.Errorf("missing finding %q in %+v", want, fs)
			}
		}
		if hasFinding(fs, "surface `wiki` lists `tools` but no") {
			t.Error("wiki declares commands")
		}
		for _, f := range fs {
			if strings.Contains(f.Message, "outbound") && f.Level != "warn" {
				t.Errorf("cross-check findings are warnings: %+v", f)
			}
		}
	})
	t.Run("wiring lines", func(t *testing.T) {
		dir, _, _ := outboundPool(t, outboundTestConfig)
		lines := strings.Join(OutboundWiringLines(dir), "\n")
		for _, want := range []string{"tracker", "createIssue", "^gh issue create", "ops, pm"} {
			if !strings.Contains(lines, want) {
				t.Errorf("wiring lines lack %q:\n%s", want, lines)
			}
		}
	})
}
