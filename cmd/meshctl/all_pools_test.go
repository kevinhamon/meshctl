package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// allPoolsHost isolates a host for the --all-pools commands: a temp
// XDG_CONFIG_HOME (pool registry, harness cache) and HOME, a PATH holding only
// git and a fake `claude` (never run: its presence is the probe), no AGENTS_DIR
// and no harness or dispatch markers. It registers a valid pool ("live", one
// agent wired for claude and one wired for nothing), a path that no longer
// exists ("gone") and a directory that is not a pool ("plain").
func allPoolsHost(t *testing.T) (live string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	for k, v := range map[string]string{
		"XDG_CONFIG_HOME": t.TempDir(), "HOME": home, "PATH": bin, "AGENTS_DIR": "",
		"CLAUDECODE": "", "OPENCODE": "", "AGENT_DISPATCH_DEPTH": "", "AGENT_MESH_DISTILLING": "",
		"MESHCTL_OPENCODE_MAJOR": "", "MESHCTL_CLAUDE_MAJOR": "", "MESHCTL_NO_UPDATE_CHECK": "1",
		"GIT_CONFIG_GLOBAL": filepath.Join(home, ".gitconfig"), "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid",
	} {
		t.Setenv(k, v)
	}
	now := time.Now()
	base := t.TempDir()
	live = filepath.Join(base, "live")
	if _, err := mesh.InitPool(live, "live", now); err != nil {
		t.Fatal(err)
	}
	writeAgent := func(name string, markers ...string) {
		dir := filepath.Join(live, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, mesh.ManifestName), []byte("name: "+name+"\ntitle: "+name+"\nrole: test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, m := range markers {
			p := filepath.Join(dir, m)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeAgent("alpha", ".claude/settings.json")
	writeAgent("beta")
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ path, name string }{{live, "live"}, {filepath.Join(base, "gone"), "gone"}, {plain, "plain"}} {
		if _, err := mesh.RegisterPool(e.path, e.name, now); err != nil {
			t.Fatal(err)
		}
	}
	return live
}

// runCLI runs meshctl with args and returns its stdout and error.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		io.Copy(&b, r)
		done <- b.String()
	}()
	cmd := rootCmd()
	cmd.SetArgs(args)
	runErr := cmd.Execute()
	w.Close()
	os.Stdout = old
	return <-done, runErr
}

// line returns the first output line containing every one of parts.
func line(out string, parts ...string) string {
	for _, l := range strings.Split(out, "\n") {
		ok := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	return ""
}

// matrixRow returns the fields of the output line whose first two fields
// are pool and agent.
func matrixRow(out, pool, agent string) []string {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == pool && f[1] == agent {
			return f
		}
	}
	return nil
}

func TestDoctorAllPools_Text(t *testing.T) {
	live := allPoolsHost(t)
	out, _ := runCLI(t, "doctor", "--all-pools")

	if !strings.Contains(out, "=== pool live ("+live+") ===") {
		t.Fatalf("no header for the live pool:\n%s", out)
	}
	for _, name := range []string{"gone", "plain"} {
		if strings.Contains(out, "=== pool "+name) {
			t.Errorf("%s must be skipped, not checked:\n%s", name, out)
		}
	}
	if line(out, "gone", "path does not exist", "pool forget gone") == "" {
		t.Errorf("missing pool not reported:\n%s", out)
	}
	if line(out, "plain", "holds no pool") == "" {
		t.Errorf("non-pool dir not reported:\n%s", out)
	}
	// Matrix: claude is installed here (fake binary), opencode is not.
	head := matrixRow(out, "POOL", "AGENT")
	if strings.Join(head, " ") != "POOL AGENT "+strings.Join(mesh.RegisteredHarnessIDs(), " ") {
		t.Fatalf("matrix header = %v:\n%s", head, out)
	}
	if r := matrixRow(out, "live", "alpha"); strings.Join(r, " ") != "live alpha wired n/a" {
		t.Errorf("alpha row = %v, want claude wired, opencode n/a:\n%s", r, out)
	}
	if r := matrixRow(out, "live", "beta"); strings.Join(r, " ") != "live beta missing n/a" {
		t.Errorf("beta row = %v, want claude missing, opencode n/a:\n%s", r, out)
	}
	// The walk never forgets an entry.
	if pools, _ := mesh.RegisteredPools(); len(pools) != 3 {
		t.Fatalf("registry changed: %+v", pools)
	}
	// The harness cache landed in the temp config dir.
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "meshctl", "harnesses.json")); err != nil {
		t.Errorf("harness cache not written: %v", err)
	}
}

func TestDoctorAllPools_ExitFollowsPoolErrors(t *testing.T) {
	live := allPoolsHost(t)
	// An unparseable manifest is a doctor error.
	if err := os.MkdirAll(filepath.Join(live, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "broken", mesh.ManifestName), []byte("name: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "doctor", "--all-pools")
	var ce *mesh.CodedError
	if !errors.As(err, &ce) || ce.Code != 1 || !strings.Contains(ce.Msg, "live") {
		t.Fatalf("want a coded exit naming the pool, got %v\n%s", err, out)
	}
}

func TestDoctorAllPools_JSON(t *testing.T) {
	allPoolsHost(t)
	out, _ := runCLI(t, "doctor", "--all-pools", "--json")
	var got []allPoolsDoctorJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("want one element per registered pool, got %d", len(got))
	}
	byName := map[string]allPoolsDoctorJSON{}
	for _, e := range got {
		byName[e.Pool] = e
	}
	if e := byName["live"]; e.State != mesh.PoolOK || e.Report == nil || len(e.Agents) != 2 {
		t.Errorf("live: %+v", e)
	}
	if e := byName["gone"]; e.State != mesh.PoolMissing || e.Report != nil || e.Reason == "" {
		t.Errorf("gone: %+v", e)
	}
	if e := byName["plain"]; e.State != mesh.PoolNotPool || e.Report != nil {
		t.Errorf("plain: %+v", e)
	}
	for _, a := range byName["live"].Agents {
		if a.Agent == "alpha" && a.Harnesses[mesh.HarnessClaude] != mesh.HarnessWired {
			t.Errorf("alpha: %+v", a)
		}
		if a.Agent == "beta" && a.Harnesses[mesh.HarnessClaude] != mesh.HarnessNotWired {
			t.Errorf("beta: %+v", a)
		}
	}
}

func TestPoolUpgradeAllPools_RefusesPerPoolFlags(t *testing.T) {
	allPoolsHost(t)
	for _, args := range [][]string{
		{"pool", "upgrade", "--all-pools", "--force"},
		{"pool", "upgrade", "--all-pools", "--headless"},
	} {
		if out, err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "--all-pools does not take") || out != "" {
			t.Errorf("%v: want a refusal before any output, got %v\n%s", args, err, out)
		}
	}
	t.Setenv("AGENT_DISPATCH_DEPTH", "1")
	if out, err := runCLI(t, "pool", "upgrade", "--all-pools"); err == nil || !strings.Contains(err.Error(), "needs a human") || out != "" {
		t.Errorf("no human at the prompt: want a refusal, got %v\n%s", err, out)
	}
}

func TestPoolUpgradeAllPools_CheckIsReadOnly(t *testing.T) {
	live := allPoolsHost(t)
	before := snapshotTree(t, live)
	out, err := runCLI(t, "pool", "upgrade", "--all-pools", "--check")
	if !strings.Contains(out, "=== pool live ("+live+") ===") || line(out, "gone", "path does not exist") == "" || line(out, "plain", "holds no pool") == "" {
		t.Fatalf("missing header or skipped list:\n%s", out)
	}
	var ce *mesh.CodedError
	if !errors.As(err, &ce) || ce.Code != 1 || !strings.Contains(ce.Msg, "pending") {
		t.Fatalf("a fresh pool's agents need wiring: want exit 1 pending, got %v\n%s", err, out)
	}
	if after := snapshotTree(t, live); after != before {
		t.Fatalf("--check wrote to the pool:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	out, _ = runCLI(t, "pool", "upgrade", "--all-pools", "--check", "--json")
	var got []allPoolsCheckJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 3 {
		t.Fatalf("want a 3-element JSON array: %v\n%s", err, out)
	}
	if got[0].Pool != "live" || got[0].Check == nil || got[1].State != mesh.PoolMissing || got[2].State != mesh.PoolNotPool {
		t.Fatalf("unexpected elements: %+v", got)
	}
}

func TestPoolUpgradeAllPools_AppliesEachPoolAndContinuesPastFailures(t *testing.T) {
	live := allPoolsHost(t)
	// A second pool, registered first, whose upgrade fails: its agent has a
	// file where .claude/ must go, so re-wiring it errors.
	bad := filepath.Join(filepath.Dir(live), "bad")
	if _, err := mesh.InitPool(bad, "bad", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bad, "zed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "zed", mesh.ManifestName), []byte("name: zed\ntitle: zed\nrole: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "zed", ".claude"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "meshctl", "pools.json")
	b, err := os.ReadFile(reg)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Pools []mesh.PoolEntry `json:"pools"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Pools = append([]mesh.PoolEntry{{Name: "bad", Path: bad}}, doc.Pools...)
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(reg, b, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "pool", "upgrade", "--all-pools")
	if !strings.Contains(out, "=== pool bad (") || !strings.Contains(out, "=== pool live (") {
		t.Fatalf("every present pool gets a header:\n%s", out)
	}
	if !strings.Contains(out, "skipped pools:") {
		t.Fatalf("skipped pools not listed:\n%s", out)
	}
	// bad fails first; the walk still reaches live and upgrades it.
	if strings.Index(out, "=== pool bad (") > strings.Index(out, "=== pool live (") {
		t.Fatalf("registry order lost:\n%s", out)
	}
	if line(out, "✗ zed:") == "" {
		t.Fatalf("bad pool's error not reported:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(live, "alpha", "CLAUDE.local.md")); err != nil {
		t.Fatalf("live pool not upgraded after bad failed: %v\n%s", err, out)
	}
	var ce *mesh.CodedError
	if !errors.As(err, &ce) || ce.Code != 1 || !strings.Contains(ce.Msg, "bad") {
		t.Fatalf("want a coded exit naming the failed pool, got %v\n%s", err, out)
	}
	if !mesh.IsPool(live) {
		t.Fatal("live pool lost its marker")
	}
}

// snapshotTree lists every path under root with its size and mtime, outside
// .git directories (git refreshes its index on a read-only status).
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() && fi.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		if fi.IsDir() {
			fmt.Fprintf(&b, "%s/\n", rel)
		} else {
			fmt.Fprintf(&b, "%s %d %d\n", rel, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}
