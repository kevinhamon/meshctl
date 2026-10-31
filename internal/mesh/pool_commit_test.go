package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pool_commit_test.go — the PostToolUse hook commits a confirmed agent write
// under pool/ with the user's identity, and
// meshctl's own doctrine upgrade commits the same way.

// poolCommitFixture is a migrated pool repo with one peer and a distinct user
// identity, so the commit author is observable.
func poolCommitFixture(t *testing.T) (dir string, a *Agent) {
	t.Helper()
	dir, a = poolGateFixture(t)
	t.Setenv("GIT_AUTHOR_NAME", "Pool Human")
	t.Setenv("GIT_AUTHOR_EMAIL", "human@pool.test")
	t.Setenv("GIT_COMMITTER_NAME", "Pool Human")
	t.Setenv("GIT_COMMITTER_EMAIL", "human@pool.test")
	return dir, a
}

// appendFile appends text to path (the write the tool already made).
func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func postEdit(a *Agent, session, tool, path string, resp string) HookPayload {
	p := editAt(a, session, tool, path)
	p.ToolResponse = json.RawMessage(resp)
	return p
}

func postBash(a *Agent, session, command, resp string) HookPayload {
	in, _ := json.Marshal(map[string]string{"command": command})
	return HookPayload{Cwd: a.Dir(), SessionID: session, ToolName: "Bash", ToolInput: in, ToolResponse: json.RawMessage(resp)}
}

func poolHead(t *testing.T, dir string) (subject, author, body string) {
	t.Helper()
	pool := PoolDir(dir)
	return gitOut(t, pool, "log", "-1", "--format=%s"), gitOut(t, pool, "log", "-1", "--format=%an <%ae>"), gitOut(t, pool, "log", "-1", "--format=%b")
}

func poolCommitCount(t *testing.T, dir string) string {
	t.Helper()
	return gitOut(t, PoolDir(dir), "rev-list", "--count", "HEAD")
}

func TestPostToolUse_GlossaryWriteCommitsWithTermSubject(t *testing.T) {
	dir, a := poolCommitFixture(t)
	before := poolCommitCount(t, dir)
	g := PoolGlossaryPath(dir)
	appendFile(t, g, "\n**Fact owner**: the one peer whose knowledge base holds a canonical fact.\n")

	HookPostToolUse(dir, postEdit(a, "sess-1", "Edit", g, `{"filePath":"x","success":true}`), time.Now())

	if after := poolCommitCount(t, dir); after == before {
		t.Fatalf("expected one commit on the pool repo, count stayed %s", before)
	}
	subject, author, body := poolHead(t, dir)
	if subject != "glossary: Fact owner" {
		t.Errorf("subject = %q, want %q", subject, "glossary: Fact owner")
	}
	if author != "Pool Human <human@pool.test>" {
		t.Errorf("author = %q, want the user's git identity", author)
	}
	if !strings.Contains(body, "architect") || !strings.Contains(body, "sess-1") {
		t.Errorf("body must name the peer and the session, got:\n%s", body)
	}
	if dirty := gitDirtyPaths(PoolDir(dir)); len(dirty) != 0 {
		t.Errorf("pool repo left dirty after the commit: %v", dirty)
	}
}

func TestPostToolUse_SeveralTermsCommitOnceFirstInSubject(t *testing.T) {
	dir, a := poolCommitFixture(t)
	before := poolCommitCount(t, dir)
	g := PoolGlossaryPath(dir)
	appendFile(t, g, "\n## Ownership\n\n**Fact owner**: the peer holding a canonical fact.\n\n**Perspective**: a peer's own reading of a fact it does not own.\n")

	HookPostToolUse(dir, postEdit(a, "sess-1", "Write", g, `{"type":"update"}`), time.Now())

	if got := poolCommitCount(t, dir); got != incr(before) {
		t.Fatalf("want exactly one commit, count %s → %s", before, got)
	}
	subject, _, body := poolHead(t, dir)
	if subject != "glossary: Fact owner" {
		t.Errorf("subject = %q, want the first term", subject)
	}
	if !strings.Contains(body, "Perspective") {
		t.Errorf("body must list the remaining terms, got:\n%s", body)
	}
}

func TestPostToolUse_DefinitionEditNamesItsTerm(t *testing.T) {
	dir, a := poolCommitFixture(t)
	g := PoolGlossaryPath(dir)
	appendFile(t, g, "\n**Fact owner**: the peer holding a canonical fact.\n_Avoid_: master\n")
	gitOut(t, PoolDir(dir), "commit", "-qam", "seed")
	b, _ := os.ReadFile(g)
	os.WriteFile(g, []byte(strings.Replace(string(b), "_Avoid_: master", "_Avoid_: master, primary", 1)), 0o644)

	HookPostToolUse(dir, postEdit(a, "sess-1", "Edit", g, `{}`), time.Now())

	if subject, _, _ := poolHead(t, dir); subject != "glossary: Fact owner" {
		t.Errorf("an edit inside an entry names the entry's term; subject = %q", subject)
	}
}

func TestPostToolUse_DoctrineAndConfigSubjects(t *testing.T) {
	dir, a := poolCommitFixture(t)
	local := DoctrineLocalPath(dir)
	appendFile(t, local, "\n## Review rules\n\nEvery ADR names its owner.\n")
	HookPostToolUse(dir, postEdit(a, "sess-1", "Edit", local, `{}`), time.Now())
	if subject, _, _ := poolHead(t, dir); subject != "doctrine: Review rules" {
		t.Errorf("doctrine subject = %q", subject)
	}

	cfg := PoolConfigPath(dir)
	appendFile(t, cfg, "reconcile:\n  order: [architect]\n")
	HookPostToolUse(dir, postEdit(a, "sess-1", "Edit", cfg, `{}`), time.Now())
	if subject, _, body := poolHead(t, dir); subject != "config: reconcile" || !strings.Contains(body, "sess-1") {
		t.Errorf("config commit = %q / %q", subject, body)
	}
}

func TestPostToolUse_ShellWriteCommits(t *testing.T) {
	dir, a := poolCommitFixture(t)
	before := poolCommitCount(t, dir)
	g := PoolGlossaryPath(dir)
	appendFile(t, g, "\n**Batch**: the set of outbound calls one summary covers.\n")

	cmd := "cat >> " + g + " <<'EOF'\n**Batch**: the set of outbound calls one summary covers.\nEOF"
	HookPostToolUse(dir, postBash(a, "sess-2", cmd, `{"stdout":"","stderr":"","interrupted":false}`), time.Now())

	if got := poolCommitCount(t, dir); got != incr(before) {
		t.Fatalf("shell write: want one commit, count %s → %s", before, got)
	}
	if subject, _, body := poolHead(t, dir); subject != "glossary: Batch" || !strings.Contains(body, "sess-2") {
		t.Errorf("shell commit = %q / %q", subject, body)
	}
}

// TestPostToolUse_InterpreterWriteCommits: a python heredoc that writes a pool
// file is committed like a shell redirect (#13).
func TestPostToolUse_InterpreterWriteCommits(t *testing.T) {
	dir, a := poolCommitFixture(t)
	before := poolCommitCount(t, dir)
	d := DoctrineLocalPath(dir)
	appendFile(t, d, "\n- a new house rule\n")

	cmd := "python3 - <<'EOF'\np='" + d + "'; s=open(p).read()\nopen(p,'w').write(s)\nEOF"
	HookPostToolUse(dir, postBash(a, "sess-2", cmd, `{"stdout":"","stderr":"","interrupted":false}`), time.Now())

	if got := poolCommitCount(t, dir); got != incr(before) {
		t.Fatalf("interpreter write: want one commit, count %s → %s", before, got)
	}
}

func TestPostToolUse_FailureOrOutsidePoolCommitsNothing(t *testing.T) {
	dir, a := poolCommitFixture(t)
	g := PoolGlossaryPath(dir)
	appendFile(t, g, "\n**Fact owner**: x.\n")
	before := poolCommitCount(t, dir)

	for _, p := range []HookPayload{
		postEdit(a, "sess-1", "Edit", g, `{"success":false,"error":"old_string not found"}`),
		postEdit(a, "sess-1", "Edit", g, `{"is_error":true}`),
		postEdit(a, "sess-1", "Edit", g, `"Error: file has been modified since read"`),
		postBash(a, "sess-1", "echo x >> "+g, `{"stdout":"","stderr":"","interrupted":true}`),
		postEdit(a, "sess-1", "Edit", filepath.Join(a.Dir(), "notes.md"), `{}`),
	} {
		HookPostToolUse(dir, p, time.Now())
	}
	if got := poolCommitCount(t, dir); got != before {
		t.Fatalf("failed or out-of-pool calls must commit nothing, count %s → %s", before, got)
	}
}

func TestPostToolUse_NeverStagesIgnoredFiles(t *testing.T) {
	dir, a := poolCommitFixture(t)
	// A write that names both a tracked file and ignored runtime/scratch files.
	up := filepath.Join(DoctrinePath(dir), "agent-comms.md"+upstreamSuffix)
	os.WriteFile(up, []byte("upstream scratch\n"), 0o644)
	g := PoolGlossaryPath(dir)
	appendFile(t, g, "\n**Fact owner**: x.\n")

	HookPostToolUse(dir, postBash(a, "sess-1", "touch "+up+" "+g, `{"stdout":""}`), time.Now())

	files := gitOut(t, PoolDir(dir), "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(files, upstreamSuffix) {
		t.Fatalf("ignored file staged into the commit:\n%s", files)
	}
	if !strings.Contains(files, PoolGlossaryFile) {
		t.Fatalf("tracked write not committed:\n%s", files)
	}
	if tracked := gitOut(t, PoolDir(dir), "ls-files"); strings.Contains(tracked, upstreamSuffix) {
		t.Fatalf("ignored file tracked:\n%s", tracked)
	}
}

func TestPoolUpgrade_DoctrineRewriteCommits(t *testing.T) {
	dir, _ := poolCommitFixture(t)
	// A stamped doctrine file meshctl wrote earlier, now stale: the upgrade
	// replaces it and the replacement is a reviewable commit.
	name := CommsFile
	stale := filepath.Join(DoctrinePath(dir), name)
	os.WriteFile(stale, []byte("old doctrine\n"), 0o644)
	if err := stampEmitted(DoctrinePath(dir), []string{name}); err != nil {
		t.Fatal(err)
	}
	gitOut(t, PoolDir(dir), "add", "-A")
	gitOut(t, PoolDir(dir), "commit", "-qm", "seed stale doctrine")
	// A human's uncommitted local edit must not be swept into meshctl's commit.
	appendFile(t, DoctrineLocalPath(dir), "\nhalf-written rule\n")

	res, err := UpgradeDoctrine(dir, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Did) == 0 {
		t.Fatalf("expected the stale doctrine to be rewritten")
	}
	subject, author, _ := poolHead(t, dir)
	if subject != "doctrine: upgrade to "+doctrineVersion() {
		t.Errorf("subject = %q", subject)
	}
	if author != "Pool Human <human@pool.test>" {
		t.Errorf("author = %q", author)
	}
	files := gitOut(t, PoolDir(dir), "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, name) || strings.Contains(files, DoctrineLocalFile) {
		t.Errorf("upgrade commit must carry the rewritten doctrine only:\n%s", files)
	}

	// Already current: nothing more to commit.
	n := poolCommitCount(t, dir)
	if _, err := UpgradeDoctrine(dir, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if poolCommitCount(t, dir) != n {
		t.Errorf("a no-op upgrade must not commit")
	}
}

func TestSessionAck_RecordAndLookup(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	if HasSessionAck(a.Dir(), "sess-1", "h1") {
		t.Fatal("no ack recorded yet")
	}
	if err := RecordSessionAck(a.Dir(), "sess-1", "h1"); err != nil {
		t.Fatal(err)
	}
	_ = RecordSessionAck(a.Dir(), "sess-1", "h1") // idempotent
	if !HasSessionAck(a.Dir(), "sess-1", "h1") {
		t.Fatal("recorded ack not found")
	}
	if HasSessionAck(a.Dir(), "sess-1", "h2") || HasSessionAck(a.Dir(), "sess-2", "h1") {
		t.Fatal("ack must be keyed by session and hash")
	}
	if p := SessionAckPath(a.Dir(), "sess-1"); p != filepath.Join(a.Dir(), MemoryDir, SummaryDir, "sess-1.acks") {
		t.Fatalf("ack path = %s", p)
	}
	b, _ := os.ReadFile(SessionAckPath(a.Dir(), "sess-1"))
	if strings.Count(string(b), "h1") != 1 {
		t.Fatalf("duplicate ack lines:\n%s", b)
	}
}

func TestHygiene_CleansOldSessionAcks(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	_ = RecordSessionAck(a.Dir(), "old", "h1")
	_ = RecordSessionAck(a.Dir(), "fresh", "h1")
	old := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(SessionAckPath(a.Dir(), "old"), old, old)

	items := PoolHygiene(dir, time.Now())
	if !strings.Contains(hygieneMessages(items), "session ack") {
		t.Fatalf("missing session-ack hygiene item:\n%s", hygieneMessages(items))
	}
	if _, errs := ApplyHygiene(items); len(errs) != 0 {
		t.Fatal(errs)
	}
	if fileExists(SessionAckPath(a.Dir(), "old")) || !fileExists(SessionAckPath(a.Dir(), "fresh")) {
		t.Fatal("hygiene must remove the old ack file only")
	}
}

func incr(s string) string {
	n, _ := strconv.Atoi(s)
	return strconv.Itoa(n + 1)
}
