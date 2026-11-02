package mesh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// headlessPool is a migrated pool whose `pool upgrade --check` flags nothing
// for a human: the pool repo is committed and pushed, and config holds body.
func headlessPool(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := migratedRoot(t)
	if body != "" {
		writeGrantsConfig(t, root, body)
	}
	dir := PoolDir(root)
	remote := filepath.Join(t.TempDir(), "pool.git")
	gitOut(t, dir, "init", "--bare", "-q", remote)
	gitOut(t, dir, "remote", "add", "origin", remote)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "config")
	gitOut(t, dir, "push", "-q", "-u", "origin", "main")
	return root
}

func cleanWarnings(t *testing.T, root string) {
	t.Helper()
	plan, err := PlanPoolUpgrade(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := UpgradeCheckWarnings(plan, false, time.Now()); len(w) > 0 {
		t.Fatalf("fixture must be clean, got %v", w)
	}
}

func runHeadless(t *testing.T, root string) (applied bool, err error) {
	t.Helper()
	err = RunHeadlessPoolUpgrade(root, HeadlessUpgradeOptions{Cause: "--headless", Version: "v9.9.9", Now: time.Now()},
		func() error { applied = true; return nil })
	return applied, err
}

func TestHeadlessPoolUpgrade_NoGrantRefusesAndChangesNothing(t *testing.T) {
	root := headlessPool(t, "")
	cleanWarnings(t, root)
	applied, err := runHeadless(t, root)
	if !errors.Is(err, ErrPoolUpgradeNeedsHuman) || !strings.Contains(err.Error(), "no `pool-upgrade` grant") || !strings.Contains(err.Error(), "--headless") {
		t.Fatalf("want a refusal naming the missing grant and the cause, got %v", err)
	}
	if applied {
		t.Fatal("a refused upgrade must not run")
	}
	if _, statErr := os.Stat(PolicyLogPath(root)); statErr == nil {
		t.Fatal("a refusal must not write the policy log")
	}
}

func TestHeadlessPoolUpgrade_GrantAndCleanCheckApplyAndRecord(t *testing.T) {
	root := headlessPool(t, "grants:\n  pool:\n    pool-upgrade: {}\n")
	cleanWarnings(t, root)
	applied, err := runHeadless(t, root)
	if err != nil || !applied {
		t.Fatalf("granted clean upgrade must apply: applied=%v err=%v", applied, err)
	}
	got := PolicyActionsSince(root, PoolGrantPeer, time.Time{})
	if len(got) != 1 || got[0].Grant != GrantPoolUpgrade || !strings.Contains(got[0].Detail, "v9.9.9") || !strings.Contains(got[0].Detail, "step(s)") {
		t.Fatalf("want one pool-upgrade policy line, got %+v", got)
	}
}

func TestHeadlessPoolUpgrade_ApplyErrorIsStillRecorded(t *testing.T) {
	root := headlessPool(t, "grants:\n  pool:\n    pool-upgrade: {}\n")
	boom := errors.New("boom")
	err := RunHeadlessPoolUpgrade(root, HeadlessUpgradeOptions{Cause: "--headless", Now: time.Now()}, func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("want the apply error back, got %v", err)
	}
	if got := PolicyActionsSince(root, PoolGrantPeer, time.Time{}); len(got) != 1 || !strings.Contains(got[0].Detail, "boom") {
		t.Fatalf("a started run is recorded with its outcome, got %+v", got)
	}
}

func TestHeadlessPoolUpgrade_WarningsWaitForAHuman(t *testing.T) {
	root := headlessPool(t, "grants:\n  pool:\n    pool-upgrade: {}\n")
	// An uncommitted pool path is a --check warning (the pool-repo reminder).
	if err := os.WriteFile(filepath.Join(PoolDir(root), "stray.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied, err := runHeadless(t, root)
	if !errors.Is(err, ErrPoolUpgradeNeedsHuman) || !strings.Contains(err.Error(), "waits for a human") || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("want a refusal listing the warnings, got %v", err)
	}
	if applied {
		t.Fatal("an upgrade with warnings must not run")
	}
	if got := PolicyActionsSince(root, PoolGrantPeer, time.Time{}); len(got) != 0 {
		t.Fatalf("nothing recorded on refusal, got %+v", got)
	}
}

func TestHeadlessPoolUpgrade_DoctrineConflictIsAWarning(t *testing.T) {
	plan := PoolUpgradePlan{PoolRoot: t.TempDir(), Conflicts: []string{"doctrine/core.md"}}
	w := UpgradeCheckWarnings(plan, false, time.Now())
	if len(w) != 1 || !strings.Contains(w[0], "doctrine conflict: doctrine/core.md") {
		t.Fatalf("got %v", w)
	}
}

func TestHeadlessPoolUpgrade_PausedRefuses(t *testing.T) {
	root := headlessPool(t, "grants:\n  paused: true\n  pool:\n    pool-upgrade: {}\n")
	applied, err := runHeadless(t, root)
	if !errors.Is(err, ErrPoolUpgradeNeedsHuman) || !strings.Contains(err.Error(), "paused") || applied {
		t.Fatalf("paused must refuse: applied=%v err=%v", applied, err)
	}
}

func TestHeadlessPoolUpgrade_CapRefuses(t *testing.T) {
	root := headlessPool(t, "grants:\n  pool:\n    pool-upgrade: { max_per_day: 1 }\n")
	if _, err := runHeadless(t, root); err != nil {
		t.Fatal(err)
	}
	applied, err := runHeadless(t, root)
	if !errors.Is(err, ErrPoolUpgradeNeedsHuman) || !strings.Contains(err.Error(), "grant cap reached") || applied {
		t.Fatalf("cap must refuse: applied=%v err=%v", applied, err)
	}
}

func TestCLINoHumanCause(t *testing.T) {
	t.Setenv("AGENT_DISPATCH_DEPTH", "")
	t.Setenv(distillGuardEnv, "")
	if c := CLINoHumanCause(false); c != "" {
		t.Fatalf("a plain run has a human at the prompt, got %q", c)
	}
	if c := CLINoHumanCause(true); c != "--headless" {
		t.Fatalf("got %q", c)
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if c := CLINoHumanCause(false); !strings.Contains(c, "dispatch depth") {
		t.Fatalf("got %q", c)
	}
}
