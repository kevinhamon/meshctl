package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// hygiene.go — pool hygiene: the drift a pool accumulates across meshctl
// iterations (stale runtime state, retired files, legacy vocab, leftovers of
// earlier upgrades). Each item is either auto-fixable (a safe, local, reversible
// or regenerable change) or manual (text a human wrote, which meshctl reports
// with a precise pointer and never edits). `meshctl doctor` reports them,
// `doctor --fix` applies the fixable ones, and `pool upgrade` applies them as its
// last step so an upgraded pool is also a clean one.

// HygieneItem is one piece of drift.
type HygieneItem struct {
	Agent   string // "pool" for pool-level items
	Message string
	Fix     func() error // nil ⇒ manual
}

// Fixable reports whether the item has an automatic repair.
func (h HygieneItem) Fixable() bool { return h.Fix != nil }

// hygieneStateMaxAge bounds per-session state files (nudge markers, rule acks):
// a session older than this is long dead, so its state is garbage.
const hygieneStateMaxAge = 7 * 24 * time.Hour

// hygieneBackupMaxAge bounds doctrine `.bak-*` files written by `pool upgrade
// --force`; after this they are noise next to the live doctrine.
const hygieneBackupMaxAge = 14 * 24 * time.Hour

// reRetiredCLI matches references to the retired `agentmesh` binary name and
// the removed `--project` flag in hand-written agent files.
var reRetiredCLI = regexp.MustCompile(`(?m)\bagentmesh +(intuition|memory|kb|ask|send|msg|inbox|sent|doctor|handoff|session|agent|pool)\b|--project +[A-Za-z]`)

// PoolHygiene scans the pool and every agent for drift. Items are ordered
// pool-level first, then by agent name.
func PoolHygiene(agentsDir string, now time.Time) []HygieneItem {
	var items []HygieneItem
	items = append(items, poolLevelHygiene(agentsDir, now)...)
	agents, _, err := ScanAgents(agentsDir)
	if err != nil {
		return items
	}
	for _, a := range agents {
		items = append(items, agentHygiene(agentsDir, a, now)...)
	}
	return items
}

// ApplyHygiene runs every fixable item's repair. Returns the messages of the
// items fixed and any errors (an error on one item does not stop the others).
func ApplyHygiene(items []HygieneItem) (fixed []string, errs []error) {
	for _, it := range items {
		if it.Fix == nil {
			continue
		}
		if err := it.Fix(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", it.Agent, it.Message, err))
			continue
		}
		fixed = append(fixed, "["+it.Agent+"] "+it.Message)
	}
	return fixed, errs
}

// ManualItems returns the items that need a human.
func ManualItems(items []HygieneItem) []HygieneItem {
	var out []HygieneItem
	for _, it := range items {
		if it.Fix == nil {
			out = append(out, it)
		}
	}
	return out
}

func removeFix(path string) func() error {
	return func() error {
		err := os.RemoveAll(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
}

// --- pool level ---

func poolLevelHygiene(agentsDir string, now time.Time) []HygieneItem {
	var items []HygieneItem
	pool := func(msg string, fix func() error) {
		items = append(items, HygieneItem{Agent: "pool", Message: msg, Fix: fix})
	}

	// Pool repo nag: manual by construction — meshctl never pushes.
	if nag := PoolRepoNag(agentsDir); nag != "" {
		pool(nag, nil)
	}

	// Member list (pool-clone C5): stale against disk ⇒ another machine would
	// rebuild a different pool. Refreshing it is meshctl's own file, so fixable.
	if IsPool(agentsDir) && !PoolDirIsLegacy(agentsDir) {
		if drift, err := MembersDrift(agentsDir); err == nil && len(drift) > 0 {
			pool("member list out of date ("+strings.Join(drift, "; ")+") — run `meshctl pool upgrade` to refresh "+
				poolRel(agentsDir, MembersFile), func() error { _, err := RefreshMembers(agentsDir); return err })
		}
	}

	// Members a clone left behind (kept in the list until they arrive).
	if IsPool(agentsDir) {
		kept := NotClonedHere(agentsDir)
		paths := make([]string, 0, len(kept))
		for p := range kept {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			dest := filepath.Join(agentsDir, filepath.FromSlash(p))
			why := "it has no git remote"
			switch {
			case dirExists(dest):
				why = "a folder that is not this agent is already there — move it aside, then: git clone " + kept[p].Remote + " " + dest
			case kept[p].Remote != "":
				why = "its clone failed — retry: git clone " + kept[p].Remote + " " + dest
			}
			pool(fmt.Sprintf("%s is in the member list but was not cloned to this machine (%s) — copy it to %s; if it is retired, delete %s and run `meshctl pool upgrade` to drop it",
				kept[p].Name, why, dest, cloneMissingPath(agentsDir)), nil)
		}
	}

	// Doctrine leftovers.
	doctrine := DoctrinePath(agentsDir)
	if entries, err := os.ReadDir(doctrine); err == nil {
		for _, e := range entries {
			name := e.Name()
			p := filepath.Join(doctrine, name)
			switch {
			case strings.Contains(name, ".bak-"):
				if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > hygieneBackupMaxAge {
					pool("doctrine backup "+name+" is older than 14 days", removeFix(p))
				}
			case strings.HasSuffix(name, upstreamSuffix):
				base := strings.TrimSuffix(name, upstreamSuffix)
				pool(fmt.Sprintf("doctrine %s differs from this meshctl's copy — merge %s into it (diff -u %s %s), "+
					"or `meshctl pool upgrade --force` to take meshctl's copy (yours is backed up); keep pool-specific "+
					"additions in %s/%s, which upgrades never touch", base, name, filepath.Join(doctrine, base), p, poolRel(agentsDir, DoctrineDirName), DoctrineLocalFile), nil)
			}
		}
	}

	// Dispatch lock whose holder is gone.
	lockPath := DispatchLockPath(agentsDir)
	if dirExists(lockPath) && lockReclaimable(lockPath, now) {
		pool("dispatch lock held by a dead process ("+readLockInfo(lockPath)+")", removeFix(lockPath))
	}

	// Orphan KB worktrees: no live session owns them.
	wtRoot := WorktreesPath(agentsDir)
	if entries, err := os.ReadDir(wtRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wt := filepath.Join(wtRoot, e.Name())
			agent := AgentFromWorktreePath(agentsDir, wt)
			if agent != "" && workWorktreeReq(agent, e.Name()) != "" {
				if it, ok := workWorktreeHygiene(agentsDir, agent, wt, workWorktreeReq(agent, e.Name()), now); ok {
					items = append(items, it)
				}
				continue
			}
			session := strings.TrimPrefix(e.Name(), agent+"-")
			if agent != "" && sessionAlive(AgentDir(agentsDir, agent), session, now) {
				continue
			}
			// A KB branch holding commits main lacks (a needs-confirm park, or
			// an unfinished session) may be real work: never reaped, and
			// warned only past BranchMaxAge.
			if agent != "" {
				if it, held := kbBranchHygiene(agentsDir, agent, session, wt, now); held {
					if it.Message != "" {
						items = append(items, it)
					}
					continue
				}
			}
			who := agent
			if who == "" {
				who = "unknown agent"
			}
			name := e.Name()
			pool("orphan KB worktree "+name+" ("+who+", no live session)", func() error {
				if agent != "" {
					ad := AgentDir(agentsDir, agent)
					_ = exec.Command("git", "-C", ad, "worktree", "remove", "--force", wt).Run()
					_ = exec.Command("git", "-C", ad, "branch", "-D", kbBranchFor(ad, session)).Run()
					_ = exec.Command("git", "-C", ad, "worktree", "prune").Run()
				}
				return removeFix(wt)()
			})
		}
	}
	return items
}

// hygieneWorkGrace is how long a delegated-work worktree is left alone. A
// fresh branch has no commits yet, so it is trivially "merged"; only after
// this does merged-and-clean mean "landed by hand, worktree left behind".
const hygieneWorkGrace = 24 * time.Hour

// workWorktreeHygiene classifies one delegated-work worktree. It
// normally lives until `meshctl work merge`; hygiene only steps in when the
// branch was already landed some other way (fixable: remove worktree +
// branch) or has sat unmerged past the state window (manual: a human decides
// to merge or --abort, since it may hold real work).
func workWorktreeHygiene(agentsDir, agent, wt, req string, now time.Time) (HygieneItem, bool) {
	info, err := os.Stat(wt)
	if err != nil {
		return HygieneItem{}, false
	}
	age := now.Sub(info.ModTime())
	dir := AgentDir(agentsDir, agent)
	branch := workBranch(req)
	name := filepath.Base(wt)
	st, stErr := git(wt, "status", "--porcelain")
	clean := stErr == nil && strings.TrimSpace(st) == ""
	_, ancErr := git(dir, "merge-base", "--is-ancestor", branch, mainBranch(dir))
	merged := ancErr == nil
	switch {
	case merged && clean && age > hygieneWorkGrace:
		return HygieneItem{Agent: "pool", Message: "delegated-work worktree " + name + " (" + branch + " already merged into " + agent + "'s main)",
			Fix: func() error {
				_, _ = git(dir, "worktree", "remove", "--force", wt)
				_, _ = git(dir, "branch", "-D", branch)
				_, _ = git(dir, "worktree", "prune")
				return removeFix(wt)()
			}}, true
	case !merged && age > BranchMaxAge:
		return HygieneItem{Agent: "pool", Message: fmt.Sprintf("delegated-work branch %s (%s) unmerged for %d days — review, then `meshctl work merge %s` or `--abort`",
			branch, agent, int(age.Hours()/24), req)}, true
	}
	return HygieneItem{}, false
}

// --- agent level ---

func agentHygiene(agentsDir string, a *Agent, now time.Time) []HygieneItem {
	var items []HygieneItem
	add := func(msg string, fix func() error) {
		items = append(items, HygieneItem{Agent: a.Name, Message: msg, Fix: fix})
	}
	dir := a.Dir()

	// Stale claims: heartbeat past the window and owner session gone.
	if entries, err := os.ReadDir(a.InboxPath()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".claim") {
				continue
			}
			cp := filepath.Join(a.InboxPath(), e.Name())
			c, err := readClaim(cp)
			if err != nil {
				add("unreadable claim sidecar "+e.Name(), removeFix(cp))
				continue
			}
			if reclaimable(strings.TrimSuffix(cp, ".claim"), c, now) {
				add("stale claim "+strings.TrimSuffix(e.Name(), ".md.claim")+" (session "+c.OwnerSession+" is gone)", removeFix(cp))
			}
		}
	}

	// Legacy KB branches (session/<id>, from before mesh/kb/) that no worktree
	// references: renamed in place. One a worktree still holds is left for
	// `kb finish`, which accepts either form.
	for _, id := range legacyKBBranches(dir) {
		if dirExists(worktreePath(agentsDir, a.Name, id)) {
			continue
		}
		id := id
		add("legacy KB branch "+legacyKBBranch(id)+" → "+KBBranch(id), func() error {
			if !gitRefExists(dir, legacyKBBranch(id)) {
				return nil
			}
			_, err := git(dir, "branch", "-m", legacyKBBranch(id), KBBranch(id))
			return err
		})
	}

	// Stale presence records.
	if entries, err := os.ReadDir(sessionsDir(dir)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue // jobs/: below
			}
			p := filepath.Join(sessionsDir(dir), e.Name())
			s, err := readSession(p)
			if err != nil || (int(now.Unix()-s.LastBeat) > staleSec() && !s.harnessAlive()) {
				add("stale session record "+e.Name(), removeFix(p))
			}
		}
	}
	// Scheduled-job lists of processes that have exited (or, kept per
	// session, of sessions that are gone).
	if entries, err := os.ReadDir(jobsDir(dir)); err == nil {
		for _, e := range entries {
			p := filepath.Join(jobsDir(dir), e.Name())
			var js JobSet
			b, err := os.ReadFile(p)
			gone := err != nil || json.Unmarshal(b, &js) != nil
			switch {
			case gone:
			case js.HarnessPID > 0:
				gone = !processAlive(js.HarnessPID)
			default:
				gone = !fileExists(sessionPath(dir, js.Session))
			}
			if gone {
				add("scheduled-job list of an ended session "+e.Name(), removeFix(p))
			}
		}
	}

	// Decisions whose status lives only in the human bullet: recall cannot
	// tell a superseded decision from its replacement without frontmatter.
	// The fix prepends the YAML block and leaves the text untouched.
	if need := adrNeedsFrontmatter(filepath.Join(dir, KnowledgeDir)); len(need) > 0 {
		paths := make([]string, 0, len(need))
		for _, d := range need {
			paths = append(paths, filepath.Join(dir, KnowledgeDir, "decisions", d.File))
		}
		add(fmt.Sprintf("%d decision(s) without status frontmatter (recall cannot drop superseded ones; adds `status:` from the Status bullet)", len(need)), func() error {
			for _, p := range paths {
				if err := migrateADRFrontmatter(p); err != nil {
					return err
				}
			}
			return nil
		})
	}

	// Retired memory index (recall computes live since v1.1; the file is dead).
	if p := filepath.Join(dir, MemoryDir, MemoryIndexFile); fileExists(p) {
		add("retired "+MemoryDir+"/"+MemoryIndexFile+" (recall is computed live; the file is unused)", removeFix(p))
	}

	// Per-session state older than the window: nudge markers, rule acks,
	// session acks (pool_commit.go), outbound audit and pending-ask files
	// (outbound_batch.go).
	for _, spec := range []struct{ glob, what string }{
		{filepath.Join(dir, MemoryDir, ".distill-nudge-*.json"), "distillation nudge marker"},
		{filepath.Join(dir, MemoryDir, ruleAckDir, "*.json"), "KB-rule ack"},
		{filepath.Join(dir, MemoryDir, SummaryDir, "*"+SessionAckFileExt), "session ack"},
		{filepath.Join(dir, MemoryDir, OutboundDir, "*.jsonl"), "outbound audit"},
		{filepath.Join(dir, MemoryDir, OutboundDir, "*.pending"), "outbound pending-ask"},
	} {
		matches, _ := filepath.Glob(spec.glob)
		var old []string
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && now.Sub(info.ModTime()) > hygieneStateMaxAge {
				old = append(old, m)
			}
		}
		if len(old) > 0 {
			paths := old
			add(fmt.Sprintf("%d %s file(s) from sessions older than 7 days", len(old), spec.what), func() error {
				for _, p := range paths {
					if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
						return err
					}
				}
				return nil
			})
		}
	}

	// Legacy intake status spellings and trailing comments on the status line.
	if reqs, err := ListRequests(a.InboxPath()); err == nil {
		for _, r := range reqs {
			raw := strings.TrimSpace(r.Status)
			word := raw
			if i := strings.IndexAny(raw, " \t#"); i >= 0 {
				word = raw[:i]
			}
			canon, ok := CanonicalStatus(word)
			if !ok || canon == raw {
				continue
			}
			path := r.path
			add(fmt.Sprintf("intake %s status %q → %q", r.ID, raw, canon), func() error {
				return setFrontmatterFields(path, map[string]string{"status": canon})
			})
		}
	}

	// References to the retired CLI name/flags in hand-written files. Managed
	// files (skills, the distiller subagent) are regenerated by upgrade; these
	// are the human's, so report file:line and leave them.
	for _, rel := range retiredCLIScanFiles(dir) {
		p := filepath.Join(dir, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), distillerAgentMarker) {
			continue // meshctl-managed; upgrade rewrites it
		}
		var lines []string
		for i, ln := range strings.Split(string(b), "\n") {
			if reRetiredCLI.MatchString(ln) {
				lines = append(lines, fmt.Sprint(i+1))
			}
		}
		if len(lines) > 0 {
			add(rel+":"+strings.Join(lines, ",")+" references the retired `agentmesh` command or `--project` flag — "+
				"replace with `meshctl … --agent "+a.Name+"`", nil)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Fix == nil && items[j].Fix != nil })
	return items
}

// retiredCLIScanFiles lists the hand-authored files worth scanning for retired
// CLI references: the role file, local settings, and subagent definitions.
func retiredCLIScanFiles(dir string) []string {
	out := []string{"CLAUDE.md", "AGENTS.md", filepath.Join(".claude", "settings.json"), filepath.Join(".claude", "settings.local.json")}
	if entries, err := os.ReadDir(filepath.Join(dir, ".claude", "agents")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				out = append(out, filepath.Join(".claude", "agents", e.Name()))
			}
		}
	}
	var present []string
	for _, rel := range out {
		if fileExists(filepath.Join(dir, rel)) {
			present = append(present, rel)
		}
	}
	return present
}

// DoctrineLocalFile is the pool-local doctrine addendum: house rules, the real
// roster, tool policy. meshctl never writes or upgrades it, so a pool keeps its
// customisations there and the core doctrine stays pristine (upgradable without
// conflicts).
const DoctrineLocalFile = "local.md"

// DoctrineLocalPath returns the addendum path for a pool.
func DoctrineLocalPath(agentsDir string) string {
	return filepath.Join(DoctrinePath(agentsDir), DoctrineLocalFile)
}

// kbBranchHygiene classifies an unheld KB worktree whose branch carries
// commits main lacks: held=true means it is not an orphan to reap; the item
// is a manual warning once the worktree is older than BranchMaxAge (empty
// Message before that).
func kbBranchHygiene(agentsDir, agent, session, wt string, now time.Time) (HygieneItem, bool) {
	dir := AgentDir(agentsDir, agent)
	branch := kbBranchFor(dir, session)
	out, err := git(dir, "rev-list", "--count", mainBranch(dir)+".."+branch)
	if err != nil || strings.TrimSpace(out) == "0" {
		return HygieneItem{}, false
	}
	info, err := os.Stat(wt)
	if err != nil {
		return HygieneItem{}, true
	}
	age := now.Sub(info.ModTime())
	if age <= BranchMaxAge {
		return HygieneItem{}, true
	}
	return HygieneItem{Agent: "pool", Message: fmt.Sprintf("KB branch %s (%s) unmerged for %d days — confirm it from %s's own session (`meshctl kb finish --agent %s --session %s`) or `--abort`",
		branch, agent, int(age.Hours()/24), agent, agent, session)}, true
}
