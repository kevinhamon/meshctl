package console

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// push.go — the Push button on the pool repo's "ahead of its remote" finding:
// POST /api/pool/push/check lists the commits that would
// go; POST /api/pool/push pushes them, after the person confirmed. Only the
// pool repo, only its tracked branch, never forced, never waiting for a
// password.

// PushResult is the push endpoint's answer. Outcome: pushed, rejected (the
// remote moved on), auth (no credentials without a prompt) or failed.
type PushResult struct {
	Outcome string `json:"outcome"`
	Output  string `json:"output"`
	Command string `json:"command"` // what the person runs in a terminal instead
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) pushCheck(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	writeJSON(w, mesh.PoolPushPreview(e.Dir()))
}

func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	if !e.pushing.TryLock() {
		http.Error(w, "a push is already running for this pool", http.StatusConflict)
		return
	}
	defer e.pushing.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	outcome, out, err := mesh.PoolPush(ctx, e.Dir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	e.RefreshDoctor()
	writeJSON(w, PushResult{Outcome: outcome, Output: out, Command: "git -C " + mesh.PoolDir(e.Dir()) + " push"})
}

// An agent repo's Commit and Push buttons, on doctor's
// "uncommitted path(s)" and "commit(s) ahead" findings. Both check endpoints
// answer the repo's state; commit takes the person's message and the status
// they saw, and refuses when the tree changed since.

// agentRepo resolves ?agent= to the agent's workspace.
func (s *Server) agentRepo(w http.ResponseWriter, r *http.Request) (*Engine, string, bool) {
	e, ok := s.engine(w, r)
	if !ok {
		return nil, "", false
	}
	a, ok := s.agentFor(e, r.URL.Query().Get("agent"))
	if !ok {
		http.Error(w, "no such agent", http.StatusNotFound)
		return nil, "", false
	}
	return e, a.Dir(), true
}

func (s *Server) agentRepoCheck(w http.ResponseWriter, r *http.Request) {
	if e, dir, ok := s.agentRepo(w, r); ok {
		st := mesh.AgentRepoSync(dir)
		if st.Action == mesh.ActionCommit {
			st.Suggested = mesh.SuggestCommitMessage(dir, e.version)
		}
		writeJSON(w, st)
	}
}

// CommitRequest is the commit endpoint's body.
type CommitRequest struct {
	Message string   `json:"message"`
	Seen    []string `json:"seen"` // the status lines the person confirmed
}

// CommitResult is the commit endpoint's answer: the repo's state after, so the
// page can offer the push next.
type CommitResult struct {
	Output string              `json:"output"`
	After  mesh.AgentRepoState `json:"after"`
}

func (s *Server) agentCommit(w http.ResponseWriter, r *http.Request) {
	e, dir, ok := s.agentRepo(w, r)
	if !ok {
		return
	}
	var req CommitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	if !e.repoGit.TryLock() {
		http.Error(w, "a commit or push is already running for this pool", http.StatusConflict)
		return
	}
	defer e.repoGit.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	out, err := mesh.AgentRepoCommit(ctx, dir, req.Message, req.Seen)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	e.RefreshDoctor()
	writeJSON(w, CommitResult{Output: out, After: mesh.AgentRepoSync(dir)})
}

func (s *Server) agentPush(w http.ResponseWriter, r *http.Request) {
	e, dir, ok := s.agentRepo(w, r)
	if !ok {
		return
	}
	if !e.repoGit.TryLock() {
		http.Error(w, "a commit or push is already running for this pool", http.StatusConflict)
		return
	}
	defer e.repoGit.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	outcome, out, err := mesh.AgentRepoPush(ctx, dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	e.RefreshDoctor()
	writeJSON(w, PushResult{Outcome: outcome, Output: out, Command: "git -C " + dir + " push"})
}
