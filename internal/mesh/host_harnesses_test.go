package mesh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fakeHost stubs the binary lookup and the version probe, so these tests never
// resolve or run a real harness binary. bins maps a binary name to a file in a
// temp dir; a name absent from it is not on PATH. variants is what the probe
// reports per harness id (absent ⇒ not installed); probes counts each call.
type fakeHost struct {
	dir      string
	bins     map[string]string
	variants map[string]int
	probes   map[string]int
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, h := range RegisteredHarnesses() {
		t.Setenv(harnessProbeOverrideEnv(h.ID), "")
	}
	f := &fakeHost{dir: t.TempDir(), bins: map[string]string{}, variants: map[string]int{}, probes: map[string]int{}}
	oldLook, oldProbe := harnessLookPath, probeHarness
	harnessLookPath = func(name string) (string, error) {
		if p, ok := f.bins[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	probeHarness = func(h HarnessSpec) (int, bool) {
		f.probes[h.ID]++
		v, ok := f.variants[h.ID]
		return v, ok
	}
	t.Cleanup(func() { harnessLookPath, probeHarness = oldLook, oldProbe })
	return f
}

// install puts a fake binary for harness id on the fake PATH.
func (f *fakeHost) install(t *testing.T, id string, variant int, content string) {
	t.Helper()
	h, ok := LookupHarness(id)
	if !ok {
		t.Fatalf("harness %s not registered", id)
	}
	p := filepath.Join(f.dir, h.Binary)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	f.bins[h.Binary] = p
	f.variants[id] = variant
}

func (f *fakeHost) uninstall(t *testing.T, id string) {
	t.Helper()
	h, _ := LookupHarness(id)
	delete(f.bins, h.Binary)
	delete(f.variants, id)
}

func (f *fakeHost) resetProbes() { f.probes = map[string]int{} }

func TestInstalledHarnesses_CacheHitProbesNothing(t *testing.T) {
	f := newFakeHost(t)
	f.install(t, HarnessClaude, 0, "claude v1")
	f.install(t, HarnessOpencode, 2, "opencode v2")

	want := []InstalledHarness{{ID: HarnessClaude}, {ID: HarnessOpencode, Variant: 2}}
	if got := InstalledHarnesses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("first call: got %+v, want %+v", got, want)
	}
	if f.probes[HarnessClaude] != 1 || f.probes[HarnessOpencode] != 1 {
		t.Fatalf("first call must probe each harness once, got %v", f.probes)
	}
	path, _ := hostHarnessCachePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache not written: %v", err)
	}

	f.resetProbes()
	if got := InstalledHarnesses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cached call: got %+v, want %+v", got, want)
	}
	if len(f.probes) != 0 {
		t.Fatalf("a cache hit must not probe, got %v", f.probes)
	}
}

func TestInstalledHarnesses_ChangedBinaryReprobesOnlyThatHarness(t *testing.T) {
	f := newFakeHost(t)
	f.install(t, HarnessClaude, 0, "claude v1")
	f.install(t, HarnessOpencode, 1, "opencode v1")
	InstalledHarnesses()

	// Upgrade opencode in place: new size and mtime.
	f.resetProbes()
	f.install(t, HarnessOpencode, 2, "opencode v2, a bigger binary")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(f.bins["opencode"], later, later); err != nil {
		t.Fatal(err)
	}
	got := InstalledHarnesses()
	if f.probes[HarnessOpencode] != 1 || f.probes[HarnessClaude] != 0 {
		t.Fatalf("only the changed harness is re-probed, got %v", f.probes)
	}
	if want := []InstalledHarness{{ID: HarnessClaude}, {ID: HarnessOpencode, Variant: 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// Same size, only the mtime moves: still a change.
	f.resetProbes()
	earlier := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.bins["claude"], earlier, earlier); err != nil {
		t.Fatal(err)
	}
	InstalledHarnesses()
	if f.probes[HarnessClaude] != 1 || f.probes[HarnessOpencode] != 0 {
		t.Fatalf("an mtime change re-probes that harness only, got %v", f.probes)
	}
}

func TestInstalledHarnesses_AppearAndVanish(t *testing.T) {
	f := newFakeHost(t)
	f.install(t, HarnessClaude, 0, "claude")
	if got := InstalledHarnesses(); len(got) != 1 || got[0].ID != HarnessClaude {
		t.Fatalf("got %+v", got)
	}

	// Not installed is cached too: no re-probe while it stays absent.
	f.resetProbes()
	InstalledHarnesses()
	if len(f.probes) != 0 {
		t.Fatalf("an absent harness is cached, got %v", f.probes)
	}

	f.resetProbes()
	f.install(t, HarnessOpencode, 2, "opencode")
	if got := InstalledHarnesses(); len(got) != 2 {
		t.Fatalf("appeared harness missing: %+v", got)
	}
	if f.probes[HarnessOpencode] != 1 || f.probes[HarnessClaude] != 0 {
		t.Fatalf("appear re-probes that harness only, got %v", f.probes)
	}

	f.resetProbes()
	f.uninstall(t, HarnessClaude)
	if got := InstalledHarnesses(); len(got) != 1 || got[0].ID != HarnessOpencode {
		t.Fatalf("vanished harness still listed: %+v", got)
	}
	if f.probes[HarnessClaude] != 1 || f.probes[HarnessOpencode] != 0 {
		t.Fatalf("vanish re-probes that harness only, got %v", f.probes)
	}
}

func TestInstalledHarnesses_OverrideEnvIsPartOfTheKey(t *testing.T) {
	if got := harnessProbeOverrideEnv(HarnessOpencode); got != opencodeMajorEnv {
		t.Fatalf("override env for opencode = %q, want %q", got, opencodeMajorEnv)
	}
	f := newFakeHost(t)
	f.install(t, HarnessOpencode, 2, "opencode")
	InstalledHarnesses()

	f.resetProbes()
	t.Setenv(opencodeMajorEnv, "1")
	f.variants[HarnessOpencode] = 1
	got := InstalledHarnesses()
	if f.probes[HarnessOpencode] != 1 {
		t.Fatalf("setting the override must re-probe, got %v", f.probes)
	}
	if len(got) != 1 || got[0].Variant != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestInstalledHarnesses_CorruptOrForeignCacheIsRewritten(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":      "{not json",
		"wrong schema": `{"schema": 99, "harnesses": {"claude": {"installed": true}}}`,
		"no map":       `{"schema": 1}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeHost(t)
			f.install(t, HarnessClaude, 0, "claude")
			path, err := hostHarnessCachePath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := InstalledHarnesses(); len(got) != 1 || got[0].ID != HarnessClaude {
				t.Fatalf("got %+v", got)
			}
			if f.probes[HarnessClaude] != 1 {
				t.Fatalf("a bad cache reads as empty, got %v", f.probes)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var c hostHarnessCache
			if err := json.Unmarshal(b, &c); err != nil || c.Schema != hostHarnessCacheSchema || !c.Harnesses[HarnessClaude].Installed {
				t.Fatalf("cache not rewritten: %s (%v)", b, err)
			}
			// No temp files left behind by the atomic write.
			if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".harnesses-*.tmp")); len(m) != 0 {
				t.Fatalf("temp files left: %v", m)
			}
		})
	}
}

func TestInstalledHarnesses_UnwritableCacheStillAnswers(t *testing.T) {
	f := newFakeHost(t)
	f.install(t, HarnessClaude, 0, "claude")
	// A file where the cache's directory should be: nothing can be written.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.WriteFile(filepath.Join(xdg, "meshctl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := InstalledHarnesses(); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if err := saveHostHarnessCache(filepath.Join(xdg, "meshctl", "harnesses.json"), hostHarnessCache{}); err == nil {
		t.Fatal("expected the save to fail")
	}
}
