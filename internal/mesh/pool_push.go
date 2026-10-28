package mesh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// pool_push.go — the console's Push button for the pool repo (ADR-0079
// amendment). ADR-0066 has meshctl commit and the person push; the button is
// the person pushing, from the page. It pushes only the pool repo's current
// branch to the branch it tracks, never with --force, and never waits for a
// password. Agents never reach it: the gate denies a shell call naming the
// endpoint, as it denies git in the pool repo.

// ConsolePoolPushPath is the console endpoint that pushes the pool repo; the
// gate denies an agent's shell command that names it.
const ConsolePoolPushPath = "/api/pool/push"

// PoolPushState is what the Push button shows before it runs.
type PoolPushState struct {
	Ready    bool     `json:"ready"`    // clean, tracking a remote branch, and ahead of it
	Finding  string   `json:"finding"`  // the doctor line the button sits on ("" when not ready)
	Upstream string   `json:"upstream"` // e.g. origin/main
	Commits  []string `json:"commits"`  // "<short hash> <subject>", newest first
}

// PoolPushPreview reads whether the pool repo can be pushed and what would go.
func PoolPushPreview(poolRoot string) PoolPushState {
	st := PoolRepoHygiene(poolRoot)
	out := PoolPushState{Upstream: st.Upstream, Commits: []string{}}
	if !st.IsRepo || !st.HasRemote || st.Dirty > 0 || !st.HasUpstream || st.Ahead == 0 {
		return out
	}
	out.Ready, out.Finding = true, PoolRepoNag(poolRoot)
	if b, err := exec.Command("git", "-C", PoolDir(poolRoot), "log", "--format=%h %s", "@{u}..HEAD").Output(); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				out.Commits = append(out.Commits, l)
			}
		}
	}
	return out
}

// Pool push outcomes.
const (
	PushPushed   = "pushed"
	PushRejected = "rejected" // the remote has commits this pool does not
	PushAuth     = "auth"     // no usable credentials without a prompt
	PushFailed   = "failed"
)

// PoolPush pushes the pool repo's current branch to the branch it tracks.
// The outcome names what the person does next; output is git's own.
func PoolPush(ctx context.Context, poolRoot string) (outcome, output string, err error) {
	if st := PoolPushPreview(poolRoot); !st.Ready {
		return "", "", errors.New("the pool repo has nothing to push, or has uncommitted changes")
	}
	return pushTrackedBranch(ctx, PoolDir(poolRoot))
}

// pushTrackedBranch pushes dir's current branch to the branch it tracks:
// never forced, prompts off, no terminal. Shared by the pool's Push button and
// an agent repo's.
func pushTrackedBranch(ctx context.Context, dir string) (outcome, output string, err error) {
	cfg := func(key string) string {
		b, _ := exec.Command("git", "-C", dir, "config", "--get", key).Output()
		return strings.TrimSpace(string(b))
	}
	b, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", "", fmt.Errorf("the repo is not on a branch")
	}
	branch := strings.TrimSpace(string(b))
	remote, merge := cfg("branch."+branch+".remote"), cfg("branch."+branch+".merge")
	if remote == "" || merge == "" {
		return "", "", fmt.Errorf("branch %s tracks no remote branch", branch)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "push", "--porcelain", remote, "HEAD:"+merge)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	noTTY(cmd)
	b, runErr := cmd.CombinedOutput()
	output = strings.TrimSpace(string(b))
	switch {
	case runErr == nil:
		return PushPushed, output, nil
	case ctx.Err() != nil:
		return PushAuth, output + "\n(git did not finish in time; it may be waiting for credentials)", nil
	}
	low := strings.ToLower(output)
	for _, s := range []string{"[rejected]", "non-fast-forward", "fetch first"} {
		if strings.Contains(low, s) {
			return PushRejected, output, nil
		}
	}
	for _, s := range []string{"permission denied", "authentication failed", "could not read username",
		"host key verification failed", "terminal prompts disabled", "could not read from remote"} {
		if strings.Contains(low, s) {
			return PushAuth, output, nil
		}
	}
	return PushFailed, output, nil
}
