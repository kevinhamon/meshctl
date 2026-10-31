package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// kbReconcileCmd is `meshctl kb reconcile`. Without --peer it prints
// the walk order and writes nothing; with --peer it writes that canonical
// peer's dated worksheet and prints its path. asJSON is kb's shared --json.
func kbReconcileCmd(asJSON *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Print the reconciliation walk order, or write one canonical peer's worksheet (--peer)",
		Long: `Without --peer: the walk order across canonical peers — declared mutates surfaces,
conflicts in the latest reconcile report (conflicting pairs walked together), inbound
mesh:// links, unconfirmed entries; reconcile.order in the pool config overrides. Writes nothing.
With --peer: writes <peer>/.memory/reconcile/<date>.worksheet.md and prints its path.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			if peer != "" {
				path, err := mesh.KBReconcileWorksheet(dir, peer, time.Now())
				if err != nil {
					return err
				}
				fmt.Println(path)
				return nil
			}
			w, err := mesh.ReconcileWalkOrder(dir)
			if err != nil {
				return err
			}
			if *asJSON {
				out, err := json.MarshalIndent(w, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			fmt.Print(w.Markdown())
			if len(w.Skipped) > 0 {
				fmt.Fprintf(os.Stderr, "skipped (perspective): %s\n", strings.Join(w.Skipped, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "canonical peer to write the worksheet for (default: print the walk order)")
	return cmd
}
