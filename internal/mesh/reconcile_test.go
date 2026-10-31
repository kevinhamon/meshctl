package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kb reconcile: walk order and worksheet.

func walkNames(w ReconcileWalk) []string {
	var out []string
	for _, p := range w.Peers {
		out = append(out, p.Name)
	}
	return out
}

func writeReconcileReport(t *testing.T, a *Agent, date, body string) {
	t.Helper()
	dir := ReconcileDir(a.Dir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, date+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reconcilePool: alpha mutates two surfaces, bravo one, charlie and delta
// none; echo is a perspective peer.
func reconcilePool(t *testing.T) (root string, peers map[string]*Agent) {
	t.Helper()
	root = migratedRoot(t)
	peers = map[string]*Agent{
		"alpha":   newTestAgent(t, root, "alpha", false, []string{"tickets", "github"}),
		"bravo":   newTestAgent(t, root, "bravo", false, []string{"tickets"}),
		"charlie": newTestAgent(t, root, "charlie", false, nil),
		"delta":   newTestAgent(t, root, "delta", false, nil),
	}
	newTestAgent(t, root, "echo", false, nil)
	peers["echo"] = setManifestKnowledge(t, root, "echo", KnowledgePerspective)
	return root, peers
}

func TestKBReconcileWalk_ComputedOrder(t *testing.T) {
	root, p := reconcilePool(t)
	// delta: more inbound links than charlie; charlie: more unconfirmed.
	writeKB(t, p["alpha"], "reference/a.md", "# A\n\nSee mesh://delta/reference/d.md and mesh://delta/reference/d.md.\n")
	writeKB(t, p["bravo"], "reference/b.md", "# B\n\nSee mesh://delta/reference/d.md.\n")
	writeKB(t, p["echo"], "reference/e.md", "# E\n\nmesh://charlie/reference/c1.md mesh://charlie/reference/c2.md mesh://charlie/reference/c3.md\n")
	writeKB(t, p["delta"], "reference/d.md", "# D\n")
	writeKB(t, p["charlie"], "reference/c1.md", "# C1\n")
	writeKB(t, p["charlie"], "reference/c2.md", "# C2\n")
	writeKB(t, p["charlie"], "reference/c3.md", "# C3\n")

	w, err := ReconcileWalkOrder(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(walkNames(w), ","), "alpha,bravo,delta,charlie"; got != want {
		t.Fatalf("walk order = %s, want %s (mutates, then inbound, perspective skipped)", got, want)
	}
	if strings.Join(w.Skipped, ",") != "echo" {
		t.Fatalf("perspective peers must be reported skipped: %v", w.Skipped)
	}
	out := w.Markdown()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("one peer per line, got:\n%s", out)
	}
	for _, want := range []string{"alpha", "mutates 2", "inbound 0"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("line 1 missing %q: %s", want, lines[0])
		}
	}
	if !strings.Contains(lines[2], "delta") || !strings.Contains(lines[2], "inbound 2") {
		t.Fatalf("delta line should carry its inbound count (perspective links not counted, files counted once): %s", lines[2])
	}
	if !strings.Contains(lines[3], "charlie") || !strings.Contains(lines[3], "unconfirmed 3") || !strings.Contains(lines[3], "inbound 0") {
		t.Fatalf("charlie line should carry unconfirmed 3, inbound 0 (echo is never read): %s", lines[3])
	}
	if strings.Contains(out, "echo") {
		t.Fatalf("perspective peer must not be in the walk:\n%s", out)
	}
	if _, err := os.Stat(ReconcileDir(p["alpha"].Dir())); !os.IsNotExist(err) {
		t.Fatalf("the walk order writes nothing")
	}
}

func TestKBReconcileWalk_ConflictsRankAndPairsInterleave(t *testing.T) {
	root, p := reconcilePool(t)
	// charlie and delta tie on mutates; charlie's latest report has a conflict
	// with alpha, so charlie ranks above delta and alpha comes right after it.
	writeReconcileReport(t, p["charlie"], "2026-10-01",
		"- reference/x.md · conflict → mesh://delta/reference/y.md · \"a\" vs \"b\"\n")
	writeReconcileReport(t, p["charlie"], "2026-10-10",
		"- reference/x.md · conflict → mesh://alpha/reference/z.md · \"daily\" vs \"weekly\"\n"+
			"- reference/w.md · consistent · \"ok\"\n")
	writeReconcileReport(t, p["alpha"], "2026-10-11",
		"- reference/z.md · conflict → mesh://charlie/reference/x.md · \"weekly\" vs \"daily\"\n")

	w, err := ReconcileWalkOrder(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(walkNames(w), ","), "alpha,charlie,bravo,delta"; got != want {
		t.Fatalf("walk order = %s, want %s (alpha's conflict partner charlie walked right after it)", got, want)
	}
	out := w.Markdown()
	if !strings.Contains(out, "conflicts 1") || !strings.Contains(out, "with alpha") {
		t.Fatalf("conflict count and pair must be shown (latest report only):\n%s", out)
	}
}

func TestKBReconcileWalk_ConfigOrderOverrides(t *testing.T) {
	root, _ := reconcilePool(t)
	appendFile(t, PoolConfigPath(root), "reconcile:\n  order: [charlie, echo, nobody, bravo]\n")
	w, err := ReconcileWalkOrder(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(walkNames(w), ","), "charlie,bravo,alpha,delta"; got != want {
		t.Fatalf("walk order = %s, want %s (configured first, rest in computed order)", got, want)
	}
	out := w.Markdown()
	if !strings.Contains(strings.Split(out, "\n")[0], "reconcile.order") {
		t.Fatalf("a pinned peer should say it comes from reconcile.order:\n%s", out)
	}
	if strings.Contains(out, "echo") || strings.Contains(out, "nobody") {
		t.Fatalf("perspective and unknown names in reconcile.order are skipped:\n%s", out)
	}
}

func TestKBReconcileWorksheet_RefusesPerspectivePeer(t *testing.T) {
	root, p := reconcilePool(t)
	_, err := KBReconcileWorksheet(root, "echo", time.Now())
	if err == nil || !strings.Contains(err.Error(), "perspective") {
		t.Fatalf("want a perspective refusal with reason, got %v", err)
	}
	if _, err := os.Stat(ReconcileDir(p["echo"].Dir())); !os.IsNotExist(err) {
		t.Fatalf("a refused worksheet writes nothing")
	}
}

func TestKBReconcileWorksheet_EntriesInWalkOrderWithEvidence(t *testing.T) {
	root, p := reconcilePool(t)
	a := p["alpha"]
	if err := StateSet(a, "## Now\n\nShipping the circulation window.\n\n## Active\n\n- circulation\n", time.Now()); err != nil {
		t.Fatal(err)
	}
	writeKB(t, a, "handoff.md", "# Handoff\n\nPick up circulation.\n")
	writeKB(t, a, "glossary.md", "# Glossary\n\n## Circulation window\n\n**Circulation window**: the renewal period.\n")
	writeKB(t, a, "decisions/0001-circulation-daily.md", "---\nstatus: accepted\n---\n# 0001. Circulation is daily\n")
	writeKB(t, a, "architecture/circulation-flow.md", "# Circulation flow\n\nRenews daily.\n")
	writeKB(t, a, "reference/renewal.md", "# Renewal\n\nDaily renewal.\n")
	writeKB(t, a, "playbooks/run-circulation.md", "# Run circulation\n")
	writeKB(t, a, "experience/2026-10-01-circulation-slip.md", "# Circulation slip\n")
	writeKB(t, a, "candidates/circulation-weekly.md", "# Circulation weekly?\n")
	writeKB(t, p["bravo"], "architecture/circulation-window.md", "# Circulation window\n\nBravo renews weekly.\n")
	writeKB(t, p["echo"], "architecture/circulation-window.md", "# Circulation window critique\n")
	// Doctrine and standing orders sharing the term.
	if err := os.WriteFile(filepath.Join(DoctrinePath(root), "local.md"),
		[]byte("# Local\n\nThe circulation window is set by the lending operator.\n\nUnrelated house rule.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendFile(t, filepath.Join(a.Dir(), "CLAUDE.md"), "\n\nAlways quote the circulation window from the operator feed.\n")

	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	path, err := KBReconcileWorksheet(root, "alpha", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(a.Dir(), ".memory", "reconcile", "2026-10-18.worksheet.md"); path != want {
		t.Fatalf("worksheet path = %s, want %s", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := string(b)
	order := []string{
		"## state.md#now", "## state.md#active", "## handoff.md", "## glossary.md#circulation-window",
		"## decisions/0001-circulation-daily.md", "## architecture/circulation-flow.md", "## reference/renewal.md",
		"## playbooks/run-circulation.md", "## experience/2026-10-01-circulation-slip.md",
	}
	last := -1
	for _, h := range order {
		i := strings.Index(ws, h+" ")
		if i < 0 {
			i = strings.Index(ws, h+"\n")
		}
		if i < 0 {
			t.Fatalf("worksheet missing entry %q:\n%s", h, ws)
		}
		if i < last {
			t.Fatalf("entry %q out of walk order:\n%s", h, ws)
		}
		last = i
	}
	if strings.Contains(ws, "candidates/") || strings.Contains(ws, "README.md") {
		t.Fatalf("candidates and directory indexes are not entries:\n%s", ws)
	}
	flow := worksheetSection(ws, "## architecture/circulation-flow.md")
	if !strings.Contains(flow, "Renews daily.") {
		t.Fatalf("entry body missing:\n%s", flow)
	}
	if !strings.Contains(flow, "mesh://bravo/architecture/circulation-window.md") || strings.Contains(flow, "mesh://echo/") {
		t.Fatalf("canonical hits (bravo, never echo) missing:\n%s", flow)
	}
	if !strings.Contains(flow, "The circulation window is set by the lending operator.") ||
		!strings.Contains(flow, "Always quote the circulation window") || strings.Contains(flow, "Unrelated house rule") {
		t.Fatalf("doctrine and standing-order passages sharing the term expected, unrelated ones not:\n%s", flow)
	}
}

func worksheetSection(ws, heading string) string {
	i := strings.Index(ws, heading)
	if i < 0 {
		return ""
	}
	rest := ws[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return heading + rest
}

func TestKBReconcileWorksheet_CitationsMarkedWithoutFetching(t *testing.T) {
	root, p := reconcilePool(t)
	a := p["alpha"]
	writeKB(t, p["bravo"], "reference/limits.md", "# Limits\n")
	if err := os.MkdirAll(filepath.Join(a.Dir(), "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Dir(), "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeKB(t, a, "reference/cites.md", "# Cites\n\n"+
		"Limits live in mesh://bravo/reference/limits.md and mesh://bravo/reference/gone.md.\n"+
		"Code: `src/main.go` and `src/missing.go:12`.\n"+
		"Docs: https://example.com/limits and https://bad_host!/x.\n")

	path, err := KBReconcileWorksheet(root, "alpha", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	sec := worksheetSection(string(b), "## reference/cites.md")
	for _, want := range []string{
		"- resolved · mesh://bravo/reference/limits.md",
		"- unresolved · mesh://bravo/reference/gone.md",
		"- resolved · src/main.go",
		"- unresolved · src/missing.go",
		"- resolved · https://example.com/limits",
		"- unresolved · https://bad_host!/x",
	} {
		if !strings.Contains(sec, want) {
			t.Fatalf("citation line %q missing:\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "unresolved · mesh://bravo/reference/limits.md") || strings.Contains(sec, "unresolved · https://example.com") {
		t.Fatalf("resolvable citations marked unresolved:\n%s", sec)
	}
}

func TestKBReconcileWorksheet_SkipsPairsConfirmedOnBothSides(t *testing.T) {
	root, p := reconcilePool(t)
	a, b := p["alpha"], p["bravo"]
	writeKB(t, a, "architecture/circulation-flow.md", "# Circulation flow\n\nRenews daily.\n")
	writeKB(t, b, "architecture/circulation-flow.md", "# Circulation flow\n\nBravo renews weekly.\n")
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	const entry = "## architecture/circulation-flow.md"

	ws := func() string {
		t.Helper()
		w, err := BuildReconcileWorksheet(root, "alpha", now)
		if err != nil {
			t.Fatal(err)
		}
		return w.Markdown()
	}
	if s := worksheetSection(ws(), entry); !strings.Contains(s, "mesh://bravo/architecture/circulation-flow.md") {
		t.Fatalf("first pass must list the pair:\n%s", s)
	}
	confirmKB(t, a, "architecture/circulation-flow.md")
	if s := worksheetSection(ws(), entry); s == "" {
		t.Fatal("one side confirmed: the pair is still open")
	}
	confirmKB(t, b, "architecture/circulation-flow.md")
	if got := ws(); strings.Contains(got, entry) {
		t.Fatalf("both sides confirmed and unchanged: the pair must be skipped:\n%s", got)
	}
	writeKB(t, b, "architecture/circulation-flow.md", "# Circulation flow\n\nBravo renews monthly now.\n")
	if s := worksheetSection(ws(), entry); !strings.Contains(s, "mesh://bravo/architecture/circulation-flow.md") {
		t.Fatalf("an edit on the other side brings the pair back:\n%s", s)
	}
	confirmKB(t, b, "architecture/circulation-flow.md")
	writeKB(t, a, "architecture/circulation-flow.md", "# Circulation flow\n\nRenews twice daily.\n")
	if s := worksheetSection(ws(), entry); s == "" {
		t.Fatal("an edit on this side brings the pair back")
	}
}
