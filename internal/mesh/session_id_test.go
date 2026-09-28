package mesh

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveSessionID_Precedence(t *testing.T) {
	now := time.Now()
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if a, b := ResolveSessionID(1, now), ResolveSessionID(2, now); a == b {
		t.Fatalf("minted ids should differ per process, both %q", a)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "cc-123")
	if got := ResolveSessionID(1, now); got != "cc-123" {
		t.Fatalf("want CLAUDE_CODE_SESSION_ID, got %q", got)
	}

	t.Setenv("CLAUDE_SESSION_ID", "explicit")
	if got := ResolveSessionID(1, now); got != "explicit" {
		t.Fatalf("CLAUDE_SESSION_ID should win, got %q", got)
	}
}

// Claim and release run as separate meshctl processes; under a harness session
// id they must resolve to the same owner so the release is honoured.
func TestClaimRelease_AcrossProcesses(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cc-abc")
	dir := t.TempDir()
	req := filepath.Join(dir, "agent", "intake", "req-1.md")
	if err := os.MkdirAll(filepath.Dir(req), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok := ClaimRequest(req, ResolveSessionID(1001, now), 1001, now); !ok {
		t.Fatal("claim failed")
	}
	if err := ReleaseClaim(req, ResolveSessionID(1002, now.Add(time.Second)), now); err != nil {
		t.Fatalf("release from second process refused: %v", err)
	}
	if _, err := os.Stat(claimPath(req)); !os.IsNotExist(err) {
		t.Fatalf("claim file still present: %v", err)
	}
}

// With no harness id in the environment (opencode), claim/release fall back to
// the sole live registered session so they still agree across processes.
func TestResolveAgentSessionID_SoleLiveSession(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	if a1, a2 := ResolveAgentSessionID(a, 1, now), ResolveAgentSessionID(a, 2, now); a1 == a2 {
		t.Fatalf("no sessions: want minted per-process ids, both %q", a1)
	}
	if _, err := SessionStart(a, "oc-1", 1, now); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentSessionID(a, 2, now); got != "oc-1" {
		t.Fatalf("want sole live session oc-1, got %q", got)
	}
	if _, err := SessionStart(a, "oc-2", 3, now); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentSessionID(a, 2, now); got == "oc-1" || got == "oc-2" {
		t.Fatalf("ambiguous: must not guess a session, got %q", got)
	}
}

// The user-prompt hook heartbeats presence, so a session that outlives the
// stale window keeps its claims.
func TestHookUserPrompt_HeartbeatsPresence(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	start := time.Now()
	HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "long"}, 1, start)
	later := start.Add(time.Duration(staleSec()+60) * time.Second)
	HookUserPrompt(dir, HookPayload{Cwd: a.Dir(), SessionID: "long"}, later)
	if !sessionAlive(a.Dir(), "long", later) {
		t.Fatal("session not alive after user-prompt heartbeat")
	}
}

func TestReleaseClaim_LiveVsAbandoned(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	req, err := WriteRequest(a, "guidance", "x", "tester", "P2", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SessionStart(a, "owner", 1, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClaimRequest(req.path, "owner", 1, now); !ok {
		t.Fatal("claim failed")
	}
	if err := ReleaseClaim(req.path, "other", now); err == nil {
		t.Fatal("released another live session's claim")
	}
	// Owner gone, heartbeat stale ⇒ abandoned ⇒ anyone may release.
	_ = SessionEnd(a, "owner")
	later := now.Add(time.Duration(staleSec()+60) * time.Second)
	if err := ReleaseClaim(req.path, "other", later); err != nil {
		t.Fatalf("abandoned claim not releasable: %v", err)
	}
}

func TestAsk_ReleasesDispatchClaim(t *testing.T) {
	dir := meshRoot(t)
	adv := newTestAgent(t, dir, "architect", true, nil)
	var childSession string
	runner := func(_ context.Context, _, _, _, _ string, env []string, _, _ io.Writer) int {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "CLAUDE_SESSION_ID="); ok {
				childSession = v
			}
		}
		return 0
	}
	req, rc, err := Ask(dir, adv, AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: runner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || rc != 0 {
		t.Fatalf("rc=%d err=%v", rc, err)
	}
	if !strings.HasPrefix(childSession, "dispatch-") {
		t.Fatalf("child not given dispatch session id, got %q", childSession)
	}
	if fileExists(claimPath(req.path)) {
		t.Fatal("dispatch claim left behind")
	}
}
