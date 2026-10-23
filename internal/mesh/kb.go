package mesh

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrKBConflict signals that a KB merge hit a conflict a headless run must not
// auto-resolve (ADR-0011): the branch + worktree are left intact and the caller
// sets the driving request to needs-human.
var ErrKBConflict = errors.New("kb merge conflict — needs human")

func worktreePath(agentsDir, agent, session string) string {
	safe := strings.ReplaceAll(session, "/", "-")
	return filepath.Join(WorktreesPath(agentsDir), agent+"-"+safe)
}

// KBBranchPrefix namespaces KB session branches in the agent's repo, beside
// WorkBranchPrefix (spec: shared understanding, decision 17; ADR-0067).
// LegacyKBBranchPrefix is the form earlier meshctl versions created; finish,
// --abort and the listing still accept it, and hygiene renames it once no
// worktree references it.
const (
	KBBranchPrefix       = "mesh/kb/"
	LegacyKBBranchPrefix = "session/"
)

// KBBranch is the branch name of a KB session.
func KBBranch(session string) string { return KBBranchPrefix + session }

func legacyKBBranch(session string) string { return LegacyKBBranchPrefix + session }

// kbBranchFor resolves the branch a session's worktree is on: the mesh/kb/
// form when it exists, else the legacy session/ form when that is what the
// repo holds, else the mesh/kb/ form for a branch yet to be created.
func kbBranchFor(agentDir, session string) string {
	if gitRefExists(agentDir, KBBranch(session)) {
		return KBBranch(session)
	}
	if gitRefExists(agentDir, legacyKBBranch(session)) {
		return legacyKBBranch(session)
	}
	return KBBranch(session)
}

// legacyKBBranches lists the session ids of the repo's session/<id> branches
// (sorted); nil when the directory is not a git repo.
func legacyKBBranches(agentDir string) []string {
	out, err := git(agentDir, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+LegacyKBBranchPrefix)
	if err != nil {
		return nil
	}
	var ids []string
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimSpace(ln); strings.HasPrefix(ln, LegacyKBBranchPrefix) {
			ids = append(ids, strings.TrimPrefix(ln, LegacyKBBranchPrefix))
		}
	}
	sort.Strings(ids)
	return ids
}

func gitRefExists(dir, branch string) bool {
	_, err := git(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// KBHolder returns the live session holding the KB worktree of a session id
// (one worktree, one session: spec decision 6), or nil when no worktree exists
// or no live session is registered under that id.
func KBHolder(agentsDir string, agent *Agent, session string, now time.Time) *Session {
	if !dirExists(worktreePath(agentsDir, agent.Name, session)) {
		return nil
	}
	s, err := readSession(sessionPath(agent.Dir(), session))
	if err != nil || int(now.Unix()-s.LastBeat) > staleSec() {
		return nil
	}
	return s
}

// ListKBSessions returns the session ids that currently own a KB worktree for
// the agent, recovered from the worktree directory names (<agent>-<session>).
func ListKBSessions(agentsDir string, agent *Agent) []string {
	entries, err := os.ReadDir(WorktreesPath(agentsDir))
	if err != nil {
		return nil
	}
	prefix := agent.Name + "-"
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || workWorktreeReq(agent.Name, e.Name()) != "" {
			continue // delegated-work worktrees are not KB sessions (work.go)
		}
		out = append(out, strings.TrimPrefix(e.Name(), prefix))
	}
	sort.Strings(out)
	return out
}

// ResolveKBSession picks the session a `kb finish`/`abort` acts on. Precedence:
// an explicit id (--session); else the minted per-process id if it already owns
// a worktree; else the sole discovered worktree for the agent. It errors when
// nothing or more than one candidate exists — so an ambiguous finish can never
// merge the wrong branch. This is what lets begin and finish run in separate
// shells (where the minted s-<pid>-<nano> id differs) without CLAUDE_SESSION_ID.
func ResolveKBSession(agentsDir string, agent *Agent, minted, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if dirExists(worktreePath(agentsDir, agent.Name, minted)) {
		return minted, nil
	}
	found := ListKBSessions(agentsDir, agent)
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no KB worktree for %s — run `meshctl kb begin` first", agent.Name)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("multiple KB worktrees for %s: %s — pass --session <id>", agent.Name, strings.Join(found, ", "))
	}
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// mainBranch resolves the repo's integration branch: prefer main, else master,
// else the current branch.
func mainBranch(dir string) string {
	for _, b := range []string{"main", "master"} {
		if _, err := git(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	out, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "main"
	}
	return strings.TrimSpace(out)
}

// KBBegin creates an isolated worktree+branch (mesh/kb/<session>) for a KB
// write and points the session at it. Returns the worktree path. Re-running it
// from the session that holds the worktree is idempotent; when another live
// session holds it, begin refuses and creates nothing, so a confirmation
// signed in one session never covers edits from another (spec decision 6).
// The caller's identity is the harness session id, resolved as claims resolve
// it (ResolveAgentSessionID).
func KBBegin(agentsDir string, agent *Agent, session string, now time.Time) (string, error) {
	wt := worktreePath(agentsDir, agent.Name, session)
	if dirExists(wt) {
		if h := KBHolder(agentsDir, agent, session, now); h != nil && h.ID != ResolveAgentSessionID(agent, os.Getpid(), now) {
			return "", fmt.Errorf("KB session %s is held by live session %s (pid %d) in %s — one worktree, one session: "+
				"run `meshctl kb begin` without --session for your own worktree, or let that session `meshctl kb finish` first",
				session, h.ID, h.PID, agent.Name)
		}
		return wt, nil // idempotent — already begun
	}
	if err := os.MkdirAll(WorktreesPath(agentsDir), 0o755); err != nil {
		return "", err
	}
	branch := KBBranch(session)
	// A legacy session/<id> branch with no worktree (left by an older meshctl)
	// is renamed, so a begun session is always on the mesh/kb/ form.
	if !gitRefExists(agent.Dir(), branch) && gitRefExists(agent.Dir(), legacyKBBranch(session)) {
		if _, err := git(agent.Dir(), "branch", "-m", legacyKBBranch(session), branch); err != nil {
			return "", err
		}
	}
	// If the branch already exists (re-begin after abort left it), reuse it.
	if gitRefExists(agent.Dir(), branch) {
		if _, err := git(agent.Dir(), "worktree", "add", wt, branch); err != nil {
			return "", err
		}
	} else {
		if _, err := git(agent.Dir(), "worktree", "add", "-b", branch, wt); err != nil {
			return "", err
		}
	}

	// Point the session at the branch, if a session record exists.
	if s, err := readSession(sessionPath(agent.Dir(), session)); err == nil {
		s.KBBranch = branch
		_ = writeSession(s)
	}
	return wt, nil
}
