package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gate_pool_test.go — pool writes are gate-asked with the peer's summary as
// the ask text (ADR-0066, spec decision 11); shell git under pool/ is denied
// (decision 12); no human at the prompt ⇒ deny (cross-cutting 68).

// poolGateFixture is a migrated pool with one peer whose session is sess-1.
func poolGateFixture(t *testing.T) (dir string, a *Agent) {
	t.Helper()
	dir = migratedRoot(t)
	a = newTestAgent(t, dir, "architect", true, nil)
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv("AGENT_MESH_DISTILLING", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	return dir, a
}

func writeSummary(t *testing.T, a *Agent, session, body string) string {
	t.Helper()
	p := SummaryPath(a.Dir(), session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const poolSummary = `## Intent
Add the term "fact owner" to the pool glossary.

## Grounding
- ADR-0065 names one owner per canonical fact.

## Precedent
- *new*: searched the doctrine and the architect KB.
`

func TestGate_PoolWriteAsksWithSummary(t *testing.T) {
	dir, a := poolGateFixture(t)
	writeSummary(t, a, "sess-1", poolSummary)
	glossary := PoolGlossaryPath(dir)
	doctrine := DoctrineLocalPath(dir)

	cases := []struct {
		name string
		p    HookPayload
	}{
		{"Write glossary", editAt(a, "sess-1", "Write", glossary)},
		{"Edit local doctrine", editAt(a, "sess-1", "Edit", doctrine)},
		{"MultiEdit config", editAt(a, "sess-1", "MultiEdit", filepath.Join(PoolDir(dir), "config.yaml"))},
		{"NotebookEdit under pool", HookPayload{Cwd: a.Dir(), SessionID: "sess-1", ToolName: "NotebookEdit",
			ToolInput: []byte(`{"notebook_path":"` + filepath.Join(PoolDir(dir), "notes.ipynb") + `"}`)}},
		{"relative Write from the peer dir", editAt(a, "sess-1", "Write", "../pool/glossary.md")},
		{"shell append", bash(a, "sess-1", "echo '**Fact owner**: x' >> ../pool/glossary.md")},
		{"shell sed -i", bash(a, "sess-1", "sed -i '' 's/owner/fact owner/' "+glossary)},
		{"shell cd then touch", bash(a, "sess-1", "cd ../pool && touch doctrine/local.md")},
		{"shell tee absolute", bash(a, "sess-1", "printf x | tee "+doctrine)},
		{"shell cwd in pool", HookPayload{Cwd: PoolDir(dir), SessionID: "sess-1", ToolName: "Bash", PeerAgent: "architect",
			ToolInput: []byte(`{"command":"echo x > glossary.md"}`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := GatePreToolUse(dir, c.p)
			if !d.Ask || d.Deny || d.Check != GateCheckPoolPath {
				t.Fatalf("want ask/pool-path, got %+v", d)
			}
			for _, want := range []string{`Intent: Add the term "fact owner" to the pool glossary.`, "ADR-0065 names one owner", "*new*: searched", "deny if this was not read to you as a confirmation round"} {
				if !strings.Contains(d.Reason, want) {
					t.Fatalf("ask text %q lacks %q", d.Reason, want)
				}
			}
		})
	}
}

func TestGate_PoolWriteDeniedWithoutUsableSummary(t *testing.T) {
	dir, a := poolGateFixture(t)
	glossary := PoolGlossaryPath(dir)
	want := SummaryPath(a.Dir(), "sess-1")

	check := func(t *testing.T, p HookPayload) {
		t.Helper()
		d := GatePreToolUse(dir, p)
		if !d.Deny || d.Check != GateCheckPoolPath {
			t.Fatalf("want deny/pool-path, got %+v", d)
		}
		if !strings.Contains(d.Reason, want) {
			t.Fatalf("deny reason %q must name the summary path %q", d.Reason, want)
		}
	}
	t.Run("no summary file-tool", func(t *testing.T) { check(t, editAt(a, "sess-1", "Write", glossary)) })
	t.Run("no summary shell", func(t *testing.T) { check(t, bash(a, "sess-1", "echo x >> "+glossary)) })
	writeSummary(t, a, "sess-1", "\n")
	t.Run("empty summary", func(t *testing.T) { check(t, editAt(a, "sess-1", "Write", glossary)) })
	writeSummary(t, a, "sess-1", "## Grounding\n- x\n\n## Precedent\n- y\n")
	t.Run("summary without Intent", func(t *testing.T) { check(t, editAt(a, "sess-1", "Write", glossary)) })
}

// With no session id in the payload the gate resolves it the way the KB-rule
// ack does (the harness env), so the deny names a path the peer can write.
func TestGate_PoolWriteResolvesSessionFromEnv(t *testing.T) {
	dir, a := poolGateFixture(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "env-sess")
	p := editAt(a, "", "Write", PoolGlossaryPath(dir))
	d := GatePreToolUse(dir, p)
	if !d.Deny || !strings.Contains(d.Reason, SummaryPath(a.Dir(), "env-sess")) {
		t.Fatalf("got %+v", d)
	}
}

func TestGate_PoolWriteDeniedWithNoHumanAtThePrompt(t *testing.T) {
	dir, a := poolGateFixture(t)
	writeSummary(t, a, "sess-1", poolSummary)
	glossary := PoolGlossaryPath(dir)
	hub := newTestAgent(t, dir, "developer", false, nil)
	peer := func(work bool, p HookPayload) HookPayload {
		p.Cwd, p.PeerAgent, p.InSession, p.Work = hub.Dir(), "architect", true, work
		return p
	}

	cases := []struct {
		name string
		env  map[string]string
		p    HookPayload
	}{
		{"dispatch depth", map[string]string{"AGENT_DISPATCH_DEPTH": "1"}, editAt(a, "sess-1", "Write", glossary)},
		{"dispatch depth shell", map[string]string{"AGENT_DISPATCH_DEPTH": "2"}, bash(a, "sess-1", "echo x >> "+glossary)},
		{"distilling", map[string]string{"AGENT_MESH_DISTILLING": "1"}, editAt(a, "sess-1", "Write", glossary)},
		{"consult mode", nil, peer(false, editAt(a, "sess-1", "Write", glossary))},
		{"consult mode shell", nil, peer(false, bash(a, "sess-1", "echo x >> "+glossary))},
		{"work mode", nil, peer(true, editAt(a, "sess-1", "Edit", glossary))},
		{"work mode shell", nil, peer(true, bash(a, "sess-1", "cd "+PoolDir(dir)+" && touch glossary.md"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			d := GatePreToolUse(dir, c.p)
			if !d.Deny || d.Ask || d.Check != GateCheckPoolPath || !strings.Contains(d.Reason, "no human at the prompt") {
				t.Fatalf("want deny 'no human at the prompt' (pool-path), got %+v", d)
			}
		})
	}
	// Depth 0 is not headless: the same write asks.
	t.Setenv("AGENT_DISPATCH_DEPTH", "0")
	if d := GatePreToolUse(dir, editAt(a, "sess-1", "Write", glossary)); !d.Ask {
		t.Fatalf("depth 0 must ask, got %+v", d)
	}
}

func TestGate_ShellGitUnderPoolDenied(t *testing.T) {
	dir, a := poolGateFixture(t)
	writeSummary(t, a, "sess-1", poolSummary) // a summary never makes git in pool/ askable
	pool := PoolDir(dir)
	deny := []HookPayload{
		bash(a, "sess-1", "git -C ../pool status"),
		bash(a, "sess-1", "git -C "+pool+" add glossary.md && git -C "+pool+" commit -m x"),
		bash(a, "sess-1", "cd ../pool && git add -A"),
		bash(a, "sess-1", "cd "+pool+"; git log --oneline"),
		bash(a, "sess-1", "git add "+pool+"/glossary.md"),
		bash(a, "sess-1", "git diff ../pool/doctrine/local.md"),
		{Cwd: pool, SessionID: "sess-1", ToolName: "Bash", PeerAgent: "architect", ToolInput: []byte(`{"command":"git status"}`)},
	}
	for _, p := range deny {
		d := GatePreToolUse(dir, p)
		if !d.Deny || d.Check != GateCheckPoolPath || !strings.Contains(d.Reason, "never run git") {
			t.Fatalf("%s: want deny (never run git in pool/), got %+v", string(p.ToolInput), d)
		}
	}
	allow := []HookPayload{
		bash(a, "sess-1", "git status"),                       // the peer's own repo
		bash(a, "sess-1", "git -C "+a.Dir()+" log --oneline"), // explicit own dir
		bash(a, "sess-1", "grep -n 'git -C pool' notes.md"),   // a mention is not an invocation
		bash(a, "sess-1", "cat ../pool/glossary.md"),
	}
	for _, p := range allow {
		if d := GatePreToolUse(dir, p); d.Deny || d.Ask {
			t.Fatalf("%s: want allow, got %+v", string(p.ToolInput), d)
		}
	}
}

func TestGate_PoolReadsAndNonPoolWritesAllowed(t *testing.T) {
	dir, a := poolGateFixture(t)
	glossary := PoolGlossaryPath(dir)
	wt := filepath.Join(WorktreesPath(dir), "architect-s1", "knowledge", "x.md")
	cases := []struct {
		name string
		p    HookPayload
	}{
		{"Read tool", editAt(a, "sess-1", "Read", glossary)},
		{"cat", bash(a, "sess-1", "cat "+glossary)},
		{"cat with stderr to null", bash(a, "sess-1", "cat ../pool/glossary.md 2>/dev/null")},
		{"grep", bash(a, "sess-1", "grep -n 'Fact owner' "+glossary)},
		{"ls pool", bash(a, "sess-1", "ls -la ../pool")},
		{"write into a KB worktree under the pool dir", editAt(a, "sess-1", "Write", wt)},
		{"shell write into a worktree", bash(a, "sess-1", "echo x > "+wt)},
		{"write in own knowledge", editAt(a, "sess-1", "Write", filepath.Join(a.Dir(), KnowledgeDir, "candidates", "x.md"))},
		{"redirect to /tmp from the pool cwd", HookPayload{Cwd: PoolDir(dir), SessionID: "sess-1", ToolName: "Bash", PeerAgent: "architect",
			ToolInput: []byte(`{"command":"cat glossary.md > /tmp/out.md"}`)}},
		{"heredoc naming the pool path is prose", bash(a, "sess-1", "cat <<'EOF'\necho x > ../pool/glossary.md\nEOF")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if d := GatePreToolUse(dir, c.p); d.Deny || d.Ask {
				t.Fatalf("want allow, got %+v", d)
			}
		})
	}
	// Outside any pool the rule is inert.
	if d := GatePreToolUse("", editAt(a, "sess-1", "Write", glossary)); d.Deny || d.Ask {
		t.Fatalf("no pool root: want allow, got %+v", d)
	}
}

// TestGate_ShellWriteTargetsAgreeAcrossRules: the pool-knowledge rule and the
// KB main-checkout rule read a shell line's write targets the same way —
// cp/ln/install/rsync write only their destination, `dd of=` writes, mv and
// rm write every operand.
func TestGate_ShellWriteTargetsAgreeAcrossRules(t *testing.T) {
	dir, a := poolGateFixture(t)
	writeSummary(t, a, "sess-1", poolSummary)
	glossary := PoolGlossaryPath(dir)
	kbFile := filepath.Join(a.Dir(), KnowledgeDir, "reference", "x.md")
	tmp := t.TempDir()

	pool := map[string]bool{ // command → pool write asked
		"cp " + glossary + " " + tmp + "/g.md":        false,
		"cp " + tmp + "/g.md " + glossary:             true,
		"rsync -a " + glossary + " " + tmp + "/":      false,
		"ln -s " + glossary + " " + tmp + "/g.md":     false,
		"install -m 644 " + tmp + "/g.md " + glossary: true,
		"dd if=" + tmp + "/g.md of=" + glossary:       true,
		"dd if=" + glossary + " of=" + tmp + "/g.md":  false,
		"mv " + glossary + " " + tmp + "/g.md":        true,
		"rm " + glossary:                              true,
		"cat " + glossary + " > " + tmp + "/g.md":     false,
	}
	for cmd, want := range pool {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if got := d.Check == GateCheckPoolPath && (d.Ask || d.Deny); got != want {
			t.Errorf("pool rule %q: gated=%v, want %v (%+v)", cmd, got, want, d)
		}
	}
	kb := map[string]bool{ // command → KB main-checkout write denied
		"cp " + kbFile + " " + tmp + "/x.md":       false,
		"cp " + tmp + "/x.md " + kbFile:            true,
		"dd if=" + tmp + "/x.md of=" + kbFile:      true,
		"dd if=" + kbFile + " of=" + tmp + "/x.md": false,
		"mv " + kbFile + " " + tmp + "/x.md":       true,
	}
	for cmd, want := range kb {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if got := d.Deny && strings.Contains(d.Reason, KnowledgeDir); got != want {
			t.Errorf("kb rule %q: denied=%v, want %v (%+v)", cmd, got, want, d)
		}
	}
}
