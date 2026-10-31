package mesh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Background distillation — the enforcement half of the cognition loop.
//
// The boundary nudge (hook.go) ASKS the model to distil. A capable
// model obliges; a weaker one reads the nudge and carries on, so the KB never
// fills and the mesh degrades to a pile of empty knowledge/ trees. Distillation
// therefore cannot be discretionary. This runs it at session end regardless of
// what the model decided, for any harness that exposes a background primitive.
//
// distiller.md defines the procedure harness-neutrally and defers the "how" to
// each harness. Claude Code binds it to an in-session subagent; opencode had no
// binding at all, which is why cognition only ever happened there when the
// model felt like it.

// distillGuardEnv marks a process that IS a distillation run. The spawned
// harness process fires the same session hooks as any other session, so without
// this a session-end distill spawns a distill that spawns a distill, forever.
const distillGuardEnv = "AGENT_MESH_DISTILLING"

// DistillingNow reports whether this process is itself a distillation run.
func DistillingNow() bool { return os.Getenv(distillGuardEnv) == "1" }

// distillMarkerPath is the once-per-session sentinel. session.idle can fire
// repeatedly in a single opencode session; without this every idle period would
// start another distillation of the same window.
func distillMarkerPath(agentDir, sessionID string) string {
	sid := reSessionSanitize.ReplaceAllString(sessionID, "_")
	if sid == "" {
		sid = "nosession"
	}
	return filepath.Join(agentDir, ".memory", ".distilled-"+sid)
}

// DistillPrompt is the harness-neutral instruction handed to a background run.
// The doctrine holds the procedure; this points at it and pins the owner.
//
// The owner is passed explicitly on purpose: recall resolves the agent from
// $AGENT_NAME or the cwd basename, so a distiller left to infer it queries the
// wrong namespace and silently returns nothing (distiller.md, Inputs).
func DistillPrompt(poolRoot string, a *Agent, sessionID string) string {
	window := "this session"
	if sessionID != "" {
		window = "session " + sessionID
	}
	header := fmt.Sprintf(
		"Run the background distillation procedure below.\n\n"+
			"agent: %s\nwindow: %s\n\n"+
			"AGENT_NAME is already set to %s in your environment, so `meshctl intuition "+
			"recall` and `meshctl memory recall` resolve the right namespace with no extra "+
			"flags. If you pass one anyway it is `--agent %s`; there is no `--project` flag "+
			"and using it fails the call outright.\n\n"+
			"You are running headlessly with no user to ask. Work only from what this "+
			"workspace and meshctl give you; ignore any step of the procedure that needs a "+
			"tool you do not have rather than abandoning the whole run. Draft candidates "+
			"only — never promote consequential knowledge on your own. If nothing durable "+
			"came out of this window, write nothing and exit.\n\n",
		a.Name, window, a.Name, a.Name)

	// Inline the doctrine rather than pointing at it. The run is headless with
	// cwd set to the agent workspace, and the doctrine lives one level up under
	// the pool directory — which a headless harness auto-rejects as an
	// out-of-tree read. Pointing at the path yields a distiller that cannot
	// read its own procedure and exits looking like a clean no-op.
	path := filepath.Join(DoctrinePath(poolRoot), "distiller.md")
	body, err := os.ReadFile(path)
	if err != nil {
		// Fall back to the reference; better than no instruction at all, and
		// the log will show the read failure if the harness also refuses it.
		return header + "Procedure: " + path
	}
	return header + "--- BEGIN PROCEDURE (" + path + ") ---\n" +
		string(body) + "\n--- END PROCEDURE ---"
}

// SpawnDistill starts the agent's distillation off the main thread and returns
// immediately, reporting whether a run was started.
//
// Fail-open throughout: any error means no distillation, never a failed session.
func SpawnDistill(poolRoot string, a *Agent, sessionID string) bool {
	if a == nil || DistillingNow() {
		return false
	}
	return spawnDistillArgv(a, sessionID, HarnessFor(a).DistillCmd(DistillPrompt(poolRoot, a, sessionID)))
}

// SpawnPeerDistill distils a peer's in-session window (a subagent that ran
// under a caller's Claude Code session) into the PEER's own KB. The
// in-session subagent has no session end of its own and the caller's session
// end distils the caller, so without this a correction made to a consulted or
// delegated peer is never learned by it.
//
// The caller is Claude Code, so the run is `claude -p` whatever the peer's own
// harness — and deliberately not claudeHarness.DistillCmd, which stays nil so
// a normal Claude session keeps its in-session distiller. The allowlist covers
// the distiller procedure and nothing more: reads, the recall/consolidate
// verbs, and writes confined to the peer's candidates and pending decisions.
func SpawnPeerDistill(poolRoot string, a *Agent, sessionID string) bool {
	if a == nil || DistillingNow() {
		return false
	}
	return spawnDistillArgv(a, sessionID, peerDistillArgv(DistillPrompt(poolRoot, a, sessionID)))
}

// peerDistillArgv is the headless Claude Code invocation for a peer distill.
// Path rules are relative to the run's cwd, the peer's workspace.
func peerDistillArgv(prompt string) []string {
	return []string{"claude", "-p", prompt,
		"--permission-mode", "default",
		"--allowedTools", "Read", "Grep", "Glob",
		"Edit(./knowledge/candidates/**)", "Write(./knowledge/candidates/**)",
		"Edit(./knowledge/" + PendingDecisionsFile + ")", "Write(./knowledge/" + PendingDecisionsFile + ")",
		"Bash(meshctl memory recall:*)", "Bash(meshctl memory sessions:*)",
		"Bash(meshctl intuition recall:*)", "Bash(meshctl kb consolidate:*)",
	}
}

// spawnDistillArgv starts argv detached in the agent's workspace, once per
// session window, logging to DistillLogPath. Fail-open: false on any error.
func spawnDistillArgv(a *Agent, sessionID string, argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return false // harness CLI not installed — nothing to spawn
	}

	// Once per session, claimed before spawning so concurrent idle events race
	// to the marker rather than to the model.
	marker := distillMarkerPath(a.Dir(), sessionID)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return false
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false // already distilled this session
	}
	_ = f.Close()

	cmd := exec.Command(bin, argv[1:]...)
	cmd.Dir = a.Dir()
	cmd.Env = append(os.Environ(), distillGuardEnv+"=1", "AGENT_NAME="+a.Name)
	// Capture output to a log rather than discarding it. This runs unattended
	// and nobody is watching it fail; a distiller that errors on startup and a
	// distiller that correctly found nothing durable are indistinguishable
	// otherwise, and the first looks exactly like the cognition gap this whole
	// mechanism exists to close.
	cmd.Stdin = nil
	if log, err := os.OpenFile(DistillLogPath(a.Dir(), sessionID),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = log, log
		defer log.Close() // the child keeps its own dup of the fd
	}
	// Its own process group, so the run outlives the hook process (which exits
	// in milliseconds) instead of being torn down with the parent's group.
	// Platform-specific (POSIX only) — see detach_unix.go / detach_other.go.
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(marker) // let a later idle retry
		return false
	}
	_ = cmd.Process.Release()
	return true
}

// DistillLogPath is where a background run's output lands. Kept per session so
// a failure is attributable to the window that produced it.
func DistillLogPath(agentDir, sessionID string) string {
	sid := reSessionSanitize.ReplaceAllString(sessionID, "_")
	if sid == "" {
		sid = "nosession"
	}
	return filepath.Join(agentDir, ".memory", "distill-"+sid+".log")
}

// harnessAutoDistills reports whether the harness runs distillation itself at
// session end, in which case the in-session boundary nudge is redundant and
// would only duplicate the run. Probed with an empty prompt: the argv shape
// does not depend on the prompt's contents.
func harnessAutoDistills(a *Agent) bool {
	return a != nil && len(HarnessFor(a).DistillCmd("")) > 0
}
