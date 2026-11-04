package mesh

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

const grantsWithAllow = `outbound:
  tracker:
    tools: ['create.*']
    allow:
      claude: ["mcp__tracker__create", "Bash(trk create:*)"]
      opencode: { bash: { "trk create*": "allow" } }
grants:
  peers:
    pm:
      kb-promote: {}
      state-set: {}
      "outbound:tracker": {}
`

// captureRunner records what Ask hands the runner.
type captured struct {
	sys string
	env []string
}

func (c *captured) run(_ context.Context, _, _, sys, _ string, env []string, _, _ io.Writer) int {
	c.sys, c.env = sys, env
	return 0
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func TestDispatchGrantTools_NoGrantsLeavesChildUnchanged(t *testing.T) {
	root := migratedRoot(t)
	adv := newTestAgent(t, root, "arch", true, nil)
	var c captured
	o := askOpts()
	o.Runner = c.run
	if _, rc, err := Ask(root, adv, o); err != nil || rc != 0 {
		t.Fatalf("ask: rc=%d err=%v", rc, err)
	}
	if !strings.Contains(c.sys, "Do not commit, push, deploy, or touch external systems.") {
		t.Fatalf("prompt changed with no grants:\n%s", c.sys)
	}
	if got := envValue(c.env, "OPENCODE_CONFIG_CONTENT"); got != opencodeDispatchConfig {
		t.Fatalf("opencode config changed with no grants: %s", got)
	}
	if envValue(c.env, dispatchToolsEnv) != "" {
		t.Fatal("no extra tools without grants")
	}
	_, args := dispatchArgv(HarnessClaude, 0, "m", "s", "", dispatchToolsFromEnv(c.env)...)
	_, base := dispatchArgv(HarnessClaude, 0, "m", "s", "")
	if strings.Join(args, " ") != strings.Join(base, " ") {
		t.Fatalf("argv changed with no grants:\n%v\n%v", args, base)
	}
}

func TestDispatchGrantTools_GrantedPeerGetsItsTools(t *testing.T) {
	root := migratedRoot(t)
	pm := grantedPeer(t, root, "pm", "tracker")
	writeGrantsConfig(t, root, grantsWithAllow)
	var c captured
	o := askOpts()
	o.Runner = c.run
	if _, rc, err := Ask(root, pm, o); err != nil || rc != 0 {
		t.Fatalf("ask: rc=%d err=%v", rc, err)
	}
	// Prompt: the external-systems ban is replaced by the granted actions.
	if strings.Contains(c.sys, "touch external systems.") {
		t.Fatalf("ban kept despite grants:\n%s", c.sys)
	}
	for _, want := range []string{"Touch nothing outside the granted actions", "`kb-promote`", "`state-set`", "`outbound:tracker`", "respond `needs-human`"} {
		if !strings.Contains(c.sys, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// Claude: meshctl verbs, worktree and summary edits, surface allow.
	tools := dispatchToolsFromEnv(c.env)
	joined := strings.Join(tools, "\n")
	for _, want := range []string{"Bash(meshctl kb begin:*)", "Bash(meshctl kb finish:*)", "Bash(meshctl state set:*)", "mcp__tracker__create", "Bash(trk create:*)", "Edit(//"} {
		if !strings.Contains(joined, want) {
			t.Errorf("claude tools missing %q:\n%s", want, joined)
		}
	}
	for _, bad := range []string{"Bash(meshctl kb confirm:*)", "Bash(meshctl inbox triage:*)", "\nEdit\n", "\nWrite\n", "Write(/"} {
		if strings.Contains("\n"+joined+"\n", bad) {
			t.Errorf("ungranted tool %q given:\n%s", bad, joined)
		}
	}
	_, args := dispatchArgv(HarnessClaude, 0, "m", "s", "", tools...)
	if !containsArg(args, "Bash(meshctl kb finish:*)") || args[len(args)-2] != "--append-system-prompt" {
		t.Fatalf("argv: %v", args)
	}
	// opencode: bash verbs + surface pattern merged; edits limited to relative paths.
	var cfg struct {
		Permission struct {
			Edit any               `json:"edit"`
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	if err := json.Unmarshal([]byte(envValue(c.env, "OPENCODE_CONFIG_CONTENT")), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Permission.Bash["*"] != "deny" || cfg.Permission.Bash["meshctl kb finish*"] != "allow" || cfg.Permission.Bash["trk create*"] != "allow" || cfg.Permission.Bash["meshctl inbox respond*"] != "allow" {
		t.Fatalf("opencode bash: %v", cfg.Permission.Bash)
	}
	edit, ok := cfg.Permission.Edit.(map[string]any)
	if !ok || edit["*"] != "deny" || len(edit) < 3 {
		t.Fatalf("opencode edit: %v", cfg.Permission.Edit)
	}
	for p := range edit {
		if strings.HasPrefix(p, "/") {
			t.Errorf("opencode edit pattern must be relative to the workspace: %q", p)
		}
	}
}

func TestDispatchGrantTools_PausedGrantsAddNothing(t *testing.T) {
	root := migratedRoot(t)
	pm := grantedPeer(t, root, "pm", "tracker")
	writeGrantsConfig(t, root, strings.Replace(grantsWithAllow, "grants:\n", "grants:\n  paused: true\n", 1))
	g := dispatchGrants(root, pm, "s1", time.Now())
	if !g.empty() || len(g.Claude) != 0 {
		t.Fatalf("paused grants must add nothing: %+v", g)
	}
}

func TestGrantFindings_WarnsGrantedSurfaceWithoutAllow(t *testing.T) {
	root := migratedRoot(t)
	pm := grantedPeer(t, root, "pm", "tracker", "code")
	writeGrantsConfig(t, root, grantsTwoSurfaces)
	cfg := LoadPoolConfig(root)
	var msgs []string
	for _, f := range grantFindings(root, cfg, []*Agent{pm}) {
		msgs = append(msgs, f.Message)
	}
	all := strings.Join(msgs, "\n")
	if !strings.Contains(all, "`outbound:tracker` but outbound surface \"tracker\" has no `allow: { claude: … }`") {
		t.Fatalf("no allow warning:\n%s", all)
	}
}
