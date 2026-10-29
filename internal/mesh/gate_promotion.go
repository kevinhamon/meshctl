package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// gate_promotion.go — the promotion ask at `kb finish` and the main-checkout
// bypass it closes (ADR-0067 with amendment; spec decisions 18, 19, 20, 24).
//
// A KB session lands on main only through `meshctl kb finish`. When the
// session branch touches nothing but knowledge/candidates/, the finish is a
// proposal and passes silently. When it touches anything else — the current
// or the log tier — it is a promotion: the gate asks the human, and the ask
// text is the signature of the peer's own summary (summary.go): the Intent
// line, per promoted file `+n -m` and up to three added and three removed
// bullets, a flag for a promoted file the summary does not describe, and the
// deny-if-not-read line. No summary, an empty one or one with no Intent line
// is a deny naming the path; a branch that cannot be resolved or diffed is a
// deny with the cause.
//
// The diff runs here, inside meshctl, never as agent Bash: any agent command
// naming a mesh/kb/ branch is denied (deniedKBBranchCommand).
//
// The bypass: an agent editing <peer>/knowledge/** outside candidates/ on the
// peer's main checkout never meets `kb finish`. Such a write — by file tool or
// by shell — is denied; the same path inside a KB or work worktree is not.

// Check names of this file's rules (the decision log's `check=` field).
const (
	GateCheckPromotion   = "promotion"     // `kb finish` landing current/log-tier content (ask), or undiffable (deny)
	GateCheckKBMainWrite = "kb-main-write" // write to a peer's knowledge outside candidates on the main checkout
)

// CandidatesDir is the KB inbox under knowledge/: proposals, never promoted.
const CandidatesDir = "candidates"

// PendingDecisionsFile holds the distiller's open questions under knowledge/.
// Like candidates/ it is not confirmed knowledge, so it is written straight to
// the main checkout (issue #7).
const PendingDecisionsFile = "pending-decisions.md"

// PromotionAskFooter ends every promotion ask.
const PromotionAskFooter = "deny if this was not read to you as a confirmation round"

// promotionBulletMax caps the bullets shown per file and per kind; a bullet
// past promotionBulletChars is cut, so a three-file ask renders whole.
const (
	promotionBulletMax   = 3
	promotionBulletChars = 90
)

// IsPromotedPath reports whether a repo-relative path changed on a KB session
// branch is a promotion: anything not under knowledge/candidates/. A path
// outside knowledge/ counts too — the finish lands it on main all the same.
func IsPromotedPath(repoRel string) bool {
	r := strings.TrimPrefix(filepath.ToSlash(repoRel), "./")
	return !strings.HasPrefix(r, KnowledgeDir+"/"+CandidatesDir+"/")
}

// KBRelPath renders a repo-relative path kb-relative (knowledge/x.md → x.md),
// the form the summary's `### <path>` blocks and the ledger use. A path
// outside knowledge/ is returned unchanged.
func KBRelPath(p string) string {
	p = strings.Trim(strings.TrimSpace(filepath.ToSlash(p)), "`")
	p = strings.TrimPrefix(p, "./")
	return strings.TrimPrefix(p, KnowledgeDir+"/")
}

// KBBranchChanges lists the repo-relative paths that differ between the
// agent repo's main branch and branch (`git diff --name-only main...branch`),
// renames listed as a deletion plus an addition so a renamed confirmed file
// drops its old ledger line. kbBranchEntries (gate_promotion_related.go)
// diffs the same range with -M, where a rename must read as one.
func KBBranchChanges(agentDir, branch string) ([]string, error) {
	out, err := git(agentDir, "diff", "--name-only", "--no-renames", mainBranch(agentDir)+"..."+branch, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			paths = append(paths, ln)
		}
	}
	return paths, nil
}

// KBPromotedPaths resolves a KB session's branch and returns it with the
// kb-relative paths it would promote (nil when it touches candidates only).
func KBPromotedPaths(agentDir, session string) (branch string, promoted []string, err error) {
	branch = kbBranchFor(agentDir, session)
	changed, err := KBBranchChanges(agentDir, branch)
	if err != nil {
		return branch, nil, err
	}
	return branch, promotedOf(changed), nil
}

// PromotionAsk is the input of the promotion ask text.
type PromotionAsk struct {
	Agent    string   // the promoting peer
	Branch   string   // the KB session branch
	Summary  Summary  // the peer's confirmation summary
	Promoted []string // kb-relative promoted paths, in diff order
	// Related is appended after the per-file blocks as written: the lines
	// PromotionRelated renders (related canonical entries, coherence,
	// ownership, inbound links). nil ⇒ nothing.
	Related []string
}

// Change returns the summary's `### <path>` block for a kb-relative path.
func (s Summary) Change(kbRel string) (SummaryChange, bool) {
	want := KBRelPath(kbRel)
	for _, c := range s.Changes {
		if KBRelPath(c.Path) == want {
			return c, true
		}
	}
	return SummaryChange{}, false
}

// PromotionAskBody renders the per-file lines of the ask: `<path>  +n -m`
// with up to three added and three removed bullets, or
// `<path>  !! not described in ## Changes` when the summary has no block.
func PromotionAskBody(s Summary, promoted []string) []string {
	var lines []string
	bullet := func(sign, text string) string {
		if r := []rune(text); len(r) > promotionBulletChars {
			text = string(r[:promotionBulletChars-1]) + "…"
		}
		return "  " + sign + " " + text
	}
	for _, p := range promoted {
		c, ok := s.Change(p)
		if !ok {
			lines = append(lines, p+"  !! not described in ## Changes")
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  +%d -%d", p, len(c.Added), len(c.Removed)))
		for i, a := range c.Added {
			if i == promotionBulletMax {
				break
			}
			lines = append(lines, bullet("+", a))
		}
		for i, r := range c.Removed {
			if i == promotionBulletMax {
				break
			}
			lines = append(lines, bullet("-", r))
		}
		if more := len(c.Added) + len(c.Removed) - min(len(c.Added), promotionBulletMax) - min(len(c.Removed), promotionBulletMax); more > 0 {
			lines = append(lines, fmt.Sprintf("  … %d more in the summary", more))
		}
	}
	return lines
}

// RenderPromotionAsk renders the promotion ask (the signature): the Intent
// line first, a line naming the peer and branch, the per-file blocks, the
// Related section when given, and the deny-if-not-read line last. The
// rendered length is logged to the agent's gate log (RenderAsk).
func RenderPromotionAsk(agentDir string, a PromotionAsk) string {
	body := []string{fmt.Sprintf("kb finish promotes %d file(s) into `%s`'s knowledge from %s:", len(a.Promoted), a.Agent, a.Branch)}
	body = append(body, PromotionAskBody(a.Summary, a.Promoted)...)
	if len(a.Related) > 0 {
		body = append(body, "")
		body = append(body, a.Related...)
	}
	return RenderAsk(agentDir, AskText{Check: GateCheckPromotion, Intent: a.Summary.Intent, Body: body, Footer: PromotionAskFooter})
}

// kbFinishCall is a parsed `meshctl kb finish` invocation.
type kbFinishCall struct {
	abort                       bool
	agent, session, summaryFile string
}

// parseKBFinish finds the first `meshctl kb finish` simple command of a shell
// line (shellparse.go) and reads its flags; ok=false when there is none.
func parseKBFinish(command string) (c kbFinishCall, ok bool) {
	call, ok := parseMeshctlCall(command, "meshctl kb finish")
	if !ok {
		return c, false
	}
	return kbFinishCall{
		abort:       call.boolFlag("abort"),
		agent:       call.flag("agent"),
		session:     call.flag("session"),
		summaryFile: call.flag("summary-file"),
	}, true
}

// gatePromotion is the promotion rule for a Bash `meshctl kb finish`.
func gatePromotion(agentsDir string, p HookPayload) GateDecision {
	if agentsDir == "" {
		return GateDecision{}
	}
	command := shellCommandInput(p)
	if command == "" {
		return GateDecision{}
	}
	call, ok := parseKBFinish(command)
	if !ok || call.abort {
		return GateDecision{}
	}
	deny := func(reason string) GateDecision {
		return GateDecision{Deny: true, Check: GateCheckPromotion, Reason: "mesh: " + reason}
	}

	var a *Agent
	if call.agent != "" {
		a, _ = FindAgent(agentsDir, call.agent)
	} else if a = payloadAgent(agentsDir, p); a == nil {
		if name := AgentFromWorktreePath(agentsDir, p.Cwd); name != "" {
			a, _ = FindAgent(agentsDir, name)
		}
	}
	if a == nil {
		return deny("this `meshctl kb finish` may promote knowledge, but the gate cannot tell which peer is finishing — " +
			"run it from your workspace or pass `--agent <you>`, so the promotion can be asked with your summary.")
	}
	harness := p.SessionID
	if harness == "" {
		harness = ResolveAgentSessionID(a, os.Getpid(), time.Now())
	}
	session, err := ResolveKBSession(agentsDir, a, harness, call.session)
	if err != nil {
		return deny("cannot resolve the KB session branch for this `kb finish`: " + err.Error())
	}
	branch, promoted, err := KBPromotedPaths(a.Dir(), session)
	if err != nil {
		return deny("cannot diff `" + branch + "` against main, so the gate cannot tell a promotion from a candidates-only " +
			"finish: " + err.Error())
	}
	if len(promoted) == 0 {
		return GateDecision{Check: GateCheckPromotion} // candidates only: a proposal, no ask
	}

	expected := SummaryPath(a.Dir(), harness)
	if cause := noHumanCause(p); cause != "" {
		return gateNoHumanPromotion(agentsDir, a, p, session, branch, call.summaryFile, expected, promoted, cause)
	}
	files := "`" + strings.Join(promoted, "`, `") + "`"
	if call.summaryFile == "" {
		return deny("this `kb finish` promotes " + files + " into your current or log tier and needs your confirmation " +
			"summary: write it at " + expected + " (`## Intent` one line first, `## Grounding`, `## Precedent`, and `## Changes` " +
			"with one `### <path>` block per promoted file), read the confirmation round out in chat, then re-run " +
			"`meshctl kb finish --summary-file " + expected + "`.")
	}
	sumPath := resolveShellPath(call.summaryFile, p.Cwd)
	s, why := summaryProblem(sumPath)
	if why != "" {
		named := sumPath
		if filepath.Clean(sumPath) != filepath.Clean(expected) {
			named += " (expected " + expected + ")"
		}
		return deny("this `kb finish` promotes " + files + " and needs your confirmation summary first — " + why + " at " +
			named + ". Write `## Intent` (one line, first), `## Grounding`, `## Precedent` and `## Changes`, read the " +
			"confirmation round out in chat, then re-run the finish: the gate asks the human with your summary (ADR-0067)." +
			writeFirstHint(sumPath))
	}
	text := RenderPromotionAsk(a.Dir(), PromotionAsk{Agent: a.Name, Branch: branch, Summary: s, Promoted: promoted,
		Related: PromotionRelated(agentsDir, a, branch, s)})
	return GateDecision{Ask: true, Check: GateCheckPromotion, Reason: text}
}

// deniedKBMainWrite returns a reason when a file-tool or shell write targets a
// peer's knowledge/ outside candidates/ and pending-decisions.md on its main checkout (<pool>/<peer>/).
// KB and work worktrees live under the pool's worktrees dir, never there.
// agentsDir "" ⇒ inert.
func deniedKBMainWrite(agentsDir string, p HookPayload) string {
	if agentsDir == "" {
		return ""
	}
	targets := gateEditPaths(p)
	if shellTools[p.ToolName] && len(p.ToolInput) > 0 {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(p.ToolInput, &in) == nil && in.Command != "" {
			targets = append(targets, shellWriteTargets(in.Command, p.Cwd)...)
		}
	}
	for _, t := range targets {
		if !underDir(t, agentsDir) {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(agentsDir), filepath.Clean(t))
		if err != nil {
			continue
		}
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 3)
		if len(parts) < 3 || parts[1] != KnowledgeDir || !fileExists(ManifestPath(agentsDir, parts[0])) {
			continue
		}
		if kb := parts[2]; kb == CandidatesDir || strings.HasPrefix(kb, CandidatesDir+"/") || kb == PendingDecisionsFile {
			continue
		}
		return "mesh: `" + parts[0] + "/" + KnowledgeDir + "/" + parts[2] + "` is confirmed knowledge (current or log tier) on " +
			"the main checkout — it changes only through a KB session, so the human confirms what becomes true: " +
			"`meshctl kb begin`, edit in the worktree it prints, commit there, then `meshctl kb finish --summary-file " +
			"<summary>` (ADR-0067). A draft may go straight to `" + KnowledgeDir + "/" + CandidatesDir + "/`."
	}
	return ""
}
