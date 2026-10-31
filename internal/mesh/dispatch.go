package mesh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// notifyInterrupt returns a context cancelled on SIGINT/SIGTERM. Tests replace
// it to simulate an interrupt without signalling the test process.
var notifyInterrupt = func(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// The headless system prompt. Kept verbatim from the original dispatch script — the
// "you may not dispatch; on a new consequential decision set needs-human and
// stop" contract is budget/safety-critical.
const headlessSystemPrompt = "You are being invoked HEADLESSLY by the '%s' agent to process a single intake request — there is no human in this session. Do ONLY this: locate the referenced request in this workspace's intake/ directory, process it, and write the outcome back with `meshctl inbox respond <id> --status <answered|needs-info|needs-human> --response-file -` (response markdown on stdin) — intake files cannot be edited directly. If the request is not within your role or domain, do NOT answer it: run `meshctl inbox decline <id> --reason \"…\" --redirect <agent>` naming the agent it belongs to (see `meshctl agent list`; omit --redirect if none fits), then stop. Do not start unrelated work. Do not commit, push, deploy, or touch external systems. This dispatch is pre-authorized — do not wait for interactive confirmation. You MAY NOT dispatch to, or run meshctl ask against any other agent. If answering would require another agent's input, or a genuinely NEW consequential decision not covered by an accepted ADR, do NOT attempt it and do NOT fabricate: respond with status 'needs-human', name exactly which agent/decision and the specific question, write your recommendation, then stop. The human will drive the next step."

// AskOptions parameterizes a dispatch.
type AskOptions struct {
	Type     string
	Prompt   string
	From     string
	Priority string
	Related  []string
	Detach   bool

	// now + runner are injected for tests. runner defaults to the real
	// `claude -p` invocation.
	Now    time.Time
	Runner DispatchRunner
	Stdout io.Writer
	Stderr io.Writer
}

// DispatchRunner runs the headless peer. Returns the process exit code (124 for
// timeout, matching `timeout`). Injectable so tests can exercise the guards
// without spawning claude.
type DispatchRunner func(ctx context.Context, targetDir, msg, sys, model string, env []string, stdout, stderr io.Writer) int

// Ask is the preapprovable path: validate → write intake (audit trail) → claim
// → run all anti-cascade guards → dispatch the peer headlessly → block for the
// answer. Refuses non-dispatchable (state-mutating) targets.
func Ask(agentsDir string, target *Agent, o AskOptions) (*Request, int, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.From == "" {
		o.From = CallerName()
	}

	// Refuse state-mutating targets as data, not prose.
	if !target.IsDispatchable() {
		reason := "not dispatchable"
		if len(target.Mutates) > 0 {
			reason = fmt.Sprintf("mutates %v — state-mutating agents stay human-triggered", target.Mutates)
		}
		return nil, 1, fmt.Errorf("refusing to dispatch %q: %s. Use `meshctl send %s %s \"…\"` and let the human trigger it",
			target.Name, reason, target.Name, o.Type)
	}

	// Step 2: write the validated intake file (audit trail).
	req, err := WriteRequest(target, o.Type, o.Prompt, o.From, o.Priority, o.Related, now)
	if err != nil {
		return nil, 1, err
	}

	caller := o.From
	depth := envInt("AGENT_DISPATCH_DEPTH", 0)

	// Detach: write-only, return the id to poll later. No dispatch ⇒ no guards.
	if o.Detach {
		fmt.Fprintf(o.Stdout, "wrote intake %s (detached — not dispatched). Poll: meshctl inbox --agent %s\n", req.ID, target.Name)
		return req, 0, nil
	}

	// Guard 2: depth (a dispatched peer must not dispatch).
	if depth >= 1 {
		dispatchLog(agentsDir, caller, target.Name, depth, now, "BLOCKED reason=depth")
		return req, ExitDepth, coded(ExitDepth,
			"BLOCKED — dispatch depth %d>=1. A dispatched agent may not dispatch again.\n"+
				"  Resolve by returning 'needs-human' in the request file (name the agent + question); the human drives the next hop.", depth)
	}

	// Guard 4: hourly cap (bounds fan-out / runaway). Charged NOW, before the
	// lock wait, so N callers queuing at max-1 cannot all pass; refunded below
	// if this dispatch never runs.
	recent, err := hourlyCount(agentsDir, now)
	if err != nil {
		return req, 1, err
	}
	if recent >= hourlyMax() {
		dispatchLog(agentsDir, caller, target.Name, depth, now, "BLOCKED reason=hourly-cap recent=%d max=%d", recent, hourlyMax())
		return req, ExitHourlyCap, coded(ExitHourlyCap,
			"BLOCKED — hourly dispatch cap reached (%d/%d in the last hour).\n"+
				"  This is a budget circuit-breaker. Wait, raise AGENT_DISPATCH_HOURLY_MAX, or resolve remaining requests with the human.", recent, hourlyMax())
	}
	_ = recordDispatch(agentsDir, now)
	ran := false
	defer func() {
		if !ran {
			unrecordDispatch(agentsDir, now)
		}
	}()

	// An interrupt (Ctrl-C, SIGTERM) must release the lock and the claim, not
	// leave the pool blocked for the stale window. The context is cancelled on
	// signal; the lock wait and the child both watch it.
	sigCtx, stopSignals := notifyInterrupt(context.Background())
	defer stopSignals()

	// Guard 3: single-flight lock (hard; mkdir is atomic).
	lock, err := acquireDispatchLock(sigCtx, agentsDir, caller, target.Name, os.Getpid(), now)
	if err != nil {
		if sigCtx.Err() != nil {
			dispatchLog(agentsDir, caller, target.Name, depth, now, "ABORTED reason=interrupted phase=lock-wait")
			return req, 130, coded(130, "interrupted while waiting for the dispatch lock; %s was not dispatched", target.Name)
		}
		var ce *CodedError
		if errors.As(err, &ce) {
			// Distinct from the old "lock-held": the caller waited the full
			// window, so this is a genuine timeout rather than an instant
			// refusal. Logged separately so the two are never conflated when
			// reading back why a peer was not consulted.
			dispatchLog(agentsDir, caller, target.Name, depth, now,
				"TIMEOUT reason=lock-wait-exceeded waited=%ds", lockWaitSec())
			return req, ce.Code, ce
		}
		return req, 1, err
	}
	defer lock.release()
	if lock.waitedSec > 0 {
		dispatchLog(agentsDir, caller, target.Name, depth, now, "QUEUED waited=%ds", lock.waitedSec)
	}

	// Claim the target request for this dispatched session so a concurrent
	// interactive session can't double-process it.
	dispatchSession := fmt.Sprintf("dispatch-%d-%s", os.Getpid(), now.UTC().Format("150405"))
	_, _ = ClaimRequest(req.path, dispatchSession, os.Getpid(), now)
	defer func() { _ = ReleaseClaim(req.path, dispatchSession, time.Now()) }()

	ran = true
	dispatchLog(agentsDir, caller, target.Name, depth, now, "DISPATCH id=%s", req.ID)

	// Build the child invocation.
	msg := fmt.Sprintf("process intake request %s", req.ID)
	sys := fmt.Sprintf(headlessSystemPrompt, caller)
	model := dispatchModel()
	if model == "" && target.Model != "" && target.Model != "default" {
		model = target.Model
	}
	childEnv := append(os.Environ(),
		fmt.Sprintf("AGENT_DISPATCH_DEPTH=%d", depth+1),
		"AGENT_NAME="+target.Name,
		// The child's meshctl calls act as the dispatch session, so it can
		// release/re-claim the request we hold instead of being refused.
		"CLAUDE_SESSION_ID="+dispatchSession,
		// opencode reads an inline config from this env (V1 and V2): scope the
		// headless run to reads + the meshctl write-back verbs, the same
		// policy the Claude allowlist expresses (see dispatchArgv).
		"OPENCODE_CONFIG_CONTENT="+opencodeDispatchConfig,
	)

	runner := o.Runner
	if runner == nil {
		runner = realDispatch
	}
	ctx, cancel := context.WithTimeout(sigCtx, time.Duration(timeoutSec())*time.Second)
	defer cancel()

	rc := runner(ctx, target.Dir(), msg, sys, model, childEnv, o.Stdout, o.Stderr)
	if sigCtx.Err() != nil {
		dispatchLog(agentsDir, caller, target.Name, depth, now, "ABORTED reason=interrupted id=%s", req.ID)
		fmt.Fprintf(o.Stderr, "meshctl ask: interrupted; %q was stopped and the request is left %s for a human.\n", target.Name, req.Status)
		return req, 130, nil
	}
	dispatchLog(agentsDir, caller, target.Name, depth, now, "DONE rc=%d", rc)

	if rc == 124 || ctx.Err() == context.DeadlineExceeded {
		fmt.Fprintf(o.Stderr, "meshctl ask: %q timed out (%ds). Re-check the request file; escalate to human if unanswered.\n", target.Name, timeoutSec())
		rc = 124
	} else if rc != 0 {
		fmt.Fprintf(o.Stderr, "meshctl ask: %q exited %d. Re-check the request file; escalate to human if unanswered.\n", target.Name, rc)
	}
	fmt.Fprintf(o.Stdout, "meshctl ask: done. Outcome in %s (status + response).\n", req.path)
	return req, rc, nil
}

// dispatchArgv builds the headless invocation for a target's harness.
// Claude Code: `claude -p` with the scoped allowlist + appended system
// prompt. opencode V1: `opencode run --pure --auto` (no plugins → no hook recursion /
// mid-dispatch capture; auto-approve for non-interactive). A random port per run
// means dispatch never collides with a live TUI's server. opencode V2 has no
// --pure: `opencode run --standalone --auto` runs a private server (off the user's
// shared background service) that inherits AGENT_DISPATCH_DEPTH, on which the V2
// plugin drops to doctrine + gate only. Either way the system prompt is prepended
// to the message (opencode run has no --append-system-prompt). ocMajor is the
// installed opencode major (ignored for claude).
func dispatchArgv(harness string, ocMajor int, msg, sys, model string) (name string, args []string) {
	switch harness {
	case HarnessOpencode:
		args = []string{"run", "--pure", "--auto"}
		if ocMajor >= 2 {
			args = []string{"run", "--standalone", "--auto"}
		}
		if strings.Contains(model, "/") { // opencode wants provider/model; skip a bare alias
			args = append(args, "--model", model)
		}
		args = append(args, sys+"\n\n"+msg)
		return "opencode", args
	default: // claude
		// Reads plus the meshctl write-back verbs, nothing else. A dispatched
		// peer answers through `meshctl inbox respond`; it has no business
		// editing files, so Edit/Write are not granted and the permission mode
		// is the default (in -p mode an un-allowlisted tool is simply denied).
		args = []string{"-p", msg,
			"--permission-mode", "default",
			"--allowedTools", "Read", "Grep", "Glob",
			"Bash(meshctl inbox respond:*)", "Bash(meshctl inbox decline:*)", "Bash(meshctl agent list:*)",
			"Bash(meshctl memory recall:*)", "Bash(meshctl intuition recall:*)",
			"--append-system-prompt", sys,
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		return "claude", args
	}
}

// opencodeDispatchConfig is the inline opencode config a dispatched run gets
// through OPENCODE_CONFIG_CONTENT: file edits denied, shell denied except the
// meshctl verbs a peer needs to answer. `--auto` only auto-approves "ask"
// decisions; a "deny" stays denied.
const opencodeDispatchConfig = `{"permission":{"edit":"deny","webfetch":"deny","bash":{"*":"deny",` +
	`"meshctl inbox respond*":"allow","meshctl inbox decline*":"allow","meshctl agent list*":"allow",` +
	`"meshctl memory recall*":"allow","meshctl intuition recall*":"allow"}}}`

// realDispatch runs the target's harness headlessly in its workspace to process
// the intake request. The runtime is chosen by the TARGET agent's manifest harness
// — claude → `claude -p`, opencode → `opencode run` — so dispatch works
// in an opencode mesh, not just a Claude Code one.
func realDispatch(ctx context.Context, targetDir, msg, sys, model string, env []string, stdout, stderr io.Writer) int {
	harness, ocMajor := HarnessClaude, 0
	if a, err := LoadAgent(filepath.Join(targetDir, ManifestName)); err == nil {
		harness = a.HarnessName()
		if oc, ok := HarnessFor(a).(opencodeHarness); ok {
			ocMajor = oc.Major()
		}
	}
	name, args := dispatchArgv(harness, ocMajor, msg, sys, model)

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = targetDir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Own process group: a timeout or interrupt kills the harness AND the MCP
	// servers it spawned, instead of orphaning them.
	setProcGroup(cmd)
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return 124
	}
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	fmt.Fprintf(stderr, "meshctl ask: failed to launch %s (%s harness): %v\n", name, harness, err)
	return 127
}
