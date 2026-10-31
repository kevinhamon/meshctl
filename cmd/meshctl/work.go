package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// --- delegated work ---

func workCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "work",
		Short: "Delegated-work branches: list the pending mesh/work/<id> worktrees, merge or discard one",
		Long: "When a hub session delegates work (`ask --in-session --work`) to a peer whose own session is\n" +
			"live, the work lands on a mesh/work/<id> branch in a worktree instead of the peer's checkout.\n" +
			"list shows them; merge rebases one onto the peer's main, fast-forwards it and removes the\n" +
			"worktree (only from the peer's own session while it is live); --abort discards it.",
	}

	var agent string
	var asJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "Pending delegated-work branches (all agents, or --agent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			ws := mesh.ListWork(dir, agent)
			if asJSON {
				out, err := json.MarshalIndent(ws, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			if len(ws) == 0 {
				fmt.Println("no delegated work pending merge")
				return nil
			}
			for _, w := range ws {
				ahead := "?"
				if w.Ahead >= 0 {
					ahead = fmt.Sprint(w.Ahead)
				}
				dirty := ""
				if w.Dirty {
					dirty = ", uncommitted changes"
				}
				fmt.Printf("%s  %s  %s commit(s) ahead%s, since %s\n  %s\n",
					w.Agent, w.Request, ahead, dirty, w.Modified.Format("2006-01-02 15:04"), w.Worktree)
			}
			return nil
		},
	}
	list.Flags().StringVar(&agent, "agent", "", "only this agent's branches")
	list.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	var abort bool
	var mergeAgent string
	merge := &cobra.Command{
		Use:   "merge <request-id>",
		Short: "Rebase + ff-only merge a delegated-work branch into the peer's workspace (or --abort to discard it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			caller := ""
			if a, err := resolveContextAgent(dir, mergeAgent); err == nil {
				caller = a.Name
			}
			w, err := mesh.WorkMerge(dir, args[0], mesh.WorkMergeOptions{Abort: abort, Caller: caller, Now: time.Now()})
			if errors.Is(err, mesh.ErrWorkConflict) {
				fmt.Fprintln(os.Stderr, err.Error())
				return &mesh.CodedError{Code: 1, Msg: "work merge: conflict — needs human"}
			}
			if err != nil {
				return err
			}
			if abort {
				fmt.Printf("%s discarded (%s); worktree removed\n", w.Branch, w.Agent)
			} else {
				fmt.Printf("%s merged into %s's main; worktree removed\n", w.Branch, w.Agent)
			}
			return nil
		},
	}
	merge.Flags().BoolVar(&abort, "abort", false, "discard the branch + worktree instead of merging")
	merge.Flags().StringVar(&mergeAgent, "agent", "", "the agent running the merge (default: $AGENT_NAME or cwd)")

	c.AddCommand(list, merge)
	return c
}
