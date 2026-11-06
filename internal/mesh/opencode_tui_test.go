package mesh

import (
	"strings"
	"testing"
	"time"
)

func TestOpencodeTUIPlugin_Rendered(t *testing.T) {
	if strings.Contains(opencodeTUIPlugin, "__") {
		t.Fatal("unfilled placeholder in the panel source")
	}
	for _, want := range []string{opencodePluginMarker, `"hook", "status-line", "--json"`, "sidebar.content", "prompt.footer", "REFRESH_MS = 10000"} {
		if !strings.Contains(opencodeTUIPlugin, want) {
			t.Errorf("panel source lacks %q", want)
		}
	}
}

func TestStatusLineReport_MatchesTheLine(t *testing.T) {
	dir, _, arch, _ := pendingPool(t)
	r := StatusLineReport(dir, cwdPayload(arch.Dir()), time.Now())
	if r.Agent != "architect" || r.Pool == "" {
		t.Fatalf("report: %+v", r)
	}
	got := []string{}
	for _, g := range r.Groups {
		got = append(got, g.Label+"="+g.Value+"|"+g.Say)
	}
	want := []string{"needs-confirm=1|confirm kb", "intake=2 oldest 3d|check intake", "dispatched=1 oldest 0d|review work"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("groups:\n got %v\nwant %v", got, want)
	}
	if !r.Groups[0].Alert || r.Groups[1].Alert {
		t.Fatalf("alerts follow the line's red groups: %+v", r.Groups)
	}
	if out := StatusLineReport(dir, cwdPayload(t.TempDir()), time.Now()); out.Agent != "" || out.Groups == nil {
		t.Fatalf("a non-agent folder has no agent and an empty list: %+v", out)
	}
}
