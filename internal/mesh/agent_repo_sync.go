package mesh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// agent_repo_sync.go — the console's Commit and Push buttons for an agent's
// own repo (ADR-0079 amendment). ADR-0039 has a peer's knowledge safe only
// once committed and pushed; doctor names a workspace with uncommitted paths
// or unpushed commits, and the console lets the person commit or push it from
// that finding, after seeing what goes. Agents never reach the endpoints: the
// gate denies a shell call naming them, as it does the pool's push.

// Console endpoints for an agent repo's buttons; the gate denies an agent's
// shell command that names either.
const (
	ConsoleAgentCommitPath = "/api/agent/commit"
	ConsoleAgentPushPath   = "/api/agent/push"
)

// Agent repo finding actions (DoctorFinding.Action).
const (
	ActionCommit = "commit"
	ActionPush   = "push"
)

// AgentRepoState is an agent repo's git state as its buttons show it.
type AgentRepoState struct {
	Action   string   `json:"action"`   // commit, push, or "" when there is nothing to do
	Dirty    []string `json:"dirty"`    // porcelain status lines: what a commit takes
	Upstream string   `json:"upstream"` // the tracked branch, e.g. origin/main
	Commits  []string `json:"commits"`  // "<short hash> <subject>" not on the upstream, newest first
	// Suggested is the message the Commit dialog offers (SuggestCommitMessage);
	// only the console's check fills it.
	Suggested string `json:"suggested,omitempty"`
}

// AgentRepoSync reads what an agent repo would commit or push. Only a
// workspace that is its own repo with a remote has anything to do: the
// ADR-0039 findings cover the rest. Dirty comes first; a push waits for a
// clean tree, as the pool's does.
func AgentRepoSync(dir string) AgentRepoState {
	st := AgentRepoState{Dirty: []string{}, Commits: []string{}}
	if !gitHasRemote(dir) {
		return st
	}
	if d := gitDirtyPaths(dir); len(d) > 0 {
		st.Dirty, st.Action = d, ActionCommit
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Output()
	if err != nil {
		return st
	}
	st.Upstream = strings.TrimSpace(string(out))
	if b, err := exec.Command("git", "-C", dir, "log", "--format=%h %s", "@{u}..HEAD").Output(); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				st.Commits = append(st.Commits, l)
			}
		}
	}
	if st.Action == "" && len(st.Commits) > 0 {
		st.Action = ActionPush
	}
	return st
}

// AgentRepoFinding is the doctor line for an agent repo with something to
// commit or push, and the button it carries; "" when there is nothing to do.
func AgentRepoFinding(dir string) (msg, action string) {
	st := AgentRepoSync(dir)
	switch st.Action {
	case ActionCommit:
		return fmt.Sprintf("%d uncommitted path(s) — review `git -C %s status`, commit, then push (knowledge/ is unsafe until pushed)",
			len(st.Dirty), dir), ActionCommit
	case ActionPush:
		return fmt.Sprintf("%d commit(s) ahead of %s — run `git -C %s push`", len(st.Commits), st.Upstream, dir), ActionPush
	}
	return "", ""
}

// AgentRepoCommit stages everything and commits it with message, as the
// person (their git identity). seen is the status the person confirmed: when
// the tree changed since, nothing is committed, so the commit never takes a
// path they did not see.
func AgentRepoCommit(ctx context.Context, dir, message string, seen []string) (output string, err error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errors.New("a commit needs a message")
	}
	st := AgentRepoSync(dir)
	if st.Action != ActionCommit {
		return "", errors.New("the repo has nothing to commit")
	}
	if !slices.Equal(st.Dirty, seen) {
		return "", errors.New("the repo changed since you looked; open it again to see what a commit would take")
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		noTTY(cmd)
		b, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(b)), err
	}
	if out, err := run("add", "-A"); err != nil {
		return out, fmt.Errorf("git add failed: %s", out)
	}
	out, err := run("commit", "-q", "-m", message)
	if err != nil {
		return out, fmt.Errorf("git commit failed: %s", out)
	}
	return out, nil
}

// AgentRepoPush pushes an agent repo's current branch to the branch it
// tracks, with the pool push's outcomes.
func AgentRepoPush(ctx context.Context, dir string) (outcome, output string, err error) {
	if AgentRepoSync(dir).Action != ActionPush {
		return "", "", errors.New("the repo has nothing to push, or has uncommitted changes")
	}
	return pushTrackedBranch(ctx, dir)
}
