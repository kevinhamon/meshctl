package mesh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHook_SessionStart_PresenceAndHandoff(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	if err := HandoffSet(a, "current focus: wiring hooks\nnext: test", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := HookPayload{Cwd: a.Dir(), SessionID: "sessX"}
	ctx := HookSessionStart(dir, p, 123, now)
	if !strings.Contains(ctx, "Session handoff") || !strings.Contains(ctx, "wiring hooks") {
		t.Fatalf("expected handoff block, got %q", ctx)
	}
	// Presence must be registered.
	if len(SessionList(a, now)) != 1 {
		t.Fatalf("expected 1 live session after session-start hook")
	}
	// Session-end hook deregisters.
	HookSessionEnd(dir, p, 123, now)
	if len(SessionList(a, now)) != 0 {
		t.Fatalf("expected 0 live sessions after session-end hook")
	}
}

func TestHook_SessionStart_NoHandoffNoOutput(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	// Give it a remote so the ADR-0039 repo-hygiene nag is silent — isolating the
	// "no handoff ⇒ no injection" behavior under test.
	_ = exec.Command("git", "-C", a.Dir(), "remote", "add", "origin", "https://example.test/x.git").Run()
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now())
	if ctx != "" {
		t.Fatalf("expected empty context for a fresh agent with no handoff, got %q", ctx)
	}
}

func TestHook_SessionStart_RepoNagUntilRemote(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	// Fresh agent: repo exists (New inits it) but no remote → nag fires.
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now()); !strings.Contains(ctx, "no git remote") {
		t.Fatalf("expected a no-remote nag, got %q", ctx)
	}
	// Add a remote → nag clears.
	_ = exec.Command("git", "-C", a.Dir(), "remote", "add", "origin", "https://example.test/x.git").Run()
	if ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s2"}, 1, time.Now()); strings.Contains(ctx, "no git remote") {
		t.Fatalf("nag should clear once a remote exists, got %q", ctx)
	}
}

func TestHook_SessionStart_UnknownAgent(t *testing.T) {
	dir := meshRoot(t)
	// cwd basename is not a mesh agent → fail-open, empty.
	ctx := HookSessionStart(dir, HookPayload{Cwd: filepath.Join(dir, "not-an-agent"), SessionID: "s"}, 1, time.Now())
	if ctx != "" {
		t.Fatalf("expected empty context for unknown agent, got %q", ctx)
	}
}

func TestHook_DistillNudge_FireOnceOnWindDown(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "pm", true, []string{"tracker"})
	t.Setenv("AGENT_MESH_DISTILL_NUDGE_AGENTS", "pm")
	now := time.Now()

	// Prompt 1: below MIN_PROMPTS even with a wind-down phrase ⇒ no nudge.
	p := HookPayload{Cwd: a.Dir(), SessionID: "s1", Prompt: "that's all, thanks"}
	if got := HookUserPrompt(dir, p, now); strings.Contains(got, "work-boundary") {
		t.Fatalf("nudge fired on first prompt: %q", got)
	}
	// Prompt 2: wind-down + count>=MIN ⇒ fires.
	got := HookUserPrompt(dir, p, now)
	if !strings.Contains(got, "work-boundary detected") || !strings.Contains(got, "distiller") {
		t.Fatalf("expected nudge on wind-down, got %q", got)
	}
	// Prompt 3: already nudged ⇒ silent.
	if again := HookUserPrompt(dir, p, now); strings.Contains(again, "work-boundary") {
		t.Fatalf("nudge fired twice in one session: %q", again)
	}
}

func TestHook_DistillNudge_AllowlistGate(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	t.Setenv("AGENT_MESH_DISTILL_NUDGE_AGENTS", "pm") // architect not on list
	p := HookPayload{Cwd: a.Dir(), SessionID: "s", Prompt: "that's all, we're done for now"}
	for i := 0; i < 10; i++ {
		if got := HookUserPrompt(dir, p, time.Now()); strings.Contains(got, "work-boundary") {
			t.Fatalf("nudge fired for non-allowlisted agent: %q", got)
		}
	}
}

func TestHook_DistillNudge_StalenessThreshold(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "pm", true, []string{"tracker"})
	t.Setenv("AGENT_MESH_DISTILL_NUDGE_AGENTS", "pm")
	p := HookPayload{Cwd: a.Dir(), SessionID: "sT", Prompt: "keep working on the thing"} // no wind-down phrase
	fired := ""
	for i := 0; i < distillThreshold; i++ {
		if got := HookUserPrompt(dir, p, time.Now()); strings.Contains(got, "work-boundary") {
			fired = got
			break
		}
	}
	if fired == "" || !strings.Contains(fired, "exchanges into this session") {
		t.Fatalf("expected a staleness nudge at threshold, got %q", fired)
	}
}

func TestEmitHookContext_EmptyIsSilent(t *testing.T) {
	var sb strings.Builder
	if err := EmitHookContext(&sb, "SessionStart", "   "); err != nil {
		t.Fatal(err)
	}
	if sb.Len() != 0 {
		t.Fatalf("empty context must emit nothing, got %q", sb.String())
	}
	sb.Reset()
	if err := EmitHookContext(&sb, "UserPromptSubmit", "hello"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		HSO struct {
			Event string `json:"hookEventName"`
			Ctx   string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("emitted invalid JSON: %v (%q)", err, sb.String())
	}
	if out.HSO.Event != "UserPromptSubmit" || out.HSO.Ctx != "hello" {
		t.Fatalf("unexpected payload: %+v", out)
	}
}

// ensure the .memory state file was actually written (state persistence).
func TestHook_DistillNudge_StatePersisted(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "pm", true, []string{"tracker"})
	t.Setenv("AGENT_MESH_DISTILL_NUDGE_AGENTS", "pm")
	p := HookPayload{Cwd: a.Dir(), SessionID: "sP", Prompt: "x"}
	HookUserPrompt(dir, p, time.Now())
	sp := distillStatePath(a.Dir(), "sP")
	if _, err := os.Stat(sp); err != nil {
		t.Fatalf("expected distill state file at %s: %v", sp, err)
	}
}
