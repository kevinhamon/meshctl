package mesh

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRegistry_AddListForgetStale(t *testing.T) {
	// Isolate the registry to a temp XDG dir.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()

	// A real pool + a path that isn't one.
	dir := meshRoot(t)
	live := filepath.Join(dir, "livepool")
	if _, err := InitPool(live, "livepool", now); err != nil {
		t.Fatal(err)
	}

	if _, err := RegisterPool(live, "livepool", now); err != nil {
		t.Fatal(err)
	}
	// Register a bogus path (stale by construction).
	ghost := filepath.Join(dir, "ghost")
	if _, err := RegisterPool(ghost, "ghost", now); err != nil {
		t.Fatal(err)
	}

	pools, err := RegisteredPools()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Fatalf("want 2 registered, got %d", len(pools))
	}
	var liveOK, ghostStale bool
	for _, e := range pools {
		switch e.Name {
		case "livepool":
			liveOK = PoolIsLive(e)
		case "ghost":
			ghostStale = !PoolIsLive(e)
		}
	}
	if !liveOK {
		t.Fatal("live pool should be live")
	}
	if !ghostStale {
		t.Fatal("ghost path should read as stale")
	}

	// Idempotent register (same path) → still 2, name refreshed.
	if _, err := RegisterPool(live, "renamed", now); err != nil {
		t.Fatal(err)
	}
	pools, _ = RegisteredPools()
	if len(pools) != 2 {
		t.Fatalf("re-register must not duplicate; got %d", len(pools))
	}

	// Forget by name.
	ok, err := ForgetPool("ghost")
	if err != nil || !ok {
		t.Fatalf("forget ghost: ok=%v err=%v", ok, err)
	}
	pools, _ = RegisteredPools()
	if len(pools) != 1 || pools[0].Path != mustAbs(t, live) {
		t.Fatalf("after forget want [livepool], got %+v", pools)
	}

	// Forget non-existent → false, no error.
	if ok, _ := ForgetPool("nope"); ok {
		t.Fatal("forget of missing entry should return false")
	}
}

func TestRegistry_EmptyWhenAbsent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pools, err := RegisteredPools()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 0 {
		t.Fatalf("expected empty registry, got %d", len(pools))
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
