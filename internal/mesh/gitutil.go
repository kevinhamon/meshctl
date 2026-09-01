package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo hygiene (ADR-0039): an agent's durable knowledge lives in its `knowledge/`
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
