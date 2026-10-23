package mesh

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// summary_test.go — the confirmation summary file (ADR-0067, spec decision 19)
// and the ask-text renderer every gate reuses (spec decision 69).

const fullSummary = `## Intent
Add the term "fact owner" to the pool glossary and retire "owner" where it meant that.

Second intent paragraph is not the Intent line.

## Grounding
- GLOSSARY.md:12 defines fact owner (pool repo).
- ADR-0065 names one owner per canonical fact.

## Precedent
- *new*: searched doctrine/agent-comms.md and the architect KB.

## Coherence
- architect/knowledge/system-map.md#ownership: consistent

## Changes
### pool/glossary.md
+ a **Fact owner** term: the one peer that confirms a canonical fact
- + the old "owner" line that meant the same thing is removed? no: this is an added bullet
- − the old **Owner** line
- - a duplicate **Owner** line
* + a third added bullet via a star list

### knowledge/experience/ownership.md
+ one added bullet
`

func TestSummary_ParsesRequiredAndOptionalSections(t *testing.T) {
	s := ParseSummary(fullSummary)
	if s.Intent != `Add the term "fact owner" to the pool glossary and retire "owner" where it meant that.` {
		t.Fatalf("Intent=%q", s.Intent)
	}
	if !s.HasIntent() {
		t.Fatal("HasIntent must be true with an Intent line")
	}
	if !strings.Contains(s.Grounding, "GLOSSARY.md:12") || !strings.Contains(s.Grounding, "ADR-0065") {
		t.Fatalf("Grounding=%q", s.Grounding)
	}
	if !strings.Contains(s.Precedent, "*new*") {
		t.Fatalf("Precedent=%q", s.Precedent)
	}
	if !s.HasCoherence || !strings.Contains(s.Coherence, "consistent") {
		t.Fatalf("Coherence=%q has=%v", s.Coherence, s.HasCoherence)
	}
	if len(s.Changes) != 2 {
		t.Fatalf("want 2 change blocks, got %+v", s.Changes)
	}
	g := s.Changes[0]
	if g.Path != "pool/glossary.md" {
		t.Fatalf("first block path=%q", g.Path)
	}
	wantAdded := []string{
		"a **Fact owner** term: the one peer that confirms a canonical fact",
		`the old "owner" line that meant the same thing is removed? no: this is an added bullet`,
		"a third added bullet via a star list",
	}
	wantRemoved := []string{"the old **Owner** line", "a duplicate **Owner** line"}
	if strings.Join(g.Added, "|") != strings.Join(wantAdded, "|") {
		t.Fatalf("Added=%q", g.Added)
	}
	if strings.Join(g.Removed, "|") != strings.Join(wantRemoved, "|") {
		t.Fatalf("Removed=%q", g.Removed)
	}
	if s.Changes[1].Path != "knowledge/experience/ownership.md" || len(s.Changes[1].Added) != 1 || len(s.Changes[1].Removed) != 0 {
		t.Fatalf("second block=%+v", s.Changes[1])
	}
}

func TestSummary_EmptyOrNoIntentIsNotUsable(t *testing.T) {
	for name, text := range map[string]string{
		"empty":          "",
		"whitespace":     "  \n\n",
		"no intent":      "## Grounding\n- x\n\n## Precedent\n- y\n",
		"empty intent":   "## Intent\n\n## Grounding\n- x\n",
		"heading-only":   "## Intent\n## Grounding\n## Precedent\n",
		"prose, no head": "I changed the glossary.\n",
	} {
		t.Run(name, func(t *testing.T) {
			if s := ParseSummary(text); s.HasIntent() {
				t.Fatalf("HasIntent=true for %q (Intent=%q)", text, s.Intent)
			}
		})
	}
}

// The Intent line is accepted in the heading form (`## Intent` then the first
// non-empty line) and the labelled-line form a template bullet list produces.
func TestSummary_IntentLineForms(t *testing.T) {
	for name, text := range map[string]string{
		"heading":        "## Intent\nRename the term.\n## Grounding\nx\n## Precedent\ny\n",
		"heading colon":  "## Intent:\nRename the term.\n",
		"labelled":       "Intent: Rename the term.\nGrounding: x\nPrecedent: y\n",
		"bold labelled":  "- **Intent**: Rename the term.\n- **Grounding**: x\n",
		"level-1 header": "# Intent\nRename the term.\n",
	} {
		t.Run(name, func(t *testing.T) {
			if s := ParseSummary(text); s.Intent != "Rename the term." {
				t.Fatalf("Intent=%q", s.Intent)
			}
		})
	}
}

func TestSummary_PathIsPerSessionUnderConfirmDir(t *testing.T) {
	got := SummaryPath("/pool/architect", "f93b9f92-1195-4dce-b563-ec597b3d4f92")
	want := filepath.Join("/pool/architect", MemoryDir, "confirm", "f93b9f92-1195-4dce-b563-ec597b3d4f92.md")
	if got != want {
		t.Fatalf("SummaryPath=%q want %q", got, want)
	}
	// A session id can never escape the confirm directory.
	if got := SummaryPath("/pool/a", "../x/y"); filepath.Dir(got) != filepath.Join("/pool/a", MemoryDir, "confirm") {
		t.Fatalf("unsafe session id leaked into the path: %q", got)
	}
}

func TestSummary_ReadMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSummary(filepath.Join(dir, "none.md")); err == nil {
		t.Fatal("missing summary must error")
	}
	p := filepath.Join(dir, "empty.md")
	os.WriteFile(p, []byte("\n"), 0o644)
	s, err := ReadSummary(p)
	if err != nil || s.HasIntent() {
		t.Fatalf("empty summary: err=%v HasIntent=%v", err, s.HasIntent())
	}
}

// RenderAsk is the one renderer for every gate's ask text: the header names
// the gate and the action, the Intent line comes first, the body follows, and
// the rendered length is logged so prompt truncation can be diagnosed.
func TestRenderAsk_IntentFirstAndLengthLogged(t *testing.T) {
	log := filepath.Join(t.TempDir(), "gate.log")
	t.Setenv(gateLogEnv, log)
	s := ParseSummary(fullSummary)
	text := RenderAsk("", AskText{
		Check:  GateCheckPoolPath,
		Header: "mesh: this writes pool knowledge — pool/glossary.md",
		Intent: s.Intent,
		Body:   []string{"Grounding:", "- GLOSSARY.md:12"},
		Footer: "Deny if this is not what you agreed to in chat.",
	})
	lines := strings.Split(text, "\n")
	if lines[0] != "mesh: this writes pool knowledge — pool/glossary.md" {
		t.Fatalf("header is not first: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Intent: Add the term") {
		t.Fatalf("Intent line must follow the header: %q", lines[1])
	}
	if !strings.Contains(text, "- GLOSSARY.md:12") || !strings.HasSuffix(text, "Deny if this is not what you agreed to in chat.") {
		t.Fatalf("body/footer missing: %q", text)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("rendered length was not logged: %v", err)
	}
	want := "check=" + GateCheckPoolPath + " chars=" + strconv.Itoa(len(text))
	if !strings.Contains(string(b), "ask-rendered") || !strings.Contains(string(b), want) {
		t.Fatalf("log %q lacks %q", string(b), want)
	}
}
