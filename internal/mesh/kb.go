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
	return filepath.Join(agentsDir, WorktreesDir, agent+"-"+safe)
}

func kbBranch(session string) string { return "session/" + session }

// ListKBSessions returns the session ids that currently own a KB worktree for
// the agent, recovered from the worktree directory names (<agent>-<session>).
func ListKBSessions(agentsDir string, agent *Agent) []string {
	entries, err := os.ReadDir(filepath.Join(agentsDir, WorktreesDir))
	if err != nil {
		return nil
	}
	prefix := agent.Name + "-"
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
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

// KBBegin creates an isolated worktree+branch for a KB write and points the
// session at it. Returns the worktree path.
func KBBegin(agentsDir string, agent *Agent, session string, now time.Time) (string, error) {
	wt := worktreePath(agentsDir, agent.Name, session)
	if dirExists(wt) {
		return wt, nil // idempotent — already begun
	}
	if err := os.MkdirAll(filepath.Join(agentsDir, WorktreesDir), 0o755); err != nil {
		return "", err
	}
	branch := kbBranch(session)
	// If the branch already exists (re-begin after abort left it), reuse it.
	if _, err := git(agent.Dir(), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
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

// KBFinish rebases the session branch onto main, merges (ff-only), and removes
// the worktree. abort discards the branch + worktree instead. headless controls
// conflict behavior: headless never auto-resolves (returns ErrKBConflict);
// interactive leaves the rebase in progress for the human to resolve.
func KBFinish(agentsDir string, agent *Agent, session string, abort, headless bool) error {
	wt := worktreePath(agentsDir, agent.Name, session)
	branch := kbBranch(session)

	clearSessionBranch := func() {
		if s, err := readSession(sessionPath(agent.Dir(), session)); err == nil {
			s.KBBranch = ""
			_ = writeSession(s)
		}
	}

	if abort {
		_, _ = git(agent.Dir(), "worktree", "remove", "--force", wt)
		_, _ = git(agent.Dir(), "branch", "-D", branch)
		clearSessionBranch()
		return nil
	}

	if !dirExists(wt) {
		return fmt.Errorf("no KB worktree for session %s (run `meshctl kb begin` first)", session)
	}

	// Require the worktree to be committed — we merge commits, not a dirty tree.
	if status, _ := git(wt, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		return fmt.Errorf("worktree %s has uncommitted changes — commit your KB edits first", wt)
	}

	base := mainBranch(agent.Dir())

	// Rebase the session branch onto the base, inside the worktree.
	if _, err := git(wt, "rebase", base); err != nil {
		if headless {
			_, _ = git(wt, "rebase", "--abort")
			return ErrKBConflict
		}
		return fmt.Errorf("%w: resolve conflicts in %s, `git rebase --continue`, then re-run `meshctl kb finish`", ErrKBConflict, wt)
	}

	// Fast-forward the base to the rebased branch.
	if _, err := git(agent.Dir(), "merge", "--ff-only", branch); err != nil {
		return fmt.Errorf("ff-only merge of %s into %s failed: %w", branch, base, err)
	}

	// Clean up.
	if _, err := git(agent.Dir(), "worktree", "remove", "--force", wt); err != nil {
		return err
	}
	_, _ = git(agent.Dir(), "branch", "-D", branch)
	clearSessionBranch()
	// ADR-0015: refresh the disposable memory index so a KB write never leaves it
	// stale. Best-effort — a rebuild failure must not fail an already-merged write.
	_, _ = MemoryRebuild(agent)
	return nil
}
