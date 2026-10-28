package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gate_promotion_related_test.go — related canonical entries, coherence,
// ownership routing and inbound links at the promotion ask (ADR-0065,
// ADR-0069; spec decisions 31 and 32). Fixture: a four-peer pool — architect
// (the promoter, a git repo with KB session sess-1) and catalog canonical,
// lending canonical owning `circulation`, critic perspective.

type relatedPool struct {
	root                           string
	arch, catalog, lending, critic *Agent
	wt                             string
}

func newRelatedPool(t *testing.T, mainKB map[string]string) relatedPool {
	t.Helper()
	root, arch := poolGateFixture(t)
	for rel, body := range mainKB {
		writeKB(t, arch, rel, body)
	}
	gitInit(t, arch.Dir()) // commits mainKB on main
	wt, err := KBBegin(root, arch, "sess-1", time.Now())
	if err != nil {
		t.Fatalf("kb begin: %v", err)
	}
	catalog := newTestAgent(t, root, "catalog", false, nil)
	lending := owningAgent(t, root, "lending", "circulation")
	newTestAgent(t, root, "critic", false, nil)
	critic := setManifestKnowledge(t, root, "critic", KnowledgePerspective)
	writeKB(t, lending, "architecture/circulation-rules.md", "# Circulation window rules\n\nLending renews daily.\n")
	writeKB(t, critic, "architecture/circulation-critique.md", "# Circulation window critique\n\nHourly is better.\n")
	return relatedPool{root: root, arch: arch, catalog: catalog, lending: lending, critic: critic, wt: wt}
}

// commitKBBody writes kb-relative files with the given bodies into a worktree
// and commits them.
func commitKBBody(t *testing.T, wt string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(wt, KnowledgeDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, wt, "kb edits")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

const circulationSummary = `## Intent
Record the architect's view of the circulation window.

## Grounding
- circulation.yaml:3 sets a daily window.

## Precedent
- recalled the canonical scope.

## Changes
### architecture/circulation-window.md
+ the circulation window is daily
`

func finishAsk(t *testing.T, p relatedPool, summary string) GateDecision {
	t.Helper()
	sum := writeSummary(t, p.arch, "sess-1", summary)
	d := GatePreToolUse(p.root, bash(p.arch, "sess-1", "meshctl kb finish --summary-file "+sum))
	if !d.Ask || d.Deny || d.Check != GateCheckPromotion {
		t.Fatalf("want ask/promotion, got %+v", d)
	}
	return d
}

func TestPromotion_RelatedCanonicalEntriesWithoutCoherence(t *testing.T) {
	p := newRelatedPool(t, nil)
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	d := finishAsk(t, p, circulationSummary)

	for _, want := range []string{
		"Related canonical entries:",
		"mesh://lending/architecture/circulation-rules.md — Circulation window rules",
		"coherence not stated",
	} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask lacks %q:\n%s", want, d.Reason)
		}
	}
	if strings.Contains(d.Reason, "critic") {
		t.Errorf("a perspective peer is never a related entry:\n%s", d.Reason)
	}
	if !strings.HasSuffix(d.Reason, PromotionAskFooter) {
		t.Errorf("the deny-if-not-read line stays last:\n%s", d.Reason)
	}
}

func TestPromotion_CoherenceClassificationsReproducedAndConflictStillAsks(t *testing.T) {
	p := newRelatedPool(t, nil)
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	d := finishAsk(t, p, circulationSummary+`
## Coherence
- mesh://lending/architecture/circulation-rules.md: conflict — lending renews weekly in one paragraph
`)
	if !strings.Contains(d.Reason, "mesh://lending/architecture/circulation-rules.md — Circulation window rules · conflict") {
		t.Errorf("ask must reproduce the classification:\n%s", d.Reason)
	}
	if strings.Contains(d.Reason, "coherence not stated") {
		t.Errorf("a stated coherence section is not 'not stated':\n%s", d.Reason)
	}
}

func TestPromotion_CoherenceSectionPresentButEntryUnclassified(t *testing.T) {
	p := newRelatedPool(t, nil)
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	d := finishAsk(t, p, circulationSummary+"\n## Coherence\n- nothing related that I saw\n")
	if !strings.Contains(d.Reason, "Circulation window rules · not classified") {
		t.Errorf("an entry the coherence section does not name is marked:\n%s", d.Reason)
	}
}

func TestPromotion_OwnershipLineNamesOwner(t *testing.T) {
	p := newRelatedPool(t, nil)
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	d := finishAsk(t, p, circulationSummary)
	want := "architecture/circulation-window.md falls under `lending`'s owns `circulation`"
	if !strings.Contains(d.Reason, want) || !strings.Contains(d.Reason, "candidate") {
		t.Errorf("ask lacks the ownership line %q:\n%s", want, d.Reason)
	}
}

func TestPromotion_PerspectivePeerHasNoRelatedOrOwnership(t *testing.T) {
	p := newRelatedPool(t, nil)
	setManifestKnowledge(t, p.root, "architect", KnowledgePerspective)
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	d := finishAsk(t, p, circulationSummary)
	for _, not := range []string{"Related canonical entries", "coherence not stated", "falls under"} {
		if strings.Contains(d.Reason, not) {
			t.Errorf("a perspective peer's ask has no %q:\n%s", not, d.Reason)
		}
	}
}

func TestPromotion_RemovalAndRenameListInboundLinks(t *testing.T) {
	p := newRelatedPool(t, map[string]string{
		"decisions/0001-gone.md":    "# 0001. Gone decision\n",
		"playbooks/old-name.md":     "# Old name playbook\n",
		"playbooks/unreferenced.md": "# Unreferenced\n",
	})
	writeKB(t, p.catalog, "architecture/uses.md", "# Uses\n\nSee mesh://architect/decisions/0001-gone.md and mesh://architect/playbooks/old-name.md#step-one.\n")
	writeKB(t, p.critic, "architecture/notes.md", "# Notes\n\nmesh://architect/decisions/0001-gone.md\n")
	gitRun(t, p.wt, "rm", "-q", "knowledge/decisions/0001-gone.md", "knowledge/playbooks/unreferenced.md")
	gitRun(t, p.wt, "mv", "knowledge/playbooks/old-name.md", "knowledge/playbooks/new-name.md")
	gitCommitAll(t, p.wt, "remove and rename")

	d := finishAsk(t, p, circulationSummary)
	for _, want := range []string{
		"removes decisions/0001-gone.md — inbound links:",
		"catalog/knowledge/architecture/uses.md",
		"critic/knowledge/architecture/notes.md",
		"renames playbooks/old-name.md → playbooks/new-name.md — inbound links:",
		"mesh://architect/playbooks/old-name.md#step-one",
	} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("ask lacks %q:\n%s", want, d.Reason)
		}
	}
	if strings.Contains(d.Reason, "unreferenced.md — inbound") {
		t.Errorf("a removal nothing links to lists no inbound links:\n%s", d.Reason)
	}
}

// A hub session doing peer X's work in-session runs the finish with X's
// identity (the work subagent's hook carries --agent X): X's kind and X's
// roster view apply, with no hub exemption.
func TestPromotion_HubInSessionUsesPeersKindAndRoster(t *testing.T) {
	root, hub := poolGateFixture(t) // architect: the hub, canonical
	writeKB(t, hub, "architecture/circulation-hub.md", "# Circulation window hub view\n")
	lending := owningAgent(t, root, "lending", "circulation")
	writeKB(t, lending, "architecture/circulation-rules.md", "# Circulation window rules\n")

	run := func(peer *Agent) GateDecision {
		gitInit(t, peer.Dir())
		wt, err := KBBegin(root, peer, "sess-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		commitKBBody(t, wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n"})
		sum := writeSummary(t, peer, "sess-1", circulationSummary)
		pl := bash(hub, "sess-1", "meshctl kb finish --summary-file "+sum)
		pl.PeerAgent = peer.Name
		d := GatePreToolUse(root, pl)
		if !d.Ask {
			t.Fatalf("want ask, got %+v", d)
		}
		return d
	}

	catalog := newTestAgent(t, root, "catalog", false, nil)
	d := run(catalog)
	if !strings.Contains(d.Reason, "mesh://architect/architecture/circulation-hub.md") ||
		!strings.Contains(d.Reason, "mesh://lending/architecture/circulation-rules.md") {
		t.Errorf("catalog's roster view includes the hub's and lending's entries:\n%s", d.Reason)
	}
	if !strings.Contains(d.Reason, "into `catalog`'s knowledge") {
		t.Errorf("the ask is catalog's:\n%s", d.Reason)
	}

	newTestAgent(t, root, "critic", false, nil)
	critic := setManifestKnowledge(t, root, "critic", KnowledgePerspective)
	if d := run(critic); strings.Contains(d.Reason, "Related canonical entries") || strings.Contains(d.Reason, "falls under") {
		t.Errorf("the hub finishing for a perspective peer gets the peer's kind:\n%s", d.Reason)
	}
}

func TestPromotion_ThreeRelatedEntriesUnder1200AndLogged(t *testing.T) {
	p := newRelatedPool(t, nil)
	writeKB(t, p.catalog, "architecture/circulation-catalog.md", "# Circulation window on the catalog side\n\nCatalog.\n")
	writeKB(t, p.lending, "decisions/0007-circulation-window-daily.md", "---\nstatus: accepted\n---\n# 0007. Circulation window is daily for every lending patron\n")
	commitKBBody(t, p.wt, map[string]string{"architecture/circulation-window.md": "# Circulation window\n\nDaily.\n"})
	d := finishAsk(t, p, circulationSummary+`
## Coherence
- mesh://lending/architecture/circulation-rules.md: consistent
- mesh://lending/decisions/0007-circulation-window-daily.md: consistent
- mesh://catalog/architecture/circulation-catalog.md: judgment — catalog frames it per shelf
`)
	if n := strings.Count(d.Reason, "\n- mesh://"); n != 3 {
		t.Fatalf("want three related entries, got %d:\n%s", n, d.Reason)
	}
	if n := len(d.Reason); n > 1200 {
		t.Fatalf("ask with three related entries is %d chars, want about 1200 or less:\n%s", n, d.Reason)
	}
	b, _ := os.ReadFile(GateLogPath(p.arch.Dir()))
	if !strings.Contains(string(b), "ask-rendered check="+GateCheckPromotion+" chars=") {
		t.Errorf("gate log lacks the rendered length:\n%s", b)
	}
}
