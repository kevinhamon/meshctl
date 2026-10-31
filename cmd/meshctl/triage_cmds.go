package main

import (
	"fmt"
	"os"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// triageCmd is `meshctl inbox triage [--peer <name>]`: with a peer
// (or from a peer's own workspace) it writes and prints the triage worksheet;
// from a hub with no peer it prints the pool-wide walk order and writes
// nothing. agent is the inbox parent's persistent --agent flag.
func triageCmd(agent *string) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Intake triage: worksheet over a peer's pending/in-progress requests, or the pool walk order",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			var a *mesh.Agent
			switch {
			case peer != "":
				if a, err = mesh.FindAgent(dir, peer); err != nil {
					return err
				}
			default:
				// The current peer when there is one; a hub resolves none.
				a, _ = resolveContextAgent(dir, *agent)
			}
			now := time.Now()
			out, err := mesh.InboxTriage(dir, a, mesh.TriageMode{}, now)
			if err != nil {
				return err
			}
			fmt.Print(out)
			if a != nil {
				fmt.Printf("\nwrote %s\n", mesh.TriageWorksheetPath(a.Dir(), now))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer whose open requests to triage (default: the current peer; none from a hub prints the walk order)")
	cmd.AddCommand(triageApplyCmd(agent))
	return cmd
}

// triageApplyCmd is `meshctl inbox triage apply --report <file> <id>...`:
// closes up to five confirmed requests per the triage report,
// through the respond path. Gate-asked with the report lines.
func triageApplyCmd(agent *string) *cobra.Command {
	var report string
	cmd := &cobra.Command{
		Use:   "apply --report <file> <id>...",
		Short: "Close confirmed triage verdicts (superseded/done → filed, withdrawn → declined), at most 5 ids",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, *agent)
			if err != nil {
				return err
			}
			now := time.Now()
			res, err := mesh.TriageApply(dir, a, mesh.TriageApplyOptions{
				Report: report, IDs: args, Session: mesh.ResolveAgentSessionID(a, os.Getpid(), now), Now: now,
			})
			if err != nil {
				return err
			}
			fmt.Print(mesh.TriageApplyMarkdown(res))
			return nil
		},
	}
	cmd.Flags().StringVar(&report, "report", "", "the triage report (<peer>/.memory/triage/<date>.md)")
	return cmd
}
