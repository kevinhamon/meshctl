package mesh

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// state_confirm.go — `state set` is a promotion (ADR-0067; spec decision 25).
//
// The current-state digest is current-tier knowledge written on main with no
// KB session, so it meets the same confirmation as `kb finish`: the gate asks
// on `meshctl state set --summary-file <path>` with the summary as the ask
// text (gateStateSet), and the command writes the digest, records one ledger
// hash per section of it (the state header comment excluded, SectionHashes)
// and commits both as `Confirm: <Intent line>`. No human at the prompt
// (dispatch depth, a distil run) or no usable summary is a refusal.

var (
	// ErrStateHeadless: `state set` with no human at the prompt to confirm it.
	ErrStateHeadless = errors.New("state set: a human must be at the prompt")
	// ErrStateSummaryRequired: `state set` with no usable confirmation summary.
	ErrStateSummaryRequired = errors.New("state set: needs a confirmation summary")
)

// StateSetOptions configures StateSetConfirmed.
type StateSetOptions struct {
	// SummaryFile is the peer's confirmation summary (required).
	SummaryFile string
	// Session names the confirming session in the ledger and the commit.
	Session string
	// Now stamps the digest header and dates the ledger line; zero = now.
	Now time.Time
}

// StateSetResult reports what a confirmed `state set` landed.
type StateSetResult struct {
	Path     string   // the digest file
	Ledgered []string // ledger keys written (state.md#<anchor>)
	Dropped  []string // ledger keys removed (sections no longer in the digest)
	Confirm  string   // short sha of the Confirm: commit
	Intent   string   // the summary's Intent line
}

// StateSetConfirmed writes the digest as a promotion: refused headless, with
// no usable summary, or where the gate could not have asked (KBTrustGuard);
// otherwise it writes knowledge/state.md, replaces the ledger's state.md line
// with one hash per section of the new digest, and commits both files as
// `Confirm: <Intent line>`. On a failed write or commit both files are put
// back as they were. Lint is the caller's gate, as for StateSet.
func StateSetConfirmed(a *Agent, body string, o StateSetOptions) (StateSetResult, error) {
	res := StateSetResult{Path: StatePath(a)}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if HeadlessNow() {
		return res, fmt.Errorf("%w to confirm the current-state digest — `state set` is a promotion and is refused "+
			"at dispatch depth or in a distil run; leave the digest for an interactive session of %s", ErrStateHeadless, a.Name)
	}
	expected := SummaryPath(a.Dir(), o.Session)
	s, why := usableSummary(o.SummaryFile)
	if why != "" {
		return res, fmt.Errorf("%w (%s): write it at %s (`## Intent` one line first, `## Grounding`, `## Precedent`, "+
			"`## Changes` with a `### state.md` block), read the confirmation round out in chat, then re-run "+
			"`meshctl state set --summary-file %s` — the digest is untouched", ErrStateSummaryRequired, why, expected, expected)
	}
	if err := KBTrustGuard(a); err != nil {
		return res, fmt.Errorf("state set refused: %w", err)
	}
	if !isGitRepo(a.Dir()) {
		return res, fmt.Errorf("state set: %s is not a git repo — the digest is committed with its ledger line", a.Dir())
	}
	l, err := ReadLedger(a.Dir())
	if err != nil {
		return res, fmt.Errorf("state set: the ledger %s cannot be read (%v) — fix it first; the digest is untouched", ledgerRepoPath, err)
	}
	res.Intent = s.Intent

	statePath := StatePath(a)
	ledgerPath := LedgerPath(a.Dir())
	restore := snapshotFiles(statePath, ledgerPath)
	stateRepo := KnowledgeDir + "/" + StateFile
	fail := func(err error) (StateSetResult, error) {
		restore()
		_, _ = git(a.Dir(), "reset", "-q", "--", stateRepo, ledgerRepoPath)
		return res, fmt.Errorf("state set: %w — the digest and ledger were put back as they were", err)
	}

	if err := StateSet(a, body, now); err != nil {
		return fail(err)
	}
	cur, err := CurrentLedgerEntry(a.Dir(), StateFile)
	if err != nil {
		return fail(err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	for _, k := range ledgerKeys(l.Entries[StateFile].Sections) {
		if _, ok := cur.Sections[k]; !ok {
			res.Dropped = append(res.Dropped, StateFile+"#"+k)
		}
	}
	for _, k := range ledgerKeys(cur.Sections) {
		res.Ledgered = append(res.Ledgered, StateFile+"#"+k)
	}
	l.Entries[StateFile] = LedgerEntry{Sections: cur.Sections, Confirmed: now.Format("2006-01-02"), Session: o.Session}
	if err := WriteLedger(a.Dir(), l); err != nil {
		return fail(err)
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "Agent: %s\n%s%s\n\nConfirmed:\n", a.Name, ConfirmSessionTrailer, o.Session)
	for _, k := range res.Ledgered {
		msg.WriteString("- " + k + "\n")
	}
	if len(res.Dropped) > 0 {
		msg.WriteString("\nDropped from the ledger:\n")
		for _, k := range res.Dropped {
			msg.WriteString("- " + k + "\n")
		}
	}
	if _, err := git(a.Dir(), "add", "--", stateRepo, ledgerRepoPath); err != nil {
		return fail(err)
	}
	if _, err := git(a.Dir(), "commit", "-q", "--allow-empty", "-m", ConfirmSubjectPrefix+s.Intent, "-m", msg.String(),
		"--", stateRepo, ledgerRepoPath); err != nil {
		return fail(err)
	}
	sha, err := git(a.Dir(), "rev-parse", "--short", "HEAD")
	res.Confirm = strings.TrimSpace(sha)
	return res, err
}

// snapshotFiles records the files' bytes (or absence) and returns a func
// that puts them back.
func snapshotFiles(paths ...string) func() {
	type snap struct {
		b  []byte
		ok bool
	}
	saved := map[string]snap{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		saved[p] = snap{b, err == nil}
	}
	return func() {
		for p, s := range saved {
			if s.ok {
				_ = writeFileAtomic(p, s.b, 0o644)
			} else {
				_ = os.Remove(p)
			}
		}
	}
}
