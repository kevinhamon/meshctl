package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pool_dir.go — the pool directory as a git repo.
// Pool knowledge (marker, doctrine, glossary) lives in the visible `pool/`,
// which is a git repository on main; runtime state (dispatch files, worktrees)
// sits beside it, ignored. PlanPoolDir is the read-only preview `pool upgrade
// --check` prints; UpgradePoolDir applies the same steps, and `pool init` runs
// it on a fresh pool so both paths converge on one shape. A pool still in the
// hidden legacy directory keeps working untouched until the human upgrades.

// poolInitialCommit is the subject of the first commit in a pool repo.
const poolInitialCommit = "pool: initialise repository"

// Subjects of the config-split commits on an already-initialised pool repo
// (the config split): tools moved out of the identity file, or the config
// file first written on a pool migrated before the split existed.
const (
	poolConfigSplitCommit = "config: split from " + PoolMarkerFile
	poolConfigAddCommit   = "config: add " + PoolConfigFile
)

// poolIgnoreMemoryCommit is the subject of the commit adding .memory/ to an
// ignore block written before it was one of the block's lines.
const poolIgnoreMemoryCommit = "pool: ignore " + MemoryDir + "/"

// poolIgnoreBlock is the meshctl-managed block of the pool repo's ignore file:
// runtime state never enters the pool's history. Doctrine merge scratch
// (`.upstream`, `.bak-*`) is meshctl's, not knowledge, so it stays out too.
var poolIgnoreBlock = "\n" + gitignoreMarker + " — do not edit\n" +
	"# runtime state: dispatch audit, counter, lock (+ its reclaim/tomb files), worktrees\n" +
	DispatchLogFile + "\n" +
	DispatchCountFile + "\n" +
	DispatchLockFile + "\n" +
	DispatchLockFile + ".*\n" +
	WorktreesDirName + "/\n" +
	"# machine-local markers and the upgrade stamp, disposable\n" +
	MemoryDir + "/\n" +
	"# doctrine merge scratch written by `meshctl pool upgrade`\n" +
	DoctrineDirName + "/*" + upstreamSuffix + "\n" +
	DoctrineDirName + "/*.bak-*\n" +
	"# end meshctl-managed ignores\n"

// poolDirState is what PlanPoolDir and UpgradePoolDir both read, so the
// preview and the apply can never disagree about what is left to do.
type poolDirState struct {
	legacy              bool // still in the hidden directory
	foreign             bool // legacy, and a `pool/` that is not the pool already sits at the root
	staleLegacy         bool // migrated, but the hidden directory is still there beside pool/
	markerMissing       bool
	configMissing       bool
	splitTools          splitToolsState // tools still in the identity file (config split)
	splitCmds           []string
	glossaryMissing     bool
	repoMissing         bool
	ignoreMissing       bool
	memoryIgnoreMissing bool // the block predates its .memory/ line
	commitMissing       bool
	worktrees           int // live worktree links the rename must repair
}

func readPoolDirState(poolRoot string) poolDirState {
	dir := PoolDir(poolRoot)
	st := poolDirState{legacy: PoolDirIsLegacy(poolRoot)}
	cur := filepath.Join(poolRoot, CurrentPoolDir)
	legacy := filepath.Join(poolRoot, LegacyPoolDir)
	st.foreign = st.legacy && dirExists(cur)
	st.staleLegacy = !st.legacy && dirExists(legacy)
	st.markerMissing = !fileExists(filepath.Join(dir, PoolMarkerFile))
	st.configMissing = !fileExists(filepath.Join(dir, PoolConfigFile))
	st.splitTools, st.splitCmds = readSplitToolsState(poolRoot)
	st.glossaryMissing = !fileExists(filepath.Join(dir, PoolGlossaryFile))
	st.repoMissing = !poolRepoPresent(dir)
	if b, _ := os.ReadFile(filepath.Join(dir, PoolIgnoreFile)); !strings.Contains(string(b), gitignoreMarker) {
		st.ignoreMissing = true
	} else if !hasIgnoreLine(string(b), MemoryDir+"/") {
		// Blocks written before .memory/ joined them (the upgrade stamp
		// lives there, so the repo was left dirty after every upgrade).
		st.memoryIgnoreMissing = true
	}
	if st.repoMissing {
		st.commitMissing = true
	} else if _, err := git(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		st.commitMissing = true
	}
	if entries, err := os.ReadDir(filepath.Join(dir, WorktreesDirName)); err == nil {
		for _, e := range entries {
			if e.IsDir() && fileExists(filepath.Join(dir, WorktreesDirName, e.Name(), ".git")) {
				st.worktrees++
			}
		}
	}
	return st
}

// poolRepoPresent is a strict check for the pool directory's OWN repository (a
// `.git` entry inside it). isGitRepo would also say yes for a pool root that
// sits inside somebody else's work tree, and the pool must not commit there.
func poolRepoPresent(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func (st poolDirState) pending() bool {
	return st.legacy || st.markerMissing || st.configMissing || st.splitTools == splitAuto ||
		st.glossaryMissing || st.repoMissing || st.ignoreMissing || st.memoryIgnoreMissing || st.commitMissing
}

// hasIgnoreLine reports whether an ignore file carries line on a line of its own.
func hasIgnoreLine(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// configCommit is the subject of the commit the config steps need on a pool
// repo that already has history ("" when the first commit will carry them).
func (st poolDirState) configCommit() string {
	switch {
	case st.commitMissing:
		return ""
	case st.splitTools == splitAuto:
		return poolConfigSplitCommit
	case st.configMissing:
		return poolConfigAddCommit
	}
	return ""
}

// PlanPoolDir previews the pool directory migration without writing: the steps
// `pool upgrade` would take (empty when the pool is already a `pool/` repo with
// its first commit) and warnings the human must settle first.
func PlanPoolDir(poolRoot string) (steps, warnings []string) {
	st := readPoolDirState(poolRoot)
	cur, legacy := CurrentPoolDir+"/", LegacyPoolDir+"/"
	if st.foreign {
		warnings = append(warnings, cur+" already exists beside "+legacy+" and is not the pool directory — move it aside before `meshctl pool upgrade` can rename "+legacy)
	}
	if st.staleLegacy {
		warnings = append(warnings, legacy+" is still present beside "+cur+" — the pool now lives in "+cur+"; move anything you still need out of "+legacy+" and delete it")
	}
	if st.legacy {
		step := "rename " + legacy + " → " + cur
		if st.worktrees > 0 {
			step += fmt.Sprintf(" and repair %d worktree link(s)", st.worktrees)
		}
		steps = append(steps, step)
	}
	if st.markerMissing {
		steps = append(steps, "write "+cur+PoolMarkerFile+" (pool identity marker)")
	}
	if st.configMissing {
		steps = append(steps, "write "+cur+PoolConfigFile+" (human-owned settings: knowledge, outbound, reconcile, tools)")
	}
	switch st.splitTools {
	case splitAuto:
		steps = append(steps, "move `tools` from "+cur+PoolMarkerFile+" to "+cur+PoolConfigFile+" ("+strings.Join(st.splitCmds, ", ")+")")
	case splitManual:
		warnings = append(warnings, cur+PoolMarkerFile+" still carries `tools` ("+strings.Join(st.splitCmds, ", ")+") and "+cur+PoolConfigFile+" already declares its own — merge them into "+cur+PoolConfigFile+" by hand and delete `tools` from "+cur+PoolMarkerFile)
	}
	if c := st.configCommit(); c != "" {
		steps = append(steps, "commit "+cur+" with your git identity (`"+c+"`)")
	}
	if st.glossaryMissing {
		steps = append(steps, "write "+cur+PoolGlossaryFile+" (pool glossary, empty)")
	}
	if st.repoMissing {
		steps = append(steps, "git init "+cur+" on main")
	}
	if st.ignoreMissing {
		steps = append(steps, "write "+cur+PoolIgnoreFile+" (ignore dispatch runtime files and worktrees)")
	}
	if st.memoryIgnoreMissing {
		steps = append(steps, "add "+MemoryDir+"/ to "+cur+PoolIgnoreFile+" (machine-local state)")
	}
	if st.commitMissing {
		steps = append(steps, "first commit in "+cur+" with your git identity (`"+poolInitialCommit+"`)")
	}
	if drift, err := MembersDrift(poolRoot); err == nil && len(drift) > 0 && !st.foreign {
		steps = append(steps, "refresh "+cur+MembersFile+" ("+strings.Join(drift, "; ")+")")
	}
	return steps, warnings
}

// UpgradePoolDir applies the pool directory migration: renames the legacy
// hidden directory to `pool/`, makes it a git repo on main, writes the ignore
// file and makes the first commit with the user's configured git identity (no
// identity is set here — a pool's history is the human's). Each step is
// skipped when already done, so an already-migrated pool is a no-op and a
// fresh `pool init` converges on the same shape. Returns what it did.
func UpgradePoolDir(poolRoot string, now time.Time) ([]string, error) {
	abs, err := filepath.Abs(poolRoot)
	if err != nil {
		return nil, err
	}
	st := readPoolDirState(abs)
	if !st.pending() {
		return refreshMembersStep(abs, nil)
	}
	var did []string
	cur, legacy := filepath.Join(abs, CurrentPoolDir), filepath.Join(abs, LegacyPoolDir)

	if st.legacy {
		if st.foreign {
			return nil, fmt.Errorf("%s/ already exists and is not the pool directory — move it aside, then rerun `meshctl pool upgrade`", CurrentPoolDir)
		}
		if err := os.Rename(legacy, cur); err != nil {
			return did, err
		}
		did = append(did, "renamed "+LegacyPoolDir+"/ → "+CurrentPoolDir+"/")
		if n := repairWorktreeLinks(cur); n > 0 {
			did = append(did, fmt.Sprintf("repaired %d worktree link(s)", n))
		}
	}
	dir := PoolDir(abs)
	if st.markerMissing {
		if err := WritePoolMarker(abs, "", doctrineVersion(), now, false); err != nil {
			return did, err
		}
		did = append(did, "pool marker: "+poolRel(abs, PoolMarkerFile))
	}
	if st.configMissing {
		if err := copyTemplate("templates/pool/"+PoolConfigFile, filepath.Join(dir, PoolConfigFile)); err != nil {
			return did, err
		}
		did = append(did, "pool config: "+poolRel(abs, PoolConfigFile))
	}
	if st.splitTools == splitAuto {
		if err := splitToolsFromIdentity(abs); err != nil {
			return did, err
		}
		did = append(did, "moved `tools` ("+strings.Join(st.splitCmds, ", ")+") from "+poolRel(abs, PoolMarkerFile)+" to "+poolRel(abs, PoolConfigFile))
	}
	if st.glossaryMissing {
		if err := copyTemplate("templates/pool/"+PoolGlossaryFile, filepath.Join(dir, PoolGlossaryFile)); err != nil {
			return did, err
		}
		did = append(did, "pool glossary: "+poolRel(abs, PoolGlossaryFile))
	}
	if st.repoMissing {
		if _, err := git(dir, "init", "-b", "main"); err != nil {
			// Older git without -b: plain init, then point HEAD at main.
			if _, err := git(dir, "init"); err != nil {
				return did, err
			}
			_, _ = git(dir, "symbolic-ref", "HEAD", "refs/heads/main")
		}
		did = append(did, "git init "+poolRel(abs)+"/ on main")
	}
	if st.ignoreMissing {
		f, err := os.OpenFile(filepath.Join(dir, PoolIgnoreFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return did, err
		}
		_, werr := f.WriteString(poolIgnoreBlock)
		f.Close()
		if werr != nil {
			return did, werr
		}
		did = append(did, "wrote "+poolRel(abs, PoolIgnoreFile))
	}
	if st.memoryIgnoreMissing {
		if err := ensureGitignoreLine(dir, MemoryDir+"/"); err != nil {
			return did, err
		}
		did = append(did, "added "+MemoryDir+"/ to "+poolRel(abs, PoolIgnoreFile))
		if !st.commitMissing {
			if _, err := commitPoolPaths(dir, []string{PoolIgnoreFile}, poolIgnoreMemoryCommit, ""); err != nil {
				return did, fmt.Errorf("commit in %s/ failed (%v) — set git user.name and user.email, then rerun `meshctl pool upgrade`", PoolDirName(abs), err)
			}
		}
	}
	if st.commitMissing {
		// The member list goes in the first commit with the rest.
		if did, err = refreshMembersStep(abs, did); err != nil {
			return did, err
		}
		if _, err := git(dir, "add", "-A"); err != nil {
			return did, err
		}
		if _, err := git(dir, "commit", "-q", "-m", poolInitialCommit); err != nil {
			return did, fmt.Errorf("first commit in %s/ failed (%v) — set git user.name and user.email, then rerun `meshctl pool upgrade`", PoolDirName(abs), err)
		}
		did = append(did, "first commit in "+poolRel(abs)+"/ (`"+poolInitialCommit+"`)")
	} else if subject := st.configCommit(); subject != "" {
		// The repo already has history: the config split is its own commit, in
		// the user's identity like every other pool commit.
		if _, err := git(dir, "add", "--", PoolMarkerFile, PoolConfigFile); err != nil {
			return did, err
		}
		if _, err := git(dir, "commit", "-q", "-m", subject); err != nil {
			return did, fmt.Errorf("commit in %s/ failed (%v) — set git user.name and user.email, then rerun `meshctl pool upgrade`", PoolDirName(abs), err)
		}
		did = append(did, "committed "+poolRel(abs)+"/ (`"+subject+"`)")
	}
	return refreshMembersStep(abs, did)
}

// refreshMembersStep is the member-list step of the pool directory upgrade
// (pool-clone C1): rewrite and commit members.yaml when it differs from disk.
func refreshMembersStep(poolRoot string, did []string) ([]string, error) {
	changed, err := RefreshMembers(poolRoot)
	if changed {
		did = append(did, "member list: "+poolRel(poolRoot, MembersFile))
	}
	return did, err
}

// repairWorktreeLinks re-points the peer repos' worktree metadata at the
// renamed worktrees directory (each worktree's `.git` file names the owning
// repo, which holds the back-pointer). Best-effort: a worktree whose repo is
// gone is hygiene's job, not the upgrade's.
func repairWorktreeLinks(poolDir string) int {
	entries, err := os.ReadDir(filepath.Join(poolDir, WorktreesDirName))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		wt := filepath.Join(poolDir, WorktreesDirName, e.Name())
		b, err := os.ReadFile(filepath.Join(wt, ".git"))
		if err != nil {
			continue
		}
		gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		marker := string(filepath.Separator) + ".git" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
		i := strings.Index(gitdir, marker)
		if i < 0 {
			continue
		}
		if _, err := git(gitdir[:i], "worktree", "repair", wt); err == nil {
			n++
		}
	}
	return n
}
