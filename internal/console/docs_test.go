package console

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// docs_test.go — documents listed and served read-only, sandboxed.

func writeDoc(t *testing.T, a *mesh.Agent, rel, body string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(a.Dir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	_ = os.Chtimes(p, when, when)
	return p
}

func TestListDocs_KindsOrderAndSkips(t *testing.T) {
	_, a := testPool(t)
	writeDoc(t, a, "docs/2026-10-01-old.md", "# old", 48*time.Hour)
	writeDoc(t, a, "docs/2026-10-27-design.md", "# new", time.Minute)
	writeDoc(t, a, "docs/report.html", "<p>r</p>", time.Hour)
	writeDoc(t, a, "docs/sub/chart.png", "png", 2*time.Hour)
	writeDoc(t, a, "docs/local/secret.pdf", "%PDF-1.4", 3*time.Hour)
	writeDoc(t, a, "docs/data.xlsx", "x", 4*time.Hour)
	writeDoc(t, a, "docs/.DS_Store", "x", 0)
	_ = os.Symlink("/etc/hosts", filepath.Join(a.Dir(), "docs", "hosts"))
	writeDoc(t, a, "docs/huge.md", strings.Repeat("x", maxRenderSize+1), 5*time.Hour)

	l := ListDocs(a)
	var got []string
	for _, d := range l.Docs {
		got = append(got, d.Path+":"+d.Kind)
	}
	want := "docs/2026-10-27-design.md:markdown docs/report.html:html docs/sub/chart.png:image docs/local/secret.pdf:pdf " +
		"docs/data.xlsx:other docs/huge.md:markdown docs/2026-10-01-old.md:markdown"
	if strings.Join(got, " ") != want {
		t.Fatalf("list:\n got %v\nwant %s", got, want)
	}
	for _, d := range l.Docs {
		if wantR := d.Kind != "other" && d.Path != "docs/huge.md"; d.Renderable != wantR {
			t.Errorf("%s renderable = %v", d.Path, d.Renderable)
		}
	}
}

func TestListDocs_DeclaredFoldersAndCap(t *testing.T) {
	_, a := testPool(t)
	a.Docs = []string{"reports", "gone"}
	for i := 0; i < maxDocs+3; i++ {
		writeDoc(t, a, "reports/r"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+".md", "x", time.Duration(i)*time.Minute)
	}
	l := ListDocs(a)
	if strings.Join(l.Folders, ",") != "reports,gone" || len(l.Docs)+l.More == 0 || len(l.Docs) > maxDocs {
		t.Fatalf("folders %v, %d listed, %d more", l.Folders, len(l.Docs), l.More)
	}
}

// docServer serves one pool with a document set, and a GET helper.
func docServer(t *testing.T) (*Server, *mesh.Agent, func(target string, auth bool) *httptest.ResponseRecorder) {
	t.Helper()
	dir, a := testPool(t)
	e := NewEngine(dir, "test")
	s := &Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}
	h := s.Handler()
	return s, a, func(target string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", target, nil)
		r.Host = "127.0.0.1:7717"
		r.Header.Set("Accept-Encoding", "gzip")
		if auth {
			r.Header.Set("Authorization", "Bearer secret")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}

func listed(t *testing.T, get func(string, bool) *httptest.ResponseRecorder) map[string]DocEntry {
	t.Helper()
	w := get("/api/docs?agent=architect", true)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/docs: %d %s", w.Code, w.Body)
	}
	var l DocList
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	out := map[string]DocEntry{}
	for _, d := range l.Docs {
		out[d.Path] = d
	}
	return out
}

func TestDocs_ServedSandboxedFromACapability(t *testing.T) {
	_, a, get := docServer(t)
	writeDoc(t, a, "docs/design.md", "# Design\n\n```mermaid\nflowchart LR\n  A --> B\n```\n\n</script><script>alert(1)</script>\n", 0)
	writeDoc(t, a, "docs/report.html", `<img src="img/a.png">`, 0)
	writeDoc(t, a, "docs/img/a.png", "png", 0)
	writeDoc(t, a, "docs/real.pdf", "%PDF-1.4 body", 0)
	writeDoc(t, a, "docs/fake.pdf", "<html><script>x</script>", 0)
	writeDoc(t, a, "knowledge/state.md", "# state", 0)

	if w := get("/api/docs?agent=architect", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("the list needs the token: %d", w.Code)
	}
	docs := listed(t, get)
	md := docs["docs/design.md"]
	if !strings.HasPrefix(md.URL, "/c/") {
		t.Fatalf("renderable documents carry a capability path: %+v", md)
	}

	// No token needed; sandboxed; the source cannot close the script element.
	w := get(md.URL, false)
	if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != docSandbox ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("markdown: %d %v", w.Code, w.Header())
	}
	body := w.Body.String()
	if strings.Contains(body, "</script><script>alert(1)") || !strings.Contains(body, `</script>`) ||
		!strings.Contains(body, "flowchart LR\\n  A --\\u003e B") || !strings.Contains(body, `src="/vendor/mermaid.js"`) {
		t.Fatalf("markdown shell:\n%s", body)
	}

	// A relative asset of an HTML document loads under the same capability.
	html := docs["docs/report.html"].URL
	asset := strings.TrimSuffix(html, "report.html") + "img/a.png"
	if w := get(asset, false); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" ||
		w.Header().Get("Content-Security-Policy") != docSandbox {
		t.Errorf("relative asset: %d %v", w.Code, w.Header())
	}

	// A real PDF renders unsandboxed; a disguised one never renders.
	if w := get(docs["docs/real.pdf"].URL, false); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/pdf" ||
		w.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("real pdf: %d %v", w.Code, w.Header())
	}
	if w := get(docs["docs/fake.pdf"].URL, false); w.Code != http.StatusUnsupportedMediaType || strings.Contains(w.Body.String(), "<script>") {
		t.Errorf("fake pdf: %d %s", w.Code, w.Body)
	}

	// The capability admits its folder only.
	capPrefix := strings.TrimSuffix(md.URL, "docs/design.md")
	for _, target := range []string{
		capPrefix + "knowledge/state.md",                                // another folder
		capPrefix + "docs/../knowledge/state.md",                        // traversal (the mux cleans it to the line above)
		capPrefix + "agent.yaml",                                        // the workspace itself
		"/c/" + strings.Repeat("0", 32) + "/-/architect/docs/design.md", // a forged capability
		strings.Replace(md.URL, "/architect/", "/ghost/", 1),            // another agent
	} {
		if w := get(target, false); w.Code == http.StatusOK {
			t.Errorf("%s must not be served", target)
		}
	}
	// A symlink inside the folder that leads out of it is refused.
	_ = os.Symlink(filepath.Join(a.Dir(), "agent.yaml"), filepath.Join(a.Dir(), "docs", "leak.md"))
	if w := get(capPrefix+"docs/leak.md", false); w.Code == http.StatusOK {
		t.Errorf("symlink out of the folder served: %s", w.Body)
	}
	// Still guarded by the Host check.
	r := httptest.NewRequest("GET", md.URL, nil)
	r.Host = "evil.example:7717"
	rec := httptest.NewRecorder()
	(&Server{Engine: NewEngine(filepath.Dir(a.Dir()), "t"), Settings: Settings{Port: 7717, Token: "secret"}}).Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("foreign host: %d", rec.Code)
	}
}

func TestDocs_CapabilitiesDieWithTheProcess(t *testing.T) {
	_, a, get := docServer(t)
	writeDoc(t, a, "docs/d.md", "# d", 0)
	url := listed(t, get)["docs/d.md"].URL
	_, _, get2 := docServer(t) // a restarted console: a new key
	if w := get2(url, false); w.Code == http.StatusOK {
		t.Errorf("a capability from another console process must not work")
	}
}

func TestVendor_LibrariesWithoutToken(t *testing.T) {
	_, _, get := docServer(t)
	w := get("/vendor/mermaid.js", false)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("mermaid: %d %v", w.Code, w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 64)
	_, _ = io.ReadFull(zr, head)
	if !strings.Contains(string(head), "mermaid") {
		t.Errorf("mermaid body: %q", head)
	}
	if w := get("/vendor/marked.js", false); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "marked v18.1.1") {
		t.Errorf("marked: %d", w.Code)
	}
	if w := get("/vendor/../api/snapshot", false); w.Code == http.StatusOK {
		t.Errorf("the vendor path must not reach the API")
	}
}
