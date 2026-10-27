package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// hub_test.go — one console for every pool (ADR-0077 amendment, v1.8.2).

// livePool is testPool with the pool marker, so the registry counts it live.
func livePool(t *testing.T) (string, *mesh.Agent) {
	t.Helper()
	dir, a := testPool(t)
	if err := mesh.WritePoolMarker(dir, filepath.Base(dir), "", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	return dir, a
}

func TestPoolRefs_OrderKeysAndLiveness(t *testing.T) {
	one, _ := livePool(t)
	two, _ := livePool(t)
	gone := filepath.Join(t.TempDir(), "gone")
	unregistered, _ := livePool(t)
	entries := []mesh.PoolEntry{{Name: "work", Path: one}, {Name: "old", Path: gone}, {Name: "Work", Path: two}}

	refs := PoolRefs(unregistered, entries)
	var got []string
	for _, r := range refs {
		got = append(got, r.Key+"="+r.Path)
	}
	want := []string{"agents=" + unregistered, "work=" + one, "work-2=" + two}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("refs:\n got %v\nwant %v", got, want)
	}
	// A registered start pool is listed once, in registry order.
	refs = PoolRefs(two, entries)
	if len(refs) != 2 || refs[1].Path != two || refs[1].Name != "Work" {
		t.Fatalf("registered start: %+v", refs)
	}
	if slug("my pool/2") != "my-pool-2" || slug("") != "pool" {
		t.Errorf("slug: %q %q", slug("my pool/2"), slug(""))
	}
}

// twoPoolHub serves two registered pools without running their engines;
// the tests drive the engines directly.
func twoPoolHub(t *testing.T) (*Hub, *mesh.Agent, *mesh.Agent) {
	t.Helper()
	one, a1 := livePool(t)
	two, a2 := livePool(t)
	h := NewHub(two, "test")
	h.Entries = func() ([]mesh.PoolEntry, error) {
		return []mesh.PoolEntry{{Name: "one", Path: one}, {Name: "two", Path: two}}, nil
	}
	h.Sync()
	for _, k := range []string{"one", "two"} {
		e, _, err := h.Engine(k)
		if err != nil {
			t.Fatal(err)
		}
		e.RefreshPending()
	}
	return h, a1, a2
}

func TestHub_FramesCarryThePoolAndEverySummary(t *testing.T) {
	h, a1, _ := twoPoolHub(t)
	if h.DefaultKey() != "two" {
		t.Fatalf("default is the start pool, got %q", h.DefaultKey())
	}
	ch, cancel, err := h.Subscribe("two")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	<-ch // the current frame

	// A change in pool one reaches a page showing pool two: its summary moved.
	if _, err := mesh.WriteRequest(a1, "guidance", "Split the service?", "user", "P2", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	e, _, _ := h.Engine("one")
	e.RefreshPending()
	var f Frame
	select {
	case b := <-ch:
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame after a change in another pool")
	}
	if f.PoolKey != "two" || f.Pool != h.Pools()[1].Path || len(f.Pools) != 2 {
		t.Fatalf("frame: key %q pool %q pools %+v", f.PoolKey, f.Pool, f.Pools)
	}
	if s := f.Pools[0]; s.Key != "one" || !s.Ready || s.Pending != 1 {
		t.Errorf("pool one's summary: %+v", s)
	}
	if s := f.Pools[1]; s.Pending != 0 {
		t.Errorf("pool two's summary: %+v", s)
	}
	if _, _, err := h.Subscribe("nope"); err != ErrNoPool {
		t.Errorf("unknown pool: %v", err)
	}
}

func TestHub_SyncDropsAForgottenPool(t *testing.T) {
	h, _, _ := twoPoolHub(t)
	ch, cancel, _ := h.Subscribe("one")
	defer cancel()
	<-ch
	keep := h.Pools()[1]
	h.Entries = func() ([]mesh.PoolEntry, error) { return []mesh.PoolEntry{{Name: keep.Name, Path: keep.Path}}, nil }
	h.Sync()
	if _, _, err := h.Engine("one"); err != ErrNoPool {
		t.Fatalf("forgotten pool still served: %v", err)
	}
	var f Frame
	_ = json.Unmarshal(<-ch, &f)
	if f.PoolKey != "one" || f.Pending.Agents != nil || len(f.Pools) != 1 {
		t.Fatalf("a page on a forgotten pool gets an empty frame listing the rest: %+v", f)
	}
}

func TestServer_RoutesByPool(t *testing.T) {
	h, a1, _ := twoPoolHub(t)
	r, err := mesh.WriteRequest(a1, "guidance", "Split the service?", "user", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Hub: h, Settings: Settings{Port: 7717, Token: "secret"}}
	hd := s.Handler()
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "127.0.0.1:7717"
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		hd.ServeHTTP(w, req)
		return w
	}
	var f Frame
	if w := get("/api/snapshot"); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &f) != nil || f.PoolKey != "two" {
		t.Fatalf("no pool means the default: %d %s", w.Code, w.Body)
	}
	if len(f.Pending.Agents) != 1 {
		t.Errorf("a frame reads like a snapshot: %+v", f.Pending)
	}
	if w := get("/api/snapshot?pool=one"); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &f) != nil || f.PoolKey != "one" {
		t.Fatalf("pool one: %d %s", w.Code, w.Body)
	}
	// The item lives in pool one only.
	if w := get("/api/item?pool=one&agent=architect&kind=intake&id=" + r.ID); w.Code != http.StatusOK {
		t.Errorf("item in its pool: %d %s", w.Code, w.Body)
	}
	if w := get("/api/item?pool=two&agent=architect&kind=intake&id=" + r.ID); w.Code != http.StatusNotFound {
		t.Errorf("item from another pool: %d", w.Code)
	}
	for _, target := range []string{"/api/snapshot?pool=nope", "/api/item?pool=nope&agent=architect&kind=intake&id=" + r.ID, "/api/events?pool=nope"} {
		if w := get(target); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
}

func TestLoginItemPlist_NoPoolOpensOnTheRegistry(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	p := LoginItemPlist("/bin/meshctl", "", "/usr/bin:/bin", "/Users/x/Library/Logs/meshctl-console.log")
	if strings.Contains(p, "AGENTS_DIR") || !strings.Contains(p, "<key>WorkingDirectory</key><string>/Users/x</string>") {
		t.Fatalf("plist without a pool:\n%s", p)
	}
	if lint, err := exec.LookPath("plutil"); err == nil {
		f := filepath.Join(t.TempDir(), "x.plist")
		_ = os.WriteFile(f, []byte(p), 0o644)
		if out, err := exec.Command(lint, "-lint", f).CombinedOutput(); err != nil {
			t.Fatalf("plutil: %v %s", err, out)
		}
	}
}

func TestHub_RunStartsEveryPool(t *testing.T) {
	one, _ := livePool(t)
	two, _ := livePool(t)
	h := NewHub("", "test")
	h.Entries = func() ([]mesh.PoolEntry, error) {
		return []mesh.PoolEntry{{Name: "one", Path: one}, {Name: "two", Path: two}}, nil
	}
	h.Sync()
	if h.DefaultKey() != "one" {
		t.Fatalf("outside a pool the first registered pool is the default: %q", h.DefaultKey())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for {
		f, err := h.Frame("one")
		if err == nil && len(f.Pools) == 2 && f.Pools[0].Ready && f.Pools[1].Ready {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("pools never computed: %+v %v", f.Pools, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
