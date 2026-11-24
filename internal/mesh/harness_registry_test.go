package mesh

import "testing"

func TestHarnessRegistry_BuiltinsAndResolve(t *testing.T) {
	ids := RegisteredHarnessIDs()
	if len(ids) < 2 || ids[0] != HarnessClaude || ids[1] != HarnessOpencode {
		t.Fatalf("registry order = %v, want claude, opencode first", ids)
	}
	if _, err := ResolveHarness("nope", 0); err == nil {
		t.Fatal("unknown harness must be an error at input boundaries")
	}
	if h, err := ResolveHarness("", 0); err != nil || h.Name() != DefaultHarness {
		t.Fatalf("empty ⇒ default, got %v %v", h, err)
	}
	if HarnessByName("nope").Name() != DefaultHarness {
		t.Fatal("HarnessByName keeps the lenient default for unknown ids")
	}
}

func TestHarnessRegistry_Caps(t *testing.T) {
	if c := HarnessByNameVersion(HarnessClaude, 0).Caps(); !c.GateAsk || !c.PreToolGate || !c.InSessionDistill {
		t.Fatalf("claude caps = %+v", c)
	}
	v2 := HarnessByNameVersion(HarnessOpencode, 2).Caps()
	if !v2.PreToolGate || v2.GateAsk || !v2.PromotionAsk || !v2.InSessionPeers {
		t.Fatalf("opencode v2 caps = %+v", v2)
	}
	if HarnessByNameVersion(HarnessOpencode, 1).Caps() != (HarnessCaps{}) {
		t.Fatal("opencode v1 has no gate surfaces")
	}
}

func TestHarnessRegistry_RunningAndColumns(t *testing.T) {
	env := map[string]string{"OPENCODE": "1"}
	if got := runningHarnessIn(func(k string) string { return env[k] }); got != HarnessOpencode {
		t.Fatalf("running = %q", got)
	}
	env["CLAUDECODE"] = "1"
	if got := runningHarnessIn(func(k string) string { return env[k] }); got != HarnessClaude {
		t.Fatalf("registration order breaks ties: got %q", got)
	}
	var keys []string
	for _, c := range HarnessColumns() {
		keys = append(keys, c.Key())
	}
	if len(keys) < 3 || keys[0] != "claude" || keys[1] != "opencode@2" || keys[2] != "opencode@1" {
		t.Fatalf("columns = %v", keys)
	}
	if ToolKindOf("Write") != ToolWrite || ToolKindOf("apply_patch") != ToolPatch || ToolKindOf("Glob") != "" {
		t.Fatal("tool kinds")
	}
}

func TestEnsureHooks_NamesClaudeHarnessAndUpgradesBareCommand(t *testing.T) {
	root := map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": GateHookMatcher, "hooks": []any{
			map[string]any{"type": "command", "command": "meshctl hook pre-tool-use"}}}},
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "MY_ENV=1 meshctl hook user-prompt"}}}},
	}}
	ensureHooks(root)
	hooks := root["hooks"].(map[string]any)
	cmd := func(ev string) string {
		return hooks[ev].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	}
	if got := cmd("PreToolUse"); got != "meshctl hook pre-tool-use --harness claude" {
		t.Fatalf("bare mesh command not upgraded: %q", got)
	}
	if got := cmd("UserPromptSubmit"); got != "MY_ENV=1 meshctl hook user-prompt" {
		t.Fatalf("hand-edited command must be left alone: %q", got)
	}
	if got := cmd("SessionEnd"); got != "meshctl hook session-end --harness claude" {
		t.Fatalf("new hook must name its harness: %q", got)
	}
	if len(ensureHooks(root)) != 0 {
		t.Fatal("second run must be a no-op")
	}
}
