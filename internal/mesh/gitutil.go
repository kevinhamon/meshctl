package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo hygiene: an agent's durable knowledge lives in its `knowledge/`
// tree and is only safe once committed to a git repo AND pushed to a remote. The
// pool container isn't a repo, so each agent must be its own repo. `agent new`
// inits the local repo here; the session-start hook nags until a remote exists.

// isGitRepo reports whether dir is inside a git work tree.
func isGitRepo(dir string) bool {
	if dirExists(filepath.Join(dir, ".git")) {
		return true
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// gitHasRemote reports whether dir's repo has at least one configured remote.
func gitHasRemote(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "remote").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// gitDirtyPaths lists the porcelain status lines of dir's own repository (a
// `.git` entry inside it), limited to paths when given: uncommitted changes,
// untracked files. Nil when dir is not its own repo, the status fails, or the
// tree is clean. A directory inside somebody else's work tree is not "a repo
// with dirty paths" for this purpose: the pool must never be judged by the
// state of a repo it merely sits in.
func gitDirtyPaths(dir string, paths ...string) []string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil
	}
	return gitStatusLines(dir, paths...)
}

// gitStatusLines is `git status --porcelain -- <paths>` in dir, one line per
// changed path; nil on a clean tree or when the status fails. The caller
// decides what counts as a repo (gitDirtyPaths: its own; gitDirty: any).
func gitStatusLines(dir string, paths ...string) []string {
	args := append([]string{"-C", dir, "status", "--porcelain", "--"}, paths...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	var lines []string
	// Trim only line ends: the porcelain status columns start with a space.
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

// initAgentRepo inits a local git repo in an agent workspace if it isn't one
// already, with a best-effort initial commit (tolerates missing git identity —
// init still succeeds and the agent can commit later). Best-effort throughout:
// a git failure never fails scaffolding.
func initAgentRepo(dir string) {
	if isGitRepo(dir) {
		return
	}
	if err := exec.Command("git", "-C", dir, "init", "-b", "main").Run(); err != nil {
		// Older git without -b: fall back to plain init.
		if err := exec.Command("git", "-C", dir, "init").Run(); err != nil {
			return
		}
	}
	_ = exec.Command("git", "-C", dir, "add", "-A").Run()
	// Commit only if a git identity is configured; otherwise leave it staged.
	cmd := exec.Command("git", "-C", dir, "commit", "-m", "Scaffold agent workspace (meshctl)")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=meshctl", "GIT_AUTHOR_EMAIL=meshctl@localhost",
		"GIT_COMMITTER_NAME=meshctl", "GIT_COMMITTER_EMAIL=meshctl@localhost")
	_ = cmd.Run()
}

// RepoHygiene describes an agent workspace's git state for the nag + doctor.
type RepoHygiene struct {
	IsRepo    bool
	HasRemote bool
}

// AgentRepoHygiene reports the git state of an agent workspace.
func AgentRepoHygiene(dir string) RepoHygiene {
	repo := isGitRepo(dir)
	return RepoHygiene{IsRepo: repo, HasRemote: repo && gitHasRemote(dir)}
}

// RepoNag returns a one-line reminder if the workspace's knowledge isn't yet
// safe (no repo, or a repo with no remote), else "". Persistent by construction:
// it recomputes each call and disappears once a remote exists.
func RepoNag(dir string) string {
	h := AgentRepoHygiene(dir)
	if !h.IsRepo {
		return "⚠ This agent is not a git repo — run `git init` and commit; your knowledge/ is unsafe until versioned + pushed."
	}
	if !h.HasRemote {
		return "⚠ This agent has no git remote — create one and push. Your knowledge/ lives only on this machine until then."
	}
	return ""
}
