package console

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// agentview.go — GET /api/agent: the parts of one agent's page that are not
// in the pool snapshot its knowledge base by area,
// its recent commits and its recently closed requests. Read on demand, for
// the agent on screen only.

const (
	kbAreaFiles   = 30 // newest files listed per KB folder
	recentCommits = 10
	recentClosed  = 8
)

// KBFile is one knowledge file, openable in the viewer.
type KBFile struct {
	Path     string `json:"path"` // workspace-relative
	Modified string `json:"modified"`
	URL      string `json:"url"`
}

// KBArea is a top-level entry of knowledge/: a file (state.md) or a folder
// (decisions/), with its markdown files newest first.
type KBArea struct {
	Name     string   `json:"name"`
	Folder   bool     `json:"folder"`
	Count    int      `json:"count"`
	Modified string   `json:"modified"`
	Files    []KBFile `json:"files"`
}

// Commit is one commit of the agent's repo.
type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	When    string `json:"when"`
}

// ClosedRequest is a request the agent closed and archived.
type ClosedRequest struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Closed string `json:"closed"` // when it was archived (file time)
}

// AgentView is GET /api/agent.
type AgentView struct {
	Agent   string          `json:"agent"`
	KB      []KBArea        `json:"kb"`
	Commits []Commit        `json:"commits"`
	Closed  []ClosedRequest `json:"closed"`
}

// KnowledgeAreas lists knowledge/ by top-level entry. Hidden files (the
// ledger) and the scaffolded folder READMEs are not knowledge entries.
func KnowledgeAreas(a *mesh.Agent, url func(rel string) string) []KBArea {
	out := []KBArea{}
	root := filepath.Join(a.Dir(), mesh.KnowledgeDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		rel := mesh.KnowledgeDir + "/" + name
		if !e.IsDir() {
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, KBArea{Name: name, Count: 1, Modified: stamp(fi.ModTime()),
				Files: []KBFile{{Path: rel, Modified: stamp(fi.ModTime()), URL: url(rel)}}})
			continue
		}
		area := KBArea{Name: name, Folder: true, Files: []KBFile{}}
		var files []KBFile
		_ = filepath.WalkDir(filepath.Join(root, name), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			r, _ := filepath.Rel(a.Dir(), p)
			r = filepath.ToSlash(r)
			if r == rel+"/README.md" {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			files = append(files, KBFile{Path: r, Modified: stamp(fi.ModTime())})
			return nil
		})
		sort.SliceStable(files, func(i, j int) bool { return files[i].Modified > files[j].Modified })
		area.Count = len(files)
		if len(files) > 0 {
			area.Modified = files[0].Modified
		}
		if len(files) > kbAreaFiles {
			files = files[:kbAreaFiles]
		}
		for i := range files {
			files[i].URL = url(files[i].Path)
		}
		area.Files = append(area.Files, files...)
		out = append(out, area)
	}
	return out
}

// RecentCommits is the agent repo's latest commits; none outside a repo.
func RecentCommits(dir string, n int) []Commit {
	out := []Commit{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "git", "-C", dir, "log", "-n", strconv.Itoa(n), "--format=%h%x1f%s%x1f%cI").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) == 3 {
			out = append(out, Commit{Hash: f[0], Subject: f[1], When: f[2]})
		}
	}
	return out
}

// RecentlyClosed is the agent's archived requests, newest archive first.
func RecentlyClosed(a *mesh.Agent, n int) []ClosedRequest {
	out := []ClosedRequest{}
	dir := filepath.Join(a.Dir(), mesh.IntakeArchiveDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			items = append(items, item{filepath.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	for _, it := range items {
		if len(out) == n {
			break
		}
		r, err := mesh.ReadRequest(it.path)
		if err != nil {
			continue
		}
		out = append(out, ClosedRequest{ID: r.ID, From: r.From, Type: r.Type, Title: firstLine(r.Body),
			Closed: it.mod.UTC().Format(time.RFC3339)})
	}
	return out
}

// firstLine is a request body's first line of text: its headings (the
// "## Ask" section mark) skipped.
func firstLine(body string) string {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			return firstSentence(l, 140)
		}
	}
	return ""
}

// agentView is GET /api/agent?pool=&agent=.
func (s *Server) agentView(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	a, ok := s.agentFor(e, r.URL.Query().Get("agent"))
	if !ok {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	pool := s.poolKey(r)
	v := AgentView{Agent: a.Name,
		KB:      KnowledgeAreas(a, func(rel string) string { return s.caps.docURL(pool, a.Name, mesh.KnowledgeDir, rel) }),
		Commits: RecentCommits(a.Dir(), recentCommits),
		Closed:  RecentlyClosed(a, recentClosed)}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
