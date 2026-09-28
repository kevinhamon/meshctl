package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// readText returns inline text, else the contents of file ("-" = stdin).
func readText(inline, file string) (string, error) {
	if file == "" {
		return inline, nil
	}
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return "", err
	}
	if inline != "" {
		return inline + "\n\n" + string(b), nil
	}
	return string(b), nil
}

func respondCmd() *cobra.Command {
	var agent, status, response, responseFile string
	cmd := &cobra.Command{
		Use:   "respond <id> --status <status> --response \"…\"",
		Short: "Write your outcome back into a request in your inbox (status + response section)",
		Long: "The owner's write-back. Sets status (in-progress | needs-info | needs-human | answered | filed | declined),\n" +
			"stamps `responded`, and appends a response section. intake/ is meshctl-owned — use this, never hand edits.\n" +
			"Use --response-file - to read a long response from stdin.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			text, err := readText(response, responseFile)
			if err != nil {
				return err
			}
			now := time.Now()
			r, err := mesh.RespondRequest(a, args[0], mesh.RespondOptions{
				Status: status, Response: text, Session: mesh.ResolveAgentSessionID(a, os.Getpid(), now), Now: now,
			})
			if err != nil {
				return err
			}
			fmt.Printf("%s → %s (%s)\n", r.ID, r.Status, r.Path())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	f.StringVar(&status, "status", "", "new status (required)")
	f.StringVar(&response, "response", "", "response markdown")
	f.StringVar(&responseFile, "response-file", "", "read the response from a file (- = stdin)")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

func declineCmd() *cobra.Command {
	var agent, reason, redirect, typ string
	cmd := &cobra.Command{
		Use:   "decline <id> --reason \"…\" [--redirect <agent>]",
		Short: "Decline a request that is not yours; optionally forward it to the agent that owns it",
		Long: "Sets status: declined with the reason. With --redirect, re-sends the original ask to that agent\n" +
			"(original requester kept as `from`, linked back via `related`) — send-only, never dispatches.\n" +
			"The requester sees the decline via `meshctl sent`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			now := time.Now()
			orig, fwd, err := mesh.DeclineRequest(dir, a, args[0], mesh.DeclineOptions{
				Reason: reason, RedirectTo: redirect, Type: typ,
				Session: mesh.ResolveAgentSessionID(a, os.Getpid(), now), Now: now,
			})
			if fwd != nil {
				fmt.Printf("redirected → %s as %s (%s)\n", fwd.To, fwd.ID, fwd.Path())
			}
			if err != nil {
				return err
			}
			fmt.Printf("%s → declined (%s)\n", orig.ID, orig.Path())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	f.StringVar(&reason, "reason", "", "why this is not yours (required)")
	f.StringVar(&redirect, "redirect", "", "forward the request to this agent")
	f.StringVar(&typ, "type", "", "request type for the redirect (default: the original's)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

// sentCmd — the requester's side of the loop: what came back from peers.
func sentCmd() *cobra.Command {
	var agent string
	var all bool
	from := func() (string, string, error) {
		dir, err := agentsDir()
		if err != nil {
			return "", "", err
		}
		if agent != "" {
			return dir, agent, nil
		}
		return dir, mesh.CallerName(), nil
	}
	cmd := &cobra.Command{
		Use:   "sent [ack <id>]",
		Short: "Requests you sent that peers have answered, declined, or bounced back (awaiting your read-back)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, name, err := from()
			if err != nil {
				return err
			}
			sent, err := mesh.ListSent(dir, name, !all)
			if err != nil {
				return err
			}
			if len(sent) == 0 {
				fmt.Println("(nothing awaiting read-back)")
				return nil
			}
			for _, r := range sent {
				st, _ := mesh.CanonicalStatus(r.Status)
				extra := ""
				if r.RedirectedTo != "" {
					extra = "\tredirected=" + r.RedirectedTo
				}
				if r.ReadBack != "" {
					extra += "\tread_back=" + r.ReadBack
				}
				fmt.Printf("%s\tto=%s\tstatus=%s%s\t%s\n", r.ID, r.To, st, extra, r.Path())
			}
			return nil
		},
	}
	ack := &cobra.Command{
		Use:   "ack <id>...",
		Short: "Mark peers' responses as read back (clears them from `sent` and the session-start reminder)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, name, err := from()
			if err != nil {
				return err
			}
			var errs []string
			for _, id := range args {
				if _, err := mesh.AckSent(dir, name, id, time.Now()); err != nil {
					errs = append(errs, err.Error())
					continue
				}
				fmt.Printf("acked %s\n", id)
			}
			if len(errs) > 0 {
				return fmt.Errorf("%s", strings.Join(errs, "; "))
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&agent, "agent", "", "requester name (default: $AGENT_NAME or cwd)")
	cmd.Flags().BoolVar(&all, "all", false, "every request you sent that is still in a live inbox")
	cmd.AddCommand(ack)
	return cmd
}
