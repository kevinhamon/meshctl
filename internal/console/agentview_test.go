package console

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// agentview_test.go — the agent page's knowledge, history and documents
// summary (agent-docs ticket 04).

func TestKnowledgeAreas_FilesFoldersAndSkips(t *testing.T) {
	_, a := testPool(t)
	writeDoc(t, a, "knowledge/state.md", "# state", time.Minute)
	writeDoc(t, a, "knowledge/decisions/0001-a.md", "# a", 3*time.Hour)
	writeDoc(t, a, "knowledge/decisions/0002-b.md", "# b", time.Hour)
	writeDoc(t, a, "knowledge/notes.txt", "x", 0)
	areas := KnowledgeAreas(a, func(rel string) string { return "u:" + rel })
	by := map[string]KBArea{}
	for _, ar := range areas {
		by[ar.Name] = ar
	}
	if s := by["state.md"]; s.Folder || s.Count != 1 || s.Files[0].URL != "u:knowledge/state.md" {
		t.Errorf("state.md: %+v", s)
	}
	d := by["decisions"]
	if !d.Folder || d.Count != 2 || d.Files[0].Path != "knowledge/decisions/0002-b.md" {
		t.Errorf("decisions newest first: %+v", d)
	}
	if p := by["playbooks"]; !p.Folder || p.Count != 0 {
		t.Errorf("a scaffolded README is not an entry: %+v", p)
	}
	for _, skip := range []string{"notes.txt", ".confirmed.yaml"} {
		if _, ok := by[skip]; ok {
			t.Errorf("%s is not a knowledge area", skip)
		}
	}
}

func TestRecentlyClosedAndCommits(t *testing.T) {
	_, a := testPool(t)
	r, err := mesh.WriteRequest(a, "guidance", "Split the service?\n\nDetails.", "user", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mesh.ArchiveRequest(a, r.ID); err != nil {
		t.Fatal(err)
	}
	c := RecentlyClosed(a, 5)
	if len(c) != 1 || c[0].ID != r.ID || c[0].Title != "Split the service?" || c[0].From != "user" {
		t.Fatalf("closed: %+v", c)
	}
	if commits := RecentCommits(a.Dir(), 3); len(commits) == 0 || commits[0].Subject == "" {
		t.Errorf("a scaffolded agent has a first commit: %+v", commits)
	}
	if commits := RecentCommits(t.TempDir(), 3); len(commits) != 0 {
		t.Errorf("no repo, no commits: %+v", commits)
	}
}

func TestAgentView_ThroughTheServer(t *testing.T) {
	_, a, get := docServer(t)
	writeDoc(t, a, "knowledge/state.md", "# state", 0)
	writeDoc(t, a, "docs/d.md", "# d", 0)
	w := get("/api/agent?agent=architect", true)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var v AgentView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	var state string
	for _, ar := range v.KB {
		if ar.Name == "state.md" {
			state = ar.Files[0].URL
		}
	}
	// A KB file opens in the viewer through its own capability, sandboxed.
	if r := get(state, false); r.Code != http.StatusOK || r.Header().Get("Content-Security-Policy") != docSandbox {
		t.Errorf("state.md through the viewer: %d", r.Code)
	}
	if w := get("/api/agent?agent=architect", false); w.Code != http.StatusUnauthorized {
		t.Errorf("the agent view needs the token: %d", w.Code)
	}
	if w := get("/api/agent?agent=ghost", true); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent: %d", w.Code)
	}
	// The roster counts documents for the page's refetch.
	for _, info := range Roster(filepath.Dir(a.Dir())) {
		if info.Name == "architect" && (info.Docs != 1 || info.DocsNewest == "") {
			t.Errorf("roster docs: %+v", info)
		}
	}
}

func TestWatchPaths_IncludeDocumentFolders(t *testing.T) {
	dir, a := testPool(t)
	_ = os.MkdirAll(filepath.Join(a.Dir(), "docs"), 0o755)
	root, _ := filepath.EvalSymlinks(filepath.Join(a.Dir(), "docs"))
	if !strings.Contains(strings.Join(WatchPaths(dir), "\n"), root) {
		t.Errorf("docs/ is watched")
	}
}
