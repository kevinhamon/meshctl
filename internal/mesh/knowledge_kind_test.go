package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setManifestKnowledge appends a `knowledge:` key to a scaffolded peer's
// agent.yaml and reloads it.
func setManifestKnowledge(t *testing.T, root, name, kind string) *Agent {
	t.Helper()
	mp := filepath.Join(root, name, ManifestName)
	b, err := os.ReadFile(mp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mp, append(b, []byte("knowledge: "+kind+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := FindAgent(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEffectiveKnowledge_ManifestElsePoolElseCanonical(t *testing.T) {
	cases := []struct {
		name, pool, manifest, want string
	}{
		{"absent at both levels is canonical", "", "", KnowledgeCanonical},
		{"pool default applies", KnowledgePerspective, "", KnowledgePerspective},
		{"manifest overrides pool", KnowledgePerspective, KnowledgeCanonical, KnowledgeCanonical},
		{"manifest alone", "", KnowledgePerspective, KnowledgePerspective},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := PoolConfig{Settings: PoolSettings{Knowledge: c.pool}}
			a := &Agent{Name: "x", Knowledge: c.manifest}
			if got := EffectiveKnowledge(cfg, a); got != c.want {
				t.Fatalf("EffectiveKnowledge = %q, want %q", got, c.want)
			}
		})
	}
}

func TestEffectiveKnowledge_ReadsManifestKeyFromDisk(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: perspective\n"), 0o644)
	newTestAgent(t, root, "bard", false, nil)
	newTestAgent(t, root, "scribe", false, nil)
	scribe := setManifestKnowledge(t, root, "scribe", "canonical")
	bard, _ := FindAgent(root, "bard")
	cfg := LoadPoolConfig(root)
	if got := EffectiveKnowledge(cfg, bard); got != KnowledgePerspective {
		t.Fatalf("bard = %q, want pool default perspective", got)
	}
	if got := EffectiveKnowledge(cfg, scribe); got != KnowledgeCanonical {
		t.Fatalf("scribe = %q, want manifest override canonical", got)
	}
}

func TestManifestValidate_KnowledgeValue(t *testing.T) {
	base := Agent{Name: "x", Title: "t", Accepts: []Accept{{Type: "fyi"}}, Badge: Badge{Label: "X"}}
	for _, kind := range []string{"", "canonical", "perspective"} {
		a := base
		a.Knowledge = kind
		for _, p := range a.Validate() {
			if strings.Contains(p, "knowledge") {
				t.Fatalf("knowledge %q must validate, got %q", kind, p)
			}
		}
	}
	a := base
	a.Knowledge = "shared"
	found := false
	for _, p := range a.Validate() {
		if strings.Contains(p, `knowledge "shared"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("knowledge \"shared\" must be a validation problem: %v", a.Validate())
	}
}

func TestDoctor_ErrorsOnInvalidKnowledgeAtEitherLevel(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: global\n"), 0o644)
	newTestAgent(t, root, "bard", false, nil)
	setManifestKnowledge(t, root, "bard", "opinion")

	findings, ok := Doctor(root)
	if ok {
		t.Fatal("an invalid knowledge value is an error; doctor must not pass")
	}
	var pool, peer bool
	for _, f := range findings {
		if f.Level != "error" {
			continue
		}
		if f.Agent == "pool" && strings.Contains(f.Message, `knowledge "global"`) {
			pool = true
		}
		if f.Agent == "bard" && strings.Contains(f.Message, `knowledge "opinion"`) {
			peer = true
		}
	}
	if !pool || !peer {
		t.Fatalf("want errors for pool (%v) and peer (%v): %+v", pool, peer, findings)
	}
}

func TestDoctor_ValidKnowledgeIsSilent(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: perspective\n"), 0o644)
	newTestAgent(t, root, "bard", false, nil)
	setManifestKnowledge(t, root, "bard", "canonical")
	findings, _ := Doctor(root)
	if hasFinding(findings, "knowledge \"") {
		t.Fatalf("valid kinds must not be flagged: %+v", findings)
	}
}

// setManifestOwns replaces a scaffolded peer's `owns:` line.
func setManifestOwns(t *testing.T, root, name string, owns string) {
	t.Helper()
	mp := filepath.Join(root, name, ManifestName)
	b, err := os.ReadFile(mp)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "owns:") {
			l = "owns: [" + owns + "]"
		}
		lines = append(lines, l)
	}
	os.WriteFile(mp, []byte(strings.Join(lines, "\n")), 0o644)
}

func TestDoctor_WarnsOnOwnsSharedByCanonicalPeers(t *testing.T) {
	root := migratedRoot(t)
	newTestAgent(t, root, "architect", false, nil)
	newTestAgent(t, root, "builder", false, nil)
	setManifestOwns(t, root, "architect", "release-workflow, adrs")
	setManifestOwns(t, root, "builder", "release-workflow")

	findings, _ := Doctor(root)
	var hit *DoctorFinding
	for i, f := range findings {
		if strings.Contains(f.Message, "owns") && strings.Contains(f.Message, "release-workflow") {
			hit = &findings[i]
		}
	}
	if hit == nil || hit.Level != "warn" || hit.Agent != "architect,builder" {
		t.Fatalf("want one warn naming architect,builder for release-workflow: %+v", findings)
	}
	if hasFinding(findings, "`adrs`") {
		t.Fatalf("a tag owned once must not be flagged: %+v", findings)
	}
}

func TestDoctor_OwnsSharedWithPerspectivePeerIsSilent(t *testing.T) {
	for _, viaPool := range []bool{false, true} {
		root := migratedRoot(t)
		newTestAgent(t, root, "architect", false, nil)
		newTestAgent(t, root, "bard", false, nil)
		setManifestOwns(t, root, "architect", "release-workflow")
		setManifestOwns(t, root, "bard", "release-workflow")
		if viaPool {
			// pool default perspective, architect opts back into canonical
			os.WriteFile(PoolConfigPath(root), []byte("knowledge: perspective\n"), 0o644)
			setManifestKnowledge(t, root, "architect", "canonical")
		} else {
			setManifestKnowledge(t, root, "bard", "perspective")
		}
		findings, _ := Doctor(root)
		if hasFinding(findings, "release-workflow") {
			t.Fatalf("viaPool=%v: a perspective peer is exempt from owns uniqueness: %+v", viaPool, findings)
		}
	}
}

func TestDirectory_PrintsEachPeersKnowledgeKind(t *testing.T) {
	root := migratedRoot(t)
	os.WriteFile(PoolConfigPath(root), []byte("knowledge: perspective\n"), 0o644)
	newTestAgent(t, root, "architect", false, nil)
	newTestAgent(t, root, "bard", false, nil)
	setManifestKnowledge(t, root, "architect", "canonical")
	agents, _, err := ScanAgents(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := LoadPoolConfig(root)

	md := DirectoryMarkdown(agents, cfg)
	if !strings.Contains(md, "| Knowledge |") {
		t.Fatalf("roster table needs a Knowledge column:\n%s", md)
	}
	rows := map[string]string{}
	for _, l := range strings.Split(md, "\n") {
		for _, n := range []string{"architect", "bard"} {
			if strings.HasPrefix(l, "| **"+n+"** |") {
				rows[n] = l
			}
		}
	}
	if !strings.Contains(rows["architect"], "| canonical |") || !strings.Contains(rows["bard"], "| perspective |") {
		t.Fatalf("rows must carry the effective kind: %q", rows)
	}

	js, err := DirectoryJSON(agents, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name      string `json:"name"`
		Knowledge string `json:"knowledge"`
	}
	if err := json.Unmarshal([]byte(js), &entries); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Name] = e.Knowledge
	}
	if got["architect"] != "canonical" || got["bard"] != "perspective" {
		t.Fatalf("JSON directory kinds = %v", got)
	}
}
