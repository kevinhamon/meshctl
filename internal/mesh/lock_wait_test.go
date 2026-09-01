package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortLockPolling makes the acquire loop spin fast enough for a test.
func shortLockPolling(t *testing.T) {
	t.Helper()
	prev := lockPollInterval
	lockPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { lockPollInterval = prev })
}

func TestAcquireLock_WaitsForAnInFlightDispatch(t *testing.T) {
	// The common case: a peer is mid-dispatch and frees the lock shortly after.
	// The caller should queue and then proceed -- refusing here is what made an
	// agent conclude the architect was unreachable and carry on without it.
	shortLockPolling(t)
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "10")

	dir := t.TempDir()
	lockPath := filepath.Join(dir, DispatchLock)
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.RemoveAll(lockPath)
	}()

	l, err := acquireDispatchLock(dir, "caller", "architect", os.Getpid(), time.Now())
	if err != nil {
		t.Fatalf("should have queued and acquired, got: %v", err)
	}
	defer l.release()
	if !l.held {
		t.Error("lock reported not held")
	}
}

func TestAcquireLock_TimeoutSaysBusyNotUnavailable(t *testing.T) {
	// If the wait genuinely expires, the message must be impossible to read as
	// "this peer does not exist" -- that misreading is the defect.
	shortLockPolling(t)
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "1")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DispatchLock), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := acquireDispatchLock(dir, "caller", "architect", os.Getpid(), time.Now())
	if err == nil {
		t.Fatal("expected a timeout while the lock stayed held")
	}
	msg := err.Error()
	for _, want := range []string{
		"BUSY",
		"architect is reachable",
		"NOT an unavailable peer",
		"do NOT re-send",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "BLOCKED") {
		t.Error("message still leads with BLOCKED, which reads as a refusal rather than a queue")
	}
}

func TestAcquireLock_ReclaimsStaleLockWithoutWaiting(t *testing.T) {
	// A holder that died must not make every later caller sit out the full
	// wait; staleness reclaim has to win over queuing.
	shortLockPolling(t)
	t.Setenv("AGENT_DISPATCH_LOCK_WAIT_SEC", "60")
	t.Setenv("AGENT_DISPATCH_STALE_SEC", "1")

	dir := t.TempDir()
	lockPath := filepath.Join(dir, DispatchLock)
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	l, err := acquireDispatchLock(dir, "caller", "architect", os.Getpid(), time.Now())
	if err != nil {
		t.Fatalf("stale lock should be reclaimed, got: %v", err)
	}
	defer l.release()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("reclaim took %v — it should not have queued", elapsed)
	}
}

func TestAcquireLock_UncontendedIsImmediate(t *testing.T) {
	shortLockPolling(t)
	dir := t.TempDir()

	start := time.Now()
	l, err := acquireDispatchLock(dir, "caller", "architect", os.Getpid(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	if l.waitedSec != 0 {
		t.Errorf("uncontended acquire reported waiting %ds", l.waitedSec)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("uncontended acquire took %v", elapsed)
	}
}
