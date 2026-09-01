package mesh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitPool_EmitsFromEmbedNoClone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "academic")
	now := time.Now()

	did, err := InitPool(root, "academic", now)
	if err != nil {
		t.Fatalf("init pool: %v", err)
	}
	_ = did

	// Marker + identity.
	if !IsPool(root) {
		t.Fatal("pool marker dir not created")
	}
	mb, err := os.ReadFile(PoolMarkerPath(root))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !contains(string(mb), "name: academic") {
		t.Fatalf("marker missing identity: %s", mb)
	}
	// Doctrine emitted to agent-mesh/ (the path CLAUDE.md references), no .git.
	for _, f := range []string{CommsFile, "distiller.md"} {
		if !fileExists(filepath.Join(root, DoctrineDir, f)) {
			t.Fatalf("doctrine %s not emitted", f)
		}
	}
	if dirExists(filepath.Join(root, DoctrineDir, ".git")) {
		t.Fatal("pool should not contain a git clone")
	}
	// Runtime dir.
	if !dirExists(filepath.Join(root, WorktreesDir)) {
		t.Fatal("worktrees runtime dir not created")
	}
	// Second init aborts (already a pool).
	if _, err := InitPool(root, "academic", now); err == nil {
		t.Fatal("re-init of an existing pool must error")
	}
}

func TestAgentsDir_DetectsMarker(t *testing.T) {
	t.Setenv("AGENTS_DIR", "") // force the cwd-walk path
	root := filepath.Join(t.TempDir(), "pool")
	if _, err := InitPool(root, "pool", time.Now()); err != nil {
		t.Fatal(err)
	}
	// From a nested subdir, AgentsDir should resolve up to the marker.
	sub := filepath.Join(root, "some", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// t.Chdir restores cwd on cleanup (Go 1.24+).
	t.Chdir(sub)
	got, err := AgentsDir()
	if err != nil {
		t.Fatal(err)
	}
	// macOS /tmp symlinks to /private/tmp — compare resolved paths.
	gotR, _ := filepath.EvalSymlinks(got)
	wantR, _ := filepath.EvalSymlinks(root)
	if gotR != wantR {
		t.Fatalf("AgentsDir=%s, want %s", gotR, wantR)
	}
}

func TestUpgradeDoctrine_ReEmits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	if _, err := InitPool(root, "pool", time.Now()); err != nil {
		t.Fatal(err)
	}
	comms := filepath.Join(root, DoctrineDir, CommsFile)
	// Tamper with the emitted doctrine.
	if err := os.WriteFile(comms, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeDoctrine(root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(comms)
	if string(b) == "STALE" {
		t.Fatal("upgrade did not re-emit the doctrine")
	}
}

func TestInitMesh_WritesMarkerAddOnly(t *testing.T) {
	root := t.TempDir() // a plain dir, not yet a pool
	if _, err := InitMesh(root); err != nil {
		t.Fatal(err)
	}
	if !IsPool(root) {
		t.Fatal("InitMesh should write the pool marker")
	}
	// Tamper identity, re-run: add-only must NOT overwrite it.
	marker := PoolMarkerPath(root)
	os.WriteFile(marker, []byte("name: custom-kept\n"), 0o644)
	if _, err := InitMesh(root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(marker)
	if !contains(string(b), "custom-kept") {
		t.Fatalf("InitMesh clobbered an existing marker: %s", b)
	}
}
