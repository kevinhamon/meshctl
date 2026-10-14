package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeKB writes <agent>/knowledge/<rel> and returns the absolute path.
func writeKB(t *testing.T, a *Agent, rel, body string) string {
	t.Helper()
	p := filepath.Join(a.Dir(), KnowledgeDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseADR_FrontmatterAndBulletForms(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Bullet form with bold + trailing notes, successor spelled ADR-NNNN.
	bullet := write("0045-user-audit.md", "# 0045. User audit event durability\n\n- **Status:** **superseded by ADR-0050** (2026-08-10) — premise failed\n- **Date:** 2026-07-28\n")
	d, err := ParseADR(bullet)
	if err != nil {
		t.Fatal(err)
	}
	if d.Num != "0045" || d.Title != "User audit event durability" || d.Status != ADRSuperseded || d.Date != "2026-07-28" {
		t.Fatalf("bullet parse: %+v", d)
	}
	if len(d.SupersededBy) != 1 || d.SupersededBy[0] != "0050" || d.Frontmatter || d.Active() {
		t.Fatalf("bullet superseded-by: %+v", d)
	}
	// Bullet with implementation note stays accepted.
	impl := write("0031-pool-init.md", "# 0031. Pool init\n\n- **Status:** accepted — **implemented** (`meshctl pool init`)\n")
	if d, _ := ParseADR(impl); d.Status != ADRAccepted || !d.Active() {
		t.Fatalf("accepted-with-note: %+v", d)
	}
	// Frontmatter wins over the bullet; updates/supersedes lists parse; date as YAML date.
	fm := write("0050-audit-transactional.md", "---\nstatus: accepted\ndate: 2026-08-10\nsupersedes: [45]\nupdates:\n  - architecture/backend-apis.md\n  - knowledge/state.md\n---\n# 0050. Audit durability is transactional\n\n- **Status:** proposed\n")
	d, _ = ParseADR(fm)
	if !d.Frontmatter || d.Status != ADRAccepted || d.Date != "2026-08-10" {
		t.Fatalf("frontmatter parse: %+v", d)
	}
	if len(d.Supersedes) != 1 || d.Supersedes[0] != "0045" {
		t.Fatalf("supersedes: %+v", d.Supersedes)
	}
	if strings.Join(d.Updates, ",") != "architecture/backend-apis.md,state.md" {
		t.Fatalf("updates (knowledge/ prefix stripped): %+v", d.Updates)
	}
}

func TestADRFrontmatterMigration_AdditiveAndIdempotent(t *testing.T) {
	kb := t.TempDir()
	dec := filepath.Join(kb, "decisions")
	_ = os.MkdirAll(dec, 0o755)
	p := filepath.Join(dec, "0045-user-audit.md")
	orig := "# 0045. User audit\n\n- **Status:** superseded by ADR-0050\n- **Date:** 2026-07-28\n\n## Context\nbody\n"
	_ = os.WriteFile(p, []byte(orig), 0o644)
	_ = os.WriteFile(filepath.Join(dec, "README.md"), []byte("# index\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dec, "template.md"), []byte("- **Status:** proposed | accepted\n"), 0o644)

	need := adrNeedsFrontmatter(kb)
	if len(need) != 1 || need[0].File != "0045-user-audit.md" {
		t.Fatalf("needs frontmatter: %+v", need)
	}
	if err := migrateADRFrontmatter(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	if !strings.HasPrefix(got, "---\nstatus: superseded\ndate: 2026-07-28\nsuperseded_by: [0050]\n---\n") {
		t.Fatalf("frontmatter block:\n%s", got)
	}
	if !strings.HasSuffix(got, orig) {
		t.Fatalf("original text must be untouched:\n%s", got)
	}
	if err := migrateADRFrontmatter(p); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(p); string(b2) != got {
		t.Fatal("second migration must be a no-op")
	}
	if n := adrNeedsFrontmatter(kb); len(n) != 0 {
		t.Fatalf("after migration nothing should need it: %+v", n)
	}
	if !inactiveKBFiles(kb)["decisions/0045-user-audit.md"] {
		t.Fatal("superseded decision must be inactive")
	}
}

func TestRecall_TiersAndSupersededDropped(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "grid-expert", true, nil)
	writeKB(t, a, "decisions/0001-settlement-window.md", "---\nstatus: superseded\nsuperseded_by: [2]\n---\n# 0001. Settlement window is weekly\n")
	writeKB(t, a, "decisions/0002-settlement-window-daily.md", "---\nstatus: accepted\nsupersedes: [1]\n---\n# 0002. Settlement window is daily\n")
	writeKB(t, a, "architecture/settlement.md", "# Settlement window\n\nDaily since 0002. The warehouse exposes it as a view.\n")
	writeKB(t, a, "candidates/settlement-window-idea.md", "# Settlement window idea\n")
	writeKB(t, a, "handoff.md", "## Current State\nsettlement window WIP\n")

	hits, err := Recall(a, "settlement window", 20)
	if err != nil {
		t.Fatal(err)
	}
	var sawSuperseded, sawHandoff bool
	tierOf := map[string]string{}
	first := ""
	for i, h := range hits {
		if h.Kind != "kb" {
			continue
		}
		if i == 0 || first == "" {
			first = h.Tier
		}
		for _, w := range h.Where {
			if strings.Contains(w, "0001-") {
				sawSuperseded = true
			}
			if strings.HasSuffix(w, HandoffFile) {
				sawHandoff = true
			}
			tierOf[w] = h.Tier
		}
	}
	if sawSuperseded {
		t.Fatalf("superseded decision must not be recalled: %+v", hits)
	}
	if sawHandoff {
		t.Fatalf("handoff headings must not be topics: %+v", hits)
	}
	if tierOf["knowledge/architecture/settlement.md"] != TierCurrent ||
		tierOf["knowledge/decisions/0002-settlement-window-daily.md"] != TierLog ||
		tierOf["knowledge/candidates/settlement-window-idea.md"] != TierInbox {
		t.Fatalf("tiers: %+v", tierOf)
	}
	if first != TierCurrent {
		t.Fatalf("current tier must rank first; hits=%+v", hits)
	}

	// Bodies: only current-tier hits are inlined, within the budget.
	withBodies, err := RecallWithBodies(a, "settlement window", 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range withBodies {
		switch {
		case h.Kind == "kb" && h.Tier == TierCurrent:
			if !strings.Contains(h.Body, "Daily since 0002") {
				t.Fatalf("current-tier body missing: %+v", h)
			}
		case h.Body != "":
			t.Fatalf("only current-tier hits get bodies: %+v", h)
		}
	}
	if md := RecallMarkdown(withBodies); !strings.Contains(md, "[kb/current]") || !strings.Contains(md, "  Daily since 0002") {
		t.Fatalf("markdown:\n%s", md)
	}
	// Zero budget ⇒ pointers only.
	plain, _ := RecallWithBodies(a, "settlement window", 20, 0)
	for _, h := range plain {
		if h.Body != "" {
			t.Fatal("budget 0 must not inline bodies")
		}
	}
}

func TestStateLintAndSet(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "warehouse", true, nil)
	if p := StateLint(StateTemplate); len(p) < 1 || !strings.Contains(strings.Join(p, " "), "placeholder") {
		t.Fatalf("template must fail lint: %v", p)
	}
	good := "## Now\nAll loads are MERGE on object key.\n\n## Active\n- none\n\n## Recently decided\n- ADR-0002 daily settlement window\n\n## Open questions\n- none\n"
	if p := StateLint(good); len(p) != 0 {
		t.Fatalf("good body: %v", p)
	}
	long := good + strings.Repeat("- filler\n", 40)
	if p := StateLint(long); len(p) != 1 || !strings.Contains(p[0], "budget") {
		t.Fatalf("over budget: %v", p)
	}
	if StateBlock(a) != "" {
		t.Fatal("no digest ⇒ empty block")
	}
	now := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	if err := StateSet(a, good, now); err != nil {
		t.Fatal(err)
	}
	s, _ := StateShow(a)
	if !strings.HasPrefix(s, "<!-- meshctl state — warehouse updated 2026-10-14T01:00:00Z") || !strings.Contains(s, "## Now") {
		t.Fatalf("state file:\n%s", s)
	}
	// Re-setting from `show` output does not stack headers.
	if err := StateSet(a, s, now); err != nil {
		t.Fatal(err)
	}
	s2, _ := StateShow(a)
	if strings.Count(s2, "<!-- meshctl state") != 1 {
		t.Fatalf("header stacked:\n%s", s2)
	}
	if blk := StateBlock(a); !strings.HasPrefix(blk, "# Current state — digest") || !strings.Contains(blk, "MERGE on object key") {
		t.Fatalf("block:\n%s", blk)
	}
	// Session start injects the digest ahead of the handoff.
	_ = HandoffSet(a, "## Current State\nx\n## Next Step\ny\n## Open Threads\n- none\n## Pointers\n- none\n", now)
	ctx := HookSessionStart(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1"}, 1, now)
	si, hi := strings.Index(ctx, "# Current state — digest"), strings.Index(ctx, "# Session handoff")
	if si < 0 || hi < 0 || si > hi {
		t.Fatalf("session start order: state=%d handoff=%d\n%s", si, hi, ctx)
	}
	// Brief carries it too.
	brief, err := Brief(dir, a, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "## Current state — digest") {
		t.Fatalf("brief lacks the digest:\n%s", brief)
	}
}

func TestKBConsolidate_DebtAndDoctor(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "warehouse", true, nil)
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)

	// Accepted decision dated today that updates a doc last touched a month ago.
	writeKB(t, a, "decisions/0003-merge-on-key.md", "---\nstatus: accepted\ndate: 2026-10-14\nupdates: [architecture/loads.md]\n---\n# 0003. Merge on key\n")
	loads := writeKB(t, a, "architecture/loads.md", "# Loads\nold text\n")
	_ = os.Chtimes(loads, old, old)
	// A bullet-only decision (needs frontmatter) and a broken superseded link.
	writeKB(t, a, "decisions/0001-old.md", "# 0001. Old\n- **Status:** superseded by ADR-0099\n")
	// An aged candidate and a fresh one.
	aged := writeKB(t, a, "candidates/stale-idea.md", "# Stale idea\n")
	_ = os.Chtimes(aged, old, old)
	writeKB(t, a, "candidates/fresh-idea.md", "# Fresh idea\n")
	writeKB(t, a, "candidates/README.md", "# Candidates\n")
	// A handoff over budget, no state digest.
	_ = HandoffSet(a, "## Current State\nx\n## Next Step\ny\n## Open Threads\n- none\n## Pointers\n"+strings.Repeat("- p\n", 45), now)

	r := KBConsolidate(a, now)
	if r.Clean() {
		t.Fatal("report must not be clean")
	}
	if len(r.NoFrontmatter) != 1 || r.NoFrontmatter[0] != "decisions/0001-old.md" {
		t.Fatalf("no-frontmatter: %+v", r.NoFrontmatter)
	}
	if len(r.Unlanded) != 1 || !strings.Contains(r.Unlanded[0], "ADR-0003") || !strings.Contains(r.Unlanded[0], "architecture/loads.md") {
		t.Fatalf("unlanded: %+v", r.Unlanded)
	}
	if !r.StateMissing || len(r.NotInDigest) != 1 || !strings.Contains(r.NotInDigest[0], "ADR-0003") {
		t.Fatalf("digest debt: missing=%v notInDigest=%+v", r.StateMissing, r.NotInDigest)
	}
	if len(r.AgedCandidates) != 1 || !strings.Contains(r.AgedCandidates[0], "stale-idea.md (30 days)") {
		t.Fatalf("aged: %+v", r.AgedCandidates)
	}
	if len(r.HandoffProblems) == 0 || len(r.SupersededBroken) != 1 {
		t.Fatalf("handoff=%v superseded=%v", r.HandoffProblems, r.SupersededBroken)
	}
	md := r.Markdown()
	for _, want := range []string{"## Decisions without machine-readable status (1)", "## Accepted decisions not landed", "## No state digest", "## Candidates older than 14 days", "## Handoff problems", "meshctl kb begin"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}

	// Doctor: the unlanded decision is an error; digest, candidates, handoff are warnings.
	findings, ok := Doctor(dir)
	if ok {
		t.Fatal("doctor must fail on an unlanded accepted decision")
	}
	var text []string
	for _, f := range findings {
		if f.Agent == "warehouse" {
			text = append(text, f.Level+": "+f.Message)
		}
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{
		"error: decisions/0003-merge-on-key.md (accepted) updates architecture/loads.md",
		"warn: no knowledge/state.md digest",
		"warn: 1 candidate(s) older than 14 days",
		"warn: knowledge/handoff.md: ",
		"error: 0001-old.md is marked superseded by 99, but no such decision file exists",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("doctor lacks %q:\n%s", want, all)
		}
	}

	// Hygiene offers the frontmatter migration as fixable; applying it clears the debt item.
	items := PoolHygiene(dir, now)
	var mig *HygieneItem
	for i := range items {
		if strings.Contains(items[i].Message, "without status frontmatter") {
			mig = &items[i]
		}
	}
	if mig == nil || !mig.Fixable() {
		t.Fatalf("no fixable migration item: %s", hygieneMessages(items))
	}
	if fixed, errs := ApplyHygiene([]HygieneItem{*mig}); len(errs) != 0 || len(fixed) != 1 {
		t.Fatalf("apply: fixed=%v errs=%v", fixed, errs)
	}
	if r2 := KBConsolidate(a, now); len(r2.NoFrontmatter) != 0 {
		t.Fatalf("still needs frontmatter: %+v", r2.NoFrontmatter)
	}

	// Landing the decision (touch the doc) and writing the digest clears those items.
	_ = os.Chtimes(loads, now, now)
	if err := StateSet(a, "## Now\nMERGE on key.\n\n## Active\n- none\n\n## Recently decided\n- ADR-0003 merge on key\n\n## Open questions\n- none\n", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r3 := KBConsolidate(a, now)
	if len(r3.Unlanded) != 0 || r3.StateMissing || len(r3.NotInDigest) != 0 {
		t.Fatalf("after landing: %+v", r3)
	}
	// The only error left is the deliberately broken superseded link.
	findings, _ = Doctor(dir)
	for _, f := range findings {
		if f.Agent == "warehouse" && f.Level == "error" && !strings.Contains(f.Message, "0001-old.md") {
			t.Fatalf("unexpected error after landing: %s", f.Message)
		}
	}
}

func TestDoctor_CleanKBHasNoS1Noise(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "warehouse", true, nil)
	// A fresh agent (no decisions/, no architecture/) gets no state-digest nag.
	findings, _ := Doctor(dir)
	for _, f := range findings {
		if f.Agent == a.Name && (strings.Contains(f.Message, StateFile) || strings.Contains(f.Message, "candidate(s) older")) {
			t.Fatalf("unexpected S1 finding on a fresh agent: %s", f.Message)
		}
	}
	if r := KBConsolidate(a, time.Now()); !r.Clean() && len(r.NotInDigest) != 0 {
		t.Fatalf("fresh agent consolidate: %+v", r)
	}
}
