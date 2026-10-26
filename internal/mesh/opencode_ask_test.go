package mesh

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// opencode_ask_test.go — the opencode V2 ask path for promotions (ADR-0075).

func writeOpencodeJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setHarness rewrites the agent's manifest harness and reloads it.
func setHarness(t *testing.T, dir string, a *Agent, harness string) *Agent {
	t.Helper()
	p := ManifestPath(filepath.Dir(a.Dir()), a.Name)
	b, _ := os.ReadFile(p)
	if regexp.MustCompile(`(?m)^harness:`).Match(b) {
		b = regexp.MustCompile(`(?m)^harness:.*$`).ReplaceAll(b, []byte("harness: "+harness))
	} else {
		b = append(b, []byte("harness: "+harness+"\n")...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := FindAgent(dir, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOcPerms_LastMatchWinsEnvOverridesV1AllowWins(t *testing.T) {
	dir := t.TempDir()
	cmd := "meshctl kb finish --summary-file s.md"
	if got := readOcPerms(dir, true).shellEffect(cmd); got != "allow" {
		t.Fatalf("no rule: opencode allows a shell call, got %q", got)
	}
	writeOpencodeJSON(t, dir, `{"permissions":[
	  {"action":"shell","resource":"meshctl kb finish *--summary-file*","effect":"ask"},
	  {"action":"shell","resource":"*","effect":"allow"}]}`)
	if got := readOcPerms(dir, true).shellEffect(cmd); got != "allow" {
		t.Fatalf("a later allow * wins, got %q", got)
	}
	writeOpencodeJSON(t, dir, `{"permissions":[
	  {"action":"shell","resource":"*","effect":"allow"},
	  {"action":"shell","resource":"meshctl kb finish *--summary-file*","effect":"ask"}]}`)
	ps := readOcPerms(dir, true)
	if got := ps.shellEffect(cmd); got != "ask" {
		t.Fatalf("the last matching rule asks, got %q", got)
	}
	if got := ps.shellEffect("meshctl kb finish"); got != "allow" {
		t.Fatalf("a candidates-only finish is not asked, got %q", got)
	}
	if got := ps.shellEffect("meshctl kb finish --agent a --summary-file s.md"); got != "ask" {
		t.Fatalf("`*` spans flags, got %q", got)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permissions":[{"action":"*","resource":"*","effect":"allow"}]}`)
	if got := readOcPerms(dir, true).shellEffect(cmd); got != "allow" {
		t.Fatalf("OPENCODE_CONFIG_CONTENT overrides the project, got %q", got)
	}
	if got := readOcPerms(dir, false).shellEffect(cmd); got != "ask" {
		t.Fatalf("without the env the project rule stands, got %q", got)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permission":{"bash":{"meshctl*":"allow"}}}`)
	if got := readOcPerms(dir, true).shellEffect(cmd); got != "allow" {
		t.Fatalf("a V1-form allow wins whatever its order, got %q", got)
	}
}

func TestEnsureOpencodeAskRules_AppendedLastAndIdempotent(t *testing.T) {
	root := map[string]any{"permissions": []any{
		map[string]any{"action": "shell", "resource": "*", "effect": "allow"},
	}}
	if added := ensureOpencodeAskRules(root); len(added) != len(opencodeAskRules) {
		t.Fatalf("added %v", added)
	}
	b, _ := json.Marshal(root)
	dir := t.TempDir()
	writeOpencodeJSON(t, dir, string(b))
	if !opencodeAskWired(dir, false) {
		t.Fatalf("rules appended after a user allow-all must win: %s", b)
	}
	if added := ensureOpencodeAskRules(root); len(added) != 0 {
		t.Fatalf("second run added %v", added)
	}
}

func TestEnsureOpencodeConfig_AskRulesOnV2Only(t *testing.T) {
	v1, v2 := t.TempDir(), t.TempDir()
	if _, err := ensureOpencodeConfig(v1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureOpencodeConfig(v2, 2); err != nil {
		t.Fatal(err)
	}
	if opencodeAskWired(v1, false) {
		t.Error("V1 has no gate to hand the ask to; no rules")
	}
	if !opencodeAskWired(v2, false) {
		t.Error("V2 wiring must carry the ask rules")
	}
}

func opencodePayload(a *Agent, command string) HookPayload {
	p := bash(a, "sess-1", command)
	p.ToolName = "shell"
	p.Harness = HarnessOpencode
	return p
}

func TestGate_OpencodePromotionGoesToOpencodesAsk(t *testing.T) {
	dir, a, wt := promotionFixture(t)
	commitKB(t, wt, "playbooks/release.md")
	sum := writeSummary(t, a, "sess-1", promotionSummary)
	plain := "meshctl kb finish --summary-file " + sum

	if d := GatePreToolUse(dir, bash(a, "sess-1", plain)); !d.Ask {
		t.Fatalf("Claude Code keeps the gate's own ask, got %+v", d)
	}
	d := GatePreToolUse(dir, opencodePayload(a, plain))
	if !d.Deny || !strings.Contains(d.Reason, "opencode") || !strings.Contains(d.Reason, "meshctl agent onboard") {
		t.Fatalf("no ask rule: deny naming the fix, got %+v", d)
	}

	if _, err := ensureOpencodeConfig(a.Dir(), 2); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{plain, "cd " + a.Dir() + " && " + plain} {
		if d := GatePreToolUse(dir, opencodePayload(a, ok)); d.Deny || d.Ask || d.Check != GateCheckPromotion {
			t.Errorf("%q: opencode asks, so the gate lets it through; got %+v", ok, d)
		}
	}
	for _, bad := range []string{
		"env X=1 " + plain,
		`meshctl "kb" finish --summary-file ` + sum,
		`bash -c "` + plain + `"`,
		"/usr/local/bin/" + plain,
	} {
		if d := GatePreToolUse(dir, opencodePayload(a, bad)); !d.Deny {
			t.Errorf("%q: opencode would not ask on this form; got %+v", bad, d)
		}
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permissions":[{"action":"shell","resource":"*","effect":"allow"}]}`)
	if d := GatePreToolUse(dir, opencodePayload(a, plain)); !d.Deny {
		t.Fatalf("an injected allow-all overrides the ask rule: deny, got %+v", d)
	}
}

func TestKBTrustGuard_OpencodeV2AndRunningHarness(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)

	if err := KBTrustGuard(a); err != nil {
		t.Fatalf("wired Claude Code agent from a terminal: %v", err)
	}
	t.Setenv("OPENCODE", "1")
	if err := KBTrustGuard(a); !errors.Is(err, ErrKBTrustGuard) || !strings.Contains(err.Error(), "runs under opencode") {
		t.Fatalf("opencode running in a Claude-wired workspace: %v", err)
	}
	t.Setenv("OPENCODE", "")

	a = setHarness(t, dir, a, HarnessOpencode)
	if err := KBTrustGuard(a); !errors.Is(err, ErrKBTrustGuard) {
		t.Fatalf("opencode agent with no V2 plugin: %v", err)
	}
	if _, err := HarnessFor(a).WireAgent(a.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := KBTrustGuard(a); err != nil {
		t.Fatalf("opencode V2 agent with plugin and ask rules: %v", err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permissions":[{"action":"shell","resource":"*","effect":"allow"}]}`)
	if err := KBTrustGuard(a); !errors.Is(err, ErrKBTrustGuard) {
		t.Fatalf("ask rules overridden by the env: %v", err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("CLAUDECODE", "1")
	if err := KBTrustGuard(a); !errors.Is(err, ErrKBTrustGuard) || !strings.Contains(err.Error(), "runs under claude") {
		t.Fatalf("Claude Code running in an opencode workspace: %v", err)
	}
}

// The regression this ADR fixes: an opencode V2 agent can promote again.
func TestKBFinish_OpencodeV2AgentPromotes(t *testing.T) {
	t.Setenv(opencodeMajorEnv, "2")
	t.Setenv("OPENCODE", "1")
	dir, a, wt := finishFixture(t)
	a = setHarness(t, dir, a, HarnessOpencode)
	if _, err := HarnessFor(a).WireAgent(a.Dir()); err != nil {
		t.Fatal(err)
	}
	writeWT(t, wt, "knowledge/playbooks/release.md", "# Release\n\nTag then push.\n")
	res, err := KBFinish(dir, a, "sess-1", KBFinishOptions{SummaryFile: writeFinishSummary(t, a, "sess-1", "Record the release playbook."), Now: finishNow})
	if err != nil {
		t.Fatalf("opencode V2 promotion refused: %v", err)
	}
	if len(res.Promoted) == 0 {
		t.Fatalf("nothing promoted: %+v", res)
	}
}
