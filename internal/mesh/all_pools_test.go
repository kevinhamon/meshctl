package mesh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllPools_ClassifiesEveryEntryAndForgetsNone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	base := t.TempDir()

	live := filepath.Join(base, "live")
	if _, err := InitPool(live, "live", now); err != nil {
		t.Fatal(err)
	}
	notPool := filepath.Join(base, "plain")
	if err := os.MkdirAll(notPool, 0o755); err != nil {
		t.Fatal(err)
	}
	aFile := filepath.Join(base, "afile")
	if err := os.WriteFile(aFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ path, name string }{
		{live, "live"}, {filepath.Join(base, "gone"), "gone"}, {notPool, "plain"}, {aFile, "afile"},
	} {
		if _, err := RegisterPool(e.path, e.name, now); err != nil {
			t.Fatal(err)
		}
	}

	got, err := AllPools()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]PoolState{"live": PoolOK, "gone": PoolMissing, "plain": PoolNotPool, "afile": PoolNotPool}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	order := []string{"live", "gone", "plain", "afile"}
	for i, w := range got {
		if w.Name != order[i] {
			t.Fatalf("registry order lost: %d is %s, want %s", i, w.Name, order[i])
		}
		if w.State != want[w.Name] {
			t.Errorf("%s: state %s, want %s", w.Name, w.State, want[w.Name])
		}
		if (w.State == PoolOK) != (w.Reason() == "") {
			t.Errorf("%s: reason %q for state %s", w.Name, w.Reason(), w.State)
		}
	}

	// The walk reports; it never forgets.
	pools, _ := RegisteredPools()
	if len(pools) != 4 {
		t.Fatalf("walk changed the registry: %+v", pools)
	}
}

func TestAllPools_EmptyRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := AllPools()
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// writeMatrixAgent writes a minimal agent with the given workspace markers.
func writeMatrixAgent(t *testing.T, pool, name string, markers ...string) {
	t.Helper()
	dir := filepath.Join(pool, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("name: "+name+"\ntitle: "+name+"\nrole: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, m := range markers {
		p := filepath.Join(dir, m)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHarnessMatrix_WiredMissingNA(t *testing.T) {
	pool := t.TempDir()
	writeMatrixAgent(t, pool, "alpha", ".claude/settings.json")
	writeMatrixAgent(t, pool, "beta", "opencode.json")
	writeMatrixAgent(t, pool, "gamma")

	// claude installed here, opencode not.
	rows, err := HarnessMatrix(pool, []InstalledHarness{{ID: HarnessClaude}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]HarnessCell{
		"alpha": {HarnessClaude: HarnessWired, HarnessOpencode: HarnessNotOnHost},
		"beta":  {HarnessClaude: HarnessNotWired, HarnessOpencode: HarnessWired}, // wired even though not installed
		"gamma": {HarnessClaude: HarnessNotWired, HarnessOpencode: HarnessNotOnHost},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows: %+v", len(rows), rows)
	}
	for _, r := range rows {
		for id, cell := range want[r.Agent] {
			if r.Cells[id] != cell {
				t.Errorf("%s/%s: %s, want %s", r.Agent, id, r.Cells[id], cell)
			}
		}
		if len(r.Cells) != len(RegisteredHarnessIDs()) {
			t.Errorf("%s: one cell per registered harness, got %v", r.Agent, r.Cells)
		}
	}
}
