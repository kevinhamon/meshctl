package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEffectiveTools_MergeAgentWins(t *testing.T) {
	pool := PoolConfig{Tools: []ToolRef{
		{Cmd: "tea", Use: "gitea CLI"},
		{Cmd: "gh", Avoid: "use tea here"},
	}}
	agent := &Agent{Tools: []ToolRef{
		{Cmd: "tea", Use: "gitea CLI — pinned"}, // override
		{Cmd: "myx", Use: "bespoke deploy"},     // add
	}}
	eff := EffectiveTools(pool, agent)
	if len(eff) != 3 {
		t.Fatalf("want 3 effective tools, got %d: %+v", len(eff), eff)
	}
	byCmd := map[string]ToolRef{}
	for _, tr := range eff {
		byCmd[tr.Cmd] = tr
	}
	if byCmd["tea"].Use != "gitea CLI — pinned" {
		t.Fatalf("agent should override pool tea: %q", byCmd["tea"].Use)
	}
	if byCmd["gh"].Avoid == "" || byCmd["myx"].Use == "" {
		t.Fatal("pool avoid + agent-only tool must survive the merge")
	}
}

func TestToolsMarkdown(t *testing.T) {
	md := ToolsMarkdown([]ToolRef{
		{Cmd: "tea", Use: "gitea CLI"},
		{Cmd: "gh", Avoid: "use tea here"},
		{Cmd: "myx", Use: "deploy", Help: "myx help"},
	})
	if !strings.Contains(md, "`tea` — gitea CLI (`tea --help`)") {
		t.Fatalf("default help wrong: %s", md)
	}
	if !strings.Contains(md, "avoid `gh` — use tea here") {
		t.Fatalf("avoid line wrong: %s", md)
	}
	if !strings.Contains(md, "(`myx help`)") {
		t.Fatalf("explicit help wrong: %s", md)
	}
	if ToolsMarkdown(nil) != "" {
		t.Fatal("empty tools ⇒ empty markdown")
	}
}

func TestLoadPoolConfig_Tools(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, PoolMarkerDir), 0o755)
	os.WriteFile(PoolMarkerPath(root), []byte(`name: p
created: 2026-07-25T00:00:00Z
doctrine_version: comms-1b
tools:
  - { cmd: tea, use: "Gitea CLI — prefer over gh", help: "tea --help" }
  - { cmd: gh, avoid: "use tea in this mesh" }
`), 0o644)
	c := LoadPoolConfig(root)
	if c.Name != "p" || len(c.Tools) != 2 || c.Tools[0].Cmd != "tea" || c.Tools[1].Avoid == "" {
		t.Fatalf("pool.yaml tools not parsed: %+v", c)
	}
}

func TestHookSessionStart_InjectsTools(t *testing.T) {
	dir := meshRoot(t)
	// Mesh-wide tool ref in the marker.
	os.WriteFile(PoolMarkerPath(dir), []byte("name: t\ntools:\n  - { cmd: tea, use: \"Gitea CLI\" }\n"), 0o644)
	a := newTestAgent(t, dir, "architect", true, nil)
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now())
	if !strings.Contains(ctx, "Tools for this mesh") || !strings.Contains(ctx, "`tea`") {
		t.Fatalf("session-start should inject the tool reference, got: %q", ctx)
	}
}
