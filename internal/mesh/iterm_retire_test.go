package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeShippedITerm lays down the last iTerm2 profile files meshctl shipped,
// rendered for one agent the way the old scaffold did.
func writeShippedITerm(t *testing.T, dir string) {
	t.Helper()
	read := func(n string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "iterm", n))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	values := strings.NewReplacer("__NAME__", "architect", "__BADGE__", "ARCH").Replace(read("values.yaml"))
	values = strings.Replace(values, "  red: 0.50", "  red: 0.2", 1)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, retiredITermDir), 0o755))
	must(os.WriteFile(filepath.Join(dir, retiredTaskfile), []byte(read("Taskfile.yaml")), 0o644))
	must(os.WriteFile(filepath.Join(dir, retiredITermProfile), []byte(read("profile.json.tmpl")), 0o644))
	must(os.WriteFile(filepath.Join(dir, retiredITermValues), []byte(values), 0o644))
}

func TestRetireITerm_RemovesShippedFiles(t *testing.T) {
	dir := t.TempDir()
	writeShippedITerm(t, dir)
	if got := planITermRetire(dir); len(got) != 3 {
		t.Fatalf("plan: %v", got)
	}
	notes, err := retireITerm(dir)
	if err != nil || len(notes) != 1 {
		t.Fatalf("%v %v", notes, err)
	}
	for _, p := range []string{retiredTaskfile, retiredITermDir, retiredITermValues} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s still there", p)
		}
	}
	if again, _ := retireITerm(dir); again != nil {
		t.Fatalf("second run must be a no-op: %v", again)
	}
}

func TestRetireITerm_KeepsCustomisedProfile(t *testing.T) {
	for name, customise := range map[string]func(dir string) error{
		"added image": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, retiredITermDir, "bg.png"), []byte("png"), 0o644)
		},
		"edited values": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, retiredITermValues), []byte("profile:\n  name: x\n"), 0o644)
		},
		"edited profile": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, retiredITermProfile), []byte("{}"), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeShippedITerm(t, dir)
			if err := customise(dir); err != nil {
				t.Fatal(err)
			}
			if got := planITermRetire(dir); got != nil {
				t.Fatalf("a customised profile and its Taskfile stay: %v", got)
			}
		})
	}
}

func TestRetireITerm_LeavesForeignTaskfile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, retiredTaskfile), []byte("version: \"3\"\ntasks:\n  build: {cmds: [go build]}\n"), 0o644)
	if got := planITermRetire(dir); got != nil {
		t.Fatalf("someone's own Taskfile is not meshctl's: %v", got)
	}
}

func TestNew_WritesNoTaskfileOrITerm(t *testing.T) {
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "architect", true, nil)
	for _, p := range []string{retiredTaskfile, retiredITermDir, retiredITermValues} {
		if _, err := os.Stat(filepath.Join(a.Dir(), p)); !os.IsNotExist(err) {
			t.Errorf("new agent has %s", p)
		}
	}
}
