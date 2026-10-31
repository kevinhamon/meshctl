package mesh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The pool directory is resolved once per pool root; every runtime path is
// built from that result. A fresh root resolves to the visible `pool/`;
// a root still carrying the hidden legacy directory resolves to it
// until `pool upgrade` moves it.
func TestPoolDir_ResolvesEveryRuntimePath(t *testing.T) {
	for _, layout := range []struct {
		name  string
		setup func(root string)
		dir   string
	}{
		{"fresh root defaults to pool/", func(string) {}, "pool"},
		{"legacy hidden dir is recognised", func(root string) {
			os.MkdirAll(filepath.Join(root, ".agentmesh"), 0o755)
		}, ".agentmesh"},
		{"pool/pool.yaml wins over a leftover legacy dir", func(root string) {
			os.MkdirAll(filepath.Join(root, ".agentmesh"), 0o755)
			os.MkdirAll(filepath.Join(root, "pool"), 0o755)
			os.WriteFile(filepath.Join(root, "pool", "pool.yaml"), []byte("name: x\n"), 0o644)
		}, "pool"},
	} {
		t.Run(layout.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			os.MkdirAll(root, 0o755)
			layout.setup(root)
			want := func(rel string) string { return filepath.Join(root, layout.dir, rel) }
			cases := []struct {
				name string
				got  string
				want string
			}{
				{"pool dir", PoolDir(root), filepath.Join(root, layout.dir)},
				{"marker", PoolMarkerPath(root), want("pool.yaml")},
				{"dispatch log", DispatchLogPath(root), want("dispatch.log")},
				{"dispatch count", DispatchCountPath(root), want("dispatch.count")},
				{"dispatch lock", DispatchLockPath(root), want("dispatch.lock")},
				{"worktrees", WorktreesPath(root), want("worktrees")},
				{"doctrine", DoctrinePath(root), want("doctrine")},
				{"local doctrine", DoctrineLocalPath(root), want(filepath.Join("doctrine", "local.md"))},
				{"ignore file", PoolIgnorePath(root), want(".gitignore")},
				{"glossary", PoolGlossaryPath(root), want("glossary.md")},
			}
			for _, c := range cases {
				if c.got != c.want {
					t.Errorf("%s: got %s, want %s", c.name, c.got, c.want)
				}
			}
			if got := PoolDirName(root); got != layout.dir {
				t.Errorf("PoolDirName: got %q, want %q", got, layout.dir)
			}
			if got := PoolDirIsLegacy(root); got != (layout.dir == ".agentmesh") {
				t.Errorf("PoolDirIsLegacy: got %v for %s", got, layout.dir)
			}
		})
	}
}

func TestIsPool_RecognisesBothMarkers(t *testing.T) {
	// A visible pool/ dir alone is not a pool — the marker file is; a legacy
	// hidden dir is a pool on its own (pre-marker pools exist).
	root := filepath.Join(t.TempDir(), "pool")
	if IsPool(root) {
		t.Fatal("empty dir must not be a pool")
	}
	os.MkdirAll(filepath.Join(root, "pool"), 0o755)
	if IsPool(root) {
		t.Fatal("a bare pool/ dir without pool.yaml must not be a pool")
	}
	if err := WritePoolMarker(root, "p", "v", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if !IsPool(root) || !fileExists(filepath.Join(root, "pool", "pool.yaml")) {
		t.Fatal("pool/pool.yaml must make the dir a pool")
	}

	legacy := filepath.Join(t.TempDir(), "legacy")
	os.MkdirAll(filepath.Join(legacy, ".agentmesh"), 0o755)
	if !IsPool(legacy) {
		t.Fatal("legacy .agentmesh/ must still be a pool")
	}
}

func TestDiscovery_WalksUpToEitherMarker(t *testing.T) {
	t.Setenv("AGENTS_DIR", "")
	for _, layout := range []struct {
		name  string
		setup func(root string)
	}{
		{"current", func(root string) { WritePoolMarker(root, "p", "v", time.Now(), false) }},
		{"legacy", func(root string) {
			os.MkdirAll(filepath.Join(root, ".agentmesh"), 0o755)
			WritePoolMarker(root, "p", "v", time.Now(), false)
		}},
	} {
		t.Run(layout.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "pool")
			os.MkdirAll(root, 0o755)
			layout.setup(root)
			ws := filepath.Join(root, "architect", "knowledge")
			if err := os.MkdirAll(ws, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(ws)
			got, err := AgentsDir()
			if err != nil {
				t.Fatal(err)
			}
			// macOS temp dirs are symlinked; compare resolved paths.
			wantRoot, _ := filepath.EvalSymlinks(root)
			gotRoot, _ := filepath.EvalSymlinks(got)
			if gotRoot != wantRoot {
				t.Fatalf("AgentsDir: got %s, want %s", got, root)
			}
		})
	}
}
