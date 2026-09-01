package mesh

import (
	"strings"
	"testing"
)

func TestTerminalIdentity_DarkBadge(t *testing.T) {
	a := &Agent{Name: "architect", Title: "Software Architect",
		Badge: Badge{Label: "ARCH", R: 0.0, G: 0.3, B: 0.8}}
	got := TerminalIdentity(a)

	if !strings.Contains(got, "\x1b]0;architect\x07") {
		t.Errorf("missing OSC title-set for name; got %q", got)
	}
	if !strings.Contains(got, "48;2;0;77;204m") { // 0.3→77, 0.8→204
		t.Errorf("missing/incorrect truecolor bg; got %q", got)
	}
	if !strings.Contains(got, "\x1b[97m") { // dark badge ⇒ white fg
		t.Errorf("expected white fg on dark badge; got %q", got)
	}
	if !strings.Contains(got, "ARCH") || !strings.Contains(got, "Software Architect") {
		t.Errorf("missing label/title; got %q", got)
	}
}

func TestTerminalIdentity_LightBadgeAndLabelFallback(t *testing.T) {
	a := &Agent{Name: "qa", Badge: Badge{Label: "", R: 0.9, G: 0.9, B: 0.9}}
	got := TerminalIdentity(a)

	if !strings.Contains(got, " qa ") { // label defaults to the name
		t.Errorf("label should fall back to name; got %q", got)
	}
	if !strings.Contains(got, "\x1b[30m") { // light badge ⇒ black fg
		t.Errorf("expected black fg on light badge; got %q", got)
	}
}
