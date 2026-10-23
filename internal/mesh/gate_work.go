package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// gate_work.go — the in-session peer gate in WORK mode (ADR-0063). A peer a
// hub session delegates owned work to may edit and commit, but only in its own
// workspace (or its delegated-work worktree) and its declared workdirs. It may
// never write the caller's workspace or another agent's, and never dispatch.
//
// Shell commands are judged best-effort, as everywhere in the gate: the write
// targets a command line names (`cd`, `git -C`, redirections, path arguments)
// are resolved and checked; a write whose target cannot be resolved is allowed
// only when the session's cwd is itself an allowed root. The deny list plus
// fail-open on what the gate cannot read is the contract.

// peerWorkDeniedMeshctl is the subset of peerDeniedMeshctl a delegated peer
// still may not run: dispatch and pool-level state (plus merging a work
// branch). KB, handoff and git are its own. Matched by ShellInvokes' matcher.
var peerWorkDeniedMeshctl = []string{
	"meshctl ask", "meshctl send", "meshctl msg ask", "meshctl msg send",
	"meshctl pool", "meshctl update", "meshctl session", "meshctl work merge",
}

// reWorkWriteOp matches a simple command that can change files or git state
// (shellAnyCommand).
var reWorkWriteOp = regexp.MustCompile(`^(?:` +
	`git\s+(?:-C\s+\S+\s+)?(?:commit|push|add|rm|mv|checkout|switch|merge|rebase|reset|stash|cherry-pick|am|apply|tag|worktree|restore|clean|pull)\b` +
	`|(?:tee|sed\s+-[a-zA-Z]*i|mv|cp|rm|touch|truncate|dd|install|ln|rsync|mkdir|chmod|chown|patch)\b` +
	`)`)

var (
	reShellCd    = regexp.MustCompile(`(?:^|[;&|(]\s*)cd\s+("[^"]+"|'[^']+'|[^\s;&|)]+)`)
	reShellGitC  = regexp.MustCompile(`\bgit\s+-C\s+("[^"]+"|'[^']+'|\S+)`)
	reShellToken = regexp.MustCompile(`(?:^|\s)("[^"]*"|'[^']*'|[^\s;&|()<>]+)`)
	reGitPush    = regexp.MustCompile(`\bgit\s+(?:-C\s+("[^"]+"|'[^']+'|\S+)\s+)?push\b([^;&|]*)`)
)

const peerWorkDenyDispatch = "mesh: you are a peer doing delegated work in-session — you may not dispatch or touch pool state " +
	"(`meshctl ask|send|pool|update|session|work merge`, Agent). If the work needs another agent or a new decision, " +
	"respond `needs-human` naming it, give your recommendation, and stop."

// peerWorkRoots are the directories a delegated peer may write: its
// workspace, its worktrees (delegated-work and `kb begin` ones alike), and its
// declared workdirs.
func peerWorkRoots(agentsDir string, peer *Agent) []string {
	roots := []string{peer.Dir()}
	if agentsDir != "" {
		wt := WorktreesPath(agentsDir)
		if entries, err := os.ReadDir(wt); err == nil {
			for _, e := range entries {
				p := filepath.Join(wt, e.Name())
				if e.IsDir() && AgentFromWorktreePath(agentsDir, p) == peer.Name {
					roots = append(roots, p)
				}
			}
		}
	}
	return append(roots, peer.WorkdirPaths()...)
}

func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if underDir(path, r) {
			return true
		}
	}
	return false
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// resolveShellPath resolves a command-line path against base, expanding ~.
func resolveShellPath(s, base string) string {
	s = unquote(s)
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, strings.TrimPrefix(s, "~"))
		}
	}
	if !filepath.IsAbs(s) && base != "" {
		s = filepath.Join(base, s)
	}
	return filepath.Clean(s)
}

func peerWorkWriteReason(peer *Agent, target string) string {
	return "mesh: you are `" + peer.Name + "` doing delegated work in-session — writes are allowed only in your workspace " +
		"(`" + peer.Dir() + "` or its delegated-work worktree) and your declared workdirs; `" + target + "` is outside them. " +
		"For shell writes, name the target explicitly (`cd <dir> && …`, `git -C <dir> …`)."
}

// deniedPeerWorkAction returns a reason when a delegated peer's tool call
// leaves its write scope or dispatches. "" ⇒ fall through to the rest of the
// gate (path stores, intake, KB rules), which still applies.
func deniedPeerWorkAction(agentsDir string, p HookPayload) string {
	peer := payloadAgent(agentsDir, p)
	if peer == nil {
		// Cannot establish the scope: hold the strict consult contract.
		return deniedPeerAction(p)
	}
	if p.ToolName == "Agent" || p.ToolName == "Task" {
		return peerWorkDenyDispatch
	}
	roots := peerWorkRoots(agentsDir, peer)
	for _, path := range gateEditPaths(p) {
		if !underAny(path, roots) {
			return peerWorkWriteReason(peer, path)
		}
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
	// The command's own arguments (shellparse.go): heredoc prose naming the
	// caller's path is not a write to it, and /dev/null or fd-dup
	// redirections are not writes at all.
	cmd := ShellOwnArguments(in.Command)
	if shellInvokesAny(cmd, peerWorkDeniedMeshctl) {
		return peerWorkDenyDispatch
	}
	if reason := deniedDefaultBranchPush(peer, cmd, p.Cwd); reason != "" {
		return reason
	}
	writes := shellAnyCommand(cmd, reWorkWriteOp)
	for _, m := range rePeerRedirect.FindAllStringSubmatch(cmd, -1) {
		writes = writes || m[1] != "/dev/null"
	}
	if !writes {
		return ""
	}

	// Explicit targets: each `cd` moves the base the rest resolve against.
	base := p.Cwd
	explicit := false
	for _, m := range reShellCd.FindAllStringSubmatch(cmd, -1) {
		base = resolveShellPath(m[1], base)
		explicit = true
		if !underAny(base, roots) {
			return peerWorkWriteReason(peer, base)
		}
	}
	for _, m := range reShellGitC.FindAllStringSubmatch(cmd, -1) {
		dir := resolveShellPath(m[1], base)
		explicit = true
		if !underAny(dir, roots) {
			return peerWorkWriteReason(peer, dir)
		}
	}
	for _, m := range rePeerRedirect.FindAllStringSubmatch(cmd, -1) {
		if m[1] == "/dev/null" || strings.HasPrefix(m[1], "&") {
			continue
		}
		t := resolveShellPath(m[1], base)
		explicit = true
		if !underAny(t, roots) && !outsidePool(agentsDir, t) {
			return peerWorkWriteReason(peer, t)
		}
	}
	// Path arguments that name somewhere in the pool (another agent, the
	// caller) are refused; paths outside the pool (/tmp) are not the mesh's.
	for _, m := range reShellToken.FindAllStringSubmatch(cmd, -1) {
		tok := unquote(m[1])
		if !(strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~/") || strings.HasPrefix(tok, "../") || strings.HasPrefix(tok, "./")) {
			continue
		}
		t := resolveShellPath(tok, base)
		if !underAny(t, roots) && !outsidePool(agentsDir, t) {
			return peerWorkWriteReason(peer, t)
		}
	}
	if !explicit && !underAny(base, roots) {
		return peerWorkWriteReason(peer, base)
	}
	return ""
}

// outsidePool reports whether path is outside the pool root (so not another
// agent's workspace). With no known pool nothing is "outside".
func outsidePool(agentsDir, path string) bool {
	return agentsDir != "" && !underDir(path, agentsDir)
}

// deniedDefaultBranchPush refuses `git push` to the default branch of one of
// the peer's workdirs repos: product repos take changes by branch and PR. The
// peer's own workspace pushes its KB as usual.
func deniedDefaultBranchPush(peer *Agent, cmd, cwd string) string {
	workdirs := peer.WorkdirPaths()
	if len(workdirs) == 0 {
		return ""
	}
	base := cwd
	if m := reShellCd.FindAllStringSubmatch(cmd, -1); len(m) > 0 {
		for _, c := range m {
			base = resolveShellPath(c[1], base)
		}
	}
	for _, m := range reGitPush.FindAllStringSubmatch(cmd, -1) {
		repo := base
		if m[1] != "" {
			repo = resolveShellPath(m[1], base)
		}
		if !underAny(repo, workdirs) {
			continue
		}
		def := mainBranch(repo)
		var refs []string
		for _, a := range strings.Fields(m[2]) {
			if !strings.HasPrefix(a, "-") {
				refs = append(refs, a)
			}
		}
		hit := false
		if len(refs) <= 1 {
			// `git push` / `git push origin` pushes the current branch.
			cur, err := git(repo, "rev-parse", "--abbrev-ref", "HEAD")
			hit = err == nil && strings.TrimSpace(cur) == def
		}
		for _, r := range refs[min(1, len(refs)):] {
			dst := r
			if i := strings.LastIndex(r, ":"); i >= 0 {
				dst = r[i+1:]
			}
			dst = strings.TrimPrefix(strings.TrimPrefix(dst, "+"), "refs/heads/")
			hit = hit || dst == def || (dst == "HEAD" && func() bool {
				cur, err := git(repo, "rev-parse", "--abbrev-ref", "HEAD")
				return err == nil && strings.TrimSpace(cur) == def
			}())
		}
		if hit {
			return "mesh: `" + repo + "` is a product repo (a declared workdir) — never push to its default branch `" + def +
				"`. Push a feature branch and open a PR, per that repo's convention."
		}
	}
	return ""
}
