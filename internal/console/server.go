package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// DefaultPort is the console's fixed port (decision C9: the login item and a
// pinned browser window both need a stable address).
const DefaultPort = 7717

// cookieName carries the token after the first visit, so the page's own
// requests need no token in the URL.
const cookieName = "meshctl_console"

// Settings are the console's per-user settings: where it listens and the
// secret every request must carry. They live in the user's config
// directory, never in a pool.
type Settings struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// SettingsPath is <user config dir>/meshctl/console.json.
func SettingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "meshctl", "console.json"), nil
}

// ReadSettings reads the settings without creating them; the default port
// and no token when there are none yet.
func ReadSettings() Settings {
	s := Settings{Port: DefaultPort}
	if p, err := SettingsPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &s)
		}
	}
	if s.Port == 0 {
		s.Port = DefaultPort
	}
	return s
}

// LoadSettings reads the settings, creating them (a fresh random token, the
// default port) on first use. The file is readable by the user only.
func LoadSettings() (Settings, error) {
	p, err := SettingsPath()
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &s) == nil && s.Token != "" {
		if s.Port == 0 {
			s.Port = DefaultPort
		}
		return s, nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return Settings{}, err
	}
	s = Settings{Port: DefaultPort, Token: hex.EncodeToString(buf)}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return Settings{}, err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// URL is the address to open, token included: the first visit trades it
// for a cookie and drops it from the address bar.
func (s Settings) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.Port, s.Token)
}

// Server serves the hub's pools, or one engine's when Hub is nil. Every
// request must name this console in its Host header (a website that
// resolves its own name to 127.0.0.1 cannot) and carry the token; a request
// that changes something (the re-check) must also come from the console's
// own page. With a hub, ?pool=<key> picks the pool; none means the default.
type Server struct {
	Hub      *Hub
	Engine   *Engine
	Settings Settings
	UI       http.Handler // the page (ticket 04); nil serves a placeholder
}

// engine is the request's pool's engine; false (and a 404 written) when
// ?pool= names no pool served.
func (s *Server) engine(w http.ResponseWriter, r *http.Request) (*Engine, bool) {
	if s.Hub == nil {
		return s.Engine, true
	}
	e, _, err := s.Hub.Engine(r.URL.Query().Get("pool"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return nil, false
	}
	return e, true
}

// Handler is the console's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/item", s.item)
	mux.HandleFunc("POST /api/doctor/recheck", s.recheck)
	mux.HandleFunc("POST /api/focus", s.focus)
	ui := s.UI
	if ui == nil {
		ui = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "meshctl console: the view is not built yet; see /api/snapshot")
		})
	}
	mux.Handle("GET /", ui)
	return s.guard(mux)
}

func (s *Server) allowedHosts() map[string]bool {
	p := strconv.Itoa(s.Settings.Port)
	return map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true}
}

// guard enforces the host check and the token on every request.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHosts()[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if q := r.URL.Query().Get("token"); q != "" {
			if !s.tokenOK(q) {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
			// Persistent, so a pinned console window that is closed and
			// reopened at the plain address stays signed in.
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.Settings.Token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteStrictMode, MaxAge: 400 * 24 * 3600})
			if r.Method == http.MethodGet && r.URL.Path == "/" {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		} else if !s.authorized(r) {
			http.Error(w, "missing or bad token: open the URL `meshctl console` prints", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !s.allowedOrigin(o) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedOrigin(o string) bool {
	for h := range s.allowedHosts() {
		if o == "http://"+h {
			return true
		}
	}
	return false
}

func (s *Server) tokenOK(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.Settings.Token)) == 1
}

func (s *Server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && s.tokenOK(c.Value) {
		return true
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " && s.tokenOK(h[7:]) {
		return true
	}
	return false
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	var v any
	if s.Hub != nil {
		f, err := s.Hub.Frame(r.URL.Query().Get("pool"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		v = f
	} else {
		v = s.Engine.Snapshot()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// events streams snapshots as server-sent events: the current one at once,
// then one per change, with a comment every 25 s to keep the connection open.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var (
		ch     <-chan []byte
		cancel func()
	)
	if s.Hub != nil {
		var err error
		if ch, cancel, err = s.Hub.Subscribe(r.URL.Query().Get("pool")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	} else {
		ch, cancel = s.Engine.Subscribe()
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	keep := time.NewTicker(25 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
			fl.Flush()
		case <-keep.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) recheck(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	e.Recheck()
	w.WriteHeader(http.StatusAccepted)
}

// ListenAndServe serves on 127.0.0.1 only, never another interface.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Settings.Port)))
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) {
			return fmt.Errorf("port %d is in use — is a console already running? (%w)", s.Settings.Port, err)
		}
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}
