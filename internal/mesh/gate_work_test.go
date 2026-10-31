package mesh

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workPool: inSessionPool plus a product repo declared as the architect's
// workdir (a git repo on main) and a delegated-work worktree directory.
func workPool(t *testing.T) (dir string, caller, arch *Agent, repo string) {
	t.Helper()
	dir, caller, arch = inSessionPool(t)
	repo = filepath.Join(t.TempDir(), "product")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if _, err := addManifestWorkdirs(ManifestPath(dir, "architect"), []string{repo}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(WorktreesPath(dir), "architect-work-req1"), 0o755); err != nil {
		t.Fatal(err)
	}
	arch, _ = FindAgent(dir, "architect")
	return dir, caller, arch, repo
}

func TestGate_InSessionPeerWorkContract(t *testing.T) {
	dir, caller, arch, repo := workPool(t)
	work := func(tool string, in map[string]string) HookPayload {
		b, _ := json.Marshal(in)
		return HookPayload{Cwd: caller.Dir(), ToolName: tool, ToolInput: b, PeerAgent: "architect", InSession: true, Work: true, SessionID: "sess-1"}
	}
	bash := func(c string) HookPayload { return work("Bash", map[string]string{"command": c}) }
	edit := func(path string) HookPayload { return work("Edit", map[string]string{"file_path": path}) }

	denyBash := []string{
		"meshctl ask pm guidance 'x'",
		"cd ../architect && meshctl msg send pm fyi x",
		"meshctl pool upgrade",
		"meshctl session list",
		"meshctl work merge req1 --agent architect",
		"echo hi > notes.md",                       // cwd is the caller: unresolvable ⇒ caller's workspace
		"echo hi > ../developer/notes.md",          // the caller's workspace
		"cd ../architect && cp x.md ../developer/", // path argument into the caller
		"git -C ../developer commit -am x",
		"rm -rf " + filepath.Join(dir, "developer", "knowledge"),
		"git -C " + repo + " push origin main",
		"cd " + repo + " && git push",
		"git -C " + repo + " push origin HEAD:main",
	}
	allowBash := []string{
		"cd ../architect && git add knowledge/x.md && git commit -m x",
		"git -C ../architect commit -am x",
		"meshctl kb begin --agent architect",
		"meshctl handoff set --agent architect <<'EOF'\nx\nEOF",
		"echo hi > ../architect/knowledge/candidates/x.md",
		"cd " + repo + " && git switch -c fix/x && git commit -am x",
		"git -C " + repo + " push -u origin fix/x",
		"cd " + filepath.Join(WorktreesPath(dir), "architect-work-req1") + " && git commit -am x",
		"git -C ../architect log --oneline -5",
		"grep -rn split ../developer/ | head",
		"cd ../architect && cp knowledge/a.md /tmp/a.md",
	}
	for _, c := range denyBash {
		if d := GatePreToolUse(dir, bash(c)); !d.Deny {
			t.Errorf("work peer should be denied: %s", c)
		}
	}
	for _, c := range allowBash {
		if d := GatePreToolUse(dir, bash(c)); d.Deny {
			t.Errorf("work peer should be allowed: %s (%s)", c, d.Reason)
		}
	}
	for _, p := range []string{
		filepath.Join(arch.Dir(), "knowledge", "candidates", "x.md"),
		filepath.Join(repo, "src", "main.go"),
		filepath.Join(WorktreesPath(dir), "architect-work-req1", "state.md"),
		filepath.Join(WorktreesPath(dir), "architect-work-req1", "knowledge", "playbooks", "x.md"),
		"../architect/knowledge/candidates/0001-x.md",
	} {
		if d := GatePreToolUse(dir, edit(p)); d.Deny {
			t.Errorf("work peer should edit %s (%s)", p, d.Reason)
		}
	}
	for _, p := range []string{
		filepath.Join(caller.Dir(), "CLAUDE.md"),
		"notes.md", // relative to the caller's cwd
		filepath.Join(dir, "catalog-expert", "knowledge", "x.md"),
		filepath.Join(arch.Dir(), IntakeDir, "x.md"), // still meshctl-owned
		// Confirmed knowledge on the main checkout changes only via kb finish.
		filepath.Join(arch.Dir(), "knowledge", "playbooks", "x.md"),
		"../architect/knowledge/decisions/0001-x.md",
	} {
		if d := GatePreToolUse(dir, edit(p)); !d.Deny {
			t.Errorf("work peer must not edit %s", p)
		}
	}
	if d := GatePreToolUse(dir, work("Agent", map[string]string{"subagent_type": "mesh-pm-work"})); !d.Deny {
		t.Error("a work peer must not delegate")
	}
	// Consult mode is unchanged by any of this: edits stay denied.
	p := edit(filepath.Join(arch.Dir(), "knowledge", "x.md"))
	p.Work = false
	if !GatePreToolUse(dir, p).Deny {
		t.Error("consult peer must still be denied edits")
	}
}

func TestGate_DelegatingWorkToMutatingPeerAsks(t *testing.T) {
	dir, caller, _ := inSessionPool(t)
	newTestAgent(t, dir, "pm", false, []string{"tracker"})
	call := func(st string) GateDecision {
		in, _ := json.Marshal(map[string]string{"subagent_type": st, "prompt": "x"})
		return GatePreToolUse(dir, HookPayload{Cwd: caller.Dir(), ToolName: "Agent", ToolInput: in})
	}
	d := call("mesh-pm-work")
	if !d.Ask || d.Deny || !strings.Contains(d.Reason, "tracker") {
		t.Fatalf("mutating delegation must ask: %+v", d)
	}
	for _, st := range []string{"mesh-architect-work", "mesh-architect", "general-purpose", ""} {
		if d := call(st); d.Ask || d.Deny {
			t.Errorf("%q must pass: %+v", st, d)
		}
	}

	var buf bytes.Buffer
	if err := EmitGateDecision(&buf, d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"permissionDecision":"ask"`) {
		t.Fatalf("claude envelope: %s", buf.String())
	}
	buf.Reset()
	if err := (opencodeHarness{}).EmitGate(&buf, d); err != nil || !strings.Contains(buf.String(), "approve") {
		t.Fatalf("opencode must treat ask as deny: %q %v", buf.String(), err)
	}
}
