package mesh

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// grantedPeer writes a hand-authored manifest for a dispatchable peer that
// mutates the given surfaces (the scaffolder coerces such a peer to
// dispatchable:false, so it is written directly) and returns it loaded.
func grantedPeer(t *testing.T, root, name string, mutates ...string) *Agent {
	t.Helper()
	newTestAgent(t, root, name, false, nil)
	mp := filepath.Join(root, name, ManifestName)
	body := "name: " + name + "\ntitle: T\naccepts:\n  - {type: guidance}\ndispatchable: true\nmutates: [" + strings.Join(mutates, ", ") + "]\nbadge: {label: T}\n"
	if err := os.WriteFile(mp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAgent(mp)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

const grantsTwoSurfaces = `outbound:
  tracker:
    tools: ['create.*']
  code:
    tools: ['push.*']
grants:
  peers:
    pm:
      "outbound:tracker": {}
      "outbound:code": {}
`

func askOpts() AskOptions {
	return AskOptions{Type: "guidance", Prompt: "x", From: "tester", Runner: okRunner, Now: time.Now(), Stdout: io.Discard, Stderr: io.Discard}
}

func TestDispatchGrants_NoGrantsRefusalUnchanged(t *testing.T) {
	root := migratedRoot(t)
	pm := grantedPeer(t, root, "pm", "tracker")
	_, rc, err := Ask(root, pm, askOpts())
	if err == nil || rc == 0 {
		t.Fatalf("want refusal, got rc=%d err=%v", rc, err)
	}
	for _, want := range []string{"refusing to dispatch", "state-mutating agents stay human-triggered", "no `outbound:tracker` grant in grants.peers.pm", "meshctl send pm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
	// Not declared dispatchable: refused whatever the grants.
	writeGrantsConfig(t, root, grantsTwoSurfaces)
	adv := newTestAgent(t, root, "quiet", false, nil)
	if r := DispatchRefusal(root, adv); r != "not dispatchable" {
		t.Fatalf("got %q", r)
	}
}

func TestDispatchGrants_AllGrantedDispatches(t *testing.T) {
	root := migratedRoot(t)
	writeGrantsConfig(t, root, grantsTwoSurfaces)
	pm := grantedPeer(t, root, "pm", "tracker", "code")
	if r := DispatchRefusal(root, pm); r != "" {
		t.Fatalf("all granted: %q", r)
	}
	if _, rc, err := Ask(root, pm, askOpts()); err != nil || rc != 0 {
		t.Fatalf("dispatch: rc=%d err=%v", rc, err)
	}
	// The anti-cascade guards still apply to a granted peer.
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if _, rc, _ := Ask(root, pm, askOpts()); rc != ExitDepth {
		t.Fatalf("depth guard: want rc=%d got %d", ExitDepth, rc)
	}
}

func TestDispatchGrants_OneMissingNamed(t *testing.T) {
	root := migratedRoot(t)
	writeGrantsConfig(t, root, strings.Replace(grantsTwoSurfaces, "      \"outbound:code\": {}\n", "", 1))
	pm := grantedPeer(t, root, "pm", "tracker", "code")
	r := DispatchRefusal(root, pm)
	if !strings.Contains(r, "no `outbound:code` grant in grants.peers.pm") || strings.Contains(r, "outbound:tracker") {
		t.Fatalf("refusal must name only the missing grant: %q", r)
	}
	if _, rc, err := Ask(root, pm, askOpts()); err == nil || rc == 0 || !strings.Contains(err.Error(), "outbound:code") {
		t.Fatalf("want refusal naming outbound:code, got rc=%d err=%v", rc, err)
	}
}

func TestDispatchGrants_PausedRefuses(t *testing.T) {
	root := migratedRoot(t)
	writeGrantsConfig(t, root, strings.Replace(grantsTwoSurfaces, "grants:\n", "grants:\n  paused: true\n", 1))
	pm := grantedPeer(t, root, "pm", "tracker", "code")
	if r := DispatchRefusal(root, pm); !strings.Contains(r, "grants are paused") {
		t.Fatalf("got %q", r)
	}
	// Pause does not touch a peer that mutates nothing.
	adv := newTestAgent(t, root, "advisor", true, nil)
	if r := DispatchRefusal(root, adv); r != "" {
		t.Fatalf("advisory peer under pause: %q", r)
	}
}

func TestDispatchGrants_UnparseableConfigRefuses(t *testing.T) {
	root := migratedRoot(t)
	writeGrantsConfig(t, root, "grants: [unclosed\n")
	pm := grantedPeer(t, root, "pm", "tracker")
	if r := DispatchRefusal(root, pm); !strings.Contains(r, "cannot be parsed") {
		t.Fatalf("got %q", r)
	}
}

func TestDispatchGrants_DirectoryShowsGrantedPeer(t *testing.T) {
	root := migratedRoot(t)
	pm := grantedPeer(t, root, "pm", "tracker", "code")
	agents := []*Agent{pm}
	if md := DirectoryMarkdown(agents, LoadPoolConfig(root)); !strings.Contains(md, "send-only (mutates: tracker,code)") {
		t.Fatalf("no grants: output must be unchanged:\n%s", md)
	}
	writeGrantsConfig(t, root, grantsTwoSurfaces)
	cfg := LoadPoolConfig(root)
	if md := DirectoryMarkdown(agents, cfg); !strings.Contains(md, "ask+send (mutates: tracker,code, granted)") {
		t.Fatalf("granted:\n%s", md)
	}
	js, err := DirectoryJSON(agents, cfg)
	if err != nil || !strings.Contains(js, `"dispatchable": true`) {
		t.Fatalf("json: %v\n%s", err, js)
	}
}
