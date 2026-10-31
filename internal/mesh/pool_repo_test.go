package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pushedPoolRepo gives a migrated pool a bare remote with main pushed: the
// one state in which the pool repo nag is silent.
func pushedPoolRepo(t *testing.T, root string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "pool.git")
	gitOut(t, ".", "init", "--bare", bare)
	pool := PoolDir(root)
	gitOut(t, pool, "remote", "add", "origin", bare)
	gitOut(t, pool, "push", "-u", "origin", "main")
	return pool
}

func TestPoolRepoNag_States(t *testing.T) {
	// A pool with no marker is not yet a pool with an identity: nothing to version, no nag.
	if nag := PoolRepoNag(meshRoot(t)); nag != "" {
		t.Fatalf("legacy pool without a marker must stay silent, got %q", nag)
	}

	// Legacy pool with a marker: not a repository, and `pool upgrade` is the fix.
	legacy := legacyRoot(t)
	if nag := PoolRepoNag(legacy); !strings.Contains(nag, "not a git repository") || !strings.Contains(nag, "meshctl pool upgrade") {
		t.Fatalf("legacy pool: want not-a-repository naming pool upgrade, got %q", nag)
	}

	root := migratedRoot(t)
	pool := PoolDir(root)
	if nag := PoolRepoNag(root); !strings.Contains(nag, "no git remote") {
		t.Fatalf("migrated, no remote: got %q", nag)
	}

	bare := filepath.Join(t.TempDir(), "pool.git")
	gitOut(t, ".", "init", "--bare", bare)
	gitOut(t, pool, "remote", "add", "origin", bare)
	if nag := PoolRepoNag(root); !strings.Contains(nag, "never been pushed") || !strings.Contains(nag, "push -u origin main") {
		t.Fatalf("remote but never pushed: got %q", nag)
	}

	gitOut(t, pool, "push", "-u", "origin", "main")
	if nag := PoolRepoNag(root); nag != "" {
		t.Fatalf("clean, pushed pool must be silent, got %q", nag)
	}

	os.WriteFile(filepath.Join(pool, "notes.md"), []byte("x\n"), 0o644)
	if nag := PoolRepoNag(root); !strings.Contains(nag, "1 uncommitted path(s)") || !strings.Contains(nag, "commit") {
		t.Fatalf("dirty: got %q", nag)
	}

	gitOut(t, pool, "add", "-A")
	gitOut(t, pool, "commit", "-q", "-m", "notes")
	if nag := PoolRepoNag(root); !strings.Contains(nag, "1 commit(s) ahead of origin/main") || !strings.Contains(nag, "git -C "+pool+" push") {
		t.Fatalf("ahead: got %q", nag)
	}

	// Runtime state is ignored by the pool repo and must not read as dirty.
	gitOut(t, pool, "push")
	os.WriteFile(DispatchLogPath(root), []byte("x\n"), 0o644)
	os.MkdirAll(DispatchLockPath(root), 0o755)
	if nag := PoolRepoNag(root); nag != "" {
		t.Fatalf("ignored runtime files must not nag, got %q", nag)
	}
}

func TestHook_SessionStart_PoolRepoNagUntilPushed(t *testing.T) {
	root := migratedRoot(t)
	a := newTestAgent(t, root, "architect", true, nil)
	// The peer's own repo-hygiene nag is silenced so only the pool line is under test.
	gitOut(t, a.Dir(), "remote", "add", "origin", "https://example.test/x.git")

	ctx := HookSessionStart(root, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now())
	if !strings.Contains(ctx, "pool directory pool/ has no git remote") {
		t.Fatalf("expected the pool repo nag at session start, got %q", ctx)
	}
	// It sits with the peer's repo warning: above the state digest and handoff,
	// i.e. before anything that is not identity.
	if i, j := strings.Index(ctx, "# Mesh identity"), strings.Index(ctx, "pool directory pool/"); i < 0 || j < i {
		t.Fatalf("nag must follow the identity line: %q", ctx)
	}

	pushedPoolRepo(t, root)
	ctx = HookSessionStart(root, HookPayload{Cwd: a.Dir(), SessionID: "s"}, 1, time.Now())
	if ctx != IdentityLine(root, a) {
		t.Fatalf("clean pushed pool must inject nothing beyond identity, got %q", ctx)
	}

	// Legacy pool with a marker: every peer is told to upgrade.
	legacy := legacyRoot(t)
	b := newTestAgent(t, legacy, "architect", true, nil)
	gitOut(t, b.Dir(), "remote", "add", "origin", "https://example.test/x.git")
	if ctx := HookSessionStart(legacy, HookPayload{Cwd: b.Dir(), SessionID: "s"}, 1, time.Now()); !strings.Contains(ctx, "is not a git repository") || !strings.Contains(ctx, "meshctl pool upgrade") {
		t.Fatalf("legacy pool: expected not-a-repository line, got %q", ctx)
	}
}

func TestDoctor_PoolRepoNagIsAWarning(t *testing.T) {
	root := migratedRoot(t)
	newTestAgent(t, root, "architect", true, nil)
	findings, _ := Doctor(root)
	var got *DoctorFinding
	for i := range findings {
		if strings.Contains(findings[i].Message, "pool directory pool/ has no git remote") {
			got = &findings[i]
		}
	}
	if got == nil {
		t.Fatalf("doctor must report the pool repo nag, got %+v", findings)
	}
	if got.Agent != "pool" || got.Level != "warn" {
		t.Fatalf("pool repo nag must be a pool-level warning, got %+v", *got)
	}

	pushedPoolRepo(t, root)
	findings, _ = Doctor(root)
	if hasFinding(findings, "pool directory pool/") {
		t.Fatalf("clean pushed pool must not be reported, got %+v", findings)
	}

	// Legacy pool: not a repository, no crash.
	legacy := legacyRoot(t)
	newTestAgent(t, legacy, "architect", true, nil)
	if findings, _ := Doctor(legacy); !hasFinding(findings, "is not a git repository") {
		t.Fatalf("legacy pool must report not-a-repository, got %+v", findings)
	}
}

func TestPoolHygiene_PoolRepoNagIsManual(t *testing.T) {
	root := migratedRoot(t)
	newTestAgent(t, root, "architect", true, nil)
	var got *HygieneItem
	items := PoolHygiene(root, time.Now())
	for i := range items {
		if strings.Contains(items[i].Message, "pool directory pool/ has no git remote") {
			got = &items[i]
		}
	}
	if got == nil {
		t.Fatalf("hygiene must list the pool repo nag, got:\n%s", hygieneMessages(items))
	}
	if got.Agent != "pool" || got.Fixable() {
		t.Fatalf("pool repo nag must be a non-fixable pool item, got %+v", *got)
	}

	pool := pushedPoolRepo(t, root)
	if msgs := hygieneMessages(PoolHygiene(root, time.Now())); strings.Contains(msgs, "pool directory pool/") {
		t.Fatalf("clean pushed pool must not be listed, got:\n%s", msgs)
	}
	os.WriteFile(filepath.Join(pool, "glossary.md"), []byte("**Term**: edited\n"), 0o644)
	if msgs := hygieneMessages(PoolHygiene(root, time.Now())); !strings.Contains(msgs, "uncommitted path(s)") {
		t.Fatalf("dirty pool must be listed, got:\n%s", msgs)
	}

	legacy := legacyRoot(t)
	newTestAgent(t, legacy, "architect", true, nil)
	if msgs := hygieneMessages(PoolHygiene(legacy, time.Now())); !strings.Contains(msgs, "is not a git repository") {
		t.Fatalf("legacy pool must be listed as not a repository, got:\n%s", msgs)
	}
}
