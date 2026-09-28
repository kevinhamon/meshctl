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

func TestUpgradeDoctrine_ReplacesPristine(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	if _, err := InitPool(root, "pool", time.Now()); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, DoctrineDir)
	comms := filepath.Join(dest, CommsFile)
	// Simulate an older binary's emit: stale content, stamped as meshctl-written.
	if err := os.WriteFile(comms, []byte("OLD RELEASE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stampEmitted(dest, []string{CommsFile}); err != nil {
		t.Fatal(err)
	}
	res, err := UpgradeDoctrine(root, false, time.Now())
	if err != nil || len(res.Conflicts) != 0 {
		t.Fatalf("pristine upgrade: conflicts=%v err=%v", res.Conflicts, err)
	}
	if b, _ := os.ReadFile(comms); string(b) == "OLD RELEASE" {
		t.Fatal("upgrade did not replace pristine doctrine")
	}
	if res2, _ := UpgradeDoctrine(root, false, time.Now()); len(res2.Did) != 0 {
		t.Fatalf("second upgrade should be a no-op, did %v", res2.Did)
	}
}

func TestUpgradeDoctrine_ProtectsLocalEdits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	if _, err := InitPool(root, "pool", time.Now()); err != nil {
		t.Fatal(err)
	}
	comms := filepath.Join(root, DoctrineDir, CommsFile)
	if err := os.WriteFile(comms, []byte("LOCAL RULE 10"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := UpgradeDoctrine(root, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %v", res.Conflicts)
	}
	if b, _ := os.ReadFile(comms); string(b) != "LOCAL RULE 10" {
		t.Fatal("upgrade clobbered local edits")
	}
	if !fileExists(comms + upstreamSuffix) {
		t.Fatal("no .upstream copy for merging")
	}

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := UpgradeDoctrine(root, true, now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(comms + ".bak-20260102-030405"); string(b) != "LOCAL RULE 10" {
		t.Fatal("--force did not back up local edits")
	}
	if b, _ := os.ReadFile(comms); string(b) == "LOCAL RULE 10" {
		t.Fatal("--force did not overwrite")
	}
	if fileExists(comms + upstreamSuffix) {
		t.Fatal("stale .upstream left after force")
	}
}

// A pool that predates stamps (no .emitted.json) must be treated as edited.
func TestUpgradeDoctrine_UnstampedDifferentIsConflict(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	if _, err := InitPool(root, "pool", time.Now()); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, DoctrineDir)
	_ = os.Remove(filepath.Join(dest, doctrineStampFile))
	_ = os.WriteFile(filepath.Join(dest, CommsFile), []byte("custom"), 0o644)
	if res, _ := UpgradeDoctrine(root, false, time.Now()); len(res.Conflicts) != 1 {
		t.Fatalf("unstamped custom doctrine not protected: %+v", res)
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
