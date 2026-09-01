package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hasFinding(fs []DoctorFinding, substr string) bool {
	for _, f := range fs {
		if strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func TestCognitionTrigger_OpencodeNeedsItsCLI(t *testing.T) {
	// opencode distils by spawning `opencode run`. If the binary is absent the
	// spawn fails its PATH lookup and returns false with no output, so the KB
	// simply never fills.
	a := &Agent{Name: "x", dir: t.TempDir(), Harness: HarnessOpencode}
	fs := cognitionTriggerFindings(a)

	if ToolOnPath("opencode") {
		if hasFinding(fs, "not on $PATH") {
			t.Error("opencode is installed; should not warn about PATH")
		}
		return
	}
	if !hasFinding(fs, "not on $PATH") {
		t.Errorf("opencode absent from PATH but no warning: %+v", fs)
	}
}

func TestCognitionTrigger_ClaudeOutsideAllowlistHasNoTrigger(t *testing.T) {
	// Claude Code has no background primitive, so the boundary nudge is its
	// only trigger -- and the nudge is allowlist-gated. An agent outside the
	// allowlist has nothing driving cognition at all.
	t.Setenv("AGENT_MESH_DISTILL_NUDGE_AGENTS", "someone-else")
	a := &Agent{Name: "not-listed", dir: t.TempDir(), Harness: HarnessClaude}
	if fs := cognitionTriggerFindings(a); !hasFinding(fs, "no trigger") {
		t.Errorf("expected a no-trigger warning, got %+v", fs)
	}
}

func TestCognitionTrigger_AllowlistedClaudeIsFine(t *testing.T) {
	t.Setenv("AGENT_MESH_DISTILL_NUDGE_AGENTS", "listed")
	a := &Agent{Name: "listed", dir: t.TempDir(), Harness: HarnessClaude}
	if fs := cognitionTriggerFindings(a); hasFinding(fs, "no trigger") {
		t.Errorf("allowlisted agent should have a trigger, got %+v", fs)
	}
}

func TestSessionSignal_OpencodeNonGitWorkspace(t *testing.T) {
	// The opencode plugin sends no transcript path, so git is the only
	// remaining signal source. Without it, records are empty shells and
	// distillation can only ever report "nothing to distil".
	a := &Agent{Name: "x", dir: t.TempDir(), Harness: HarnessOpencode}
	if fs := sessionSignalFindings(a); !hasFinding(fs, "session records will be empty") {
		t.Errorf("expected an empty-records warning, got %+v", fs)
	}
}

func TestSessionSignal_ClaudeExemptFromGitRequirement(t *testing.T) {
	// Claude Code supplies a transcript, so records have content regardless.
	a := &Agent{Name: "x", dir: t.TempDir(), Harness: HarnessClaude}
	if fs := sessionSignalFindings(a); len(fs) != 0 {
		t.Errorf("claude has a transcript source; expected no finding, got %+v", fs)
	}
}

func TestOpencodeInstructions_MissingClaudeMd(t *testing.T) {
	// opencode discovers AGENTS.md only. Without CLAUDE.md in `instructions`
	// the role file never loads and the agent runs on global config alone.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"),
		[]byte(`{"instructions":["OTHER.md"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Name: "x", dir: dir, Harness: HarnessOpencode}
	if fs := opencodeInstructionsFindings(a); !hasFinding(fs, "does not list CLAUDE.md") {
		t.Errorf("expected a missing-instructions warning, got %+v", fs)
	}
}

func TestOpencodeInstructions_PresentIsClean(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"),
		[]byte(`{"instructions":["CLAUDE.md"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Name: "x", dir: dir, Harness: HarnessOpencode}
	if fs := opencodeInstructionsFindings(a); len(fs) != 0 {
		t.Errorf("expected no finding, got %+v", fs)
	}
}

func TestOpencodeInstructions_MalformedJSONIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{nope`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Name: "x", dir: dir, Harness: HarnessOpencode}
	fs := opencodeInstructionsFindings(a)
	if len(fs) != 1 || fs[0].Level != "error" {
		t.Errorf("malformed config should be an error, got %+v", fs)
	}
}

func TestDoctrineFlag_DetectsNonexistentProjectFlag(t *testing.T) {
	// Doctrine is executed verbatim. A flag that does not exist fails every
	// call that follows it, and the model reports the capability as
	// unavailable rather than the instruction as wrong.
	pool := t.TempDir()
	dir := filepath.Join(pool, DoctrineDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "distiller.md"),
		[]byte("Pass `--project <agent>` on every call.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fs := doctrineFlagFindings(pool); !hasFinding(fs, "--project") {
		t.Errorf("expected a bad-flag warning, got %+v", fs)
	}
}

func TestDoctrineFlag_CleanDoctrinePasses(t *testing.T) {
	pool := t.TempDir()
	dir := filepath.Join(pool, DoctrineDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "distiller.md"),
		[]byte("Pass `--agent <agent>` on every call.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fs := doctrineFlagFindings(pool); len(fs) != 0 {
		t.Errorf("expected no finding, got %+v", fs)
	}
}

func TestDoctrineFlag_NoDoctrineDirIsFine(t *testing.T) {
	if fs := doctrineFlagFindings(t.TempDir()); len(fs) != 0 {
		t.Errorf("missing doctrine dir should be silent, got %+v", fs)
	}
}

func TestDoctrineFlag_WarningAgainstTheFlagIsNotItselfAFlagged(t *testing.T) {
	// Doctrine that names --project in order to warn against it must not trip
	// the check. Otherwise fixing the doctrine leaves the warning permanently
	// in place, which trains you to ignore doctor output.
	pool := t.TempDir()
	dir := filepath.Join(pool, DoctrineDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "distiller.md"),
		[]byte("Pass `--agent <agent>` on every call\n(there is no `--project` flag; passing one fails the call).\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if fs := doctrineFlagFindings(pool); len(fs) != 0 {
		t.Errorf("a warning against the flag should not be flagged, got %+v", fs)
	}
}
