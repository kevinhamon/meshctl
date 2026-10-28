package mesh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPoolPush_PreviewAndPush(t *testing.T) {
	root := migratedRoot(t)
	pool := pushedPoolRepo(t, root)
	if st := PoolPushPreview(root); st.Ready {
		t.Fatalf("a pushed pool has nothing to push, got %+v", st)
	}

	os.WriteFile(filepath.Join(pool, "notes.md"), []byte("x\n"), 0o644)
	if st := PoolPushPreview(root); st.Ready {
		t.Fatalf("uncommitted changes: not ready, got %+v", st)
	}
	if _, _, err := PoolPush(context.Background(), root); err == nil {
		t.Fatalf("push with uncommitted changes must refuse")
	}

	gitOut(t, pool, "add", "-A")
	gitOut(t, pool, "commit", "-q", "-m", "notes")
	st := PoolPushPreview(root)
	if !st.Ready || st.Upstream != "origin/main" || len(st.Commits) != 1 || !strings.HasSuffix(st.Commits[0], " notes") {
		t.Fatalf("ahead by one: got %+v", st)
	}
	if st.Finding != PoolRepoNag(root) {
		t.Fatalf("finding must be the doctor line: %q vs %q", st.Finding, PoolRepoNag(root))
	}

	outcome, out, err := PoolPush(context.Background(), root)
	if err != nil || outcome != PushPushed {
		t.Fatalf("push: %v %s %s", err, outcome, out)
	}
	if nag := PoolRepoNag(root); nag != "" {
		t.Fatalf("pushed pool must be silent, got %q", nag)
	}
}

func TestPoolPush_RejectedWhenRemoteMovedOn(t *testing.T) {
	root := migratedRoot(t)
	pool := pushedPoolRepo(t, root)
	bare := strings.TrimSpace(gitOut(t, pool, "remote", "get-url", "origin"))

	// Someone else pushed from another clone.
	other := filepath.Join(t.TempDir(), "other")
	gitOut(t, ".", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "theirs.md"), []byte("y\n"), 0o644)
	gitOut(t, other, "add", "-A")
	gitOut(t, other, "commit", "-q", "-m", "theirs")
	gitOut(t, other, "push", "-q")

	os.WriteFile(filepath.Join(pool, "mine.md"), []byte("x\n"), 0o644)
	gitOut(t, pool, "add", "-A")
	gitOut(t, pool, "commit", "-q", "-m", "mine")
	outcome, out, err := PoolPush(context.Background(), root)
	if err != nil || outcome != PushRejected {
		t.Fatalf("want rejected (never forced), got %v %s %s", err, outcome, out)
	}
}

func TestGate_ConsolePoolPushDenied(t *testing.T) {
	dir, a := poolGateFixture(t)
	for _, cmd := range []string{
		"curl -X POST 'http://127.0.0.1:7717/api/pool/push?pool=x&token=t'",
		"curl -s -XPOST http://localhost:7717" + ConsolePoolPushPath,
	} {
		d := GatePreToolUse(dir, bash(a, "sess-1", cmd))
		if !d.Deny || d.Check != GateCheckPoolPath || !strings.Contains(d.Reason, "never push the pool repo") {
			t.Fatalf("%s: want deny, got %+v", cmd, d)
		}
	}
	if d := GatePreToolUse(dir, bash(a, "sess-1", "curl -s http://127.0.0.1:7717/api/snapshot")); d.Deny {
		t.Fatalf("other console reads stay allowed, got %+v", d)
	}
}
