package mesh

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// edit builds a PreToolUse payload for a file-editing tool.
func edit(tool, filePath string) HookPayload {
	p := HookPayload{ToolName: tool}
	if filePath != "" {
		p.ToolInput = json.RawMessage(`{"file_path":` + strconv.Quote(filePath) + `}`)
	}
	return p
}

func TestGate_DenyAndAllow(t *testing.T) {
	cases := []struct {
		name string
		p    HookPayload
		deny bool
	}{
		// --- competing memory stores: DENY ---
		{"claude auto-memory write", edit("Write", "/Users/k/.claude/projects/p/memory/note.md"), true},
		{"claude auto-memory edit", edit("Edit", "/home/u/.claude/projects/x/memory/MEMORY.md"), true},
		// --- hand-written intake: DENY ---
		{"hand-written intake", edit("Write", "/Users/k/agents/architect/"+IntakeDir+"/msg.md"), true},
		{"intake via MultiEdit", edit("MultiEdit", "/Users/k/agents/pm/"+IntakeDir+"/queued.json"), true},

		// --- the mesh's OWN stores: ALLOW ---
		{"mesh knowledge tree", edit("Write", "/Users/k/agents/architect/knowledge/playbooks/x.md"), false},
		{"mesh .memory dir", edit("Write", "/Users/k/agents/architect/.memory/index.json"), false},
		// --- unrelated work: ALLOW ---
		{"ordinary source write", edit("Write", "/Users/k/project/src/main.go"), false},
		{"read of a memory path", edit("Read", "/Users/k/.claude/projects/p/memory/note.md"), false},
		{"bash", HookPayload{ToolName: "Bash"}, false},
		{"edit with no path", HookPayload{ToolName: "Edit"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GatePreToolUse("", c.p).Deny
			if got != c.deny {
				t.Fatalf("Deny=%v, want %v (tool=%q)", got, c.deny, c.p.ToolName)
			}
		})
	}
}

func TestGate_EmitEnvelope(t *testing.T) {
	// Deny ⇒ Claude Code's PreToolUse deny envelope with a reason.
	var buf bytes.Buffer
	if err := EmitGateDecision(&buf, GatePreToolUse("", edit("Write", "/x/.claude/projects/p/memory/y.md"))); err != nil {
		t.Fatal(err)
	}
	var out struct {
		HSO struct {
			Event  string `json:"hookEventName"`
			Dec    string `json:"permissionDecision"`
			Reason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("deny output is not valid JSON: %v (%s)", err, buf.String())
	}
	if out.HSO.Event != "PreToolUse" || out.HSO.Dec != "deny" || out.HSO.Reason == "" {
		t.Fatalf("bad deny envelope: %+v", out.HSO)
	}

	// Allow ⇒ nothing written (default flow proceeds).
	buf.Reset()
	if err := EmitGateDecision(&buf, GatePreToolUse("", edit("Write", "/x/src/main.go"))); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("allow must emit nothing, got %q", buf.String())
	}
}

// TestGate_DoctorRequiresGate is the enforcement proof: an agent missing the
// PreToolUse gate is a doctor ERROR (doctrine unenforced), and onboarding
// (EnsureAgentSettings) clears it. This is the before/after the human runs.
func TestGate_DoctorRequiresGate(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)

	// Onboard wires it: gate present, stamp current, no gate finding.
	if _, err := EnsureAgentSettings(a.Dir()); err != nil {
		t.Fatal(err)
	}
	if hasFinding(gateWiringFindings(a), "enforcement gate not wired") {
		t.Fatal("freshly-wired agent should have the gate")
	}
	if v := WiringVersion(a.Dir()); v != MeshWiringVersion {
		t.Fatalf("stamp = v%d, want v%d", v, MeshWiringVersion)
	}
	sp := filepath.Join(a.Dir(), ".claude", "settings.json")
	root := map[string]any{}
	b, _ := os.ReadFile(sp)
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	if !hookCommandPresent(root["hooks"].(map[string]any)["PreToolUse"].([]any), "meshctl hook pre-tool-use") {
		t.Fatal("PreToolUse gate not written to settings.json")
	}

	// Downgrade to the pre-gate state the 13 live agents are in: strip PreToolUse
	// and the stamp.
	delete(root["hooks"].(map[string]any), "PreToolUse")
	nb, _ := json.MarshalIndent(root, "", "  ")
	if err := os.WriteFile(sp, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(a.Dir(), ".claude", meshWiringStampFile))

	// Now doctor must FAIL on this agent for the gate specifically.
	if !hasFinding(gateWiringFindings(a), "enforcement gate not wired") {
		t.Fatal("unwired agent must report the gate as missing")
	}
	findings, ok := Doctor(dir)
	if ok {
		t.Fatal("Doctor must not be ok while an agent's enforcement gate is missing")
	}
	if !hasFinding(findings, "enforcement gate not wired") {
		t.Fatal("Doctor output must name the missing gate")
	}

	// Re-onboard: the same distribution pipe re-installs the gate + stamp.
	if _, err := EnsureAgentSettings(a.Dir()); err != nil {
		t.Fatal(err)
	}
	if hasFinding(gateWiringFindings(a), "enforcement gate not wired") {
		t.Fatal("re-onboard must restore the gate")
	}
	if v := WiringVersion(a.Dir()); v != MeshWiringVersion {
		t.Fatalf("re-onboard stamp = v%d, want v%d", v, MeshWiringVersion)
	}
}

// TestGate_ShellParsingFalsePositives: fd-dups, /dev/null redirections and
// heredoc bodies are not the command's own arguments, and a subcommand match
// is an invocation only at a command start (both seen in practice).
func TestGate_ShellParsingFalsePositives(t *testing.T) {
	dir, caller, arch, _ := workPool(t)
	req := filepath.Join(arch.Dir(), IntakeDir, "2026-01-01-developer-x.md")
	bash := func(cwd, cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: cwd, ToolName: "Bash", ToolInput: in, SessionID: "sess-1"}
	}
	consult := func(cmd string) HookPayload {
		p := bash(caller.Dir(), cmd)
		p.PeerAgent, p.InSession = "architect", true
		return p
	}
	work := func(cmd string) HookPayload {
		p := consult(cmd)
		p.Work = true
		return p
	}
	cases := []struct {
		name string
		p    HookPayload
		deny bool
	}{
		// --- fd-dups and /dev/null are not shell writes ---
		{"own intake read, stderr to /dev/null", bash(arch.Dir(), "cat "+req+" 2>/dev/null"), false},
		{"own intake read, stderr dup", bash(arch.Dir(), "cat intake/2026-01-01-developer-x.md 2>&1"), false},
		{"own intake read, both to /dev/null", bash(arch.Dir(), "grep -n status "+req+" >/dev/null 2>&1"), false},
		{"own intake read, stdout to /dev/null", bash(arch.Dir(), "cat intake/2026-01-01-developer-x.md 1> /dev/null"), false},
		{"own intake read, &> /dev/null", bash(arch.Dir(), "cat intake/2026-01-01-developer-x.md &>/dev/null"), false},
		{"consult peer: own intake read with dup", consult("cat ../architect/intake/2026-01-01-developer-x.md 2>&1 | head"), false},
		{"work peer: own intake read with /dev/null", work("cat ../architect/intake/2026-01-01-developer-x.md 2>/dev/null"), false},
		// --- a real redirect is still a write ---
		{"intake redirect", bash(arch.Dir(), "echo hi > intake/2026-01-01-developer-x.md 2>/dev/null"), true},
		{"intake append", bash(arch.Dir(), "cat body.md >> "+req+" 2>&1"), true},
		{"intake append after /dev/null", bash(arch.Dir(), "cat body.md 2>/dev/null >> "+req), true},
		{"consult peer: redirect to a file", consult("cat a 2>/dev/null > out.txt"), true},
		{"consult peer: append to a log", consult("cmd 2>&1 >> log"), true},
		{"work peer: redirect into the caller", work("echo hi 2>/dev/null > ../developer/notes.md"), true},
		// --- heredoc bodies are not arguments ---
		{"consult peer: heredoc naming a private path and a branch", consult("meshctl inbox respond 2026-01-01-developer-x --agent architect --status answered --response-file - <<'EOF'\n# Answer\n> see ../developer/knowledge/notes.md and session/abc\n$ echo x > /tmp/y\nEOF"), false},
		{"work peer: heredoc naming the caller", work("meshctl handoff set --agent architect <<'EOF'\n> quote\nsee ../developer/notes.md on branch session/abc\nEOF"), false},
		{"plain session: heredoc naming intake", bash(arch.Dir(), "meshctl inbox respond 2026-01-01-developer-x --status answered --response-file - <<EOF\nwrote intake/2026-01-01-developer-x.md > done\nEOF"), false},
		{"heredoc, then a real intake redirect", bash(arch.Dir(), "cat <<'EOF' > "+req+"\nhello\nEOF"), true},
		{"work peer: heredoc, then the caller's path as an argument", work("cat <<'EOF' | tee ../developer/notes.md\nhello\nEOF"), true},
		{"work peer: the caller's path as an argument", work("cp x.md ../developer/notes.md"), true},
		// --- subcommand matches anchor to a command start ---
		{"consult peer: kb finish", consult("meshctl kb finish --summary-file x --agent architect"), true},
		{"consult peer: kb finish via bash -c", consult(`bash -c "meshctl kb finish --agent architect"`), true},
		{"consult peer: kb finish via sh -c after a pipe", consult("echo y | sh -c 'meshctl kb finish'"), true},
		{"consult peer: kb finish after &&", consult("cd ../architect && meshctl kb finish"), true},
		{"consult peer: grep for kb finish in a script", consult(`grep -n "meshctl kb finish" tools/wrapper.sh`), false},
		{"consult peer: cat of a wrapper script", consult("cat tools/wrapper.sh"), false},
		{"consult peer: heredoc mentioning kb finish", consult("meshctl inbox respond 2026-01-01-developer-x --agent architect --status answered --response-file - <<'EOF'\nthen run meshctl kb finish\nEOF"), false},
		{"consult peer: kb finish as an echoed string", consult(`echo "meshctl kb finish" "git push"`), false},
		{"consult peer: rm as a grep pattern", consult(`grep -rn "rm -rf" ../architect/knowledge/`), false},
		{"consult peer: git commit in a log search", consult("git -C ../architect log --grep 'git commit' --oneline"), false},
		{"work peer: pool as a grep pattern", work(`grep -rn "meshctl pool upgrade" ../architect/knowledge/`), false},
		{"work peer: pool upgrade via bash -c", work(`bash -c "meshctl pool upgrade"`), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := GatePreToolUse(dir, c.p)
			if d.Deny != c.deny {
				t.Fatalf("Deny=%v, want %v (reason=%q)", d.Deny, c.deny, d.Reason)
			}
		})
	}
}

// TestShellInvokes pins the contract of the shared matcher later gates reuse
// (promotion, pool-path, branch-name, triage apply): an invocation is the
// subcommand words at a command start, never a mention.
func TestShellInvokes(t *testing.T) {
	cases := []struct {
		cmd, sub string
		want     bool
	}{
		{"meshctl kb finish --summary-file x", "meshctl kb finish", true},
		{"meshctl kb finish", "meshctl kb", true},
		{"meshctl kb", "meshctl kb finish", false},
		{"meshctl kbfinish", "meshctl kb", false},
		{"  meshctl   kb\tfinish", "meshctl kb finish", true},
		{"cd x; meshctl kb finish", "meshctl kb finish", true},
		{"cd x && meshctl kb finish", "meshctl kb finish", true},
		{"a || meshctl kb finish", "meshctl kb finish", true},
		{"echo y | meshctl kb finish", "meshctl kb finish", true},
		{"(meshctl kb finish)", "meshctl kb finish", true},
		{"echo $(meshctl kb finish)", "meshctl kb finish", true},
		{"x=1 meshctl kb finish", "meshctl kb finish", true},
		{"./bin/meshctl kb finish", "meshctl kb finish", true},
		{"meshctl --agent=a kb finish", "meshctl kb finish", true},
		{"meshctl kb --agent=a finish", "meshctl kb finish", true},
		{`bash -c "meshctl kb finish"`, "meshctl kb finish", true},
		{`sh -c 'cd x && meshctl kb finish'`, "meshctl kb finish", true},
		{"bash -lc \"meshctl state set <<'EOF'\nx\nEOF\"", "meshctl state set", true},
		{"meshctl inbox triage apply --report r.md", "meshctl inbox triage apply", true},
		{"if meshctl kb confirm; then echo ok; fi", "meshctl kb confirm", true},
		{"cat tools/wrapper.sh", "meshctl kb finish", false},
		{`grep "meshctl kb finish" tools/wrapper.sh`, "meshctl kb finish", false},
		{`echo "meshctl kb finish"`, "meshctl kb finish", false},
		{"echo meshctl kb finish", "meshctl kb finish", false},
		{"cat > w.sh <<'EOF'\nmeshctl kb finish\nEOF", "meshctl kb finish", false},
		{"cat <<EOF\nmeshctl kb finish\nEOF\nmeshctl kb finish", "meshctl kb finish", true},
		{"meshctl inbox triage", "meshctl inbox triage apply", false},
		{"git log --grep 'git commit'", "git commit", false},
		{"git -C ../a log", "git commit", false},
		{"", "meshctl kb finish", false},
		{"meshctl kb finish", "", false},
	}
	for _, c := range cases {
		if got := ShellInvokes(c.cmd, c.sub); got != c.want {
			t.Errorf("ShellInvokes(%q, %q) = %v, want %v", c.cmd, c.sub, got, c.want)
		}
	}
}

func TestShellOwnArguments(t *testing.T) {
	cases := []struct{ in, want string }{
		{"cat x 2>/dev/null", "cat x"},
		{"cat x 2>&1", "cat x"},
		{"cat x >/dev/null 2>&1; echo $?", "cat x; echo $?"},
		{"cat x 1> /dev/null", "cat x"},
		{"cat x &>/dev/null", "cat x"},
		{"cat x > out.txt", "cat x > out.txt"},
		{"cat x >> log 2>&1", "cat x >> log"},
		{"cat x </dev/null", "cat x"},
		{"cat x 3>&-", "cat x"},
		{"cat <<'EOF' > y\nbody > z\nEOF", "cat > y"},
		{"cat <<-EOF\n\tbody\n\tEOF\necho done", "cat\necho done"},
		{"a <<X <<Y\n1\nX\n2\nY\nb", "a\nb"},
		{"cat <<< 'here string'", "cat <<< 'here string'"},
		{"echo \"a 2>/dev/null\"", "echo \"a 2>/dev/null\""},
	}
	for _, c := range cases {
		if got := ShellOwnArguments(c.in); got != c.want {
			t.Errorf("ShellOwnArguments(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGate_KBBranchDeny: a shell command naming a mesh/kb/ branch outside
// `meshctl kb` is denied, so a KB branch is never merged, rebased or deleted by
// hand (a closed bypass). A mention in a script, a heredoc or a `meshctl kb`
// invocation is not a hit.
func TestGate_KBBranchDeny(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	bash := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: a.Dir(), ToolName: "Bash", ToolInput: in, SessionID: "sess-1"}
	}
	cases := []struct {
		name string
		cmd  string
		deny bool
	}{
		{"git merge", "git merge mesh/kb/abc", true},
		{"git branch -D", "git branch -D mesh/kb/abc", true},
		{"git rebase via -C", "git -C ../architect rebase mesh/kb/abc", true},
		{"git push of the branch", "git push origin mesh/kb/abc", true},
		{"full ref", "git update-ref -d refs/heads/mesh/kb/abc", true},
		{"after &&", "cd ../architect && git merge --ff-only mesh/kb/abc", true},
		{"via bash -c", `bash -c "git merge mesh/kb/abc"`, true},
		{"range argument", "git diff main...mesh/kb/abc", true},
		{"meshctl kb finish", "meshctl kb finish --agent architect", false},
		{"meshctl kb finish with session", "meshctl kb finish --session abc --agent architect", false},
		{"meshctl kb finish --abort", "meshctl kb finish --abort", false},
		{"meshctl kb begin", "meshctl kb begin", false},
		{"meshctl kb via path", "./bin/meshctl kb finish", false},
		{"cat of a script mentioning the branch", "cat tools/wrapper.sh", false},
		{"heredoc mentioning the branch", "meshctl handoff set --agent architect <<'EOF'\nsee branch mesh/kb/abc\nEOF", false},
		{"work branch", "git merge mesh/work/req1", false},
		{"plain git", "git status", false},
		{"legacy branch mention is not a mesh/kb branch", "git log session/abc", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, pool := range []string{dir, ""} {
				// Only the branch rule is under test: a `kb finish` with no
				// KB session is denied by the promotion rule.
				d := GatePreToolUse(pool, bash(c.cmd))
				if (d.Deny && d.Check == GateCheckKBBranch) != c.deny {
					t.Fatalf("pool=%q Deny=%v, want %v (reason=%q)", pool, d.Deny, c.deny, d.Reason)
				}
				if c.deny {
					if d.Check != GateCheckKBBranch {
						t.Fatalf("Check=%q, want %q", d.Check, GateCheckKBBranch)
					}
					if !strings.Contains(d.Reason, "meshctl kb finish") {
						t.Fatalf("reason must point at meshctl kb finish: %q", d.Reason)
					}
				}
			}
		})
	}
}
