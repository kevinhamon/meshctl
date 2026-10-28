package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// upgrade.go — the console's Upgrade button (console layout spec C30–C33,
// ADR-0079): POST /api/pool/upgrade/check shows what `meshctl pool upgrade
// --check` would change; POST /api/pool/upgrade runs `meshctl pool upgrade`
// for that pool, after the person confirmed the preview. Both run the console's
// own binary, so the button and the command never differ; nothing is forced
// and nothing is pushed.

// Runner runs the installed meshctl with args for the pool at dir and
// returns its output and exit code; tests replace it.
type Runner func(ctx context.Context, dir string, args ...string) (output string, exit int, err error)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// runSelf runs this binary in the pool, as a person would from its root.
func runSelf(ctx context.Context, dir string, args ...string) (string, int, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", -1, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AGENTS_DIR="+dir, "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	exit := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exit, err = ee.ExitCode(), nil
	}
	return ansi.ReplaceAllString(string(out), ""), exit, err
}

func (s *Server) runner() Runner {
	if s.Run != nil {
		return s.Run
	}
	return runSelf
}

// UpgradeResult is either endpoint's answer. Outcome (after a run):
// "upgraded", "needs-person" (upgraded, with items only a person can do,
// such as merging locally edited doctrine), or "failed".
type UpgradeResult struct {
	Output  string `json:"output"`
	Exit    int    `json:"exit"`
	Pending bool   `json:"pending,omitempty"` // the check found something to upgrade
	Outcome string `json:"outcome,omitempty"`
}

func writeUpgrade(w http.ResponseWriter, res UpgradeResult) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) upgradeCheck(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	out, exit, err := s.runner()(ctx, e.Dir(), "pool", "upgrade", "--check")
	if err != nil {
		http.Error(w, "could not run meshctl: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeUpgrade(w, UpgradeResult{Output: out, Exit: exit, Pending: strings.Contains(out, "upgrade pending")})
}

func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	if !e.upgrading.TryLock() {
		http.Error(w, "an upgrade is already running for this pool", http.StatusConflict)
		return
	}
	defer e.upgrading.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, exit, err := s.runner()(ctx, e.Dir(), "pool", "upgrade")
	if err != nil {
		http.Error(w, "could not run meshctl: "+err.Error(), http.StatusInternalServerError)
		return
	}
	e.RefreshPending()
	e.Recheck()
	res := UpgradeResult{Output: out, Exit: exit, Outcome: "failed"}
	switch {
	case exit == 0:
		res.Outcome = "upgraded"
	case strings.Contains(out, "the pool is upgraded") || strings.Contains(out, "has local edits"):
		res.Outcome = "needs-person"
	}
	writeUpgrade(w, res)
}
