package main

import (
	"fmt"
	"os"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// kbConfirmCmd is `meshctl kb confirm` (ADR-0070): bulk confirmation of
// reviewed entries from a reconcile report, on main, ledger and Confirm:
// commit in one step. agent and session are kb's shared persistent flags.
func kbConfirmCmd(agent, session *string) *cobra.Command {
	var report string
	var accept []string
	cmd := &cobra.Command{
		Use:   "confirm <path>[#<slug>]... --report <file>",
		Short: "Confirm up to five reviewed entries from a reconcile report (ledger + Confirm: commit on main)",
		Long: `Writes the ledger lines for the named entries and commits them as "Confirm: <report Intent>"
on main, in one step, with no KB branch. Gate-asked with the report's lines for these paths.
At most five paths per call; <path>#<slug> is one section of a sectioned file, a bare
sectioned path is every section and counts as one. Refuses an entry whose latest report
verdict is conflict, term-vs-fact or ungrounded (unless --accept-ungrounded <path>), a
non-owner "conflict → owner <peer>, pending" until the owner's ledger confirms the
counterpart, and an entry with no report line.`,
		Args: cobra.MinimumNArgs(1),
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
			sid := *session
			if sid == "" {
				sid = mesh.ResolveSessionID(os.Getpid(), now)
			}
			res, err := mesh.KBConfirm(dir, a, args, mesh.KBConfirmOptions{Report: report, AcceptUngrounded: accept, Session: sid, Now: now})
			if err != nil {
				return err
			}
			fmt.Printf("confirmed on main: %s Confirm: %s (%d ledger line(s) written)\n", res.Confirm, res.Intent, len(res.Ledgered))
			for _, k := range res.Ledgered {
				fmt.Println("  " + k)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&report, "report", "", "the reconcile report holding these entries' verdicts, <you>/.memory/reconcile/<date>.md (required)")
	cmd.Flags().StringArrayVar(&accept, "accept-ungrounded", nil, "a path whose ungrounded verdict the human overrode in the round (repeatable)")
	return cmd
}
