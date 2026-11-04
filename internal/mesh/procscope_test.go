package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// foreignScope makes this process's scope differ from "elsewhere", the scope
// the tests stamp on records written in another process namespace.
func foreignScope(t *testing.T) {
	t.Helper()
	prev := procScope
	procScope = func() string { return "here/pid:[1]" }
	t.Cleanup(func() { procScope = prev })
}

const elsewhere = "there/pid:[2]"

// deadPID is a pid no process has (pid_max is far below it).
const deadPID = 1 << 30

func TestProcScope_NamesTheHost(t *testing.T) {
	host, _ := os.Hostname()
	if s := computeProcScope(); s == "" || !strings.HasPrefix(s, host) {
		t.Fatalf("scope %q should start with the host name %q", s, host)
	}
}

func TestScopeLocal_EmptyIsLocal(t *testing.T) {
	foreignScope(t)
	if !scopeLocal("") || !scopeLocal("here/pid:[1]") {
		t.Fatal("an unscoped record (written before scopes) and our own scope are local")
	}
	if scopeLocal(elsewhere) {
		t.Fatal("another namespace's scope is not local")
	}
}

func writeTestSession(t *testing.T, agentDir string, s *Session) {
	t.Helper()
	if err := os.MkdirAll(sessionsDir(agentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	s.path = sessionPath(agentDir, s.ID)
	s.Agent = sessionAgent(agentDir)
	if err := writeSession(s); err != nil {
		t.Fatal(err)
	}
}

func TestLiveSessions_ForeignPIDDoesNotKeepAStaleSession(t *testing.T) {
	// Our own pid is alive here, but a pid recorded in another namespace names
	// some other process: it must not keep a stale record from being reaped.
	foreignScope(t)
	dir := t.TempDir()
	now := time.Now()
	stale := now.Unix() - int64(staleSec()) - 60
	writeTestSession(t, dir, &Session{ID: "far", LastBeat: stale, HarnessPID: os.Getpid(), ProcScope: elsewhere})
	writeTestSession(t, dir, &Session{ID: "near", LastBeat: stale, HarnessPID: os.Getpid(), ProcScope: "here/pid:[1]"})

	liveSessions(dir, now)
	if fileExists(sessionPath(dir, "far")) {
		t.Fatal("a stale session from another namespace should be reaped")
	}
	if !fileExists(sessionPath(dir, "near")) {
		t.Fatal("a stale but idle session whose harness runs here is kept")
	}
}

func TestOpenSessions_ForeignSessionFallsBackToItsHeartbeat(t *testing.T) {
	foreignScope(t)
	dir := t.TempDir()
	a := &Agent{Name: "peer", dir: dir}
	now := time.Now()
	writeTestSession(t, dir, &Session{ID: "fresh", LastBeat: now.Unix(), HarnessPID: deadPID, ProcScope: elsewhere})
	writeTestSession(t, dir, &Session{ID: "old", Started: 1, LastBeat: now.Unix() - int64(staleSec()) - 60, HarnessPID: os.Getpid(), ProcScope: elsewhere})
	writeTestSession(t, dir, &Session{ID: "gone", LastBeat: now.Unix(), HarnessPID: deadPID, ProcScope: "here/pid:[1]"})

	var ids []string
	for _, s := range OpenSessions(a, now) {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "fresh" {
		t.Fatalf("open sessions = %v, want only the freshly beating foreign one", ids)
	}
}

func TestSessionStart_RecordsItsScope(t *testing.T) {
	foreignScope(t)
	dir := t.TempDir()
	a := &Agent{Name: "peer", dir: dir}
	if _, err := SessionStart(a, "s1", os.Getpid(), time.Now()); err != nil {
		t.Fatal(err)
	}
	s, err := readSession(sessionPath(dir, "s1"))
	if err != nil || s.ProcScope != "here/pid:[1]" {
		t.Fatalf("scope = %q (%v)", s.ProcScope, err)
	}
}

func TestJobsPath_ScopedSessionsDoNotShareAFile(t *testing.T) {
	dir := t.TempDir()
	a := jobsPath(dir, &Session{ID: "a", HarnessPID: 7, ProcScope: "here/pid:[1]"})
	b := jobsPath(dir, &Session{ID: "b", HarnessPID: 7, ProcScope: elsewhere})
	legacy := jobsPath(dir, &Session{ID: "c", HarnessPID: 7})
	if a == b {
		t.Fatal("the same pid in two namespaces must not share a job list")
	}
	if filepath.Base(legacy) != filepath.Base(dir)+"-pid-7.json" {
		t.Fatalf("an unscoped session keeps its old path, got %s", legacy)
	}
}

func TestHygiene_ForeignJobListGoesByAge(t *testing.T) {
	foreignScope(t)
	dir := t.TempDir()
	now := time.Now()
	if err := os.MkdirAll(jobsDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, js JobSet) {
		b := []byte(`{"harness_pid":` + itoa(int64(js.HarnessPID)) + `,"proc_scope":"` + js.ProcScope + `","jobs":[],"at":` + itoa(js.At) + `}`)
		if err := os.WriteFile(filepath.Join(jobsDir(dir), name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Base(dir)+"-pid-1-fresh.json", JobSet{HarnessPID: deadPID, ProcScope: elsewhere, At: now.Unix()})
	write(filepath.Base(dir)+"-pid-1-old.json", JobSet{HarnessPID: os.Getpid(), ProcScope: elsewhere, At: now.Unix() - int64(staleSec()) - 60})

	var msgs []string
	for _, it := range agentHygiene(t.TempDir(), &Agent{Name: filepath.Base(dir), dir: dir}, now) {
		msgs = append(msgs, it.Message)
	}
	got := strings.Join(msgs, "\n")
	if strings.Contains(got, "pid-1-fresh.json") || !strings.Contains(got, "pid-1-old.json") {
		t.Fatalf("hygiene:\n%s", got)
	}
}

func TestLockReclaimable_ForeignHolderGoesByAge(t *testing.T) {
	foreignScope(t)
	now := time.Now()
	mk := func(info string, age time.Duration) string {
		p := filepath.Join(t.TempDir(), "lock")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "info"), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		_ = os.Chtimes(p, at, at)
		return p
	}
	dead := "pid=" + itoa(deadPID)
	if lockReclaimable(mk(dead+" scope="+elsewhere+" caller=a", 0), now) {
		t.Fatal("a fresh lock held in another namespace is not taken, whatever its pid looks like here")
	}
	if !lockReclaimable(mk(dead+" scope="+elsewhere+" caller=a", time.Duration(staleSec()+60)*time.Second), now) {
		t.Fatal("a foreign lock past the stale window is taken")
	}
	if !lockReclaimable(mk(dead+" scope=here/pid:[1] caller=a", 0), now) {
		t.Fatal("a lock whose holder died here is taken at once")
	}
	if !lockReclaimable(mk(dead+" caller=a", 0), now) {
		t.Fatal("an unscoped lock (written before scopes) keeps today's pid check")
	}
}
