package main

import (
	"testing"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestBoolFlagsKnownToGate keeps the gate's list of meshctl boolean flags in
// step with the command tree: the gate reads any other flag without `=` as
// taking the next word, so a boolean it does not know would swallow the next
// flag when it parses a gated command line.
func TestBoolFlagsKnownToGate(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if f.Value.Type() == "bool" && !mesh.IsMeshctlBoolFlag(f.Name) {
				t.Errorf("%s --%s is boolean but the gate reads it as taking a value; add it to meshctlBoolFlags", c.CommandPath(), f.Name)
			}
		}
		c.LocalNonPersistentFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd())
}
