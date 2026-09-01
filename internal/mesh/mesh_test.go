package mesh

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestAgent scaffolds a minimal agent workspace under dir and returns it.
func newTestAgent(t *testing.T, dir, name string, dispatchable bool, mutates []string) *Agent {
	t.Helper()
	spec := AgentSpec{
		Name: name, Title: name + " title", Role: "role",
		Accepts:      []Accept{{Type: "guidance", Desc: "advice"}, {Type: "fyi", Desc: "x"}},
		Dispatchable: dispatchable, Mutates: mutates,
		Badge: Badge{Label: name, R: 0.1, G: 0.2, B: 0.3},
		Bare:  true, // tests use exact names; skip the -agent repo suffix
	}
	if _, err := New(dir, spec, BgOptions{}); err != nil {
		t.Fatalf("scaffold %s: %v", name, err)
	}
	a, err := FindAgent(dir, name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return a
}

func meshRoot(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(filepath.Join(dir, "agent-mesh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-mesh", CommsFile), []byte("protocol"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Current-layout pool marker (ADR-0031/0038) so IsPool + .agentmesh/ parent exist.
	if err := os.MkdirAll(filepath.Join(dir, PoolMarkerDir), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func okRunner(_ context.Context, _, _, _, _ string, _ []string, _, _ io.Writer) int { return 0 }

func TestInvariant_MutatesForcesNotDispatchable(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "pm", true, []string{"tracker"})
	if a.IsDispatchable() {
		t.Fatal("agent with mutates must not be dispatchable")
	}
	// Manifest was written with dispatchable coerced to false, so no Validate error.
	if probs := a.Validate(); len(probs) != 0 {
		t.Fatalf("expected clean manifest, got %v", probs)
	}
	// A hand-authored conflicting manifest must fail Validate.
	bad := filepath.Join(dir, "pm", ManifestName)
	os.WriteFile(bad, []byte("name: pm\ntitle: PM\naccepts:\n  - {type: x}\ndispatchable: true\nmutates: [tracker]\nbadge: {label: PM}\n"), 0o644)
	a2, _ := LoadAgent(bad)
	probs := a2.Validate()
	found := false
	for _, p := range probs {
		if contains(p, "conflicts with mutates") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected invariant violation, got %v", probs)
	}
}

func TestAsk_RefusesNonDispatchable(t *testing.T) {
	dir := meshRoot(t)
	pm := newTestAgent(t, dir, "pm", true, []string{"tracker"})
	_, rc, err := Ask(dir, pm, AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: okRunner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || rc == 0 {
		t.Fatalf("expected refusal, got rc=%d err=%v", rc, err)
	}
}

func TestAsk_DepthGuard(t *testing.T) {
	dir := meshRoot(t)
	adv := newTestAgent(t, dir, "architect", true, nil)
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	_, rc, _ := Ask(dir, adv, AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: okRunner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard})
	if rc != ExitDepth {
		t.Fatalf("depth guard: want rc=%d got %d", ExitDepth, rc)
	}
}

func TestAsk_HourlyCap(t *testing.T) {
	dir := meshRoot(t)
	adv := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	// Prefill the count with hourlyMax recent epochs.
	var lines string
	for i := 0; i < hourlyMax(); i++ {
		lines += itoa(now.Unix()-int64(i)) + "\n"
	}
	os.WriteFile(filepath.Join(dir, DispatchCount), []byte(lines), 0o644)
	_, rc, _ := Ask(dir, adv, AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: okRunner, Now: now, Stdout: io.Discard, Stderr: io.Discard})
	if rc != ExitHourlyCap {
		t.Fatalf("hourly cap: want rc=%d got %d", ExitHourlyCap, rc)
	}
}

func TestAsk_LockHeld(t *testing.T) {
	// A caller now QUEUES behind an in-flight dispatch; this exercises the
	// timeout at the end of that wait, so the wait is shortened to keep the
	// test fast.
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "1")
	dir := meshRoot(t)
	adv := newTestAgent(t, dir, "architect", true, nil)
	// Pre-create a fresh (non-stale) lock.
	if err := os.Mkdir(filepath.Join(dir, DispatchLock), 0o755); err != nil {
		t.Fatal(err)
	}
	_, rc, err := Ask(dir, adv, AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: okRunner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard})
	if rc != ExitLockHeld {
		t.Fatalf("lock guard: want rc=%d got %d", ExitLockHeld, rc)
	}
	// The wording is load-bearing: a model that reads this as "peer does not
	// exist" proceeds without the consult, which is the failure this path was
	// rewritten to prevent.
	msg := err.Error()
	for _, want := range []string{"BUSY", "reachable", "NOT an unavailable peer"} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout message missing %q — it must not read as an absent peer\n%s", want, msg)
		}
	}
}

func TestAsk_HappyPathReleasesLock(t *testing.T) {
	dir := meshRoot(t)
	adv := newTestAgent(t, dir, "architect", true, nil)
	req, rc, err := Ask(dir, adv, AskOptions{Type: "guidance", Prompt: "please advise", From: "tester", Runner: okRunner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || rc != 0 {
		t.Fatalf("happy path: rc=%d err=%v", rc, err)
	}
	if !fileExists(req.path) {
		t.Fatal("intake file not written")
	}
	if dirExists(filepath.Join(dir, DispatchLock)) {
		t.Fatal("lock not released after dispatch")
	}
}

func TestClaim_FirstWriterWins(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	req, err := WriteRequest(a, "guidance", "do a thing", "tester", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, ok1 := ClaimRequest(req.path, "sessA", 1, now)
	_, ok2 := ClaimRequest(req.path, "sessB", 2, now)
	if !ok1 || ok2 {
		t.Fatalf("expected only first claimer to win, got ok1=%v ok2=%v", ok1, ok2)
	}
}

func TestInboxNext_NoDoubleReturn(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	// Two distinct pending requests.
	if _, err := WriteRequest(a, "guidance", "first", "u", "P2", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // ensure distinct filename slug/timestamp
	if _, err := WriteRequest(a, "guidance", "second", "u", "P2", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var mu sync.Mutex
	got := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r, _ := InboxNext(a, "sess"+itoa(int64(n)), n, true, now)
			if r != nil {
				mu.Lock()
				got[r.ID]++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for id, c := range got {
		if c > 1 {
			t.Fatalf("request %s returned to %d callers", id, c)
		}
	}
}

func TestClaim_StaleReclaimRespectsLiveSession(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	req, _ := WriteRequest(a, "guidance", "x", "u", "P2", nil, time.Now())
	base := time.Now()

	// Owner claims, then goes stale.
	ClaimRequest(req.path, "owner", 99, base)
	stale := base.Add(time.Duration(staleSec()+10) * time.Second)

	// Case 1: owner session still heartbeating → not reclaimable.
	SessionStart(a, "owner", 99, stale) // fresh heartbeat at `stale`
	if _, ok := ClaimRequest(req.path, "thief", 5, stale); ok {
		t.Fatal("reclaimed a claim whose session still heartbeats")
	}

	// Case 2: owner session gone → stale claim reclaimable.
	SessionEnd(a, "owner")
	if _, ok := ClaimRequest(req.path, "thief", 5, stale); !ok {
		t.Fatal("failed to reclaim a stale claim from a dead session")
	}
}

// tiny helpers to avoid strconv import churn in tests
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var _ = errors.Is

// TestMemory_RebuildRecallRecovery covers ADR-0015: structural rebuild, recall
// routing, and the recovery property (delete + rebuild reconstructs the index).
func TestMemory_RebuildRecallRecovery(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "arch", false, nil)
	dec := filepath.Join(a.Dir(), KnowledgeDir, "decisions")
	if err := os.MkdirAll(dec, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntags: [telemetry, drain]\n---\n# Telemetry batch drain\nbody text\n"
	if err := os.WriteFile(filepath.Join(dec, "0009-telemetry-drain.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := MemoryRebuild(a)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if len(idx.Topics) == 0 {
		t.Fatal("expected topics, got none")
	}

	hits, err := MemoryRecall(a, "telemetry")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("recall 'telemetry' returned no hits")
	}

	// recovery: rm .memory + rebuild reconstructs identical topic count
	before := len(idx.Topics)
	if err := os.RemoveAll(filepath.Join(a.Dir(), MemoryDir)); err != nil {
		t.Fatal(err)
	}
	idx2, err := MemoryRebuild(a)
	if err != nil {
		t.Fatalf("rebuild after delete: %v", err)
	}
	if len(idx2.Topics) != before {
		t.Fatalf("recovery mismatch: %d topics != %d", len(idx2.Topics), before)
	}
	if stale, _ := MemoryStale(a); stale {
		t.Fatal("freshly rebuilt index reported stale")
	}
}

func TestDispatchArgv_HarnessAware(t *testing.T) {
	// Claude: claude -p with allowlist + appended system prompt.
	name, args := dispatchArgv(HarnessClaude, "process intake X", "SYS", "")
	if name != "claude" || !containsArg(args, "-p") || !containsArg(args, "--append-system-prompt") {
		t.Fatalf("claude argv wrong: %s %v", name, args)
	}
	// opencode: opencode run --pure --auto, system prompt prepended to message.
	name, args = dispatchArgv(HarnessOpencode, "process intake X", "SYS", "")
	if name != "opencode" || !containsArg(args, "run") || !containsArg(args, "--pure") || !containsArg(args, "--auto") {
		t.Fatalf("opencode argv wrong: %s %v", name, args)
	}
	last := args[len(args)-1]
	if !contains(last, "SYS") || !contains(last, "process intake X") {
		t.Fatalf("opencode should prepend system prompt to message, got %q", last)
	}
	// opencode --model only when provider/model-shaped.
	_, a1 := dispatchArgv(HarnessOpencode, "m", "s", "gpt-5.5")
	if containsArg(a1, "--model") {
		t.Fatal("bare model alias must NOT be passed to opencode --model")
	}
	_, a2 := dispatchArgv(HarnessOpencode, "m", "s", "anthropic/claude-3-5")
	if !containsArg(a2, "--model") {
		t.Fatal("provider/model should be passed to opencode --model")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
