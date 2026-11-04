package mesh

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// session_store_test.go — presence lives in the pool, one file per session id
// naming its agent. A session id belongs to one agent only.

func TestSessionStore_RecordsLiveInThePool(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	now := time.Now()
	if _, err := SessionStart(a, "s1", 1, now); err != nil {
		t.Fatal(err)
	}
	s, err := readSession(filepath.Join(SessionStorePath(dir), "s1"))
	if err != nil || s.Agent != "architect" {
		t.Fatalf("record not in the pool store with its agent: %+v %v", s, err)
	}
	if dirExists(filepath.Join(a.Dir(), SessionsDir)) {
		t.Fatal("presence written into the agent's workspace")
	}
}

// The bug this store fixes: opencode's shared service registered one session
// under every agent. A second agent can neither take, beat nor end it.
func TestSessionStore_OneSessionOneAgent(t *testing.T) {
	dir := meshRoot(t)
	owner := newTestAgent(t, dir, "coursework", false, nil)
	other := newTestAgent(t, dir, "reviewer", false, nil)
	now := time.Now()
	if _, err := SessionStart(owner, "ses_1", 1, now); err != nil {
		t.Fatal(err)
	}
	warn, err := SessionStart(other, "ses_1", 2, now)
	if err != nil || !strings.Contains(warn, `already registered to agent "coursework"`) {
		t.Fatalf("second agent's start: %q %v", warn, err)
	}
	if err := TouchSession(other, "ses_1", 2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := SessionEnd(other, "ses_1"); err != nil {
		t.Fatal(err)
	}
	s, err := readSession(filepath.Join(SessionStorePath(dir), "ses_1"))
	if err != nil || s.Agent != "coursework" || s.LastBeat != now.Unix() {
		t.Fatalf("another agent changed the record: %+v %v", s, err)
	}
	if n := len(liveSessions(other.Dir(), now)); n != 0 {
		t.Fatalf("the session counts as live under the other agent: %d", n)
	}
	if n := len(liveSessions(owner.Dir(), now)); n != 1 {
		t.Fatalf("owner's live sessions: %d", n)
	}
	if p := planAgent(other, now); strings.Contains(strings.Join(p.Warnings, "\n"), "live session") {
		t.Fatalf("upgrade plan warns about another agent's session: %v", p.Warnings)
	}
}

// The per-agent folder of earlier versions goes once nothing in it is live.
func TestSessionStore_LegacyFolderCleanedWhenQuiet(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", false, nil)
	now := time.Now()
	legacy := filepath.Join(a.Dir(), SessionsDir)
	_ = os.MkdirAll(legacy, 0o755)
	rec := func(beat int64) []byte {
		return []byte(`{"id":"old","pid":1,"started":1,"last_beat":` + strconv.FormatInt(beat, 10) + `}`)
	}
	_ = os.WriteFile(filepath.Join(legacy, "old"), rec(now.Unix()), 0o644)
	if strings.Contains(hygieneText(a, dir, now), "presence folder") {
		t.Fatal("a folder with a fresh record is flagged")
	}
	_ = os.WriteFile(filepath.Join(legacy, "old"), rec(now.Unix()-int64(staleSec())-60), 0o644)
	if !strings.Contains(hygieneText(a, dir, now), "presence folder "+SessionsDir+"/") {
		t.Fatal("a quiet legacy folder is not flagged")
	}
}

func hygieneText(a *Agent, dir string, now time.Time) string {
	var out []string
	for _, h := range agentHygiene(dir, a, now) {
		out = append(out, h.Message)
	}
	return strings.Join(out, "\n")
}

func TestPoolIgnore_SessionsLine(t *testing.T) {
	if !strings.Contains(poolIgnoreBlock, "\n"+PoolSessionsDirName+"/\n") {
		t.Fatal("pool ignore block lacks sessions/")
	}
	found := false
	for _, l := range lateIgnores {
		found = found || l == PoolSessionsDirName+"/"
	}
	if !found {
		t.Fatal("pools with an older ignore block do not get sessions/")
	}
}
