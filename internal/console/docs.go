package console

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// docs.go — the documents an agent writes for the person, listed and served
// read-only (spec .scratch/agent-docs; ticket 01 settled the scheme).
//
// A document is code the console did not write, so it never runs with the
// console's credentials. It is served from a capability path,
// /c/<cap>/<pool>/<agent>/<path>, that needs no token: the console page gets
// the path from the guarded /api/docs, and the capability, an HMAC over the
// pool, agent and folder under a key that lives only as long as this
// process, admits that one folder, so a document's relative images and
// scripts load too. Every response but a real PDF carries a CSP sandbox
// without allow-same-origin: the document runs in an opaque origin, cannot
// read the cookie, and its requests to /api/* arrive without it. Chrome will
// not render a PDF in a sandboxed frame; a file served as a PDF must start
// with %PDF-, and nosniff keeps a disguised one from rendering as HTML.

const (
	maxDocs       = 200      // listed per agent (C6)
	maxRenderSize = 10 << 20 // larger documents are listed, not rendered (C6)
	docSandbox    = "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox"
)

//go:embed vendor/marked.js vendor/mermaid.js.gz
var vendorFS embed.FS

// DocEntry is one document in an agent's list.
type DocEntry struct {
	Path       string `json:"path"`   // workspace-relative, slash-separated
	Folder     string `json:"folder"` // the declared folder it is in
	Size       int64  `json:"size"`
	Modified   string `json:"modified"` // RFC 3339, UTC
	Kind       string `json:"kind"`     // markdown | html | pdf | image | text | other
	Renderable bool   `json:"renderable"`
	URL        string `json:"url,omitempty"` // capability path; empty when not renderable
}

// DocList is GET /api/docs.
type DocList struct {
	Agent   string     `json:"agent"`
	Folders []string   `json:"folders"`
	Docs    []DocEntry `json:"docs"`
	More    int        `json:"more"` // documents past the cap, not listed
}

// docKind classifies by extension only; content is never sniffed.
func docKind(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return "markdown"
	case ".html", ".htm", ".svg":
		return "html" // SVG can carry script: it is a document, sandboxed
	case ".pdf":
		return "pdf"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".txt", ".csv", ".json", ".yaml", ".yml", ".log":
		return "text"
	}
	return "other"
}

var docTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".svg": "image/svg+xml",
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8",
}

// ListDocs lists an agent's documents, newest first. Symlinks are never
// followed, a folder that resolves outside the workspace is skipped, and the
// scaffolded README of the default folder is not a document.
func ListDocs(a *mesh.Agent) DocList {
	l := DocList{Agent: a.Name, Folders: a.DocFolders(), Docs: []DocEntry{}}
	for _, folder := range l.Folders {
		root, ok := docRoot(a.Dir(), folder)
		if !ok {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") && p != root {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			if folder == mesh.DocsDir && rel == "README.md" {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			kind := docKind(name)
			l.Docs = append(l.Docs, DocEntry{Path: folder + "/" + filepath.ToSlash(rel), Folder: folder, Size: fi.Size(),
				Modified: fi.ModTime().UTC().Format(time.RFC3339), Kind: kind,
				Renderable: kind != "other" && fi.Size() <= maxRenderSize})
			return nil
		})
	}
	sort.SliceStable(l.Docs, func(i, j int) bool {
		if l.Docs[i].Modified != l.Docs[j].Modified {
			return l.Docs[i].Modified > l.Docs[j].Modified
		}
		return l.Docs[i].Path < l.Docs[j].Path
	})
	if len(l.Docs) > maxDocs {
		l.More = len(l.Docs) - maxDocs
		l.Docs = l.Docs[:maxDocs]
	}
	return l
}

// docRoot resolves a declared folder; false when it is missing or a symlink
// leads it out of the workspace.
func docRoot(agentDir, folder string) (string, bool) {
	ws, err := filepath.EvalSymlinks(agentDir)
	if err != nil {
		return "", false
	}
	root, err := filepath.EvalSymlinks(filepath.Join(agentDir, filepath.FromSlash(folder)))
	if err != nil || !inside(ws, root) {
		return "", false
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return "", false
	}
	return root, true
}

// inside reports whether p is root or below it.
func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// viewFolders are the folders a capability can admit: the documents
// folders, and the knowledge base (shown in the agent page's viewer).
func viewFolders(a *mesh.Agent) []string {
	return append(a.DocFolders(), mesh.KnowledgeDir)
}

// caps mints and checks capabilities; the key lives only in this process.
type caps struct {
	once sync.Once
	key  []byte
}

func (c *caps) mint(pool, agent, folder string) string {
	c.once.Do(func() {
		c.key = make([]byte, 32)
		_, _ = rand.Read(c.key)
	})
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(pool + "\x00" + agent + "\x00" + folder))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func (c *caps) ok(cap, pool, agent, folder string) bool {
	return hmac.Equal([]byte(cap), []byte(c.mint(pool, agent, folder)))
}

// docURL is the capability path of a workspace-relative document path.
func (c *caps) docURL(pool, agent, folder, rel string) string {
	var esc []string
	for _, seg := range strings.Split(rel, "/") {
		esc = append(esc, url.PathEscape(seg))
	}
	return "/c/" + c.mint(pool, agent, folder) + "/" + url.PathEscape(pool) + "/" + url.PathEscape(agent) + "/" + strings.Join(esc, "/")
}

// agentFor finds a peer of the request's pool.
func (s *Server) agentFor(e *Engine, name string) (*mesh.Agent, bool) {
	a, err := mesh.FindAgent(e.Dir(), name)
	return a, err == nil
}

// poolKey is the key a capability is minted for: the hub's key, or "-" for a
// single-engine server.
func (s *Server) poolKey(r *http.Request) string {
	if s.Hub == nil {
		return "-"
	}
	if k := r.URL.Query().Get("pool"); k != "" {
		return k
	}
	return s.Hub.DefaultKey()
}

func (s *Server) engineByKey(key string) (*Engine, bool) {
	if s.Hub == nil {
		return s.Engine, key == "-"
	}
	e, _, err := s.Hub.Engine(key)
	return e, err == nil
}

// docs is GET /api/docs?pool=&agent=.
func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	a, ok := s.agentFor(e, r.URL.Query().Get("agent"))
	if !ok {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	l := ListDocs(a)
	pool := s.poolKey(r)
	for i := range l.Docs {
		if l.Docs[i].Renderable {
			d := l.Docs[i]
			l.Docs[i].URL = s.caps.docURL(pool, a.Name, d.Folder, d.Path)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(l)
}

// capDoc is GET /c/<cap>/<pool>/<agent>/<path>: no token, the capability
// admits one folder of one agent.
func (s *Server) capDoc(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/c/"), "/", 4)
	if len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	cap, pool, agent, rel := parts[0], parts[1], parts[2], path.Clean("/" + parts[3])[1:]
	e, ok := s.engineByKey(pool)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a, ok := s.agentFor(e, agent)
	if !ok {
		http.NotFound(w, r)
		return
	}
	folder := ""
	for _, f := range viewFolders(a) {
		if strings.HasPrefix(rel, f+"/") && s.caps.ok(cap, pool, agent, f) {
			folder = f
			break
		}
	}
	if folder == "" {
		http.NotFound(w, r)
		return
	}
	root, ok := docRoot(a.Dir(), folder)
	if !ok {
		http.NotFound(w, r)
		return
	}
	file, err := filepath.EvalSymlinks(filepath.Join(a.Dir(), filepath.FromSlash(rel)))
	if err != nil || !inside(root, file) {
		http.NotFound(w, r)
		return
	}
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer") // the capability never leaves in a Referer
	h.Set("Cache-Control", "no-store")
	ext := strings.ToLower(path.Ext(rel))
	kind := docKind(rel)
	if fi.Size() > maxRenderSize && kind != "other" {
		http.Error(w, "too large to show here; open it from the workspace", http.StatusRequestEntityTooLarge)
		return
	}
	f, err := os.Open(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if ext == ".pdf" {
		head := make([]byte, 5)
		if n, _ := io.ReadFull(f, head); n == 5 && string(head) == "%PDF-" {
			h.Set("Content-Type", "application/pdf")
			_, _ = f.Seek(0, io.SeekStart)
			http.ServeContent(w, r, "", fi.ModTime(), f)
			return
		}
		h.Set("Content-Security-Policy", docSandbox)
		http.Error(w, "not a PDF", http.StatusUnsupportedMediaType)
		return
	}
	h.Set("Content-Security-Policy", docSandbox)
	switch kind {
	case "markdown":
		src, err := io.ReadAll(f)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(markdownShell(path.Base(rel), src))
		return
	case "text":
		h.Set("Content-Type", "text/plain; charset=utf-8")
	default:
		ct, known := docTypes[ext]
		if !known {
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Content-Disposition", "attachment")
		} else {
			h.Set("Content-Type", ct)
		}
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// markdownShell is the HTML page that renders one markdown document inside
// the sandbox. The source is a JSON string literal (json.Marshal escapes <,
// > and &), so it can never close the script element, and mermaid sees it
// unescaped.
func markdownShell(title string, src []byte) []byte {
	lit, _ := json.Marshal(string(src))
	t, _ := json.Marshal(title)
	var b bytes.Buffer
	b.WriteString(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root { color-scheme: light dark; --text: #1c1c1a; --muted: #6d6c66; --line: #e4e3dd; --code: #f3f2ee; --accent: #2f5fd0; }
  @media (prefers-color-scheme: dark) { :root { --text: #ecebe6; --muted: #a3a29b; --line: #32312d; --code: #232321; --accent: #8aa8f0; } }
  body { margin: 0; padding: 20px 24px 40px; font: 14.5px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; color: var(--text); background: transparent; }
  main { max-width: 860px; margin: 0 auto; }
  h1, h2, h3 { line-height: 1.25; letter-spacing: -.01em; } h1 { font-size: 24px; } h2 { font-size: 19px; border-bottom: 1px solid var(--line); padding-bottom: 4px; } h3 { font-size: 16px; }
  a { color: var(--accent); }
  code { font: 12.5px ui-monospace, SFMono-Regular, Menlo, monospace; background: var(--code); padding: 1px 4px; border-radius: 4px; }
  pre { background: var(--code); padding: 12px 14px; border-radius: 8px; overflow: auto; } pre code { background: none; padding: 0; }
  pre.mermaid { background: none; text-align: center; }
  table { border-collapse: collapse; } th, td { border: 1px solid var(--line); padding: 4px 8px; }
  blockquote { margin: 0; padding: 0 14px; border-left: 3px solid var(--line); color: var(--muted); }
  img { max-width: 100%; }
  .err { color: #b42318; font-size: 12.5px; white-space: pre-wrap; }
</style>
<title></title></head><body><main id="doc"></main>
<script>const SRC = `)
	b.Write(lit)
	b.WriteString(`; document.title = `)
	b.Write(t)
	b.WriteString(`;</script>
<script src="/vendor/marked.js"></script>
<script src="/vendor/mermaid.js"></script>
<script>
"use strict";
const doc = document.getElementById("doc");
doc.innerHTML = marked.parse(SRC);
for (const c of doc.querySelectorAll("pre > code.language-mermaid")) {
  const d = document.createElement("pre");
  d.className = "mermaid";
  d.textContent = c.textContent;
  c.parentElement.replaceWith(d);
}
const dark = matchMedia("(prefers-color-scheme: dark)").matches;
mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: dark ? "dark" : "default" });
for (const d of doc.querySelectorAll("pre.mermaid")) {
  mermaid.run({ nodes: [d] }).catch((e) => {
    const p = document.createElement("div");
    p.className = "err";
    p.textContent = "Diagram error: " + ((e && (e.message || e.str)) || e);
    d.after(p);
  });
}
</script></body></html>
`)
	return b.Bytes()
}

// vendor is GET /vendor/<lib>.js: public library code, served without the
// token so a sandboxed document can load it.
func (s *Server) vendor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/vendor/")
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	switch name {
	case "marked.js":
		b, _ := vendorFS.ReadFile("vendor/marked.js")
		_, _ = w.Write(b)
	case "mermaid.js":
		gz, _ := vendorFS.ReadFile("vendor/mermaid.js.gz")
		h.Add("Vary", "Accept-Encoding")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			_, _ = w.Write(gz)
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.Copy(w, zr)
	default:
		http.NotFound(w, r)
	}
}
