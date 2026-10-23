package mesh

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// commitAt commits everything with author and committer dated at.
func commitAt(t *testing.T, dir, msg string, at time.Time) {
	t.Helper()
	stamp := at.Format(time.RFC3339)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
			"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// gitCallCounter puts a git wrapper first on PATH that counts invocations.
func gitCallCounter(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho x >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "x")
	}
}

func TestAgedCandidates_DatesFromOneGitPass(t *testing.T) {
	root := meshRoot(t)
	a := newTestAgent(t, root, "grid", false, nil)
	gitInit(t, a.Dir())
	cands := filepath.Join(a.Dir(), KnowledgeDir, "candidates")
	now := time.Now()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cands, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("old.md", "old\n")
	write("edited.md", "edited\n")
	commitAt(t, a.Dir(), "old candidates", now.AddDate(0, 0, -30))
	write("fresh.md", "fresh\n")
	commitAt(t, a.Dir(), "fresh candidate", now.AddDate(0, 0, -2))
	write("edited.md", "edited again, not committed\n") // uncommitted edit: dated by mtime
	write("untracked.md", "never committed\n")          // untracked: dated by mtime

	calls := gitCallCounter(t)
	got := agedCandidates(a.Dir(), now)

	want := []string{"candidates/old.md (30 days)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agedCandidates = %q, want %q", got, want)
	}
	if n := calls(); n > 2 {
		t.Fatalf("agedCandidates ran git %d times for 4 candidates; want one log and one status pass", n)
	}
}

func TestWorkBranchRefs_ReadsRefFilesWithoutGit(t *testing.T) {
	root := meshRoot(t)
	a := newTestAgent(t, root, "grid", false, nil)
	gitInit(t, a.Dir())
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = a.Dir()
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("branch", WorkBranchPrefix+"packed")
	run("pack-refs", "--all") // a packed ref has no loose file
	run("branch", WorkBranchPrefix+"loose")
	run("checkout", "-q", "-b", WorkBranchPrefix+"landed")
	run("commit", "-q", "--allow-empty", "-m", "work")
	run("checkout", "-q", "main")
	run("merge", "-q", "--ff-only", WorkBranchPrefix+"landed") // what `work merge` records
	run("branch", "-D", WorkBranchPrefix+"landed")

	calls := gitCallCounter(t)
	live, merged := workBranchRefs(a.Dir())

	if want := []string{WorkBranchPrefix + "loose", WorkBranchPrefix + "packed"}; !reflect.DeepEqual(live, want) {
		t.Fatalf("live = %q, want %q", live, want)
	}
	if want := []string{WorkBranchPrefix + "landed"}; !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %q, want %q", merged, want)
	}
	if n := calls(); n != 0 {
		t.Fatalf("workBranchRefs ran git %d times; a plain .git directory needs none", n)
	}
}
