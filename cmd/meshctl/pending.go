package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// --- pending work (ADR-0071) ---

func pendingCmd() *cobra.Command {
	var agent string
	var all, asJSON bool
	c := &cobra.Command{
		Use:   "pending [--all]",
		Short: "Pending work: the session-start block for this peer, or --all per peer",
		Long: "Prints the `# Pending work` block the session-start hook injects: parked KB branches,\n" +
			"open conflicts, pending intake, read-back, delegated work and KB health, each with\n" +
			"its count, oldest age and clearing command. --all prints it per peer across the pool,\n" +
			"skipping peers with nothing pending. --json prints the versioned machine-readable form\n" +
			"(every peer with --all, including peers with nothing pending), with each line's status-line phrases.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			now := time.Now()
			if asJSON {
				var blocks []mesh.PendingBlock
				if all {
					if blocks, err = mesh.PendingAll(dir, now); err != nil {
						return err
					}
				} else {
					a, err := resolveContextAgent(dir, agent)
					if err != nil {
						return err
					}
					blocks = []mesh.PendingBlock{mesh.Pending(dir, a, now)}
				}
				return printJSON(mesh.NewPendingReport(blocks, now))
			}
			var out string
			if all {
				if out, err = mesh.PendingAllMarkdown(dir, now); err != nil {
					return err
				}
			} else {
				a, err := resolveContextAgent(dir, agent)
				if err != nil {
					return err
				}
				out = mesh.PendingMarkdown(dir, a, now)
			}
			if out == "" {
				fmt.Println("nothing pending")
				return nil
			}
			fmt.Println(out)
			return nil
		},
	}
	c.Flags().StringVar(&agent, "agent", "", "peer (default: the agent whose workspace you are in)")
	c.Flags().BoolVar(&all, "all", false, "every peer in the pool")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable report (versioned schema)")
	return c
}

// printJSON writes v as indented JSON on stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
