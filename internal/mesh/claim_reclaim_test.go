package mesh

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A stale claim whose owner is gone draws every idle session at once. Exactly
// one of them may end up owning the request, and the claim file must name that
// one: a reclaim that removed-then-created let a second reclaimer delete the
// first one's fresh claim and create its own, so both worked the request.
func TestClaim_ConcurrentStaleReclaimYieldsOneOwner(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	req, err := WriteRequest(a, "guidance", "x", "u", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	stale := base.Add(time.Duration(staleSec()+10) * time.Second)

	for round := 0; round < 20; round++ {
		// Owner claims, then its session vanishes (never registered).
		_ = os.Remove(claimPath(req.path))
		if _, ok := ClaimRequest(req.path, "owner", 99, base); !ok {
			t.Fatal("owner could not claim")
		}

		const n = 16
		var wg sync.WaitGroup
		var mu sync.Mutex
		var winners []string
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				sess := "thief" + itoa(int64(i))
				if _, ok := ClaimRequest(req.path, sess, 1000+i, stale); ok {
					mu.Lock()
					winners = append(winners, sess)
					mu.Unlock()
				}
			}(i)
		}
		close(start)
		wg.Wait()

		if len(winners) != 1 {
			t.Fatalf("round %d: %d sessions reclaimed one request: %v", round, len(winners), winners)
		}
		c, err := readClaim(claimPath(req.path))
		if err != nil {
			t.Fatalf("round %d: claim file unreadable after reclaim: %v", round, err)
		}
		if c.OwnerSession != winners[0] {
			t.Fatalf("round %d: claim file names %q but %q was told it won", round, c.OwnerSession, winners[0])
		}
		if fileExists(claimReclaimPath(claimPath(req.path))) {
			t.Fatalf("round %d: reclaim mutex left behind", round)
		}
	}
}

// A session that has not prompted for the stale window but whose harness is
// still running is on the roster (liveSessions keeps it), so its claim is not
// abandoned. Once the harness is gone the claim is reclaimable as before.
func TestClaim_IdleSessionWithLiveHarnessKeepsClaim(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	req, err := WriteRequest(a, "guidance", "x", "u", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	stale := base.Add(time.Duration(staleSec()+10) * time.Second)
	if _, ok := ClaimRequest(req.path, "owner", 99, base); !ok {
		t.Fatal("owner could not claim")
	}

	idle := &Session{ID: "owner", Started: base.Unix(), LastBeat: base.Unix(), HarnessPID: os.Getpid(), ProcScope: procScope()}
	writeTestSession(t, a.Dir(), idle)
	if _, ok := ClaimRequest(req.path, "thief", 5, stale); ok {
		t.Fatal("stole a claim from an idle session whose harness is still running")
	}
	if err := ReleaseClaim(req.path, "thief", stale); err == nil {
		t.Fatal("released a claim held by an idle session whose harness is still running")
	}

	idle.HarnessPID = deadPID
	writeTestSession(t, a.Dir(), idle)
	if _, ok := ClaimRequest(req.path, "thief", 5, stale); !ok {
		t.Fatal("failed to reclaim a stale claim once the owner's harness was gone")
	}
}

// A lock whose holder pid is alive is still reclaimed once it is older than any
// dispatch can run: after a SIGKILLed ask the pid is eventually reused by some
// unrelated long-lived process, and "live holder, never reclaim" made that lock
// permanent.
func TestLockReclaimable_LiveHolderPastCeilingIsStale(t *testing.T) {
	now := time.Now()
	mk := func(age time.Duration) string {
		p := filepath.Join(t.TempDir(), "lock")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		info := "pid=" + itoa(int64(os.Getpid())) + " scope=" + procScope() + " caller=a"
		if err := os.WriteFile(filepath.Join(p, "info"), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		_ = os.Chtimes(p, at, at)
		return p
	}
	ceiling := time.Duration(lockCeilingSec()) * time.Second
	if lockReclaimable(mk(0), now) {
		t.Fatal("a fresh lock held by a live process is not taken")
	}
	if lockReclaimable(mk(ceiling-time.Minute), now) {
		t.Fatal("a live holder inside the ceiling is left alone")
	}
	if !lockReclaimable(mk(ceiling+time.Minute), now) {
		t.Fatal("a live pid holding a lock longer than any dispatch can run is a reused pid; the lock is taken")
	}
	if lockCeilingSec() < 2*timeoutSec() || lockCeilingSec() < staleSec() {
		t.Fatalf("ceiling %ds must cover a full dispatch (2×%ds) and the stale window (%ds)", lockCeilingSec(), timeoutSec(), staleSec())
	}
}
