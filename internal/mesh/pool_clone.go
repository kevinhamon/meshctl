package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kevinhamon/meshctl/internal/selfupdate"
)

// pool_clone.go — rebuild a pool on another machine from its pool repo
// (pool-clone spec C2): clone the pool repo into <root>/pool, then every
// member and extra it lists beside it. Registering the root and running
// `pool upgrade` (the machine-specific wiring) is the command's job, after
// this. Never pushes, never forces: an existing folder is left alone.

// ClonedRepo is one member or extra and what happened to it.
type ClonedRepo struct {
	Name   string // the agent's name, or the extra's path
	Path   string // relative to the pool root
	Remote string
	State  string // CloneCloned | CloneExisting | CloneNoRemote | CloneFailed
	Err    string // why it failed
}

// Clone states.
const (
	CloneCloned   = "cloned"
	CloneExisting = "existing"  // a folder was already there: left untouched
	CloneNoRemote = "no-remote" // listed without a remote: not restorable
	CloneFailed   = "failed"    // git clone failed
	CloneBadPath  = "bad-path"  // a path outside the pool root: refused
)

// CloneResult is what ClonePool did.
type CloneResult struct {
	Root    string
	Members []ClonedRepo
	Extras  []ClonedRepo
	// Behind is set when members.yaml was written by a newer meshctl than
	// this one (C7): the version that wrote it.
	Behind string
	// NoList is set when the pool repo has no members.yaml (it was last
	// upgraded before the list existed), so only the pool itself is cloned.
	NoList bool
}

// Incomplete reports whether any member or extra could not be cloned.
func (r CloneResult) Incomplete() bool {
	for _, c := range append(append([]ClonedRepo{}, r.Members...), r.Extras...) {
		if c.State != CloneCloned && c.State != CloneExisting {
			return true
		}
	}
	return false
}

// DefaultCloneDir is the folder `pool clone <url>` creates when none is
// given: the repo's name without `.git` and a `-pool` suffix.
func DefaultCloneDir(url string) string {
	s := strings.TrimRight(strings.TrimSpace(url), "/")
	if i := strings.LastIndexAny(s, "/:\\"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	if t := strings.TrimSuffix(s, "-pool"); t != "" {
		s = t
	}
	return s
}

// ClonePool clones the pool repo at url into <root>/pool, then each member
// and extra it lists. root must not hold a pool directory already. A failed
// member clone is recorded and the rest continue; the error return is for
// the pool repo itself.
func ClonePool(url, root string) (CloneResult, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return CloneResult{}, err
	}
	res := CloneResult{Root: abs}
	poolDir := filepath.Join(abs, CurrentPoolDir)
	if _, err := os.Stat(poolDir); err == nil {
		return res, fmt.Errorf("%s already exists — clone into a new folder", poolDir)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return res, err
	}
	if _, err := git(abs, "clone", "--quiet", url, CurrentPoolDir); err != nil {
		return res, fmt.Errorf("clone the pool repo: %w", err)
	}
	if !IsPool(abs) {
		return res, fmt.Errorf("%s has no %s — not a meshctl pool repo", url, PoolMarkerFile)
	}
	list, ok := ReadMembers(abs)
	res.NoList = !ok
	if ok && selfupdate.Newer(list.Meshctl, WriterVersion) {
		res.Behind = list.Meshctl
	}
	var missing []Member
	for _, m := range list.Members {
		c := cloneOne(abs, m.Name, m.Path, m.Remote, m.Branch)
		res.Members = append(res.Members, c)
		if c.State != CloneCloned && !fileExists(filepath.Join(abs, filepath.FromSlash(m.Path), ManifestName)) {
			missing = append(missing, m) // not here, or something else is at its path
		}
	}
	if err := writeCloneMissing(abs, missing); err != nil {
		return res, err
	}
	for _, x := range LoadPoolConfig(abs).Settings.Extras {
		res.Extras = append(res.Extras, cloneOne(abs, x.Path, x.Path, x.Remote, x.Branch))
	}
	return res, nil
}

// cloneOne clones remote into <root>/<path>, or says why it did not.
func cloneOne(root, name, path, remote, branch string) ClonedRepo {
	c := ClonedRepo{Name: name, Path: path, Remote: remote}
	clean := filepath.Clean(filepath.FromSlash(path))
	if path == "" || filepath.IsAbs(clean) || clean == "." || clean == CurrentPoolDir ||
		clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		c.State, c.Err = CloneBadPath, "path must be a folder inside the pool root"
		return c
	}
	dest := filepath.Join(root, clean)
	if _, err := os.Stat(dest); err == nil {
		c.State = CloneExisting
		return c
	}
	if strings.TrimSpace(remote) == "" {
		c.State = CloneNoRemote
		return c
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		c.State, c.Err = CloneFailed, err.Error()
		return c
	}
	args := []string{"clone", "--quiet"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	if _, err := git(root, append(args, "--", remote, dest)...); err != nil {
		c.State, c.Err = CloneFailed, err.Error()
		return c
	}
	c.State = CloneCloned
	return c
}

// CloneLeftovers lists what a rebuilt pool still needs by hand on this
// machine: workdirs (ADR-0063) repos missing here and harnesses not
// installed, one line each, sorted.
func CloneLeftovers(poolRoot string) []string {
	agents, _, err := ScanAgents(poolRoot)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, a := range agents {
		for _, d := range a.WorkdirPaths() {
			if !dirExists(d) {
				add(fmt.Sprintf("workdir %s (%s) is not on this machine — clone it there", d, a.Name))
			}
		}
		if h := a.HarnessName(); !ToolOnPath(h) {
			add(fmt.Sprintf("%s runs on %s, which is not installed here", a.Name, h))
		}
	}
	sort.Strings(out)
	return out
}

// CloneDoesNotTravel is what git leaves behind on the old machine (C3): the
// lines `pool clone` prints, and the README's move checklist repeats.
var CloneDoesNotTravel = []string{
	"open requests (intake/) and their claims — answer or close them before moving, or carry them by hand",
	"closed-request history (.intake-archive/)",
	"docs/local/ — documents that never leave a machine",
	".memory/ indexes and session markers — rebuilt as agents run",
	"pool runtime state: KB worktrees, the dispatch log and counter",
}
