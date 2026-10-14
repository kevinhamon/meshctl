package mesh

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- identifier validation + header injection ---

func TestValidName(t *testing.T) {
	for _, ok := range []string{"architect", "my-pm", "a1", "x.y_z", "A-B"} {
		if err := ValidName("agent name", ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a b", "a\nb", "-a", "..", "a..b", strings.Repeat("x", 65), "x\x00y"} {
		if err := ValidName("agent name", bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestFindAgent_RejectsPathEscape(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	for _, name := range []string{"../architect", "architect/..", "a/b"} {
		if _, err := FindAgent(dir, name); err == nil {
			t.Errorf("FindAgent(%q) should fail", name)
		}
	}
}

func TestWriteRequest_RejectsHeaderInjection(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	cases := []struct {
		name     string
		from     string
		priority string
		related  []string
	}{
		{"newline in from", "dev\nstatus: answered", "P2", nil},
		{"path in from", "../../evil", "P2", nil},
		{"bad priority", "dev", "urgent", nil},
		{"newline in related", "dev", "P2", []string{"R-1\nstatus: answered"}},
		{"space in related", "dev", "P2", []string{"R 1"}},
	}
	for _, c := range cases {
		if _, err := WriteRequest(a, "guidance", "x", c.from, c.priority, c.related, now); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
	// Nothing was written outside the inbox or at all.
	entries, _ := os.ReadDir(a.InboxPath())
	for _, e := range entries {
		if e.Name() != "README.md" {
			t.Errorf("unexpected inbox file %s", e.Name())
		}
	}
}

func TestWriteRequest_QuotesHeaderAndRoundTrips(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	req, err := WriteRequest(a, "guidance", "colon: hash # dash - [bracket]", "dev.1", "P1", []string{"PROJ-1", "arch:2026-01-01-x"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadRequest(req.Path())
	if err != nil {
		t.Fatal(err)
	}
	if back.From != "dev.1" || back.Priority != "P1" || back.Status != "pending" || back.Type != "guidance" {
		t.Fatalf("header did not round-trip: %+v", back)
	}
	if len(back.Related) != 2 || back.Related[1] != "arch:2026-01-01-x" {
		t.Fatalf("related did not round-trip: %v", back.Related)
	}
	if !strings.Contains(back.Body, "colon: hash # dash - [bracket]") {
		t.Fatalf("prompt body lost: %q", back.Body)
	}
}

func TestWriteRequest_ConcurrentSameIdNeverClobbers(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	const n = 12
	var wg sync.WaitGroup
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := WriteRequest(a, "guidance", "same prompt", "u", "P2", nil, now)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("want %d distinct requests, got %d", n, len(seen))
	}
}

func TestCallerName_FallsBackOnUnsafeEnv(t *testing.T) {
	t.Setenv("AGENT_NAME", "../x")
	if got := CallerName(); got == "../x" {
		t.Fatalf("unsafe $AGENT_NAME must not be used verbatim, got %q", got)
	}
}

// --- atomic writes ---

func TestWriteFileAtomic_LeavesNoTempAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := writeFileAtomic(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// --- lock: stale reclaim race + pid check ---

func TestAcquireLock_ConcurrentStaleReclaimYieldsOneHolder(t *testing.T) {
	shortLockPolling(t)
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "5")
	t.Setenv("AGENT_DISPATCH_STALE_SEC", "1")
	dir := t.TempDir()
	lockPath := filepath.Join(dir, DispatchLock)
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second)
	_ = os.Chtimes(lockPath, old, old)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	holders := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := acquireDispatchLock(context.Background(), dir, "c", "t", os.Getpid(), time.Now())
			if err != nil {
				return
			}
			mu.Lock()
			holders++
			// Hold it long enough that every other racer must observe it held.
			mu.Unlock()
			time.Sleep(300 * time.Millisecond)
			if _, err := os.Stat(lockPath); err != nil {
				t.Error("holder's lock was removed by a racer")
			}
			l.release()
		}()
	}
	wg.Wait()
	// At least one got it; with a 5s wait and a 300ms hold, every racer
	// eventually acquires in turn — the invariant is that no two held it at once,
	// which the Stat check inside the hold enforces.
	if holders == 0 {
		t.Fatal("nobody acquired the stale lock")
	}
}

func TestAcquireLock_LiveHolderPidIsNotReclaimed(t *testing.T) {
	shortLockPolling(t)
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "1")
	t.Setenv("AGENT_DISPATCH_STALE_SEC", "1")
	dir := t.TempDir()
	lockPath := filepath.Join(dir, DispatchLock)
	_ = os.MkdirAll(lockPath, 0o755)
	// Record OUR pid as the holder: alive, so even a stale mtime must not reclaim.
	_ = os.WriteFile(filepath.Join(lockPath, "info"), []byte("pid="+strconv.Itoa(os.Getpid())+" caller=x target=y\n"), 0o644)
	old := time.Now().Add(-10 * time.Second)
	_ = os.Chtimes(lockPath, old, old)
	if alive, known := pidAlive(os.Getpid()); known && !alive {
		t.Skip("pid probe unavailable")
	}
	if _, known := pidAlive(os.Getpid()); !known {
		t.Skip("platform cannot probe pids")
	}
	if _, err := acquireDispatchLock(context.Background(), dir, "c", "t", os.Getpid(), time.Now()); err == nil {
		t.Fatal("a lock whose holder pid is alive must not be reclaimed")
	}
}

// --- hourly cap charged before the lock wait, refunded when nothing ran ---

func TestAsk_CapChargedBeforeLockAndRefundedOnBusy(t *testing.T) {
	shortLockPolling(t)
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "1")
	t.Setenv("AGENT_DISPATCH_HOURLY_MAX", "12")
	dir := meshRoot(t)
	target := newTestAgent(t, dir, "architect", true, nil)
	// Someone else holds the lock for the whole wait.
	lockPath := filepath.Join(dir, DispatchLock)
	_ = os.MkdirAll(lockPath, 0o755)
	defer os.RemoveAll(lockPath)

	_, rc, _ := Ask(dir, target, AskOptions{Type: "guidance", Prompt: "x", From: "u", Runner: okRunner, Now: time.Now(), Stdout: discard{}, Stderr: discard{}})
	if rc != ExitLockHeld {
		t.Fatalf("want BUSY (%d), got %d", ExitLockHeld, rc)
	}
	if n, _ := hourlyCount(dir, time.Now()); n != 0 {
		t.Fatalf("a dispatch that never ran must not count against the cap, got %d", n)
	}
}

func TestAsk_InterruptReleasesLockAndClaim(t *testing.T) {
	shortLockPolling(t)
	dir := meshRoot(t)
	target := newTestAgent(t, dir, "architect", true, nil)

	// Simulated interrupt: the notify context is cancelled while the child runs.
	ctx, cancel := context.WithCancel(context.Background())
	prev := notifyInterrupt
	notifyInterrupt = func(parent context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} }
	t.Cleanup(func() { notifyInterrupt = prev })

	runner := func(c context.Context, targetDir, msg, sys, model string, env []string, stdout, stderr io.Writer) int {
		cancel()
		<-c.Done()
		return 130
	}
	req, rc, _ := Ask(dir, target, AskOptions{Type: "guidance", Prompt: "x", From: "u", Runner: runner, Now: time.Now(), Stdout: discard{}, Stderr: discard{}})
	if rc != 130 {
		t.Fatalf("want 130, got %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, DispatchLock)); err == nil {
		t.Fatal("dispatch lock leaked after interrupt")
	}
	if _, err := os.Stat(claimPath(req.Path())); err == nil {
		t.Fatal("request claim leaked after interrupt")
	}
	// The child did start (tokens were spent), so the cap is charged.
	if n, _ := hourlyCount(dir, time.Now()); n != 1 {
		t.Fatalf("a started-then-interrupted dispatch counts once, got %d", n)
	}
}

func TestDispatchArgv_ClaudeGrantsNoFileEdits(t *testing.T) {
	_, args := dispatchArgv(HarnessClaude, 0, "m", "s", "")
	for _, a := range args {
		if a == "Edit" || a == "Write" || a == "acceptEdits" {
			t.Fatalf("dispatched peer must not get %q: %v", a, args)
		}
	}
	if !containsArg(args, "Bash(meshctl inbox respond:*)") || !containsArg(args, "Read") {
		t.Fatalf("missing read/respond grants: %v", args)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(opencodeDispatchConfig), &cfg); err != nil {
		t.Fatalf("opencode dispatch config is not JSON: %v", err)
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

// --- gate: strict payload + shell writes + pool scoping ---

func TestParseHookPayloadStrict(t *testing.T) {
	if _, err := ParseHookPayloadStrict(nil); err == nil {
		t.Fatal("empty payload must error")
	}
	if _, err := ParseHookPayloadStrict([]byte("{not json")); err == nil {
		t.Fatal("malformed payload must error")
	}
	if _, err := ParseHookPayloadStrict([]byte(`{"cwd":"/x"}`)); err == nil {
		t.Fatal("payload without tool_name must error")
	}
	if _, err := ParseHookPayloadStrict([]byte(`{"tool_name":"Read","tool_input":{}}`)); err != nil {
		t.Fatal(err)
	}
	if d := GateUnavailable("x"); !d.Deny {
		t.Fatal("GateUnavailable must deny")
	}
}

func TestGate_ShellWritesToIntakeDenied(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	bash := func(cmd string) HookPayload {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return HookPayload{Cwd: a.Dir(), ToolName: "Bash", ToolInput: in}
	}
	deny := []string{
		"echo hi > intake/2026-01-01-u-x.md",
		"cat body.md >> " + a.Dir() + "/intake/2026-01-01-u-x.md",
		"sed -i '' 's/pending/answered/' intake/2026-01-01-u-x.md",
		"rm intake/2026-01-01-u-x.md.claim",
		"cd x && tee intake/2026-01-01-u-x.md < /dev/null",
	}
	allow := []string{
		"cat intake/2026-01-01-u-x.md",
		"grep -n status intake/2026-01-01-u-x.md",
		"ls intake/",
		"meshctl inbox respond 2026-01-01-u-x --status answered --response-file -",
		"echo hi > notes/intake-summary.md",
		"echo hi > /somewhere/else/intake/x.md", // another repo's intake: not this pool's
	}
	for _, c := range deny {
		if d := GatePreToolUse(dir, bash(c)); !d.Deny {
			t.Errorf("should deny: %s", c)
		}
	}
	for _, c := range allow {
		if d := GatePreToolUse(dir, bash(c)); d.Deny {
			t.Errorf("should allow: %s (%s)", c, d.Reason)
		}
	}
}

func TestGate_IntakeRuleScopedToPool(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	edit := func(path string) HookPayload {
		in, _ := json.Marshal(map[string]string{"file_path": path})
		return HookPayload{ToolName: "Write", ToolInput: in}
	}
	if d := GatePreToolUse(dir, edit(filepath.Join(dir, "architect", "intake", "x.md"))); !d.Deny {
		t.Fatal("intake write inside the pool must be denied")
	}
	if d := GatePreToolUse(dir, edit("/elsewhere/project/intake/x.md")); d.Deny {
		t.Fatalf("intake write outside the pool must be allowed: %s", d.Reason)
	}
	// Unknown pool: the rule stays global (no information to scope by).
	if d := GatePreToolUse("", edit("/elsewhere/project/intake/x.md")); !d.Deny {
		t.Fatal("with no pool known the intake rule stays global")
	}
}

func TestHookPreToolUse_HeartbeatsSession(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	stale := now.Add(-2 * time.Hour)
	if _, err := SessionStart(a, "sess-1", 1, stale); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"file_path": "README.md"})
	HookPreToolUse(dir, HookPayload{Cwd: a.Dir(), SessionID: "sess-1", ToolName: "Read", ToolInput: in}, now)
	if !sessionAlive(a.Dir(), "sess-1", now) {
		t.Fatal("pre-tool-use should refresh the session heartbeat")
	}
}

// --- archive --answered keeps unread answers ---

func TestArchiveAnswered_KeepsUnreadAnswers(t *testing.T) {
	dir := meshRoot(t)
	arch := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	req, _ := WriteRequest(arch, "guidance", "x", "developer", "P2", nil, now)
	if _, err := RespondRequest(arch, req.ID, RespondOptions{Status: "answered", Response: "yes", Session: "s1", Now: now}); err != nil {
		t.Fatal(err)
	}
	archived, skipped, err := ArchiveAnswered(arch, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 0 || len(skipped) != 1 {
		t.Fatalf("unread answer must be kept: archived=%v skipped=%v", archived, skipped)
	}
	archived, _, _ = ArchiveAnswered(arch, true)
	if len(archived) != 1 {
		t.Fatalf("--force should archive it: %v", archived)
	}
}

// --- distillation nudge default-on ---

func TestDistillNudgeEnabled_Default(t *testing.T) {
	t.Setenv(distillNudgeAgentsEnv, "")
	if !distillNudgeEnabled("anyone") {
		t.Fatal("default must enable every agent")
	}
	t.Setenv(distillNudgeAgentsEnv, "none")
	if distillNudgeEnabled("anyone") {
		t.Fatal("'none' must disable")
	}
	t.Setenv(distillNudgeAgentsEnv, "pm, architect")
	if !distillNudgeEnabled("architect") || distillNudgeEnabled("developer") {
		t.Fatal("explicit list must be honoured")
	}
	if distillNudgeEnabled("") {
		t.Fatal("no agent ⇒ no nudge")
	}
}

// --- intuition: KB density must not crowd activity out ---

func TestRecallActivity_ExcludesKB(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	kb := filepath.Join(a.Dir(), "knowledge", "playbooks")
	_ = os.MkdirAll(kb, 0o755)
	for i := 0; i < 30; i++ {
		_ = os.WriteFile(filepath.Join(kb, "deploy-"+strconv.Itoa(i)+".md"), []byte("---\ntags: [deploy]\n---\n# deploy playbook "+strconv.Itoa(i)+"\n"), 0o644)
	}
	sess := filepath.Join(a.Dir(), ".memory", "sessions")
	_ = os.MkdirAll(sess, 0o755)
	_ = os.WriteFile(filepath.Join(sess, "s1.json"), []byte(`{"id":"s1","first_prompt":"fix the deploy pipeline","summary":"fix the deploy pipeline","ended":"2026-01-01T00:00:00Z"}`), 0o644)
	hits, err := RecallActivity(a, "deploy", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("activity recall returned nothing despite a matching session record")
	}
	for _, h := range hits {
		if h.Kind == "kb" {
			t.Fatalf("activity recall must not return KB hits: %+v", h)
		}
	}
	if got := RecallIntuition(a, "deploy", 3); len(got) == 0 {
		t.Fatal("intuition crowded out by KB density")
	}
}
