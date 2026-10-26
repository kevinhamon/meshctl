package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/kevinhamon/meshctl/internal/console"
	"github.com/kevinhamon/meshctl/internal/selfupdate"
	"github.com/spf13/cobra"
)

// --- the console: a live, read-only view of the pool ---

func consoleCmd() *cobra.Command {
	var port int
	var open, printURL bool
	c := &cobra.Command{
		Use:   "console",
		Short: "Serve a live, read-only view of the pool on localhost (pending work, doctor, notices)",
		Long: "Runs until stopped, serving the pool's pending work, doctor findings and pool notices at\n" +
			"http://127.0.0.1:<port>/, updated within moments of the pool's files changing. It never changes\n" +
			"the pool. Settings (port, the secret token every request carries) live in the user config\n" +
			"directory (meshctl/console.json), created on first run. --print-url prints the address with\n" +
			"its token and exits; --open also opens it in the browser.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := console.LoadSettings()
			if err != nil {
				return err
			}
			if port != 0 {
				s.Port = port
			}
			if printURL {
				fmt.Println(s.URL())
				return nil
			}
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			eng := console.NewEngine(dir, resolveVersion())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- eng.Run(ctx) }()
			srv := &console.Server{Engine: eng, Settings: s, UI: console.UI()}
			fmt.Printf("meshctl console for %s\n  %s\n", dir, s.URL())
			if open {
				openBrowser(s.URL())
			}
			go func() { errc <- srv.ListenAndServe() }()
			return <-errc
		},
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Start the console at login (macOS LaunchAgent) for this pool, and start it now",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			exe, err := selfupdate.Executable()
			if err != nil {
				return err
			}
			p, err := console.InstallLoginItem(exe, dir)
			if err != nil {
				return err
			}
			s, err := console.LoadSettings()
			if err != nil {
				return err
			}
			log, _ := console.LoginItemLog()
			fmt.Printf("installed %s\n  runs %s console for %s at login (log: %s)\n  open: %s\n", p, exe, dir, log, s.URL())
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the console login item and remove it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, err := console.UninstallLoginItem()
			if err != nil {
				return err
			}
			if removed {
				fmt.Println("removed the console login item")
			} else {
				fmt.Println("no console login item installed")
			}
			return nil
		},
	}
	c.AddCommand(install, uninstall)
	c.Flags().IntVar(&port, "port", 0, fmt.Sprintf("port to listen on (default %d, or the saved setting)", console.DefaultPort))
	c.Flags().BoolVar(&open, "open", false, "open the console in the browser")
	c.Flags().BoolVar(&printURL, "print-url", false, "print the console's address with its token, then exit")
	return c
}

// openBrowser opens url in the default browser; best effort.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
