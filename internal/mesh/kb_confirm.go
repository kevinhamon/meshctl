package mesh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// kb_confirm.go — bulk confirmation from a reconcile report. `meshctl kb
// confirm <path>... --report <file>` runs on the peer's main checkout with no
// KB branch: it writes the ledger lines for the named entries and commits them
// as `Confirm: <report Intent>` in one step (atomically). The gate asks on it
// like `kb finish`, with the report's lines for the named paths as the ask text
// (gate_kb_confirm.go).
//
// A unit is `<path>` (a whole file; for a sectioned file every section) or
// `<path>#<slug>` (one section of a sectioned file). At most KBConfirmMax
// units per call. Each unit's latest report verdict must be `consistent` or
// `judgment`; `ungrounded` passes only when accepted per path; `conflict` and
// `term-vs-fact` refuse, except a non-owner's `conflict → owner <peer>,
// pending` once the owner's ledger confirms the counterpart. Every refusal is
// computed before anything is written.

// KBConfirmMax caps the units of one `kb confirm` call.
const KBConfirmMax = 5

// ErrKBConfirmRefused: `kb confirm` refused before writing anything.
var ErrKBConfirmRefused = errors.New("kb confirm refused")

// KBConfirmOptions configures KBConfirm.
type KBConfirmOptions struct {
	Report           string   // the reconcile report (required)
	AcceptUngrounded []string // units whose `ungrounded` verdict the human overrode in the round
	Session          string   // names the confirming session in the ledger and the commit
	Now              time.Time
}

// KBConfirmResult reports what a confirm landed.
type KBConfirmResult struct {
	Ledgered []string // ledger keys written (path, or path#anchor)
	Confirm  string   // short sha of the Confirm: commit
	Intent   string   // the report's Intent (or first) line
}

// kbConfirmUnit is one resolved unit: a file and the anchors it confirms
// (nil for a whole non-sectioned file).
type kbConfirmUnit struct {
	arg, rel string
	anchors  []string
}

// kbConfirmCheck resolves the named units against the peer's knowledge and
// the report; refusals lists every unit that may not be confirmed, with why.
// Read-only; shared by KBConfirm and the gate's ask.
func kbConfirmCheck(agentsDir string, a *Agent, args []string, r ReconcileReport, accept []string) (units []kbConfirmUnit, refusals []string) {
	accepted := map[string]bool{}
	for _, p := range accept {
		accepted[kbRelPath(p)] = true
	}
	kb := filepath.Join(a.Dir(), KnowledgeDir)
	for _, arg := range args {
		addr := kbRelPath(arg)
		rel, anchor, _ := strings.Cut(addr, "#")
		refuse := func(why string) { refusals = append(refusals, arg+": "+why) }
		b, err := os.ReadFile(filepath.Join(kb, filepath.FromSlash(rel)))
		if err != nil {
			refuse("no such entry in " + KnowledgeDir + "/")
			continue
		}
		if t := kbTier(rel); t != TierCurrent && t != TierLog {
			refuse("not a current- or log-tier entry (candidates are proposals, never confirmed)")
			continue
		}
		u := kbConfirmUnit{arg: arg, rel: rel}
		var ids []string
		switch {
		case LedgerSectioned(rel):
			secs := splitSections(string(b))
			for _, s := range secs {
				if anchor == "" || s.Anchor == anchor {
					u.anchors = append(u.anchors, s.Anchor)
					ids = append(ids, rel+"#"+s.Anchor)
				}
			}
			if len(u.anchors) == 0 {
				refuse("no section #" + anchor + " in " + rel)
				continue
			}
		case anchor != "":
			refuse(rel + " is confirmed as a whole file; address it without #" + anchor)
			continue
		default:
			ids = []string{rel}
		}
		ok := true
		for _, id := range ids {
			l, found := r.Latest(id)
			if !found && id != rel {
				l, found = r.Latest(rel) // a bare line covers a section with none of its own
			}
			if why := kbConfirmVerdict(agentsDir, id, l, found, accepted[id] || accepted[rel] || accepted[addr]); why != "" {
				refusals = append(refusals, arg+": "+why)
				ok = false
			}
		}
		if ok {
			units = append(units, u)
		}
	}
	return units, refusals
}

// kbConfirmVerdict is "" when an entry's latest line lets it be confirmed,
// else why not.
func kbConfirmVerdict(agentsDir, id string, l ReconcileReportLine, found, accepted bool) string {
	if !found {
		return id + " has no report line — reconcile it first (`meshctl kb reconcile --peer`), then write its verdict"
	}
	at := fmt.Sprintf(" (report line %d)", l.Line)
	switch l.Verdict {
	case ReconcileConsistent, ReconcileJudgment:
		return ""
	case ReconcileUngrounded:
		if accepted {
			return ""
		}
		return id + " is ungrounded" + at + " — ground it, or pass `--accept-ungrounded " + id + "` after the human overrode it in the round"
	case ReconcileTermVsFact:
		return id + " is term-vs-fact" + at + " — split it: the fact to a fact tier, the term stays a definition; confirm both"
	case ReconcileConflict:
		if l.OwnerPending != "" {
			if ownerConfirms(agentsDir, l.Counterpart) {
				return ""
			}
			return id + " is a conflict pending its owner " + l.OwnerPending + at + " — refused until " + l.OwnerPending +
				"'s ledger confirms " + l.Counterpart
		}
		return id + " is a conflict with " + l.Counterpart + at + " — resolve it (fix this entry, correct the owner, or replace it with a mesh:// reference), re-verdict, then confirm"
	}
	return id + ": verdict " + l.Verdict + at + " cannot be confirmed"
}

// ownerConfirms reports whether a mesh:// counterpart is confirmed in its
// owner's ledger now (the section for a sectioned file's #slug, else the file).
func ownerConfirms(agentsDir, ref string) bool {
	r, err := ParseMeshRef(ref)
	if err != nil {
		return false
	}
	owner, err := FindAgent(agentsDir, r.Owner)
	if err != nil {
		return false
	}
	c := newLedgerCheck(owner.Dir())
	if r.Slug != "" && LedgerSectioned(r.Path) {
		return c.sectionConfirmed(r.Path, KebabSlug(r.Slug))
	}
	return c.confirmed(r.Path)
}

// KBConfirm confirms up to KBConfirmMax units of a canonical peer's knowledge
// from a reconcile report: it writes their ledger lines and commits the
// ledger as `Confirm: <report Intent>` on main, in one step. It refuses — with
// nothing written — more than KBConfirmMax units, no human at the prompt, no
// or a malformed report, a peer whose gate could not have asked
// (KBTrustGuard), a checkout not on main, an entry with uncommitted changes,
// and any unit kbConfirmCheck refuses. On a failed write or commit the ledger
// is put back.
func KBConfirm(agentsDir string, a *Agent, paths []string, o KBConfirmOptions) (KBConfirmResult, error) {
	var res KBConfirmResult
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	refuse := func(format string, args ...any) (KBConfirmResult, error) {
		return res, fmt.Errorf("%w: "+format+" — nothing was written", append([]any{ErrKBConfirmRefused}, args...)...)
	}
	switch {
	case len(paths) == 0:
		return refuse("name at least one path")
	case len(paths) > KBConfirmMax:
		return refuse("%d paths named; at most %d per call (a bare sectioned path counts as one)", len(paths), KBConfirmMax)
	case HeadlessNow():
		return refuse("a human must be at the prompt to confirm knowledge (dispatch depth or a distil run)")
	case o.Report == "":
		return refuse("no --report: name the reconcile report that holds these entries' verdicts (%s)", ReconcileReportPath(a.Dir(), now))
	}
	r, err := ReadReconcileReport(o.Report)
	if err != nil {
		return refuse("cannot read the report %s: %v", o.Report, err)
	}
	if err := r.Err(); err != nil {
		return refuse("%v", err)
	}
	if !IsCanonical(LoadPoolConfig(agentsDir), a) {
		return refuse("%s is a perspective peer — only canonical knowledge is reconciled and confirmed", a.Name)
	}
	if err := KBTrustGuard(a); err != nil {
		return res, fmt.Errorf("kb confirm refused: %w", err)
	}
	if !isGitRepo(a.Dir()) {
		return refuse("%s is not a git repo — the ledger is committed", a.Dir())
	}
	base := mainBranch(a.Dir())
	if head, _ := git(a.Dir(), "symbolic-ref", "--short", "HEAD"); strings.TrimSpace(head) != base {
		return refuse("%s's checkout is on %q, not %s — kb confirm runs on main", a.Name, strings.TrimSpace(head), base)
	}
	units, refusals := kbConfirmCheck(agentsDir, a, paths, r, o.AcceptUngrounded)
	var repo []string
	for _, u := range units {
		repo = append(repo, KnowledgeDir+"/"+u.rel)
	}
	if len(repo) > 0 {
		for _, d := range gitDirtyPaths(a.Dir(), repo...) {
			refusals = append(refusals, strings.TrimSpace(d)+": uncommitted changes — the ledger confirms committed content; commit it through `kb begin`/`kb finish` first")
		}
	}
	if len(refusals) > 0 {
		return refuse("\n  %s", strings.Join(refusals, "\n  "))
	}
	l, err := ReadLedger(a.Dir())
	if err != nil {
		return refuse("the ledger %s cannot be read (%v)", ledgerRepoPath, err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	date := now.Format("2006-01-02")
	for _, u := range units {
		cur, err := CurrentLedgerEntry(a.Dir(), u.rel)
		if err != nil {
			return refuse("%s: %v", u.arg, err)
		}
		if u.anchors == nil {
			cur.Confirmed, cur.Session = date, o.Session
			l.Entries[u.rel] = cur
			res.Ledgered = append(res.Ledgered, u.rel)
			continue
		}
		e := l.Entries[u.rel]
		secs := map[string]string{}
		for k, h := range e.Sections {
			if _, live := cur.Sections[k]; live {
				secs[k] = h
			}
		}
		for _, an := range u.anchors {
			secs[an] = cur.Sections[an]
			res.Ledgered = append(res.Ledgered, u.rel+"#"+an)
		}
		l.Entries[u.rel] = LedgerEntry{Sections: secs, Confirmed: date, Session: o.Session}
	}
	res.Intent = r.Intent

	restore := snapshotFiles(LedgerPath(a.Dir()))
	fail := func(err error) (KBConfirmResult, error) {
		restore()
		_, _ = git(a.Dir(), "reset", "-q", "--", ledgerRepoPath)
		return res, fmt.Errorf("kb confirm: %w — the ledger was put back as it was", err)
	}
	if err := WriteLedger(a.Dir(), l); err != nil {
		return fail(err)
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "Agent: %s\nReport: %s\n%s%s\n\nConfirmed:\n", a.Name, o.Report, ConfirmSessionTrailer, o.Session)
	for _, k := range res.Ledgered {
		msg.WriteString("- " + k + "\n")
	}
	if _, err := git(a.Dir(), "add", "--", ledgerRepoPath); err != nil {
		return fail(err)
	}
	if _, err := git(a.Dir(), "commit", "-q", "-m", ConfirmSubjectPrefix+res.Intent, "-m", msg.String(), "--", ledgerRepoPath); err != nil {
		return fail(err)
	}
	sha, err := git(a.Dir(), "rev-parse", "--short", "HEAD")
	res.Confirm = strings.TrimSpace(sha)
	return res, err
}

// KBConfirmAskLines are the report lines the gate shows for a `kb confirm`:
// per named unit, the latest line of each entry it covers, or a `!!` flag
// when the report has none.
func KBConfirmAskLines(a *Agent, args []string, r ReconcileReport) []string {
	var out []string
	for _, arg := range args {
		addr := kbRelPath(arg)
		rel, anchor, _ := strings.Cut(addr, "#")
		ids := []string{addr}
		if LedgerSectioned(rel) && anchor == "" {
			ids = nil
			if b, err := os.ReadFile(filepath.Join(a.Dir(), KnowledgeDir, filepath.FromSlash(rel))); err == nil {
				for _, s := range splitSections(string(b)) {
					ids = append(ids, rel+"#"+s.Anchor)
				}
			}
			if len(ids) == 0 {
				ids = []string{rel}
			}
		}
		for _, id := range ids {
			l, ok := r.Latest(id)
			if !ok && id != rel {
				l, ok = r.Latest(rel)
			}
			if !ok {
				out = append(out, id+"  !! no report line (kb confirm will refuse it)")
				continue
			}
			out = append(out, l.Raw)
		}
	}
	return out
}
