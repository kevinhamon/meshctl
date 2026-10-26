package console

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

func TestSessions_ReportOpenSessionsAndJobs(t *testing.T) {
	dir, a := testPool(t)
	now := time.Now()
	jobs := []mesh.SessionJob{{ID: "j1", Schedule: "*/10 * * * *", Recurring: true, Prompt: "check the PRs"}}
	mesh.HookSessionBeat(dir, mesh.HookPayload{Cwd: a.Dir(), SessionID: "sess-1", SessionCrons: &jobs}, now)
	got := Sessions(dir, now)
	if len(got) != 1 || got[0].Agent != "architect" || got[0].ID != "sess-1" || len(got[0].Jobs) != 1 ||
		got[0].Jobs[0].Prompt != "check the PRs" || got[0].JobsAt == "" {
		t.Fatalf("sessions: %+v", got)
	}
	e := NewEngine(dir, "test")
	e.RefreshPending()
	if s := e.Snapshot().Sessions; len(s) != 1 {
		t.Fatalf("snapshot sessions: %+v", s)
	}
	_ = os.RemoveAll(a.Dir() + "/" + mesh.SessionsDir)
	if got := Sessions(dir, now); len(got) != 0 {
		t.Fatalf("no presence ⇒ no sessions: %+v", got)
	}
}

func TestFocus_ScriptOutcomes(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if err := FocusTTY("/dev/ttys001"); err != errFocusUnsupported {
			t.Fatalf("non-macOS: %v", err)
		}
		return
	}
	old := Osascript
	defer func() { Osascript = old }()
	var gotArgs []string
	reply := "ok"
	Osascript = func(script string, args ...string) (string, error) { gotArgs = args; return reply, nil }
	if err := FocusTTY("/dev/ttys010"); err != nil || len(gotArgs) != 1 || gotArgs[0] != "/dev/ttys010" {
		t.Fatalf("ok: %v %v", err, gotArgs)
	}
	reply = "not-found"
	if err := FocusTTY("/dev/ttys010"); err != errTTYNotFound {
		t.Fatalf("not found: %v", err)
	}
	reply = "no-iterm"
	if err := FocusTTY("/dev/ttys010"); err != errNoITerm {
		t.Fatalf("no iTerm2: %v", err)
	}
	gotArgs = nil
	if err := FocusTTY(`"; do shell script "x`); err != errTTYNotFound || gotArgs != nil {
		t.Fatalf("a non-tty must never reach osascript: %v %v", err, gotArgs)
	}
}

func TestServer_FocusEndpoint(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("focus is macOS-only")
	}
	dir, a := testPool(t)
	mesh.HookSessionBeat(dir, mesh.HookPayload{Cwd: a.Dir(), SessionID: "sess-1"}, time.Now())
	old := Osascript
	defer func() { Osascript = old }()
	Osascript = func(string, ...string) (string, error) { return "ok", nil }
	s := &Server{Engine: NewEngine(dir, "test"), Settings: Settings{Port: 7717, Token: "secret"}}
	post := func(body, origin string) int {
		r := httptest.NewRequest("POST", "/api/focus", bytes.NewBufferString(body))
		r.Host = "127.0.0.1:7717"
		r.Header.Set("Authorization", "Bearer secret")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	sessions := Sessions(dir, time.Now())
	if len(sessions) != 1 {
		t.Fatalf("fixture: %+v", sessions)
	}
	want := http.StatusNoContent
	if sessions[0].TTY == "" {
		want = http.StatusConflict // the test runs without a terminal: nothing to focus
	}
	if code := post(`{"agent":"architect","session":"sess-1"}`, "http://127.0.0.1:7717"); code != want {
		t.Errorf("focus: %d, want %d", code, want)
	}
	if code := post(`{"agent":"architect","session":"nope"}`, ""); code != http.StatusNotFound {
		t.Errorf("unknown session: %d", code)
	}
	if code := post(`{"agent":"architect","session":"sess-1"}`, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-origin focus: %d", code)
	}
}
