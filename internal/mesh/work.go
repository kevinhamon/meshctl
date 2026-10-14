package mesh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// work.go — delegated-work worktrees (ADR-0063). When a hub session delegates
// work to a peer whose own session is live, the peer must not edit the checkout
// that session holds: `ask --in-session --work` gives the request its own
// worktree + branch (<pool>/.agentmesh/worktrees/<peer>-work-<reqid>, branch
// mesh/work/<reqid>), and the peer's owner later lands it with `work merge`,
// the same rebase + ff-only path as `kb finish`.

// ErrWorkConflict signals the rebase of a work branch onto the peer's main
// conflicted; the rebase is aborted and the branch + worktree are left intact.
var ErrWorkConflict = errors.New("work merge conflict — needs human")

// WorkBranchPrefix namespaces delegated-work branches in the peer's repo.
const WorkBranchPrefix = "mesh/work/"

func workBranch(reqID string) string { return WorkBranchPrefix + reqID }

func workWorktreePath(agentsDir, peer, reqID string) string {
	return filepath.Join(agentsDir, WorktreesDir, peer+PeerWorkSuffix+"-"+reqID)
}

// workWorktreeReq returns the request id when name is a delegated-work
// worktree of agent (<agent>-work-<reqid>), else "".
func workWorktreeReq(agent, name string) string {
	prefix := agent + PeerWorkSuffix + "-"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	return strings.TrimPrefix(name, prefix)
}

// WorkBegin creates the delegated-work worktree + branch for a request off
// the peer's integration branch. Idempotent. Returns the worktree path.
func WorkBegin(agentsDir string, peer *Agent, reqID string) (string, error) {
	wt := workWorktreePath(agentsDir, peer.Name, reqID)
	if dirExists(wt) {
		return wt, nil
	}
	if err := os.MkdirAll(filepath.Join(agentsDir, WorktreesDir), 0o755); err != nil {
		return "", err
	}
	branch := workBranch(reqID)
	if _, err := git(peer.Dir(), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = git(peer.Dir(), "worktree", "add", wt, branch)
		return wt, err
	}
	if _, err := git(peer.Dir(), "worktree", "add", "-b", branch, wt, mainBranch(peer.Dir())); err != nil {
		return "", err
	}
	return wt, nil
}

// PendingWork is one delegated-work worktree awaiting merge.
type PendingWork struct {
	Agent    string `json:"agent"`
	Request  string `json:"request"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	// Ahead is the number of commits on the branch not on the peer's main
	// (-1 when git could not tell).
	Ahead int `json:"ahead"`
	// Dirty reports uncommitted changes in the worktree.
	Dirty    bool      `json:"dirty"`
	Modified time.Time `json:"modified"`
}

// workWorktrees scans the pool's worktree dir for delegated-work worktrees,
// optionally of one agent. No git calls — cheap enough for session start.
func workWorktrees(agentsDir, agent string) []PendingWork {
	root := filepath.Join(agentsDir, WorktreesDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []PendingWork
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wt := filepath.Join(root, e.Name())
		owner := AgentFromWorktreePath(agentsDir, wt)
		if owner == "" || (agent != "" && owner != agent) {
			continue
		}
		req := workWorktreeReq(owner, e.Name())
		if req == "" {
			continue
		}
		var mod time.Time
		if fi, err := e.Info(); err == nil {
			mod = fi.ModTime()
		}
		out = append(out, PendingWork{Agent: owner, Request: req, Branch: workBranch(req), Worktree: wt, Modified: mod})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Request < out[j].Request
	})
	return out
}

// ListWork returns the pending delegated-work worktrees (of one agent when
// agent != ""), with commits-ahead and dirty state filled from git.
func ListWork(agentsDir, agent string) []PendingWork {
	ws := workWorktrees(agentsDir, agent)
	for i := range ws {
		w := &ws[i]
		dir := AgentDir(agentsDir, w.Agent)
		w.Ahead = -1
		if out, err := git(dir, "rev-list", "--count", mainBranch(dir)+".."+w.Branch); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
				w.Ahead = n
			}
		}
		if st, err := git(w.Worktree, "status", "--porcelain"); err == nil {
			w.Dirty = strings.TrimSpace(st) != ""
		}
	}
	return ws
}

// FindWork resolves a request id to its pending work worktree.
func FindWork(agentsDir, reqID string) (*PendingWork, error) {
	var hits []PendingWork
	for _, w := range workWorktrees(agentsDir, "") {
		if w.Request == reqID {
			hits = append(hits, w)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no delegated-work worktree for request %q — `meshctl work list` shows the pending ones", reqID)
	case 1:
		return &hits[0], nil
	default:
		return nil, fmt.Errorf("request %q has work worktrees in several agents — remove the stray one by hand", reqID)
	}
}

// WorkMergeOptions parameterizes WorkMerge.
type WorkMergeOptions struct {
	Abort bool
	// Caller is the agent running the merge. Merging moves the peer's checked-out
	// branch, so while the peer has a live session only that session may do it.
	Caller string
	Now    time.Time
}

// WorkMerge lands a delegated-work branch in the peer's workspace: rebase onto
// the peer's integration branch inside the worktree, fast-forward the peer's
// checkout, remove the worktree and branch. On a rebase conflict the rebase is
// aborted and ErrWorkConflict returned with everything left intact. Abort
// discards the branch + worktree instead (it never touches the peer's checkout,
// so it is allowed while the peer is live).
func WorkMerge(agentsDir, reqID string, o WorkMergeOptions) (*PendingWork, error) {
	w, err := FindWork(agentsDir, reqID)
	if err != nil {
		return nil, err
	}
	dir := AgentDir(agentsDir, w.Agent)
	remove := func() error {
		if _, err := git(dir, "worktree", "remove", "--force", w.Worktree); err != nil {
			return err
		}
		_, _ = git(dir, "branch", "-D", w.Branch)
		return nil
	}
	if o.Abort {
		return w, remove()
	}

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if o.Caller != w.Agent && len(liveSessions(dir, now)) > 0 {
		return nil, fmt.Errorf("%s has a live session — merging would move the branch it has checked out; run `meshctl work merge %s` from %s's own session", w.Agent, reqID, w.Agent)
	}
	if st, _ := git(w.Worktree, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		return nil, fmt.Errorf("worktree %s has uncommitted changes — commit or discard them first", w.Worktree)
	}
	base := mainBranch(dir)
	if cur, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && strings.TrimSpace(cur) != base {
		return nil, fmt.Errorf("%s is on branch %q, not %q — switch it back before merging", dir, strings.TrimSpace(cur), base)
	}
	if _, err := git(w.Worktree, "rebase", base); err != nil {
		_, _ = git(w.Worktree, "rebase", "--abort")
		return nil, fmt.Errorf("%w: %s does not rebase cleanly onto %s — resolve in %s (`git rebase %s`), then re-run, or discard with `meshctl work merge %s --abort`",
			ErrWorkConflict, w.Branch, base, w.Worktree, base, reqID)
	}
	if _, err := git(dir, "merge", "--ff-only", w.Branch); err != nil {
		return nil, fmt.Errorf("ff-only merge of %s into %s failed: %w", w.Branch, base, err)
	}
	return w, remove()
}

// WorkNag is the session-start line naming this agent's pending delegated-work
// branches, or "".
func WorkNag(agentsDir string, a *Agent) string {
	ws := workWorktrees(agentsDir, a.Name)
	switch len(ws) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("mesh: 1 delegated work branch pending merge (%s) — review it, then `meshctl work merge %s` (or `--abort`).", ws[0].Branch, ws[0].Request)
	default:
		return fmt.Sprintf("mesh: %d delegated work branches pending merge — `meshctl work list --agent %s`, then `meshctl work merge <id>`.", len(ws), a.Name)
	}
}
