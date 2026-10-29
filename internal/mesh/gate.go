package mesh

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// gate.go — the mesh's only fail-CLOSED hook. The lifecycle hooks (hook.go) are
// advisory: they inject context and exit 0, so a model is free to ignore them.
// This one can REFUSE a tool call. It exists because doctrine delivered as
// injected prose is optional, and the only way to make "route memory through the
// mesh" non-optional is to deny the competing paths so the mesh is the one road
// left (introduced in v1.2.0; the KB-rule gate, ADR-0062, extends it).
//
// Scope of "fail-closed": it denies the patterns it recognises, reliably —
// including by tool NAME alone, so a malformed tool_input cannot smuggle a write
// past it. Anything it does not recognise (an unrelated tool, an edit whose path
// it cannot read) is ALLOWED: a gate that blocked on every parse miss would
// break every turn, which is the opposite of enforcement. The gate removes known
// alternatives; it does not police all writes.
//
// It cannot force a positive act (a model that simply never calls meshctl writes
// nothing for the gate to see). Compelling positive behaviour needs a harness
// the mesh fully controls; this closes the negative space, which is what kills
// the observed drift to Claude's built-in memory.

// GateDecision is the outcome of evaluating one tool call.
type GateDecision struct {
	Deny   bool
	Reason string
	// Ask hands the call to the human: Claude Code prompts for approval with
	// Reason. Harnesses with no ask form treat it as a deny.
	Ask bool
	// Check names the rule that produced the decision (one of the GateCheck*
	// constants, gate_log.go). GatePreToolUse always sets it: an allow no
	// check touched carries GateCheckNone, and the decision log says so.
	Check string
	// Rules are the KB notes a kb-rule deny fired on; the decision log
	// records them so a human can tune a rule that misfires.
	Rules []string
}

// fileEditTools name the tools whose input carries a target path the gate reads:
// Claude Code's, and opencode's (lowercase; V2 write/edit take a `path`).
var fileEditTools = map[string]bool{
	"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
	"write": true, "edit": true, "multiedit": true,
}

// patchTools carry their target paths inside patch text (opencode apply_patch).
var patchTools = map[string]bool{"apply_patch": true, "patch": true}

// GatePreToolUse evaluates a PreToolUse payload against mesh doctrine and
// appends the attributed decision to the deciding agent's log (gate_log.go).
// agentsDir is accepted for workspace-scoped rules (KB rules, peer contracts);
// the path-shaped rules are machine-global, so the gate holds even when pool
// resolution fails.
func GatePreToolUse(agentsDir string, p HookPayload) GateDecision {
	d := gateEvaluate(agentsDir, p)
	if d.Check == "" {
		d.Check = GateCheckNone
	}
	logGateDecision(agentsDir, p, d, time.Now())
	return d
}

// gateEvaluate is the decision without the log. Every return names its check.
func gateEvaluate(agentsDir string, p HookPayload) GateDecision {
	// 1. A delegation (Agent/Task call) from the caller's session. Delegating
	//    work to a state-mutating peer (ADR-0063) is the human's call, every
	//    time: ask. Nothing else about a delegation is the gate's business.
	if delegationTools[p.ToolName] {
		if p.InSession {
			return GateDecision{Deny: true, Check: GateCheckDelegation, Reason: peerWorkDenyDispatch}
		}
		return gateDelegation(agentsDir, p)
	}

	// 1a'. Pool knowledge (ADR-0066), the deny half: git in pool/ is never an
	//      agent's to run, and a pool write with no human at the prompt is
	//      refused outright. Before the peer contract, so a delegated or
	//      consulted peer reads the cause ("no human at the prompt") rather
	//      than the contract's generic write rule. The ask half runs after the
	//      KB rules (5.).
	if d := gatePoolPathDeny(agentsDir, p); d.Deny {
		return d
	}

	// 1b. In-session peer. Consult: the peer answers through `meshctl inbox
	//     respond` and nothing else — its definition grants no edit tools, but
	//     the `tools` list cannot scope Bash, so the no-dispatch / no-write /
	//     no-commit contract is enforced here. Work: writes confined to the
	//     peer's own workspace and workdirs, still no dispatch.
	if p.InSession {
		// A consult may not run the intake triage pass (ADR-0072; triage.go).
		if d := gateTriage(p); d.Deny {
			return d
		}
		reason := ""
		if p.Work {
			reason = deniedPeerWorkAction(agentsDir, p)
		} else {
			reason = deniedPeerAction(p)
		}
		if reason != "" {
			return GateDecision{Deny: true, Check: GateCheckPeer, Reason: reason}
		}
	}

	// 2/3. File writes: inspect the target path(s).
	for _, path := range gateEditPaths(p) {
		if reason := deniedWritePath(agentsDir, path); reason != "" {
			return GateDecision{Deny: true, Check: GateCheckPath, Reason: reason}
		}
	}

	// 2b. Shell writes aimed at the same stores. A file-edit tool is not the
	//     only way to write a file; `echo > intake/x.md` or `sed -i` on one
	//     must meet the same rule, or the gate is decorative.
	if reason := deniedShellWrite(agentsDir, p); reason != "" {
		return GateDecision{Deny: true, Check: GateCheckShellWrite, Reason: reason}
	}

	// 2c. A KB session branch named by hand (ADR-0067 "bypass closed"): only
	//     `meshctl kb` merges, rebases or deletes mesh/kb/<session>, so the
	//     confirmation at finish covers exactly what was promoted.
	if reason := deniedKBBranchCommand(p); reason != "" {
		return GateDecision{Deny: true, Check: GateCheckKBBranch, Reason: reason}
	}

	// 2d. Confirmed knowledge on a peer's main checkout (ADR-0067 "bypass
	//     closed"): outside candidates/ it changes only through `kb finish`.
	if reason := deniedKBMainWrite(agentsDir, p); reason != "" {
		return GateDecision{Deny: true, Check: GateCheckKBMainWrite, Reason: reason}
	}

	// The machine-global rules end here. A hub hook seeing a subagent's call
	// (ADR-0068 amendment, gate_log.go) steps aside for everything below: the
	// subagent's own frontmatter hook, run with the peer flag, owns it.
	if p.StepAside() {
		return GateDecision{}
	}

	// 4. Standing rules from the agent's own KB (kbrules.go): deny once per
	//    session per matching rule, with the rule as the reason. This runs
	//    BEFORE the outbound logic, so a rule deny-once on an outbound call
	//    delays the outbound ask to the retry rather than skipping it.
	if d := gateKBRules(agentsDir, p); d.Deny {
		return d
	}

	// 5. Pool knowledge, the ask half (ADR-0066): a write under pool/ is asked
	//    with the peer's summary as the text, or denied naming the summary
	//    path. After the KB rules, so a rule deny-once delays the ask to the
	//    retry rather than skipping it; never in step-aside mode, since the
	//    summary is the subagent's, not the hub's.
	//    Every ask in steps 5–5e goes through opencodeAskPath: under opencode
	//    it becomes opencode's own permission ask where one is wired
	//    (ADR-0075), else a deny naming why.
	if d := gatePoolPathAsk(agentsDir, p); d.Ask || d.Deny {
		return opencodeAskPath(p, d)
	}

	// 5a. Usage of a gated command (`meshctl kb confirm --help`) runs nothing
	//     the steps 5b–5e ask about (issue #15).
	if meshctlHelpOnly(p) {
		return gateOutbound(agentsDir, p)
	}

	// 5b. Promotion at `kb finish` (ADR-0067, gate_promotion.go): asked with
	//     the peer's summary as the signature; candidates-only passes.
	if d := gatePromotion(agentsDir, p); d.Ask || d.Deny {
		return opencodeAskPath(p, d)
	}

	// 5c. Promotion at `state set` (ADR-0067, gate_state.go): the digest meets
	//     the same ask; no summary or no human at the prompt is a deny.
	if d := gateStateSet(agentsDir, p); d.Ask || d.Deny {
		return opencodeAskPath(p, d)
	}

	// 5d. Bulk confirmation at `kb confirm` (ADR-0070, gate_kb_confirm.go):
	//     asked with the reconcile report's lines for the named paths.
	if d := gateKBConfirm(agentsDir, p); d.Ask || d.Deny {
		return opencodeAskPath(p, d)
	}

	// 5e. Intake triage apply (ADR-0072, triage_apply.go): asked with the
	//     report lines for the named ids; no report is a deny.
	if d := gateTriageApply(agentsDir, p); d.Ask || d.Deny {
		return opencodeAskPath(p, d)
	}

	// 6. Outbound gate (ADR-0068, outbound.go): after the KB rules and never in
	//    step-aside mode. The one fail-closed exception: an unusable outbound
	//    config denies every outbound candidate.
	return gateOutbound(agentsDir, p)
}

// meshctlHelpOnly reports a shell call whose every meshctl invocation only
// prints usage (`--help`, `-h`, or `meshctl help …`), with at least one such
// invocation.
func meshctlHelpOnly(p HookPayload) bool {
	command := shellCommandInput(p)
	if command == "" {
		return false
	}
	seen := false
	for _, words := range ShellSimpleCommands(command) {
		if len(words) == 0 || path.Base(words[0]) != "meshctl" {
			continue
		}
		help := len(words) > 1 && words[1] == "help"
		for _, w := range words[1:] {
			help = help || w == "--help" || w == "-h"
		}
		if !help {
			return false
		}
		seen = true
	}
	return seen
}

// --- pool knowledge (ADR-0066; spec decisions 11, 12; cross-cutting 68) ---
//
// The pool glossary, the local doctrine and the pool config live in the pool
// repo (`pool/`), owned by no single peer. There is no `kb begin`/`kb finish`
// for them and no candidates detour: a peer in its own interactive session
// edits the file, and the gate asks the human with the peer's confirmation
// summary (summary.go) as the text. Everything else is a deny: no summary
// (naming the path), no human at the prompt (dispatch depth, distilling,
// consult or work mode), and shell git whose working directory or path is in
// the pool repo — meshctl commits the pool repo after an allowed write, the
// human pushes, agents never change git state there (read-only git is fine).

// HeadlessNow reports the headless environment signals of the spec's
// cross-cutting decision 68: a dispatch depth set and non-zero, or a distil
// run. Every "no human at the prompt" rule keys on this and the gate mode.
func HeadlessNow() bool {
	return envInt("AGENT_DISPATCH_DEPTH", 0) != 0 || DistillingNow()
}

// noHumanCause names why no human is at the prompt for this call, "" when
// one is (an agent's own interactive session).
func noHumanCause(p HookPayload) string {
	switch {
	case envInt("AGENT_DISPATCH_DEPTH", 0) != 0:
		return "dispatch depth " + strconv.Itoa(envInt("AGENT_DISPATCH_DEPTH", 0))
	case DistillingNow():
		return "a distil run"
	case p.InSession && p.Work:
		return "delegated work in-session"
	case p.InSession:
		return "consult mode in-session"
	}
	return ""
}

// poolTargets is what one tool call does under the pool repo: the
// pool-relative files it writes (file tools, patch tools, shell write
// operations and redirections) and, for a shell command, the first git
// invocation whose working directory or path argument is in the repo. The
// pool's ignored runtime state — the worktrees (peer repos where git is the
// peer's own), the dispatch files — is not pool knowledge and never matches.
type poolTargets struct {
	writes []string
	git    string
}

func (t poolTargets) any() bool { return len(t.writes) > 0 || t.git != "" }

// poolPathTargets resolves the call's targets under the pool repo. agentsDir
// "" (outside any pool) ⇒ none: the rule is inert there.
func poolPathTargets(agentsDir string, p HookPayload) poolTargets {
	var t poolTargets
	if agentsDir == "" {
		return t
	}
	pool := PoolDir(agentsDir)
	rel := func(path string) (string, bool) {
		if !underDir(path, pool) {
			return "", false
		}
		r, err := filepath.Rel(pool, filepath.Clean(path))
		if err != nil {
			return "", false
		}
		r = filepath.ToSlash(r)
		first := r
		if i := strings.Index(r, "/"); i >= 0 {
			first = r[:i]
		}
		switch {
		case first == WorktreesDirName, first == DispatchLogFile, first == DispatchCountFile,
			strings.HasPrefix(first, DispatchLockFile):
			return "", false
		}
		return filepath.ToSlash(filepath.Join(filepath.Base(pool), r)), true
	}
	for _, path := range gateEditPaths(p) {
		if r, ok := rel(path); ok {
			t.writes = append(t.writes, r)
		}
	}
	if !shellTools[p.ToolName] || len(p.ToolInput) == 0 {
		return t
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(p.ToolInput, &in) != nil || in.Command == "" {
		return t
	}
	pathLike := func(w string) bool {
		return strings.Contains(w, "/") || w == "." || w == ".." || strings.HasPrefix(w, "~")
	}
	// The command's own simple commands (shellparse.go): heredoc prose and
	// /dev/null or fd-dup redirections are already gone; relative paths
	// resolve against the cwd the `cd`s left.
	shellWalk(in.Command, p.Cwd, func(words []string, base string) {
		if path.Base(words[0]) != "git" || t.git != "" {
			return
		}
		// Reading the pool's history changes nothing: an agent may check
		// that meshctl committed its write (issue #8).
		if poolReadOnlyGit[gitSubcommand(words)] && !gitWritesOutput(words) {
			return
		}
		resolve := func(w string) string { return resolveShellPath(w, base) }
		dir := base
		hit := false
		for i := 1; i < len(words); i++ {
			w := words[i]
			switch {
			case w == "-C" && i+1 < len(words):
				dir = resolve(words[i+1])
				i++
			case strings.HasPrefix(w, "--work-tree="), strings.HasPrefix(w, "--git-dir="):
				if _, ok := rel(resolve(w[strings.Index(w, "=")+1:])); ok {
					hit = true
				}
			case !strings.HasPrefix(w, "-") && pathLike(w):
				if _, ok := rel(resolve(w)); ok {
					hit = true
				}
			}
		}
		if _, ok := rel(dir); ok || hit {
			t.git = strings.Join(words, " ")
		}
	})
	writes := shellWriteTargets(in.Command, p.Cwd)
	writes = append(writes, interpreterWriteTargets(in.Command, p.Cwd)...)
	seen := map[string]bool{}
	for _, w := range writes {
		if r, ok := rel(w); ok && !seen[r] {
			seen[r] = true
			t.writes = append(t.writes, r)
		}
	}
	return t
}

// poolReadOnlyGit are the git subcommands an agent may run in the pool repo:
// they read history and the working tree and write nothing.
var poolReadOnlyGit = map[string]bool{
	"log": true, "status": true, "diff": true, "show": true, "blame": true,
	"ls-files": true, "rev-parse": true,
}

// gitSubcommand is the subcommand of a git invocation's words: the first word
// after the global options (`-C <dir>`, `-c <key=val>` and bare flags), "" if
// none.
func gitSubcommand(words []string) string {
	for i := 1; i < len(words); i++ {
		switch w := words[i]; {
		case w == "-C" || w == "-c":
			i++
		case strings.HasPrefix(w, "-"):
		default:
			return w
		}
	}
	return ""
}

// gitWritesOutput reports a read-only subcommand told to write a file
// (`git diff --output=<file>`).
func gitWritesOutput(words []string) bool {
	for _, w := range words {
		if w == "--output" || strings.HasPrefix(w, "--output=") {
			return true
		}
	}
	return false
}

// gatePoolPathDeny is the deny half of the pool-knowledge rule: shell git in
// the pool repo, a shell call to the console's pool push or an agent repo's
// Commit and Push buttons, and a pool write with no human at the prompt.
func gatePoolPathDeny(agentsDir string, p HookPayload) GateDecision {
	// The console's Push button is the person pushing (ADR-0079 amendment);
	// an agent calling its endpoint is an agent pushing the pool repo.
	if strings.Contains(shellCommandInput(p), ConsolePoolPushPath) {
		return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: this calls the console's pool push (`" +
			ConsolePoolPushPath + "`) — agents never push the pool repo. The human pushes it, from the console or a terminal."}
	}
	// An agent repo's Commit and Push buttons are the person's too; an agent
	// commits and pushes its own workspace with git.
	for _, path := range []string{ConsoleAgentCommitPath, ConsoleAgentPushPath} {
		if strings.Contains(shellCommandInput(p), path) {
			return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: this calls the console's agent repo buttons (`" +
				path + "`) — those are the human's. Commit and push your own workspace with git."}
		}
	}
	t := poolPathTargets(agentsDir, p)
	if !t.any() {
		return GateDecision{}
	}
	if t.git != "" {
		return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: `" + t.git + "` runs git in the pool repo (`" +
			PoolDirName(agentsDir) + "/`) — agents never change git state there (read-only `log`, `status`, `diff`, `show` are fine). " +
			"meshctl commits the pool repo after each gate-asked write and the human pushes it; edit the pool file itself and let meshctl commit."}
	}
	if cause := noHumanCause(p); cause != "" {
		return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: this writes pool knowledge (`" +
			strings.Join(t.writes, "`, `") + "`) with no human at the prompt (" + cause + ") — pool knowledge changes only where " +
			"a human is at the prompt. Stop, and return the change with your summary (`needs-confirm`) for the human to make " +
			"from an interactive session."}
	}
	return GateDecision{}
}

// gatePoolPathAsk is the ask half: the peer's summary as the ask text, or a
// deny naming the summary path the peer must write.
func gatePoolPathAsk(agentsDir string, p HookPayload) GateDecision {
	t := poolPathTargets(agentsDir, p)
	if len(t.writes) == 0 {
		return GateDecision{}
	}
	targets := "`" + strings.Join(t.writes, "`, `") + "`"
	// Mesh-wide tools moved to the config file (ADR-0066 config split); a
	// charter written before the move still names the identity file. Say
	// where they go instead of asking the human to sign a write upgrade
	// would undo (issue #14).
	for _, w := range t.writes {
		if path.Base(w) == PoolMarkerFile && reTopLevelTools.MatchString(toolInputText(p.ToolInput)) {
			cfg := path.Join(path.Dir(w), PoolConfigFile)
			return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: this writes `tools:` into `" + w +
				"` — mesh-wide tools live in `" + cfg + "` now, not `" + PoolMarkerFile + "`. Write the entry to `" + cfg +
				"` instead, and if your charter or a KB note told you `" + PoolMarkerFile + "`, fix that text too."}
		}
	}
	a := payloadAgent(agentsDir, p)
	if a == nil {
		return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: this writes pool knowledge (" + targets +
			") but the gate cannot tell which peer is writing — run it from your workspace (or with `--agent <you>`), " +
			"so your confirmation summary can be shown to the human."}
	}
	session := p.SessionID
	if session == "" {
		session = ResolveAgentSessionID(a, os.Getpid(), time.Now())
	}
	summaryPath := SummaryPath(a.Dir(), session)
	s, why := summaryProblem(summaryPath)
	if why != "" {
		return GateDecision{Deny: true, Check: GateCheckPoolPath, Reason: "mesh: this writes pool knowledge (" + targets +
			") and needs your confirmation summary first — " + why + " at " + summaryPath + ". Write it with `## Intent` " +
			"(one line, first), `## Grounding` and `## Precedent`, read the confirmation round out in chat, then re-issue " +
			"this write: the gate asks the human with your summary as the text (ADR-0066)."}
	}
	text := RenderAsk(a.Dir(), AskText{
		Check:  GateCheckPoolPath,
		Header: "mesh: pool write — " + targets + " becomes pool knowledge for every peer (ADR-0066). `" + a.Name + "` says:",
		Intent: s.Intent,
		Body:   s.SectionLines(),
		Footer: "You may deny if this was not read to you as a confirmation round, or if a claim is uncited and not marked *assumption*.",
	})
	return GateDecision{Ask: true, Check: GateCheckPoolPath, Reason: text}
}

// reTopLevelTools finds a top-level `tools:` key in YAML text.
var reTopLevelTools = regexp.MustCompile(`(?m)^tools\s*:`)

// delegationTools launch a subagent: Claude Code's Agent (formerly Task) and
// opencode V2's subagent.
var delegationTools = map[string]bool{"Agent": true, "Task": true, "subagent": true}

// gateDelegation asks the human before work is delegated in-session to a
// state-mutating peer; any other delegation is allowed. On opencode V2 the
// ask is opencode's own, on the subagent call (ADR-0076).
func gateDelegation(agentsDir string, p HookPayload) GateDecision {
	var in struct {
		SubagentType string `json:"subagent_type"`
		Agent        string `json:"agent"`
	}
	if len(p.ToolInput) == 0 || json.Unmarshal(p.ToolInput, &in) != nil {
		return GateDecision{}
	}
	st := firstNonEmpty(in.SubagentType, in.Agent)
	if !strings.HasPrefix(st, PeerAgentPrefix) || !strings.HasSuffix(st, PeerWorkSuffix) {
		return GateDecision{}
	}
	peer, err := FindAgent(agentsDir, strings.TrimSuffix(strings.TrimPrefix(st, PeerAgentPrefix), PeerWorkSuffix))
	if err != nil || len(peer.Mutates) == 0 {
		return GateDecision{}
	}
	return opencodeDelegationAsk(p, GateDecision{Ask: true, Check: GateCheckDelegation, Reason: "mesh: `" + peer.Name + "` is a state-mutating peer (mutates: " +
		strings.Join(peer.Mutates, ", ") + ") — approve this delegated work before it runs in this session."}, st)
}

// shellTools name the tools whose input is a shell command line.
var shellTools = map[string]bool{"Bash": true, "bash": true, "shell": true}

// peerDeniedMeshctl are the meshctl subcommands a consulted peer may never
// run: dispatch (ask/send), its KB or handoff, pool state. Matched by
// ShellInvokes' matcher, so flags before the verb do not hide it.
var peerDeniedMeshctl = []string{
	"meshctl ask", "meshctl send", "meshctl msg ask", "meshctl msg send", "meshctl kb",
	"meshctl handoff set", "meshctl pool", "meshctl update", "meshctl session", "meshctl sent ack",
}

// shellInvokesAny reports whether the line invokes any of subcommands
// (ShellInvokes).
func shellInvokesAny(command string, subcommands []string) bool {
	for _, sub := range subcommands {
		if ShellInvokes(command, sub) {
			return true
		}
	}
	return false
}

// rePeerDeniedCmd matches, at the start of a simple command (shellAnyCommand),
// what else a consulted peer may never do (beyond peerDeniedMeshctl): change
// git state or write files. A mention in an argument (`grep "git commit" …`)
// is not a match.
var rePeerDeniedCmd = regexp.MustCompile(`^(?:` +
	`git\s+(?:-C\s+\S+\s+)?(?:commit|push|add|rm|mv|checkout|switch|merge|rebase|reset|stash|cherry-pick|am|apply|tag|worktree)\b` +
	`|(?:tee|sed\s+-[a-zA-Z]*i|mv|cp|rm|touch|truncate|dd|install|ln|rsync|mkdir|chmod|chown)\b` +
	`)`)

// rePeerRedirect finds shell redirections with their target; anything but
// /dev/null is a file write.
var rePeerRedirect = regexp.MustCompile(`>>?\s*([^\s&;|)]+)`)

const peerDenyReason = "mesh: you are a peer consulted in-session — answer only through `meshctl inbox respond <id> --agent <you>` " +
	"(or `inbox decline`). No file writes, commits, KB/handoff writes, pool commands or dispatch (`meshctl ask|send`); " +
	"if another agent's input or a new decision is needed, respond `needs-human` and stop."

// deniedPeerAction returns a reason when an in-session peer's tool call is
// outside its write-back contract: any file-edit or patch tool by name, and a
// shell command that dispatches, mutates git, writes files or the KB.
func deniedPeerAction(p HookPayload) string {
	if fileEditTools[p.ToolName] || patchTools[p.ToolName] {
		return peerDenyReason
	}
	if !shellTools[p.ToolName] || len(p.ToolInput) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(p.ToolInput, &in) != nil || in.Command == "" {
		return ""
	}
	// Judge the command's own arguments (shellparse.go): heredoc bodies and
	// /dev/null or fd-dup redirections are not writes.
	args := ShellOwnArguments(in.Command)
	if shellInvokesAny(args, peerDeniedMeshctl) || shellAnyCommand(args, rePeerDeniedCmd) || shellRedirects(args) {
		return peerDenyReason
	}
	return ""
}

// reShellWriteOp matches a mutating utility at the start of a simple command
// (shellAnyCommand); a redirection is the other write path (shellRedirects).
var reShellWriteOp = regexp.MustCompile(`^(?:tee|sed\s+-[a-zA-Z]*i|mv|cp|rm|touch|truncate|dd|install|ln|rsync)\b`)

// reShellIntakePath matches a request or claim file path under an intake/
// directory, or a competing memory store path, anywhere on the command line.
var reShellIntakePath = regexp.MustCompile(`(?:^|[\s"'=])(?:[^\s"']*/)?` + IntakeDir + `/[^\s"']*\.(?:md|claim)\b|/\.claude/[^\s"']*/memory/`)

// deniedShellWrite returns a reason when a shell command both names a gated
// path and carries a write operation. Reads (`cat intake/x.md`, `grep`) pass.
// The sanctioned meshctl verbs never trip this: they take no redirection and
// the path they write is not on the command line.
func deniedShellWrite(agentsDir string, p HookPayload) string {
	if !shellTools[p.ToolName] || len(p.ToolInput) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(p.ToolInput, &in) != nil || in.Command == "" {
		return ""
	}
	// The command's own arguments only (shellparse.go): a path named in a
	// heredoc body is prose, and `2>/dev/null` / `2>&1` are not writes, so a
	// read-only `cat` of an intake file passes.
	args := ShellOwnArguments(in.Command)
	if !reShellIntakePath.MatchString(args) || !(shellRedirects(args) || shellAnyCommand(args, reShellWriteOp)) {
		return ""
	}
	// Scope the intake rule to this pool when the pool is known: a command
	// writing some unrelated repo's intake/ is not the mesh's business.
	if agentsDir != "" && !strings.Contains(args, "/.claude") {
		if abs := shellIntakePathAbs(args, p.Cwd); abs != "" && !underDir(abs, agentsDir) {
			return ""
		}
	}
	return "mesh: this shell command writes a mesh-owned store (intake/ or a competing memory store). " +
		"Intake is written only through `meshctl send`/`ask`/`inbox respond`/`inbox decline`; " +
		"knowledge through `meshctl kb`/`meshctl memory`."
}

// deniedKBBranchCommand returns a reason when a shell command names a
// mesh/kb/<session> branch in any simple command that is not a `meshctl kb`
// invocation (spec decision 24). It reads the command's own arguments through
// shellparse.go, so a heredoc body, a `cat` of a script that mentions the
// branch, or `meshctl kb finish --session <id>` is not a hit; `git merge
// mesh/kb/x`, `git branch -D mesh/kb/x` and a `bash -c "…"` carrying them are.
func deniedKBBranchCommand(p HookPayload) string {
	command := shellCommandInput(p)
	if command == "" {
		return ""
	}
	for _, words := range ShellSimpleCommands(command) {
		if shellWordsMatch(words, []string{"meshctl", "kb"}) {
			continue
		}
		for _, w := range words {
			if i := strings.Index(w, KBBranchPrefix); i >= 0 {
				branch := w[i:]
				if j := strings.IndexAny(branch, " \t.:^~@"); j >= 0 {
					branch = branch[:j]
				}
				return "mesh: `" + branch + "` is a KB session branch — it is merged, rebased or deleted only by " +
					"`meshctl kb finish` (or `meshctl kb finish --abort`), never by hand, so the confirmation at finish " +
					"covers exactly what was promoted. Commit in the KB worktree, then run `meshctl kb finish`."
			}
		}
	}
	return ""
}

// shellIntakePathAbs returns the first intake path on a command line, resolved
// against cwd when relative ("" if none).
func shellIntakePathAbs(command, cwd string) string {
	m := reShellIntakePath.FindString(command)
	m = strings.TrimLeft(m, " \t\"'=")
	if m == "" || !strings.Contains(m, IntakeDir+"/") {
		return ""
	}
	if !filepath.IsAbs(m) && cwd != "" {
		m = filepath.Join(cwd, m)
	}
	return m
}

// underDir reports whether path is inside dir (both cleaned; no symlink
// resolution — the gate compares what the tool was told, not where it lands).
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gateEditPaths returns the absolute target paths of a file-writing tool call
// (nil for any other tool). opencode passes paths relative to the workspace, so
// a relative path is resolved against the payload cwd — "intake/x.md" must match
// the same rules as "/abs/agent/intake/x.md".
func gateEditPaths(p HookPayload) []string {
	var paths []string
	switch {
	case fileEditTools[p.ToolName]:
		if path := gateFilePath(p.ToolInput); path != "" {
			paths = []string{path}
		}
	case patchTools[p.ToolName]:
		paths = patchFilePaths(p.ToolInput)
	}
	for i, path := range paths {
		if !filepath.IsAbs(path) && p.Cwd != "" {
			paths[i] = filepath.Join(p.Cwd, path)
		}
	}
	return paths
}

// gateFilePath pulls the target path out of a tool_input blob: file_path
// (Claude Write/Edit/MultiEdit), notebook_path (NotebookEdit), filePath (opencode
// V1) or path (opencode V2), tolerating any or none.
func gateFilePath(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		CamelPath    string `json:"filePath"`
		Path         string `json:"path"`
	}
	_ = json.Unmarshal(raw, &in)
	for _, v := range []string{in.FilePath, in.NotebookPath, in.CamelPath, in.Path} {
		if v != "" {
			return v
		}
	}
	return ""
}

var rePatchFile = regexp.MustCompile(`(?m)^\*\*\* (?:(?:Add|Update|Delete) File|Move to): *(.+?)\s*$`)

// patchFilePaths extracts every file an apply_patch-style patch touches, from
// whichever string field carries the patch text.
func patchFilePaths(raw json.RawMessage) []string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var paths []string
	for _, v := range in {
		if s, ok := v.(string); ok {
			for _, m := range rePatchFile.FindAllStringSubmatch(s, -1) {
				paths = append(paths, m[1])
			}
		}
	}
	return paths
}

// deniedWritePath returns a non-empty reason when the path is a competing memory
// store (Claude Code auto-memory) or a hand-written intake file.
// The mesh's OWN stores — <agent>/knowledge/ and <agent>/.memory/ — are not
// matched: ".memory" never contains the "/memory/" segment, and neither carries
// a ".claude"/"intake" segment.
func deniedWritePath(agentsDir, path string) string {
	s := filepath.ToSlash(path)

	// Claude Code's built-in auto-memory: ~/.claude/**/memory/**
	if strings.Contains(s, "/.claude/") && (strings.Contains(s, "/memory/") || strings.HasSuffix(s, "/memory")) {
		return "mesh: this writes Claude Code's built-in memory store, which the mesh replaces. " +
			"Persist through the mesh (`meshctl memory` / `meshctl kb`), not ~/.claude/**/memory/**."
	}
	// Hand-written intake (ADR-0043): intake/ is an ephemeral queue owned by
	// meshctl, never authored by hand. Scoped to this pool when the pool is
	// known — some other repo's intake/ directory is not the mesh's.
	inPool := agentsDir == "" || !filepath.IsAbs(path) || underDir(path, agentsDir)
	if inPool && (strings.Contains(s, "/"+IntakeDir+"/") || strings.HasSuffix(s, "/"+IntakeDir)) {
		return "mesh: intake/ is an ephemeral queue owned by meshctl — do not hand-write it. " +
			"Send work with `meshctl send`/`meshctl ask`; write an outcome back with `meshctl inbox respond`, " +
			"or `meshctl inbox decline --reason … [--redirect <agent>]` if it is not yours."
	}
	return ""
}

// GateUnavailable is the decision when the gate could not evaluate a call at
// all (unreadable stdin, a payload that is not JSON). The gate is the mesh's one
// fail-CLOSED hook, so "could not check" must deny — an attacker-shaped or
// merely broken payload that slipped through as "allow" would make every rule
// above optional.
func GateUnavailable(why string) GateDecision {
	return GateDecision{Deny: true, Check: GateCheckUnavailable, Reason: "mesh gate could not evaluate this tool call (" + why + ") and refuses rather than fail open. " +
		"Re-run the call; if this persists, run `meshctl doctor` in the agent workspace."}
}

// EmitGateDecision writes Claude Code's PreToolUse permission-decision envelope.
// Allow ⇒ nothing written (the default flow proceeds); deny or ask ⇒ that
// envelope carrying its reason. opencode's form is the raw reason (Harness.EmitGate):
// the V2 plugin throws it from tool.execute.before. opencode V1 has no
// equivalent hook and is not gated.
func EmitGateDecision(w io.Writer, d GateDecision) error {
	decision := "deny"
	switch {
	case d.Deny:
	case d.Ask:
		decision = "ask"
	default:
		return nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": d.Reason,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
