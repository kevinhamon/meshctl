package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistillCmdPerHarness(t *testing.T) {
	// Claude Code distils via an in-session subagent, so there is nothing to
	// spawn; opencode has no such primitive and needs a separate process.
	if got := (claudeHarness{}).DistillCmd("p"); got != nil {
		t.Errorf("claude DistillCmd = %v, want nil", got)
	}
	got := (opencodeHarness{}).DistillCmd("do the thing")
	want := []string{"opencode", "run", "do the thing"}
	if len(got) != len(want) {
		t.Fatalf("opencode DistillCmd = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDistillPromptPinsOwnerExplicitly(t *testing.T) {
	// Recall resolves the agent from $AGENT_NAME or the cwd basename, so a
	// prompt that omits the owner silently queries the wrong namespace.
	a := &Agent{Name: "engineer-agent", dir: "/pool/engineer-agent"}
	got := DistillPrompt("/pool", a, "sess-1")

	for _, want := range []string{
		filepath.Join("/pool", DoctrineDir, "distiller.md"),
		"AGENT_NAME is already set to engineer-agent",
		"--agent engineer-agent",
		"session sess-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
	// `--project` does not exist on either recall command; instructing the
	// distiller to pass it makes every recall call fail with "unknown flag".
	// The prompt may still *name* the flag to warn against it -- what must not
	// appear is the applied form.
	if strings.Contains(got, "--project engineer-agent") {
		t.Errorf("prompt tells the distiller to pass a nonexistent flag\n---\n%s", got)
	}
}

func TestDistillPromptInlinesDoctrine(t *testing.T) {
	// The run is headless with cwd inside the agent workspace, and the doctrine
	// sits above it under the pool's .agentmesh/. A headless harness rejects
	// that out-of-tree read, so a prompt that merely cites the path produces a
	// distiller unable to read its own procedure -- which exits looking exactly
	// like a legitimate "nothing to distil".
	pool := t.TempDir()
	dir := filepath.Join(pool, DoctrineDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "## Steps\n1. Mine the session.\n2. Draft candidates.\n"
	if err := os.WriteFile(filepath.Join(dir, "distiller.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := DistillPrompt(pool, &Agent{Name: "x", dir: pool + "/x"}, "s1")
	if !strings.Contains(got, body) {
		t.Errorf("doctrine body not inlined\n---\n%s", got)
	}
	if !strings.Contains(got, "END PROCEDURE") {
		t.Error("inlined doctrine not delimited")
	}
}

func TestDistillPromptFallsBackWhenDoctrineMissing(t *testing.T) {
	// No doctrine on disk still has to yield a usable instruction rather than
	// an empty prompt.
	got := DistillPrompt(t.TempDir(), &Agent{Name: "x", dir: "/x"}, "s1")
	if !strings.Contains(got, "distiller.md") {
		t.Errorf("want a path reference as fallback, got:\n%s", got)
	}
}

func TestDistillPromptDefaultsWindowWithoutSession(t *testing.T) {
	a := &Agent{Name: "x", dir: "/pool/x"}
	if got := DistillPrompt("/pool", a, ""); !strings.Contains(got, "this session") {
		t.Errorf("want a default window, got:\n%s", got)
	}
}

func TestSpawnDistillRefusesToRecurse(t *testing.T) {
	// A distillation run fires the same session hooks as any other session.
	// Without the guard, session-end would spawn a distill that spawns a
	// distill, without bound.
	t.Setenv(distillGuardEnv, "1")
	a := &Agent{Name: "x", dir: t.TempDir(), Harness: HarnessOpencode}
	if SpawnDistill(t.TempDir(), a, "s1") {
		t.Error("spawned a distillation from inside a distillation run")
	}
}

func TestSpawnDistillNoAgent(t *testing.T) {
	if SpawnDistill(t.TempDir(), nil, "s1") {
		t.Error("spawned with no agent")
	}
}

func TestSpawnDistillOncePerSession(t *testing.T) {
	// session.idle can fire repeatedly within one opencode session; each firing
	// must not start another run over the same window. Verified through the
	// marker rather than by spawning: claiming it is what makes the spawn
	// exclusive.
	dir := t.TempDir()
	marker := distillMarkerPath(dir, "s1")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	a := &Agent{Name: "x", dir: dir, Harness: HarnessOpencode}
	if SpawnDistill(t.TempDir(), a, "s1") {
		t.Error("spawned a second run for a session already distilled")
	}
}

func TestDistillMarkerSanitisesSessionID(t *testing.T) {
	// Session ids arrive from the harness and reach the filesystem.
	got := distillMarkerPath("/a", "../../etc/passwd")
	if strings.Contains(got, "..") || strings.Contains(filepath.Base(got), "/") {
		t.Errorf("marker path not sanitised: %s", got)
	}
	if want := filepath.Join("/a", ".memory"); filepath.Dir(got) != want {
		t.Errorf("marker dir = %s, want %s", filepath.Dir(got), want)
	}
}

func TestNudgeSuppressedWhenHarnessAutoDistills(t *testing.T) {
	// opencode now distils at session end, so nudging the model as well would
	// run the same window twice. Claude Code still needs the nudge -- the
	// subagent is its only trigger.
	if !harnessAutoDistills(&Agent{Name: "x", Harness: HarnessOpencode}) {
		t.Error("opencode should auto-distil")
	}
	if harnessAutoDistills(&Agent{Name: "x", Harness: HarnessClaude}) {
		t.Error("claude should not auto-distil; its nudge drives an in-session subagent")
	}
	if harnessAutoDistills(nil) {
		t.Error("nil agent should not auto-distil")
	}
}
