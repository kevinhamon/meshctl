package console

import (
	"encoding/json"
	"net/http"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// settings.go — POST /api/settings: the Settings page saves how the console
// looks into the pool repo (pool/console.yaml, committed, never pushed;
// ADR-0079). The console's second change to a pool, limited to that file.

// SettingsRequest is the body: the settings, and whether to save them in
// every pool the console serves instead of the one named by ?pool=.
type SettingsRequest struct {
	mesh.ConsoleSettings
	AllPools bool `json:"all_pools"`
}

// SettingsSaved is one pool's outcome.
type SettingsSaved struct {
	Pool      string `json:"pool"`
	Committed bool   `json:"committed"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var req SettingsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := req.ConsoleSettings.Valid(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var targets []*Engine
	var names []string
	if req.AllPools && s.Hub != nil {
		for _, p := range s.Hub.Pools() {
			if e, _, err := s.Hub.Engine(p.Key); err == nil {
				targets, names = append(targets, e), append(names, p.Name)
			}
		}
	} else {
		e, ok := s.engine(w, r)
		if !ok {
			return
		}
		targets, names = []*Engine{e}, []string{s.poolKey(r)}
	}
	out := []SettingsSaved{}
	for i, e := range targets {
		committed, err := mesh.WriteConsoleSettings(e.Dir(), req.ConsoleSettings)
		res := SettingsSaved{Pool: names[i], Committed: committed}
		if err != nil {
			res.Error = err.Error()
		}
		out = append(out, res)
		e.RefreshPending()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
