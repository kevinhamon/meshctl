package mesh

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// pool_repo.go — the pool repo nag. Each peer has
// a repo and a persistent reminder until a remote exists; the pool
// directory is a repo too and gets the same treatment, one step further: the
// human is nagged while pool/ is not a repository, has no remote, is dirty,
// or is ahead of its remote. The line is recomputed on every call and
// disappears only once the pool is pushed. Nothing here pushes or commits —
// meshctl commits confirmed writes, humans push.

// PoolRepoState is the pool directory's git state, read by the nag, doctor,
// hygiene and `pool upgrade`. Zero value: not a pool with an identity.
type PoolRepoState struct {
	HasMarker   bool   // the pool has an identity (pool.yaml); without one there is nothing to version yet
	IsRepo      bool   // the pool directory holds its own repository
	HasRemote   bool   // at least one remote is configured
	Dirty       int    // uncommitted paths (tracked changes + untracked, ignores excluded)
	HasUpstream bool   // the current branch tracks a remote branch (has been pushed at least once)
	Upstream    string // the tracked branch, e.g. origin/main
	Ahead       int    // commits on HEAD not on the upstream
}

// PoolRepoHygiene reads the pool directory's git state. A pool still in the
// legacy hidden directory reads as not a repository, never as an error.
func PoolRepoHygiene(poolRoot string) PoolRepoState {
	var st PoolRepoState
	st.HasMarker = fileExists(PoolMarkerPath(poolRoot))
	if !st.HasMarker {
		return st
	}
	dir := PoolDir(poolRoot)
	st.IsRepo = poolRepoPresent(dir)
	if !st.IsRepo {
		return st
	}
	st.HasRemote = gitHasRemote(dir)
	st.Dirty = len(gitDirtyPaths(dir))
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Output(); err == nil {
		st.HasUpstream = true
		st.Upstream = strings.TrimSpace(string(out))
		if out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "@{u}..HEAD").Output(); err == nil {
			st.Ahead, _ = strconv.Atoi(strings.TrimSpace(string(out)))
		}
	}
	return st
}

// PoolRepoNag returns the one-line reminder for a pool whose knowledge is not
// yet safe, or "" when pool/ is a clean repository pushed to its remote. Each
// variant names what to do. The states are checked in the order a human
// resolves them: make it a repo, give it a remote, commit, push. A pool with
// no marker is silent: it has no identity to version, and `pool upgrade`
// writes the marker before anything else.
func PoolRepoNag(poolRoot string) string {
	st := PoolRepoHygiene(poolRoot)
	if !st.HasMarker {
		return ""
	}
	name := PoolDirName(poolRoot) + "/"
	dir := PoolDir(poolRoot)
	switch {
	case !st.IsRepo:
		return fmt.Sprintf("pool directory %s is not a git repository — run `meshctl pool upgrade` to make it one; "+
			"the pool's glossary and doctrine are unversioned until then", name)
	case !st.HasRemote:
		return fmt.Sprintf("pool directory %s has no git remote — create one, then `git -C %s remote add origin <url>` and "+
			"`git -C %s push -u origin main`; pool knowledge lives only on this machine until then", name, dir, dir)
	case st.Dirty > 0:
		return fmt.Sprintf("pool directory %s has %d uncommitted path(s) — review `git -C %s status` and commit (or discard) them, then push",
			name, st.Dirty, dir)
	case !st.HasUpstream:
		return fmt.Sprintf("pool directory %s has a remote but has never been pushed — run `git -C %s push -u origin main`", name, dir)
	case st.Ahead > 0:
		return fmt.Sprintf("pool directory %s is %d commit(s) ahead of %s — run `git -C %s push`", name, st.Ahead, st.Upstream, dir)
	}
	return ""
}
