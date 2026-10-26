package main

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// parity_test.go — the harness parity table (ADR-0075) cannot drift: every
// command is classified, and the README shows the table the code declares.
// The release workflow runs these tests before it builds anything.

const (
	parityBegin = "<!-- parity:begin (generated from internal/mesh/parity.go; do not edit by hand) -->\n"
	parityEnd   = "<!-- parity:end -->"
)

// commandPaths lists every runnable command path under root ("kb finish").
func commandPaths(root *cobra.Command) []string {
	var out []string
	var walk func(c *cobra.Command, prefix string)
	walk = func(c *cobra.Command, prefix string) {
		for _, s := range c.Commands() {
			if s.Hidden || s.Name() == "help" || s.Name() == "completion" {
				continue
			}
			p := strings.TrimSpace(prefix + " " + s.Name())
			if s.Runnable() {
				out = append(out, p)
			}
			walk(s, p)
		}
	}
	walk(root, "")
	sort.Strings(out)
	return out
}

func TestHarnessParity_EveryCommandClassified(t *testing.T) {
	classified := map[string]int{}
	for _, r := range mesh.HarnessParity {
		for _, c := range r.Commands {
			classified[c]++
		}
	}
	for _, c := range mesh.ParityNeutral {
		classified[c]++
	}
	real := map[string]bool{}
	for _, c := range commandPaths(rootCmd()) {
		real[c] = true
		switch classified[c] {
		case 0:
			t.Errorf("`meshctl %s` is not in the harness parity table: add it to a row of HarnessParity, or to ParityNeutral "+
				"if it works the same on every harness (internal/mesh/parity.go)", c)
		case 1:
		default:
			t.Errorf("`meshctl %s` is classified more than once in internal/mesh/parity.go", c)
		}
	}
	for c := range classified {
		if !real[c] {
			t.Errorf("internal/mesh/parity.go lists `meshctl %s`, which does not exist", c)
		}
	}
}

func TestHarnessParity_READMEUpToDate(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i, j := strings.Index(s, parityBegin), strings.Index(s, parityEnd)
	if i < 0 || j < i {
		t.Fatal("README.md lacks the parity markers")
	}
	if got, want := s[i+len(parityBegin):j], mesh.ParityMarkdown(); got != want {
		t.Fatalf("README.md parity table is out of date; replace the block between the markers with:\n\n%s", want)
	}
}
