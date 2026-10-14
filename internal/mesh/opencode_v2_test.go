package mesh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencode_ParseMajor(t *testing.T) {
	for in, want := range map[string]int{
		"1.18.35\n":          1,
		"opencode v2.0.28\n": 2,
		"v3.1":               3,
		"garbage":            0,
		"":                   0,
	} {
		if got := parseOpencodeMajor(in); got != want {
			t.Errorf("parseOpencodeMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestOpencode_MajorEnvOverride(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "1")
	if InstalledOpencodeMajor() != 1 || (opencodeHarness{}).Major() != 1 {
		t.Fatal("env override must win")
	}
	if (opencodeHarness{major: 2}).Major() != 2 {
		t.Fatal("a pinned major must win over detection")
	}
}

func TestOpencode_V2Wiring(t *testing.T) {
	dest := t.TempDir()
	h := opencodeHarness{major: 2}
	if _, err := h.WireAgent(dest); err != nil {
		t.Fatal(err)
	}
	pb, err := os.ReadFile(filepath.Join(dest, ".opencode", "plugins", "meshctl.js"))
	if err != nil {
		t.Fatal("V2 plugin not written to .opencode/plugins/")
	}
	for _, want := range []string{opencodePluginMarker, "export default", `id: "meshctl"`,
		`hook("context"`, `hook("execute.before"`, "pre-tool-use", "CLAUDE.md", "session.execution.succeeded"} {
		if !strings.Contains(string(pb), want) {
			t.Fatalf("V2 plugin missing %q", want)
		}
	}
	if fileExists(filepath.Join(dest, ".opencode", "plugin", "meshctl.js")) {
		t.Fatal("V2 wiring must not write the V1 plugin")
	}
	if notes, _ := h.WireAgent(dest); len(notes) != 0 {
		t.Fatalf("V2 wiring not idempotent: %v", notes)
	}
}

func TestOpencode_UpgradeAndDowngradeSwapPlugins(t *testing.T) {
	dest := t.TempDir()
	v1 := filepath.Join(dest, ".opencode", "plugin", "meshctl.js")
	v2 := filepath.Join(dest, ".opencode", "plugins", "meshctl.js")

	opencodeHarness{major: 1}.WireAgent(dest)
	if !fileExists(v1) {
		t.Fatal("V1 plugin not written")
	}
	notes, err := opencodeHarness{major: 2}.WireAgent(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(v2) || fileExists(v1) {
		t.Fatalf("upgrade must write V2 and remove meshctl's V1 plugin: %v", notes)
	}
	opencodeHarness{major: 1}.WireAgent(dest)
	if !fileExists(v1) || fileExists(v2) {
		t.Fatal("downgrade must write V1 and remove meshctl's V2 plugin")
	}
}

func TestOpencode_ForeignPluginsLeftAlone(t *testing.T) {
	dest := t.TempDir()
	v1 := filepath.Join(dest, ".opencode", "plugin", "meshctl.js")
	v2 := filepath.Join(dest, ".opencode", "plugins", "meshctl.js")
	for _, p := range []string{v1, v2} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("// mine\n"), 0o644)
	}
	opencodeHarness{major: 2}.WireAgent(dest)
	for _, p := range []string{v1, v2} {
		if b, _ := os.ReadFile(p); string(b) != "// mine\n" {
			t.Fatalf("foreign plugin at %s was touched", p)
		}
	}
}

func TestOpencode_DistillCmdPerMajor(t *testing.T) {
	v1 := opencodeHarness{major: 1}.DistillCmd("P")
	if strings.Join(v1, " ") != "opencode run P" {
		t.Fatalf("V1 distill argv: %v", v1)
	}
	v2 := opencodeHarness{major: 2}.DistillCmd("P")
	if !containsArg(v2, "--standalone") || !containsArg(v2, "--auto") || v2[len(v2)-1] != "P" {
		t.Fatalf("V2 distill argv: %v", v2)
	}
}

func TestOpencode_EmitGate(t *testing.T) {
	var b strings.Builder
	opencodeHarness{}.EmitGate(&b, GateDecision{})
	if b.Len() != 0 {
		t.Fatal("allow must be silent")
	}
	opencodeHarness{}.EmitGate(&b, GateDecision{Deny: true, Reason: "no"})
	if strings.TrimSpace(b.String()) != "no" || strings.Contains(b.String(), "hookSpecificOutput") {
		t.Fatalf("opencode gate must emit the raw reason, got %q", b.String())
	}
}

func ocPayload(cwd, tool string, input any) HookPayload {
	raw, _ := json.Marshal(input)
	return HookPayload{Cwd: cwd, SessionID: "s1", ToolName: tool, ToolInput: raw}
}

func TestGate_OpencodeShapes(t *testing.T) {
	ws := "/abs/agent"
	cases := []struct {
		name string
		p    HookPayload
		deny bool
	}{
		{"V2 write relative intake", ocPayload(ws, "write", map[string]string{"path": "intake/x.md", "content": "c"}), true},
		{"V2 edit abs intake", ocPayload(ws, "edit", map[string]string{"path": ws + "/intake/x.md"}), true},
		{"V1 write filePath", ocPayload(ws, "write", map[string]string{"filePath": ws + "/intake/x.md"}), true},
		{"claude memory store", ocPayload(ws, "write", map[string]string{"path": "/home/u/.claude/projects/p/memory/m.md"}), true},
		{"V2 write elsewhere", ocPayload(ws, "write", map[string]string{"path": "src/main.go"}), false},
		{"apply_patch into intake", ocPayload(ws, "apply_patch", map[string]string{
			"patchText": "*** Begin Patch\n*** Add File: intake/y.md\n+hi\n*** End Patch"}), true},
		{"apply_patch elsewhere", ocPayload(ws, "apply_patch", map[string]string{
			"patchText": "*** Begin Patch\n*** Update File: src/a.go\n*** End Patch"}), false},
		{"read intake is fine", ocPayload(ws, "read", map[string]string{"path": "intake/x.md"}), false},
		{"opencode claude-mem MCP write", ocPayload(ws, "claude-mem_memory_add", map[string]string{}), true},
		{"opencode claude-mem MCP read", ocPayload(ws, "claude-mem_search", map[string]string{}), false},
		{"claude plugin claude-mem write", ocPayload(ws, "mcp__plugin_claude-mem_mcp-search__memory_add", map[string]string{}), true},
	}
	for _, c := range cases {
		if got := GatePreToolUse("", c.p); got.Deny != c.deny {
			t.Errorf("%s: deny=%v want %v (%s)", c.name, got.Deny, c.deny, got.Reason)
		}
	}
}

// Both embedded plugins must at least parse as ES modules; a syntax error would
// otherwise only surface as a silent plugin-load warning inside opencode.
func TestOpencode_PluginsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"v1.mjs": opencodePluginJS, "v2.mjs": opencodePluginV2JS} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		if out, err := exec.Command(node, "--check", p).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, out)
		}
	}
}

func TestDoctor_OpencodeGateFindings(t *testing.T) {
	newAgent := func() *Agent { return &Agent{Name: "oc", dir: t.TempDir(), Harness: HarnessOpencode} }

	t.Run("V2 installed, V1 wiring ⇒ error", func(t *testing.T) {
		t.Setenv(opencodeMajorEnv, "2")
		a := newAgent()
		opencodeHarness{major: 1}.WireAgent(a.Dir())
		fs := opencodeGateFindings(a)
		if !hasFinding(fs, "wired for opencode V1 but opencode 2.x") || fs[0].Level != "error" {
			t.Fatalf("want V1-on-V2 error, got %+v", fs)
		}
	})
	t.Run("V2 installed, V2 wiring ⇒ clean", func(t *testing.T) {
		t.Setenv(opencodeMajorEnv, "2")
		a := newAgent()
		opencodeHarness{major: 2}.WireAgent(a.Dir())
		if fs := opencodeGateFindings(a); len(fs) != 0 {
			t.Fatalf("want clean, got %+v", fs)
		}
		if fs := opencodeInstructionsFindings(a); len(fs) != 0 {
			t.Fatalf("V2 must not require instructions ⊇ CLAUDE.md, got %+v", fs)
		}
	})
	t.Run("V2 installed, unwired ⇒ error", func(t *testing.T) {
		t.Setenv(opencodeMajorEnv, "2")
		if fs := opencodeGateFindings(newAgent()); !hasFinding(fs, "mesh plugin not installed") {
			t.Fatalf("want missing-plugin error, got %+v", fs)
		}
	})
	t.Run("V1 installed ⇒ advisory-only warning", func(t *testing.T) {
		t.Setenv(opencodeMajorEnv, "1")
		a := newAgent()
		opencodeHarness{major: 1}.WireAgent(a.Dir())
		fs := opencodeGateFindings(a)
		if len(fs) != 1 || fs[0].Level != "warn" || !hasFinding(fs, "enforcement gate is unavailable") {
			t.Fatalf("want single V1 gate warning, got %+v", fs)
		}
	})
	t.Run("V1 installed, V2 wiring ⇒ error", func(t *testing.T) {
		t.Setenv(opencodeMajorEnv, "1")
		a := newAgent()
		opencodeHarness{major: 2}.WireAgent(a.Dir())
		if fs := opencodeGateFindings(a); !hasFinding(fs, "wired for opencode V2 but opencode 1.x") {
			t.Fatalf("want V2-on-V1 error, got %+v", fs)
		}
	})
}
