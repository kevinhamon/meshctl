package mesh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// kb_finish.go — the atomic finish (ADR-0067; spec decisions 21, 22, 23).
//
// A KB session lands on main through `kb finish`. A candidates-only branch is
// a proposal: it fast-forwards main as it always has, with no ledger change.
// A branch that touches anything else is a promotion, and the ledger records
// that the human confirmed it — so before main moves, finish computes the
// rebased branch's promoted paths, renders the new ledger from the branch's
// content, reads the peer's summary and runs the trust guard. Then it
// fast-forwards main, writes the ledger and commits `Confirm: <Intent line>`
// as one operation: when the ledger write or the commit fails, main is reset
// to its pre-merge ref and the branch and worktree stay, so main is never left
// merged and unconfirmed (the failure the ticket 14 prototype hit).
//
// The ledger trusts the gate: the signature leaves no trace except this commit, so
// a promotion is refused where the gate could not have asked (KBTrustGuard).

// PromotionAskWiringVersion is the first mesh wiring version whose gate
// carries the promotion ask (the shared-understanding gate policy rode the v5
// bump, settings.go). A peer stamped below it is refused promotion.
const PromotionAskWiringVersion = 5

// ConfirmSubjectPrefix starts the subject of every confirmation commit.
const ConfirmSubjectPrefix = "Confirm: "

// ConfirmSessionTrailer names the confirming session in a Confirm: commit's
// body (`Session: <id>`); KBConfirmCommits finds a session's commits by it.
const ConfirmSessionTrailer = "Session: "

var (
	// ErrKBSummaryRequired: a promotion with no usable summary (none, empty,
	// or no Intent line). Refused before main is touched.
	ErrKBSummaryRequired = errors.New("kb finish: promotion needs a confirmation summary")
	// ErrKBTrustGuard: a promotion from a peer whose gate could not have
	// asked — gate absent, wiring stale, or a harness with no ask path.
	ErrKBTrustGuard = errors.New("kb finish: trust guard")
	// ErrKBConfirmFailed: the ledger write or the Confirm: commit failed after
	// the fast-forward; main was reset to its pre-merge ref.
	ErrKBConfirmFailed = errors.New("kb finish: confirmation failed")
)

// KBFinishOptions configures KBFinish.
type KBFinishOptions struct {
	// Abort discards the branch and worktree instead of merging.
	Abort bool
	// Headless never auto-resolves a rebase conflict (ErrKBConflict) and
	// parks a promotion as needs-confirm (ErrKBNeedsConfirm). Dispatch depth
	// set and non-zero, or a distil run, imply it.
	Headless bool
	// SummaryFile is the peer's confirmation summary; required when the
	// finish promotes, ignored for a candidates-only finish.
	SummaryFile string
	// Now dates the ledger lines; zero means time.Now().
	Now time.Time
}

// KBFinishResult reports what a finish landed.
type KBFinishResult struct {
	Branch   string   // the KB session branch
	Promoted []string // kb-relative promoted paths (nil: candidates only)
	Ledgered []string // ledger keys written (path, or path#anchor for sections)
	Dropped  []string // ledger keys removed (deleted file or section)
	Confirm  string   // short sha of the Confirm: commit ("" when none)
	Intent   string   // the summary's Intent line ("" when none)
}

// kbFinishFailpoint, when set (tests), is called at each stage of the
// confirm step; a non-nil error fails the finish there. Stages: "merged"
// (after the fast-forward), "ledger-written" (before the commit).
var kbFinishFailpoint func(stage string) error

func kbFinishStage(stage string) error {
	if kbFinishFailpoint == nil {
		return nil
	}
	return kbFinishFailpoint(stage)
}

// KBTrustGuard refuses a promotion when the peer's gate could not have put
// the promotion ask to a human: a harness with no ask path (opencode today),
// the PreToolUse gate not wired, or mesh wiring stamped below
// PromotionAskWiringVersion. nil means the ledger may trust the gate.
// `kb finish` and `kb confirm` both run it before touching main.
func KBTrustGuard(a *Agent) error {
	fix := "run `meshctl agent onboard " + a.Name + "`, then re-run from a gated session"
	if h := a.HarnessName(); !harnessHasAskForm(h) {
		return fmt.Errorf("%w: harness %s has no ask path, so the gate cannot ask the human to confirm a promotion — "+
			"finish it from a Claude Code session of %s (the branch and worktree are left intact)", ErrKBTrustGuard, h, a.Name)
	}
	if !gateHookWired(a.Dir()) {
		return fmt.Errorf("%w: %s's PreToolUse gate is not wired, so no promotion ask could have been shown — %s",
			ErrKBTrustGuard, a.Name, fix)
	}
	if v := WiringVersion(a.Dir()); v < PromotionAskWiringVersion {
		return fmt.Errorf("%w: %s's mesh wiring is stale (v%d < v%d, the version that carries the promotion ask) — %s",
			ErrKBTrustGuard, a.Name, v, PromotionAskWiringVersion, fix)
	}
	return nil
}

// gateHookWired reports whether the agent's .claude/settings.json wires the
// mesh PreToolUse gate (the check doctor's gateWiringFindings errors on).
func gateHookWired(agentDir string) bool {
	b, err := os.ReadFile(filepath.Join(agentDir, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	return hookCommandPresent(entries, "meshctl hook pre-tool-use")
}

// KBFinish rebases the session branch onto main and lands it ff-only, then
// removes the worktree. A promotion lands atomically with its ledger lines
// and a `Confirm: <Intent line>` commit (see the file comment); a
// candidates-only finish merges with no ledger change. Abort discards the
// branch and worktree. Headless never auto-resolves a conflict
// (ErrKBConflict); interactive leaves the rebase in progress for the human.
// The branch is mesh/kb/<session>, or the legacy session/<session>.
func KBFinish(agentsDir string, agent *Agent, session string, o KBFinishOptions) (KBFinishResult, error) {
	wt := worktreePath(agentsDir, agent.Name, session)
	branch := kbBranchFor(agent.Dir(), session)
	res := KBFinishResult{Branch: branch}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}

	clearSessionBranch := func() {
		if s, err := readSession(sessionPath(agent.Dir(), session)); err == nil {
			s.KBBranch = ""
			_ = writeSession(s)
		}
	}

	if o.Abort {
		_, _ = git(agent.Dir(), "worktree", "remove", "--force", wt)
		_, _ = git(agent.Dir(), "branch", "-D", branch)
		clearSessionBranch()
		clearParkMarker(agent.Dir(), session)
		return res, nil
	}

	if !dirExists(wt) {
		return res, fmt.Errorf("no KB worktree for session %s (run `meshctl kb begin` first)", session)
	}

	// Require the worktree to be committed — we merge commits, not a dirty tree.
	if status, _ := git(wt, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		return res, fmt.Errorf("worktree %s has uncommitted changes — commit your KB edits first", wt)
	}

	base := mainBranch(agent.Dir())
	noHuman := kbNoHumanCause(o.Headless)

	// Before the rebase: what the branch promotes (against the merge-base),
	// so a park or a refusal that does not need the rebase leaves the branch
	// exactly as the peer committed it.
	changed, err := KBBranchChanges(agent.Dir(), branch)
	if err != nil {
		return res, fmt.Errorf("cannot diff %s against %s, so a promotion cannot be told from a candidates-only finish: %w", branch, base, err)
	}
	res.Promoted = promotedOf(changed)

	// No human at the prompt: a promotion parks as needs-confirm, before
	// main moves and before any rebase (kb_needs_confirm.go); candidates-only
	// still merges.
	if len(res.Promoted) > 0 && noHuman != "" {
		return res, parkNeedsConfirm(agentsDir, agent, session, branch, wt, o.SummaryFile, res.Promoted, noHuman)
	}
	if len(res.Promoted) > 0 {
		if _, err := checkKBConfirm(agent, session, branch, changed, o.SummaryFile); err != nil {
			return res, err
		}
	}

	// Rebase the session branch onto the base, inside the worktree.
	if _, err := git(wt, "rebase", base); err != nil {
		if noHuman != "" {
			_, _ = git(wt, "rebase", "--abort")
			return res, ErrKBConflict
		}
		return res, fmt.Errorf("%w: resolve conflicts in %s, `git rebase --continue`, then re-run `meshctl kb finish`", ErrKBConflict, wt)
	}

	// The rebased branch is what lands: recompute what it promotes.
	if changed, err = KBBranchChanges(agent.Dir(), branch); err != nil {
		return res, fmt.Errorf("cannot diff %s against %s, so a promotion cannot be told from a candidates-only finish: %w", branch, base, err)
	}
	res.Promoted = promotedOf(changed)
	if len(res.Promoted) > 0 && noHuman != "" {
		return res, parkNeedsConfirm(agentsDir, agent, session, branch, wt, o.SummaryFile, res.Promoted, noHuman)
	}

	var confirm *kbConfirm
	if len(res.Promoted) > 0 {
		if confirm, err = prepareKBConfirm(agent, session, branch, base, wt, changed, o.SummaryFile, now); err != nil {
			return res, err
		}
		res.Intent, res.Ledgered, res.Dropped = confirm.intent, confirm.ledgered, confirm.dropped
	}

	pre, err := git(agent.Dir(), "rev-parse", "refs/heads/"+base)
	if err != nil {
		return res, err
	}
	pre = strings.TrimSpace(pre)

	// Fast-forward the base to the rebased branch.
	if _, err := git(agent.Dir(), "merge", "--ff-only", branch); err != nil {
		return res, fmt.Errorf("ff-only merge of %s into %s failed: %w", branch, base, err)
	}
	if confirm != nil {
		sha, err := confirm.commit(agent.Dir())
		if err != nil {
			return res, rollbackKBFinish(agent.Dir(), base, branch, wt, pre, err)
		}
		res.Confirm = sha
	}

	// Clean up.
	if _, err := git(agent.Dir(), "worktree", "remove", "--force", wt); err != nil {
		return res, err
	}
	_, _ = git(agent.Dir(), "branch", "-D", branch)
	clearSessionBranch()
	clearParkMarker(agent.Dir(), session)
	return res, nil
}

// kbConfirm is a promotion's confirm step, fully computed before main moves.
type kbConfirm struct {
	intent, body      string
	ledger            []byte
	ledgered, dropped []string
}

var ledgerRepoPath = KnowledgeDir + "/" + LedgerFile

// promotedOf is the kb-relative promoted paths among a branch's changes.
func promotedOf(changed []string) []string {
	var out []string
	for _, c := range changed {
		if IsPromotedPath(c) {
			out = append(out, KBRelPath(c))
		}
	}
	return out
}

// checkKBConfirm runs the promotion refusals that need no rebase — trust
// guard, a usable summary, a branch that leaves the ledger alone — and
// returns the summary.
func checkKBConfirm(agent *Agent, session, branch string, changed []string, summaryFile string) (Summary, error) {
	if err := KBTrustGuard(agent); err != nil {
		return Summary{}, err
	}
	expected := SummaryPath(agent.Dir(), session)
	s, why := usableSummary(summaryFile)
	if why != "" {
		return Summary{}, fmt.Errorf("%w (%s): write it at %s (`## Intent` one line first, `## Grounding`, `## Precedent`, "+
			"`## Changes`), read the confirmation round out in chat, then re-run `meshctl kb finish --summary-file %s` — "+
			"main is untouched", ErrKBSummaryRequired, why, expected, expected)
	}

	for _, c := range changed {
		if filepath.ToSlash(c) == ledgerRepoPath {
			return Summary{}, fmt.Errorf("kb finish: %s edits %s — the ledger is written only by meshctl at confirmation; "+
				"drop that change from the branch (main is untouched)", branch, ledgerRepoPath)
		}
	}
	return s, nil
}

// prepareKBConfirm runs every check a promotion needs before main is
// touched — the checkKBConfirm refusals and a readable ledger — and renders
// the ledger and commit message from the rebased branch.
func prepareKBConfirm(agent *Agent, session, branch, base, wt string, changed []string, summaryFile string, now time.Time) (*kbConfirm, error) {
	s, err := checkKBConfirm(agent, session, branch, changed, summaryFile)
	if err != nil {
		return nil, err
	}

	// The branch tip, rebased on main, holds main's ledger.
	l, err := ReadLedger(wt)
	if err != nil {
		return nil, fmt.Errorf("kb finish: the ledger %s cannot be read (%v) — fix it before promoting; main is untouched", ledgerRepoPath, err)
	}
	ledgered, dropped, err := applyFinishLedger(&l, agent.Dir(), wt, base, changed, session, now)
	if err != nil {
		return nil, err
	}

	var body strings.Builder
	fmt.Fprintf(&body, "Agent: %s\nBranch: %s\n%s%s\n", agent.Name, branch, ConfirmSessionTrailer, session)
	if len(ledgered) > 0 {
		body.WriteString("\nConfirmed:\n")
		for _, k := range ledgered {
			body.WriteString("- " + k + "\n")
		}
	}
	if len(dropped) > 0 {
		body.WriteString("\nDropped from the ledger:\n")
		for _, k := range dropped {
			body.WriteString("- " + k + "\n")
		}
	}
	return &kbConfirm{
		intent:   s.Intent,
		body:     body.String(),
		ledger:   RenderLedger(l),
		ledgered: ledgered,
		dropped:  dropped,
	}, nil
}

// applyFinishLedger updates l for the branch's changed paths: each promoted
// current- or log-tier file gets its new hash; a sectioned file re-hashes only
// the sections the branch changed (against base) and drops removed sections;
// a deleted file drops its line. It returns the keys written and dropped.
func applyFinishLedger(l *Ledger, agentDir, wt, base string, changed []string, session string, now time.Time) (ledgered, dropped []string, err error) {
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	date := now.Format("2006-01-02")
	for _, c := range changed {
		c = filepath.ToSlash(c)
		if !strings.HasPrefix(c, KnowledgeDir+"/") || !IsPromotedPath(c) || c == ledgerRepoPath {
			continue
		}
		rel := KBRelPath(c)
		if t := kbTier(rel); t != TierCurrent && t != TierLog {
			continue
		}
		if !fileExists(filepath.Join(wt, filepath.FromSlash(c))) {
			if _, ok := l.Entries[rel]; ok {
				delete(l.Entries, rel)
				dropped = append(dropped, rel)
			}
			continue
		}
		cur, err := CurrentLedgerEntry(wt, rel)
		if err != nil {
			return nil, nil, err
		}
		if !LedgerSectioned(rel) {
			cur.Confirmed, cur.Session = date, session
			l.Entries[rel] = cur
			ledgered = append(ledgered, rel)
			continue
		}
		old := map[string]string{}
		if b, err := git(agentDir, "show", base+":"+c); err == nil {
			old = SectionHashes(b)
		}
		e := l.Entries[rel]
		secs := map[string]string{}
		for a, h := range e.Sections {
			secs[a] = h
		}
		for _, a := range ledgerKeys(secs) {
			if _, ok := cur.Sections[a]; !ok {
				delete(secs, a)
				dropped = append(dropped, rel+"#"+a)
			}
		}
		for _, a := range ledgerKeys(cur.Sections) {
			if h := cur.Sections[a]; old[a] != h {
				secs[a] = h
				ledgered = append(ledgered, rel+"#"+a)
			}
		}
		if len(secs) == 0 {
			if _, ok := l.Entries[rel]; ok {
				delete(l.Entries, rel)
				dropped = append(dropped, rel)
			}
			continue
		}
		l.Entries[rel] = LedgerEntry{Sections: secs, Confirmed: date, Session: session}
	}
	return ledgered, dropped, nil
}

// commit writes the ledger on the main checkout and commits it as
// `Confirm: <Intent line>` (only the ledger path; the merged content is
// already on main). Returns the short sha.
func (c *kbConfirm) commit(agentDir string) (string, error) {
	if err := kbFinishStage("merged"); err != nil {
		return "", err
	}
	if err := writeFileAtomic(LedgerPath(agentDir), c.ledger, 0o644); err != nil {
		return "", fmt.Errorf("write ledger: %w", err)
	}
	if _, err := git(agentDir, "add", "--", ledgerRepoPath); err != nil {
		return "", err
	}
	if err := kbFinishStage("ledger-written"); err != nil {
		return "", err
	}
	if _, err := git(agentDir, "commit", "-q", "--allow-empty", "-m", ConfirmSubjectPrefix+c.intent, "-m", c.body,
		"--", ledgerRepoPath); err != nil {
		return "", err
	}
	sha, err := git(agentDir, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(sha), err
}

// rollbackKBFinish undoes a fast-forward whose confirm step failed: the
// ledger file goes back to the merged tip's content, then main is reset to
// its pre-merge ref keeping any unrelated local changes (`reset --keep`).
// The branch and worktree are untouched. The error says what state is left.
func rollbackKBFinish(agentDir, base, branch, wt, pre string, cause error) error {
	if _, err := git(agentDir, "cat-file", "-e", "HEAD:"+ledgerRepoPath); err == nil {
		_, _ = git(agentDir, "checkout", "HEAD", "--", ledgerRepoPath)
	} else {
		_, _ = git(agentDir, "rm", "-q", "--cached", "--ignore-unmatch", "--", ledgerRepoPath)
		_ = os.Remove(LedgerPath(agentDir))
	}
	if _, err := git(agentDir, "reset", "-q", "--keep", pre); err != nil {
		return fmt.Errorf("%w: %v — and resetting %s to its pre-merge ref %s failed (%v): %s is merged but UNCONFIRMED; "+
			"run `git -C %s reset --keep %s`, then re-run `meshctl kb finish` (branch %s and worktree %s are intact)",
			ErrKBConfirmFailed, cause, base, short(pre), err, base, agentDir, short(pre), branch, wt)
	}
	return fmt.Errorf("%w: %v — %s was reset to its pre-merge ref %s, so nothing landed unconfirmed; branch %s and worktree %s "+
		"are intact: fix the cause and re-run `meshctl kb finish`", ErrKBConfirmFailed, cause, base, short(pre), branch, wt)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// KBConfirmCommits lists the `Confirm:` commits on the agent repo's main
// branch made for a session (by its `Session: <id>` trailer), newest first,
// each as "<short sha> <subject>". nil when none or not a repo.
func KBConfirmCommits(agentDir, session string) []string {
	if session == "" || !isGitRepo(agentDir) {
		return nil
	}
	out, err := git(agentDir, "log", mainBranch(agentDir), "-E",
		"--grep=^"+regexp.QuoteMeta(ConfirmSessionTrailer+session)+"$", "--format=%h %s")
	if err != nil {
		return nil
	}
	var commits []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if _, subj, ok := strings.Cut(ln, " "); ok && strings.HasPrefix(subj, ConfirmSubjectPrefix) {
			commits = append(commits, ln)
		}
	}
	return commits
}
