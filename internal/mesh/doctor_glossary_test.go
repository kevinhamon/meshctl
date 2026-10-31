package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Peer glossaries retire: the
// KB template scaffolds none, doctor warns on a leftover peer glossary once
// the pool glossary exists, and warns softly on an `owns` value no pool term
// matches, only when the pool glossary has terms and no legacy glossary is
// left.

const (
	peerGlossaryWarn = "still has knowledge/glossary.md"
	ownsWarn         = "matches no pool glossary term"
)

func writePeerGlossary(t *testing.T, a *Agent, body string) {
	t.Helper()
	p := filepath.Join(a.Dir(), KnowledgeDir, "glossary.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// owningAgent scaffolds a peer whose manifest declares owns values.
func owningAgent(t *testing.T, root, name string, owns ...string) *Agent {
	t.Helper()
	spec := AgentSpec{
		Name: name, Title: name + " title", Role: "role", Owns: owns,
		Accepts: []Accept{{Type: "guidance", Desc: "advice"}},
		Badge:   Badge{Label: name, R: 0.1, G: 0.2, B: 0.3},
		Bare:    true,
	}
	if _, err := New(root, spec, ScaffoldOptions{}); err != nil {
		t.Fatalf("scaffold %s: %v", name, err)
	}
	a, err := FindAgent(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func agentFindings(fs []DoctorFinding, agent, substr string) []DoctorFinding {
	var out []DoctorFinding
	for _, f := range fs {
		if f.Agent == agent && strings.Contains(f.Message, substr) {
			out = append(out, f)
		}
	}
	return out
}

func TestScaffold_NoPeerGlossary(t *testing.T) {
	root := migratedRoot(t)
	a := newTestAgent(t, root, "architect", false, nil)
	if fileExists(filepath.Join(a.Dir(), KnowledgeDir, "glossary.md")) {
		t.Fatal("a new peer must not get a knowledge/glossary.md: the pool glossary is the one vocabulary")
	}
	for _, rel := range []string{
		filepath.Join(KnowledgeDir, "candidates", "README.md"),
		filepath.Join(".claude", "skills", "mesh-cognition", "SKILL.md"),
	} {
		b, err := os.ReadFile(filepath.Join(a.Dir(), rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "`glossary.md`") {
			t.Fatalf("%s still routes vocabulary to a peer glossary.md:\n%s", rel, b)
		}
		if !strings.Contains(string(b), "pool glossary") {
			t.Fatalf("%s should route vocabulary to the pool glossary:\n%s", rel, b)
		}
	}
}

func TestDoctor_WarnsOnPeerGlossaryOncePoolGlossaryExists(t *testing.T) {
	root := migratedRoot(t)
	a := newTestAgent(t, root, "architect", false, nil)
	writePeerGlossary(t, a, "# Glossary\n\n**Circulation window**: The period.\n")

	fs, _ := Doctor(root)
	got := agentFindings(fs, "architect", peerGlossaryWarn)
	if len(got) != 1 || got[0].Level != "warn" {
		t.Fatalf("want one warn on the leftover peer glossary, got %+v", got)
	}
	if !strings.Contains(got[0].Message, "by hand") || !strings.Contains(got[0].Message, "pool glossary") {
		t.Fatalf("warning must name the by-hand migration to the pool glossary: %q", got[0].Message)
	}
}

func TestDoctor_SilentOnPeerGlossaryWithoutPoolGlossary(t *testing.T) {
	root := migratedRoot(t)
	a := newTestAgent(t, root, "architect", false, nil)
	writePeerGlossary(t, a, "**Circulation window**: The period.\n")
	if err := os.Remove(PoolGlossaryPath(root)); err != nil {
		t.Fatal(err)
	}
	fs, _ := Doctor(root)
	if got := agentFindings(fs, "architect", peerGlossaryWarn); len(got) != 0 {
		t.Fatalf("no pool glossary yet: the peer glossary is the only vocabulary, no warning: %+v", got)
	}
}

func TestDoctor_OwnsWithoutPoolTermWarnsSoftly(t *testing.T) {
	root := migratedRoot(t)
	writePoolGlossary(t, root, "# Pool glossary\n\n**Circulation Window**: The period.\n")
	owningAgent(t, root, "architect", "circulation-window", "Renewal")

	fs, _ := Doctor(root)
	got := agentFindings(fs, "architect", ownsWarn)
	if len(got) != 1 || got[0].Level != "warn" {
		t.Fatalf("want one warn for the unmatched owns value, got %+v", got)
	}
	msg := got[0].Message
	if !strings.Contains(msg, "`Renewal`") || strings.Contains(msg, "circulation-window") {
		t.Fatalf("warning must name only the unmatched value: %q", msg)
	}
	if !strings.Contains(msg, "consider") {
		t.Fatalf("owns warning is a suggestion, not a demand: %q", msg)
	}
}

func TestDoctor_OwnsMatchIsKebabSlugCaseInsensitive(t *testing.T) {
	root := migratedRoot(t)
	writePoolGlossary(t, root, "**Circulation Window**: The period.\n**Fact owner**: The peer.\n")
	owningAgent(t, root, "architect", "Circulation-Window", "fact owner")

	fs, _ := Doctor(root)
	if got := agentFindings(fs, "architect", ownsWarn); len(got) != 0 {
		t.Fatalf("every owns value matches a term by kebab slug: %+v", got)
	}
}

func TestDoctor_OwnsSilentOnEmptyPoolGlossary(t *testing.T) {
	root := migratedRoot(t) // template glossary: prose, no terms
	owningAgent(t, root, "architect", "renewal")

	fs, _ := Doctor(root)
	if got := agentFindings(fs, "architect", ownsWarn); len(got) != 0 {
		t.Fatalf("an empty pool glossary must not trigger the owns warning: %+v", got)
	}
}

func TestDoctor_OwnsSilentWhileLegacyPeerGlossaryRemains(t *testing.T) {
	root := migratedRoot(t)
	writePoolGlossary(t, root, "**Circulation window**: The period.\n")
	a := owningAgent(t, root, "architect", "renewal")
	writePeerGlossary(t, a, "**Renewal**: Extending a loan.\n")

	fs, _ := Doctor(root)
	if got := agentFindings(fs, "architect", ownsWarn); len(got) != 0 {
		t.Fatalf("owns warning waits until the legacy glossary is migrated: %+v", got)
	}
	if got := agentFindings(fs, "architect", peerGlossaryWarn); len(got) != 1 {
		t.Fatalf("the leftover peer glossary still warns: %+v", fs)
	}
}

func TestRecall_LegacyPeerGlossaryStaysCurrentTier(t *testing.T) {
	root := migratedRoot(t)
	a := newTestAgent(t, root, "architect", false, nil)
	writePeerGlossary(t, a, "# Glossary\n\n## Renewal\n\n**Renewal**: Extending a loan.\n")

	hits, err := Recall(a, "renewal", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Kind == "kb" && h.Via == "" {
			for _, w := range h.Where {
				if strings.HasSuffix(w, "glossary.md") {
					if h.Tier != TierCurrent {
						t.Fatalf("legacy peer glossary must recall current-tier: %+v", h)
					}
					return
				}
			}
		}
	}
	t.Fatalf("legacy peer glossary not indexed: %+v", hits)
}
