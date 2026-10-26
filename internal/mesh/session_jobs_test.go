package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHarnessTerminal_WalksToTheFirstTTY(t *testing.T) {
	tree := map[int]struct {
		ppid int
		tty  string
	}{
		500: {400, ""},        // the hook
		400: {300, ""},        // a shell the harness spawned
		300: {200, "ttys010"}, // the harness
		200: {1, "ttys010"},
	}
	old := psParentTTY
	psParentTTY = func(pid int) (int, string, bool) {
		n, ok := tree[pid]
		return n.ppid, n.tty, ok
	}
	defer func() { psParentTTY = old }()
	if pid, tty := harnessTerminal(500); pid != 300 || tty != "/dev/ttys010" {
		t.Fatalf("got %d %q", pid, tty)
	}
	if pid, tty := harnessTerminal(999); pid != 0 || tty != "" {
		t.Fatalf("unknown process: %d %q", pid, tty)
	}
}

func TestHookSessionBeat_RecordsScheduledJobs(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	jobsOf := func(id string) []SessionJob {
		s, err := readSession(sessionPath(a.Dir(), id))
		if err != nil {
			t.Fatal(err)
		}
		js, _ := SessionJobs(a, s)
		return js.Jobs
	}
	jobs := []SessionJob{{ID: "39c9cdf1", Schedule: "*/10 * * * *", Recurring: true, Prompt: "check the PRs"}}
	HookSessionBeat(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1", SessionCrons: &jobs}, now)
	if j := jobsOf("s1"); len(j) != 1 || j[0].Prompt != "check the PRs" {
		t.Fatalf("jobs: %+v", j)
	}
	// A heartbeat without the field keeps them; an empty list clears them.
	HookSessionBeat(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1"}, now.Add(time.Minute))
	if len(jobsOf("s1")) != 1 {
		t.Fatal("a plain heartbeat must keep the jobs")
	}
	none := []SessionJob{}
	HookSessionBeat(dir, HookPayload{Cwd: a.Dir(), SessionID: "s1", SessionCrons: &none}, now.Add(2*time.Minute))
	if len(jobsOf("s1")) != 0 {
		t.Fatal("an empty list must clear the jobs")
	}
	p := ParseHookPayload([]byte(`{"session_id":"s2","session_crons":[{"id":"a","schedule":"0 * * * *","recurring":false,"prompt":"x"}]}`))
	if p.SessionCrons == nil || len(*p.SessionCrons) != 1 || (*p.SessionCrons)[0].Schedule != "0 * * * *" {
		t.Fatalf("payload parse: %+v", p.SessionCrons)
	}
	if ParseHookPayload([]byte(`{"session_id":"s2"}`)).SessionCrons != nil {
		t.Fatal("absent field must stay nil")
	}
}

// /clear ends one session id and starts another in the same harness process,
// which keeps its scheduled jobs: the new session shows them at once.
func TestScheduledJobs_SurviveClear(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	me := os.Getpid()
	old := psParentTTY
	// Every process in the walk reports a terminal: the harness is this test process.
	psParentTTY = func(pid int) (int, string, bool) { return 1, "ttys042", true }
	defer func() { psParentTTY = old }()
	now := time.Now()
	jobs := []SessionJob{{ID: "j1", Schedule: "*/5 * * * *", Recurring: true, Prompt: "watch the inbox"}}
	HookSessionBeat(dir, HookPayload{Cwd: a.Dir(), SessionID: "before-clear", SessionCrons: &jobs}, now)
	_ = SessionEnd(a, "before-clear")                              // /clear: SessionEnd for the old id …
	if err := TouchSession(a, "after-clear", 0, now); err != nil { // … and a new session in the same process
		t.Fatal(err)
	}
	s, err := readSession(sessionPath(a.Dir(), "after-clear"))
	if err != nil || s.HarnessPID != me {
		t.Fatalf("new session's harness: %+v %v", s, err)
	}
	js, ok := SessionJobs(a, s)
	if !ok || len(js.Jobs) != 1 || js.Jobs[0].Prompt != "watch the inbox" {
		t.Fatalf("jobs after /clear: %+v %v", js, ok)
	}
	// Hygiene keeps a live process's list and drops a dead one's.
	dead := filepath.Join(jobsDir(a.Dir()), "pid-999999.json")
	_ = os.WriteFile(dead, []byte(`{"harness_pid":999999,"jobs":[],"at":1}`), 0o644)
	var flagged []string
	for _, h := range PoolHygiene(dir, now) {
		if strings.Contains(h.Message, "scheduled-job list") {
			flagged = append(flagged, h.Message)
		}
		if strings.Contains(h.Message, "stale session record jobs") {
			t.Errorf("the jobs folder is not a session record: %s", h.Message)
		}
	}
	if len(flagged) != 1 || !strings.Contains(flagged[0], "pid-999999") {
		t.Fatalf("hygiene: %v", flagged)
	}
}

func TestOpenSessions_AliveHarnessOrFreshBeat(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	old := now.Add(-2 * time.Hour).Unix()
	_ = os.MkdirAll(sessionsDir(a.Dir()), 0o755)
	write := func(s Session) {
		s.path = sessionPath(a.Dir(), s.ID)
		if err := writeSession(&s); err != nil {
			t.Fatal(err)
		}
	}
	write(Session{ID: "idle-but-open", Started: old, LastBeat: old, HarnessPID: os.Getpid()})
	write(Session{ID: "dead", Started: old, LastBeat: old, HarnessPID: 999999})
	write(Session{ID: "fresh-unknown", Started: now.Unix(), LastBeat: now.Unix()})
	write(Session{ID: "stale-unknown", Started: old, LastBeat: old})
	got := map[string]bool{}
	for _, s := range OpenSessions(a, now) {
		got[s.ID] = true
	}
	if !got["idle-but-open"] || !got["fresh-unknown"] || got["dead"] || got["stale-unknown"] || len(got) != 2 {
		t.Fatalf("open sessions: %v", got)
	}
	if _, err := os.Stat(sessionPath(a.Dir(), "dead")); err != nil {
		t.Fatal("OpenSessions must not reap anything")
	}
}

func TestIdleOpenSession_NotReapedNorStale(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	now := time.Now()
	old := now.Add(-2 * time.Hour).Unix()
	_ = os.MkdirAll(sessionsDir(a.Dir()), 0o755)
	idle := Session{ID: "idle", Started: old, LastBeat: old, HarnessPID: os.Getpid(), path: sessionPath(a.Dir(), "idle")}
	dead := Session{ID: "dead", Started: old, LastBeat: old, HarnessPID: 999999, path: sessionPath(a.Dir(), "dead")}
	_ = writeSession(&idle)
	_ = writeSession(&dead)
	if live := liveSessions(a.Dir(), now); len(live) != 0 {
		t.Fatalf("neither is live by heartbeat: %+v", live)
	}
	if _, err := os.Stat(idle.path); err != nil {
		t.Fatal("an idle session whose harness runs must not be reaped")
	}
	if _, err := os.Stat(dead.path); !os.IsNotExist(err) {
		t.Fatal("a dead stale session is still reaped")
	}
	_ = writeSession(&dead)
	var stale []string
	for _, h := range PoolHygiene(dir, now) {
		if strings.Contains(h.Message, "stale session record") {
			stale = append(stale, h.Message)
		}
	}
	if len(stale) != 1 || !strings.Contains(stale[0], "dead") {
		t.Fatalf("hygiene must flag only the dead session: %v", stale)
	}
}
