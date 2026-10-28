package console

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// testPool scaffolds a pool with one peer.
func testPool(t *testing.T) (dir string, a *mesh.Agent) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	dir = filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(filepath.Join(dir, "agent-mesh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-mesh", mesh.CommsFile), []byte("protocol"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := mesh.AgentSpec{Name: "architect", Title: "Architect", Role: "role", Dispatchable: true,
		Accepts: []mesh.Accept{{Type: "guidance", Desc: "advice"}}, Badge: mesh.Badge{Label: "A"}, Bare: true}
	if _, err := mesh.New(dir, spec, mesh.ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	a, err := mesh.FindAgent(dir, "architect")
	if err != nil {
		t.Fatal(err)
	}
	return dir, a
}

func intakeCount(s Snapshot, agent string) int {
	for _, a := range s.Pending.Agents {
		if a.Agent != agent {
			continue
		}
		for _, l := range a.Lines {
			if l.Kind == "intake" {
				return l.Count
			}
		}
	}
	return 0
}

func TestEngine_ChangeMovesTheSnapshot(t *testing.T) {
	dir, a := testPool(t)
	e := NewEngine(dir, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()
	ch, stop := e.Subscribe()
	defer stop()

	deadline := time.After(10 * time.Second)
	wrote := false
	for {
		select {
		case b := <-ch:
			var s Snapshot
			if err := json.Unmarshal(b, &s); err != nil {
				t.Fatal(err)
			}
			if s.Schema != mesh.ReportSchema || s.Pool != dir {
				continue // the empty first frame, before Run's first compute
			}
			if !wrote {
				if intakeCount(s, "architect") != 0 {
					t.Fatalf("fresh pool has intake: %+v", s.Pending)
				}
				if _, err := mesh.WriteRequest(a, "guidance", "Split it?", "user", "P2", nil, time.Now()); err != nil {
					t.Fatal(err)
				}
				wrote = true
				continue
			}
			if intakeCount(s, "architect") == 1 {
				return // the watcher saw the new request without waiting for the tick
			}
		case <-deadline:
			b, _ := json.Marshal(e.Snapshot())
			t.Fatalf("no snapshot with the new request within 10s; engine holds: %s", b)
		}
	}
}

func TestWatchPaths_StateDirsOnly(t *testing.T) {
	dir, a := testPool(t)
	paths := strings.Join(WatchPaths(dir), "\n")
	for _, want := range []string{a.InboxPath(), filepath.Join(a.Dir(), mesh.KnowledgeDir)} {
		if !strings.Contains(paths, want) {
			t.Errorf("watch set lacks %s:\n%s", want, paths)
		}
	}
	if strings.Contains(paths, filepath.Join(mesh.KnowledgeDir, "playbooks")) {
		t.Error("knowledge sub-folders are not watched (one descriptor per file on macOS)")
	}
}

func TestRelevant_IgnoresGitChurn(t *testing.T) {
	for p, want := range map[string]bool{
		"/p/a/.git/index":              false,
		"/p/a/.git/index.lock":         false,
		"/p/a/.git/HEAD":               true,
		"/p/a/.git/packed-refs":        true,
		"/p/a/.git/refs/heads/mesh/kb": true,
		"/p/a/intake/x.md":             true,
		"/p/a/intake/x.md.tmp":         false,
	} {
		if got := Relevant(p); got != want {
			t.Errorf("Relevant(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestServer_GuardsHostTokenAndOrigin(t *testing.T) {
	dir, _ := testPool(t)
	e := NewEngine(dir, "test")
	e.RefreshPending()
	s := &Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}
	h := s.Handler()
	do := func(method, target, host string, set func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.Host = host
		if set != nil {
			set(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") }

	if w := do("GET", "/api/snapshot", "evil.example:7717", bearer); w.Code != http.StatusForbidden {
		t.Errorf("foreign host: %d", w.Code)
	}
	if w := do("GET", "/api/snapshot", "127.0.0.1:7717", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := do("GET", "/api/snapshot", "127.0.0.1:7717", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }); w.Code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", w.Code)
	}
	w := do("GET", "/api/snapshot", "localhost:7717", bearer)
	if w.Code != http.StatusOK {
		t.Fatalf("good request: %d %s", w.Code, w.Body)
	}
	var snap Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil || len(snap.Pending.Agents) != 1 {
		t.Fatalf("snapshot: %v %+v", err, snap)
	}
	// First visit with ?token= sets the cookie and drops the token from the URL.
	w = do("GET", "/?token=secret", "127.0.0.1:7717", nil)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Set-Cookie"), cookieName+"=secret") ||
		!strings.Contains(w.Header().Get("Set-Cookie"), "HttpOnly") || !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=") {
		t.Errorf("token exchange: %d %v", w.Code, w.Header())
	}
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: "secret"}) }
	if w := do("GET", "/api/snapshot", "127.0.0.1:7717", cookie); w.Code != http.StatusOK {
		t.Errorf("cookie: %d", w.Code)
	}
	// A state-changing request from another origin is refused even with the cookie.
	if w := do("POST", "/api/doctor/recheck", "127.0.0.1:7717", func(r *http.Request) { cookie(r); r.Header.Set("Origin", "https://evil.example") }); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin recheck: %d", w.Code)
	}
	if w := do("POST", "/api/doctor/recheck", "127.0.0.1:7717", func(r *http.Request) { cookie(r); r.Header.Set("Origin", "http://127.0.0.1:7717") }); w.Code != http.StatusAccepted {
		t.Errorf("same-origin recheck: %d", w.Code)
	}
}

func TestServer_EventsStreamTheSnapshot(t *testing.T) {
	dir, _ := testPool(t)
	e := NewEngine(dir, "test")
	e.RefreshPending()
	s := &Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/api/events", nil)
	req.Host = "127.0.0.1:7717"
	req.Header.Set("Authorization", "Bearer secret")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	var event string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			var snap Snapshot
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snap); err != nil {
				t.Fatal(err)
			}
			if event != "snapshot" || len(snap.Pending.Agents) != 1 {
				t.Fatalf("first event: %s %+v", event, snap)
			}
			return
		}
	}
	t.Fatal("no event received")
}

func TestSettings_CreatedOncePrivate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	s1, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := LoadSettings()
	if s1.Token == "" || s1.Token != s2.Token || s1.Port != DefaultPort {
		t.Fatalf("settings: %+v %+v", s1, s2)
	}
	p, _ := SettingsPath()
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode: %v %v", fi, err)
	}
	if !strings.Contains(s1.URL(), "127.0.0.1:7717/?token="+s1.Token) {
		t.Errorf("url: %s", s1.URL())
	}
}

func TestItemFor_RequestAndUnknown(t *testing.T) {
	dir, a := testPool(t)
	r, err := mesh.WriteRequest(a, "guidance", "Split the service?", "user", "P2", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	it, err := ItemFor(dir, "architect", "intake", r.ID, time.Now())
	if err != nil || !strings.Contains(it.Text, "Split the service?") || it.Path != r.Path() {
		t.Fatalf("intake item: %+v %v", it, err)
	}
	for _, q := range [][3]string{{"architect", "intake", "nope"}, {"architect", "conflicts", r.ID}, {"ghost", "intake", r.ID}} {
		if _, err := ItemFor(dir, q[0], q[1], q[2], time.Now()); err == nil {
			t.Errorf("ItemFor%v must fail", q)
		}
	}
	// Through the server: guarded like everything else.
	e := NewEngine(dir, "test")
	s := &Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}
	req := httptest.NewRequest("GET", "/api/item?agent=architect&kind=intake&id="+r.ID, nil)
	req.Host = "127.0.0.1:7717"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("item without token: %d", w.Code)
	}
}

func TestUI_ServedAndTextOnly(t *testing.T) {
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	// Pool data is set with textContent; innerHTML would let a request's text run as markup.
	if strings.Contains(page, "innerHTML") || strings.Contains(page, "insertAdjacentHTML") || strings.Contains(page, "document.write") {
		t.Error("the page must not insert pool data as HTML")
	}
	if strings.Contains(page, "<script src=") || strings.Contains(page, "https://") {
		t.Error("the page must not load anything from the network")
	}
}

func TestEngine_UpgradeNotice(t *testing.T) {
	dir, _ := testPool(t)
	if err := mesh.WriteUpgradeStamp(dir, "v1.7.2", time.Now()); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(dir, "v1.8.0")
	e.RefreshPending()
	ns := e.Snapshot().Notices
	if len(ns) != 1 || ns[0].Kind != "upgrade" || !strings.Contains(ns[0].Message, "v1.7.2") {
		t.Fatalf("notices: %+v", ns)
	}
	e2 := NewEngine(dir, "v1.7.2")
	e2.RefreshPending()
	if ns := e2.Snapshot().Notices; len(ns) != 0 {
		t.Fatalf("current pool ⇒ no notice: %+v", ns)
	}
}

func TestServer_DoctorFixRepairsFixableItems(t *testing.T) {
	dir, a := testPool(t)
	retired := filepath.Join(a.Dir(), mesh.MemoryDir, mesh.MemoryIndexFile)
	_ = os.MkdirAll(filepath.Dir(retired), 0o755)
	if err := os.WriteFile(retired, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(dir, "test")
	e.RefreshDoctor()
	fixable := func() int {
		n := 0
		for _, f := range e.Snapshot().Doctor.Findings {
			if f.Fixable {
				n++
			}
		}
		return n
	}
	if fixable() == 0 {
		t.Fatal("the retired index should be a fixable finding")
	}
	h := (&Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}).Handler()
	post := func(token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/doctor/fix", nil)
		r.Host = "127.0.0.1:7717"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post("", "http://127.0.0.1:7717"); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := post("secret", "https://evil.example"); w.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", w.Code)
	}
	e.fixing.Lock()
	if w := post("secret", "http://127.0.0.1:7717"); w.Code != http.StatusConflict {
		t.Errorf("a second run while one runs: %d", w.Code)
	}
	e.fixing.Unlock()
	w := post("secret", "http://127.0.0.1:7717")
	var res FixResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || len(res.Fixed) == 0 || res.Failed == nil {
		t.Fatalf("fix: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Errorf("retired index still there: %v", err)
	}
	if n := fixable(); n != 0 {
		t.Errorf("%d fixable findings after the repair", n)
	}
}

func TestServer_PoolPushGuards(t *testing.T) {
	dir, _ := testPool(t)
	e := NewEngine(dir, "test")
	h := (&Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}).Handler()
	post := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, nil)
		r.Host = "127.0.0.1:7717"
		r.Header.Set("Origin", "http://127.0.0.1:7717")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(mesh.ConsolePoolPushPath, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	var st mesh.PoolPushState
	if w := post(mesh.ConsolePoolPushPath+"/check", "secret"); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.Ready {
		t.Errorf("check on a pool with nothing to push: %d %s", w.Code, w.Body)
	}
	if w := post(mesh.ConsolePoolPushPath, "secret"); w.Code != http.StatusConflict {
		t.Errorf("nothing to push must refuse: %d %s", w.Code, w.Body)
	}
	e.pushing.Lock()
	if w := post(mesh.ConsolePoolPushPath, "secret"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already running") {
		t.Errorf("a second push while one runs: %d %s", w.Code, w.Body)
	}
	e.pushing.Unlock()
}

func TestServer_AgentRepoGuards(t *testing.T) {
	dir, _ := testPool(t)
	e := NewEngine(dir, "test")
	h := (&Server{Engine: e, Settings: Settings{Port: 7717, Token: "secret"}}).Handler()
	post := func(path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Host = "127.0.0.1:7717"
		r.Header.Set("Origin", "http://127.0.0.1:7717")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(mesh.ConsoleAgentCommitPath+"?agent=architect", "", "{}"); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := post(mesh.ConsoleAgentPushPath+"/check?agent=nobody", "secret", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent: %d", w.Code)
	}
	var st mesh.AgentRepoState
	if w := post(mesh.ConsoleAgentCommitPath+"/check?agent=architect", "secret", ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.Action != "" {
		t.Errorf("check on a repo with no remote: %d %s", w.Code, w.Body)
	}
	if w := post(mesh.ConsoleAgentCommitPath+"?agent=architect", "secret", `{"message":"m","seen":[]}`); w.Code != http.StatusConflict {
		t.Errorf("nothing to commit must refuse: %d %s", w.Code, w.Body)
	}
	if w := post(mesh.ConsoleAgentPushPath+"?agent=architect", "secret", ""); w.Code != http.StatusConflict {
		t.Errorf("nothing to push must refuse: %d %s", w.Code, w.Body)
	}
	e.repoGit.Lock()
	if w := post(mesh.ConsoleAgentPushPath+"?agent=architect", "secret", ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already running") {
		t.Errorf("a second git action while one runs: %d %s", w.Code, w.Body)
	}
	e.repoGit.Unlock()
}
