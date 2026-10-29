package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pool_clone_test.go — the member list in the pool repo and `pool clone`
// (pool-clone spec): the list is written from disk and committed alone, doctor
// says when it is stale or an agent cannot be rebuilt, and a clone of the pool
// repo rebuilds the pool from it.

// clonablePool is a migrated pool repo, pushed to a bare remote, with two
// agents that are each their own repo pushed to a bare remote of their own.
func clonablePool(t *testing.T) (root string, remotes map[string]string) {
	t.Helper()
	root = migratedRoot(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pushedPoolRepo(t, root)
	remotes = map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		spec := AgentSpec{Name: name, Title: name, Role: "r", Accepts: []Accept{{Type: "fyi", Desc: "x"}},
			Badge: Badge{Label: strings.ToUpper(name)}, Bare: true}
		dir, err := New(root, spec, ScaffoldOptions{})
		if err != nil {
			t.Fatal(err)
		}
		bare := filepath.Join(t.TempDir(), name+".git")
		gitOut(t, ".", "init", "--bare", "-b", "main", bare)
		gitOut(t, dir, "remote", "add", "origin", bare)
		gitOut(t, dir, "push", "-q", "-u", "origin", "main")
		remotes[name] = bare
	}
	return root, remotes
}

func TestMembers_RefreshWritesCommitsOnlyThatFileAndIsIdempotent(t *testing.T) {
	root, remotes := clonablePool(t)
	other := filepath.Join(PoolDir(root), "notes.md")
	os.WriteFile(other, []byte("mine\n"), 0o644)
	before := poolCommitCount(t, root)

	changed, err := RefreshMembers(root)
	if err != nil || !changed {
		t.Fatalf("first refresh must write the list: %v %v", changed, err)
	}
	l, ok := ReadMembers(root)
	if !ok || len(l.Members) != 2 || l.Meshctl == "" {
		t.Fatalf("want two members and a version, got %+v", l)
	}
	for _, m := range l.Members {
		if m.Remote != remotes[m.Name] || m.Branch != "main" || m.Path != m.Name {
			t.Fatalf("member %+v: want remote %s on main", m, remotes[m.Name])
		}
	}
	if subj, _, _ := poolHead(t, root); subj != membersSubject {
		t.Fatalf("commit subject %q", subj)
	}
	if got := poolCommitCount(t, root); got != incr(before) {
		t.Fatalf("one commit, got %s → %s", before, got)
	}
	if st := gitOut(t, PoolDir(root), "status", "--porcelain", "--", "notes.md"); !strings.Contains(st, "notes.md") {
		t.Fatal("an unrelated uncommitted file must stay uncommitted")
	}

	if changed, err := RefreshMembers(root); err != nil || changed {
		t.Fatalf("second refresh must be a no-op: %v %v", changed, err)
	}
	if got := poolCommitCount(t, root); got != incr(before) {
		t.Fatal("a no-op refresh must not commit")
	}
}

func TestDoctor_StaleMemberListIsFixableAndNoRemoteIsNotRestorable(t *testing.T) {
	root, _ := clonablePool(t)
	if _, err := RefreshMembers(root); err != nil {
		t.Fatal(err)
	}
	// A third agent appears, with no remote.
	spec := AgentSpec{Name: "gamma", Title: "g", Role: "r", Accepts: []Accept{{Type: "fyi", Desc: "x"}}, Badge: Badge{Label: "G"}, Bare: true}
	if _, err := New(root, spec, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	findings, _, _, _ := DoctorWithHygiene(root, false, time.Now())
	var stale, noRemote *DoctorFinding
	for i, f := range findings {
		if strings.Contains(f.Message, "member list out of date") {
			stale = &findings[i]
		}
		if f.Agent == "gamma" && f.Kind == FindingNotRestorable {
			noRemote = &findings[i]
		}
	}
	if stale == nil || !stale.Fixable || !strings.Contains(stale.Message, "gamma not listed") {
		t.Fatalf("want a fixable stale-list finding naming gamma, got %+v", findings)
	}
	if noRemote == nil || !strings.Contains(noRemote.Message, "cannot be rebuilt on another machine") {
		t.Fatalf("want gamma flagged not restorable, got %+v", findings)
	}

	findings, _, fixed, errs := DoctorWithHygiene(root, true, time.Now())
	if len(errs) > 0 || len(fixed) == 0 {
		t.Fatalf("--fix must refresh the list: fixed %v errs %v", fixed, errs)
	}
	for _, f := range findings {
		if strings.Contains(f.Message, "member list out of date") {
			t.Fatalf("still stale after --fix: %+v", f)
		}
	}
	if l, _ := ReadMembers(root); len(l.Members) != 3 {
		t.Fatalf("want three members after --fix, got %+v", l)
	}
}

func TestPoolClone_RebuildsThePoolFromItsRepo(t *testing.T) {
	root, remotes := clonablePool(t)
	// A member with no remote, and an extra listed by hand.
	spec := AgentSpec{Name: "gamma", Title: "g", Role: "r", Accepts: []Accept{{Type: "fyi", Desc: "x"}}, Badge: Badge{Label: "G"}, Bare: true}
	if _, err := New(root, spec, ScaffoldOptions{}); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(t.TempDir(), "tools.git")
	gitOut(t, ".", "init", "--bare", "-b", "main", extra)
	seed := filepath.Join(t.TempDir(), "seed")
	gitOut(t, ".", "clone", "-q", extra, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("tools\n"), 0o644)
	gitCommitAll(t, seed, "seed")
	gitOut(t, seed, "push", "-q", "origin", "HEAD:main")
	appendFile(t, PoolConfigPath(root), "extras:\n  - path: tools\n    remote: "+extra+"\n")
	gitCommitAll(t, PoolDir(root), "config: extras")
	if _, err := RefreshMembers(root); err != nil {
		t.Fatal(err)
	}
	poolRemote := gitOut(t, PoolDir(root), "remote", "get-url", "origin")
	gitOut(t, PoolDir(root), "push", "-q")

	// The new machine: an existing folder at beta's path is left alone.
	dest := filepath.Join(t.TempDir(), "rebuilt")
	os.MkdirAll(filepath.Join(dest, "beta"), 0o755)
	os.WriteFile(filepath.Join(dest, "beta", "mine.txt"), []byte("keep\n"), 0o644)

	res, err := ClonePool(poolRemote, dest)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, c := range append(res.Members, res.Extras...) {
		states[c.Name] = c.State
	}
	if states["alpha"] != CloneCloned || states["beta"] != CloneExisting || states["gamma"] != CloneNoRemote || states["tools"] != CloneCloned {
		t.Fatalf("clone states: %v", states)
	}
	if !res.Incomplete() {
		t.Fatal("a member without a remote must make the clone incomplete (non-zero exit)")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "beta", "mine.txt")); string(b) != "keep\n" {
		t.Fatal("an existing folder must be left untouched")
	}
	if got := gitOut(t, filepath.Join(dest, "alpha"), "remote", "get-url", "origin"); got != remotes["alpha"] {
		t.Fatalf("alpha's origin %q", got)
	}
	if !fileExists(filepath.Join(dest, "tools", "README.md")) {
		t.Fatal("the extra must be cloned")
	}

	// The new machine's upgrade keeps the member it lacks in the list, and
	// doctor says how to finish the move.
	if _, err := UpgradePoolDir(dest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if l, _ := ReadMembers(dest); len(l.Members) != 3 {
		t.Fatalf("beta and gamma must stay listed until they arrive or are dropped, got %+v", l)
	}
	var manual string
	for _, it := range PoolHygiene(dest, time.Now()) {
		if strings.Contains(it.Message, "gamma is in the member list but was not cloned") && !it.Fixable() {
			manual = it.Message
		}
	}
	if manual == "" {
		t.Fatal("want a manual item for the member not cloned here")
	}
	// Retired: delete the marker, and the next refresh drops what never came
	// (gamma, and beta, whose path holds something else).
	os.Remove(cloneMissingPath(dest))
	if changed, err := RefreshMembers(dest); err != nil || !changed {
		t.Fatalf("refresh must drop gamma and beta: %v %v", changed, err)
	}
	if l, _ := ReadMembers(dest); len(l.Members) != 1 || l.Members[0].Name != "alpha" {
		t.Fatalf("want only alpha left, got %+v", l)
	}
}

func TestPoolClone_CleanCloneConvergesAfterUpgrade(t *testing.T) {
	root, _ := clonablePool(t)
	if _, err := RefreshMembers(root); err != nil {
		t.Fatal(err)
	}
	poolRemote := gitOut(t, PoolDir(root), "remote", "get-url", "origin")
	gitOut(t, PoolDir(root), "push", "-q")

	dest := filepath.Join(t.TempDir(), "rebuilt")
	res, err := ClonePool(poolRemote, dest)
	if err != nil || res.Incomplete() || res.NoList {
		t.Fatalf("clone: %v %+v", err, res)
	}
	agents, _, err := ScanAgents(dest)
	if err != nil || len(agents) != 2 {
		t.Fatalf("ScanAgents must find both agents: %v %d", err, len(agents))
	}
	if _, err := RegisterPool(dest, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if pools, _ := RegisteredPools(); len(pools) != 1 || pools[0].Path != res.Root {
		t.Fatalf("registered: %+v", pools)
	}
	// The apply path of `pool upgrade`, as `pool clone` runs it.
	if _, err := UpgradePoolDir(dest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeDoctrine(dest, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeAgents(dest, "vtest"); err != nil {
		t.Fatal(err)
	}
	if changed, err := RefreshMembers(dest); err != nil || changed {
		t.Fatalf("the cloned list must already match disk: %v %v", changed, err)
	}
	if err := WriteUpgradeStamp(dest, "vtest", time.Now()); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPoolUpgrade(dest, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pending() {
		t.Fatalf("pool upgrade --check must report nothing pending after a clone: %+v", plan)
	}
	if st := gitOut(t, PoolDir(dest), "status", "--porcelain"); st != "" {
		t.Fatalf("the rebuilt pool repo must be clean after its upgrade, got:\n%s", st)
	}
}

func TestPoolDir_AddsMemoryIgnoreToAnOlderBlock(t *testing.T) {
	root := migratedRoot(t)
	gi := filepath.Join(PoolDir(root), PoolIgnoreFile)
	b, _ := os.ReadFile(gi)
	os.WriteFile(gi, []byte(strings.Replace(string(b), MemoryDir+"/\n", "", 1)), 0o644)
	gitCommitAll(t, PoolDir(root), "older ignore block")
	if steps, _ := PlanPoolDir(root); !strings.Contains(strings.Join(steps, "\n"), "add "+MemoryDir+"/") {
		t.Fatalf("want the .memory/ step planned, got %v", steps)
	}
	if _, err := UpgradePoolDir(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if subj, _, _ := poolHead(t, root); subj != poolIgnoreMemoryCommit && subj != membersSubject {
		t.Fatalf("head %q", subj)
	}
	if b, _ := os.ReadFile(gi); !hasIgnoreLine(string(b), MemoryDir+"/") {
		t.Fatal(".memory/ must be ignored")
	}
	if st := gitOut(t, PoolDir(root), "status", "--porcelain", "--", PoolIgnoreFile); st != "" {
		t.Fatalf("the ignore change must be committed, got %q", st)
	}
}

func TestPoolClone_RefusesAnExistingPoolAndBadPaths(t *testing.T) {
	dest := t.TempDir()
	os.MkdirAll(filepath.Join(dest, CurrentPoolDir), 0o755)
	if _, err := ClonePool("unused", dest); err == nil {
		t.Fatal("must refuse a folder that already holds a pool directory")
	}
	for _, p := range []string{"", "..", "../x", "/abs", CurrentPoolDir} {
		if c := cloneOne(t.TempDir(), "x", p, "r", ""); c.State != CloneBadPath {
			t.Fatalf("path %q: want refused, got %+v", p, c)
		}
	}
}

func TestPoolClone_VersionAndDefaultDir(t *testing.T) {
	for url, want := range map[string]string{
		"git@forge.example:me/team-pool.git": "team",
		"https://forge.example/me/team.git/": "team",
		"/srv/git/pool.git":                  "pool",
	} {
		if got := DefaultCloneDir(url); got != want {
			t.Fatalf("DefaultCloneDir(%q) = %q, want %q", url, got, want)
		}
	}
	root, _ := clonablePool(t)
	old := WriterVersion
	WriterVersion = "v99.0.0"
	if _, err := RefreshMembers(root); err != nil {
		t.Fatal(err)
	}
	WriterVersion = "v1.0.0"
	defer func() { WriterVersion = old }()
	gitOut(t, PoolDir(root), "push", "-q")
	res, err := ClonePool(gitOut(t, PoolDir(root), "remote", "get-url", "origin"), filepath.Join(t.TempDir(), "x"))
	if err != nil || res.Behind != "v99.0.0" {
		t.Fatalf("want the newer writer reported, got %v %+v", err, res)
	}
}
