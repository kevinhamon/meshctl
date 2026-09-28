// Command meshctl is the tool of record for the agent mesh:
// scaffold/onboard agents, emit the roster on demand, message peers
// via file-based intake, dispatch advisory peers headlessly with anti-cascade
// guards, and coordinate concurrent sessions (presence, atomic claims, KB
// worktrees).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/spf13/cobra"
)

// version is overridable at build time (release workflow: -X main.version=<tag>).
// When empty (e.g. `go install`), it's resolved from the module build info.
var version = ""

// resolveVersion prefers the ldflags value, else the module version from build
// info (set by `go install …@vX`), else a dev marker with the VCS revision.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		rev, dirty := "", ""
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return "devel+" + rev + dirty
		}
	}
	return "devel"
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the meshctl version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("meshctl %s (%s %s/%s)\n", resolveVersion(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		var ce *mesh.CodedError
		if errors.As(err, &ce) {
			fmt.Fprintln(os.Stderr, "meshctl: "+ce.Msg)
			os.Exit(ce.Code)
		}
		fmt.Fprintln(os.Stderr, "meshctl: "+err.Error())
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "meshctl",
		Short:         "meshctl — the command-line tool for the agent mesh",
		Version:       resolveVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("meshctl {{.Version}}\n")
	root.AddCommand(
		versionCmd(),
		// Resource groups (noun → verb).
		poolCmd(),  // init | upgrade | list
		agentCmd(), // new | onboard | list
		msgCmd(),   // ask | send
		inboxCmd(), // list | next | claim | release
		kbCmd(), memoryCmd(), sessionCmd(), handoffCmd(), intuitionCmd(), hookCmd(),
		doctorCmd(),
		// Top-level aliases for high-frequency verbs (muscle memory + one preapproval).
		askCmd(), sendCmd(),
	)
	return root
}

// --- resource-group parents ---

func poolCmd() *cobra.Command {
	c := &cobra.Command{Use: "pool", Short: "Pool lifecycle: init, upgrade doctrine, list/register/forget"}
	c.AddCommand(poolInitCmd(), poolUpgradeCmd(), poolListCmd(), poolRegisterCmd(), poolForgetCmd(), poolMigrateCmd(), poolTeardownCmd())
	return c
}

func poolMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Reconcile a legacy pool layout to the consolidated .agentmesh/ (ADR-0038)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			did, err := mesh.MigratePool(dir)
			if err != nil {
				return err
			}
			fmt.Printf("pool: %s\n", dir)
			for _, d := range did {
				fmt.Println("  " + d)
			}
			return nil
		},
	}
}

func poolTeardownCmd() *cobra.Command {
	var purge bool
	c := &cobra.Command{
		Use:   "teardown",
		Short: "Remove the pool's mesh infra (.agentmesh/) + deregister; agent repos are kept unless --purge",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			if purge {
				fmt.Fprintln(os.Stderr, "⚠ --purge will DELETE agent workspaces (and any UNPUSHED knowledge) in "+dir)
			}
			did, err := mesh.TeardownPool(dir, purge)
			if err != nil {
				return err
			}
			fmt.Printf("torn down pool: %s\n", dir)
			for _, d := range did {
				fmt.Println("  " + d)
			}
			if !purge {
				fmt.Println("  (agent repos left in place — remove manually if desired)")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&purge, "purge", false, "ALSO delete agent workspaces (destructive — unpushed knowledge is lost)")
	return c
}

func agentCmd() *cobra.Command {
	c := &cobra.Command{Use: "agent", Short: "Agent lifecycle: scaffold (new), onboard existing, bootstrap the steward, list roster, print identity"}
	c.AddCommand(newCmd(), onboardCmd(), bootstrapCmd(), directoryCmd(), identityCmd())
	return c
}

// identityCmd prints a portable per-agent terminal identity: an ANSI title-set plus a
// colored badge banner from the manifest. Works in any terminal on any OS — the
// cross-platform replacement for the optional macOS iTerm2 profile.
func identityCmd() *cobra.Command {
	var agent string
	c := &cobra.Command{
		Use:   "identity",
		Short: "Print a portable terminal title + badge for an agent (any terminal, any OS)",
		Long: "Emits ANSI escapes that set the terminal title to the agent name and print a\n" +
			"colored badge from its manifest. Run it on shell entry in an agent workspace\n" +
			"(from your shell rc or a direnv .envrc), or once per session.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			fmt.Print(mesh.TerminalIdentity(a))
			return nil
		},
	}
	c.Flags().StringVar(&agent, "agent", "", "agent name (default: $AGENT_NAME or cwd basename)")
	return c
}

// bootstrapCmd scaffolds the mesh steward (onboarding hub) into the current pool
// (ADR-0035). Idempotent; use when a pool was created with --no-steward or to add
// the hub to a pre-existing pool.
func bootstrapCmd() *cobra.Command {
	var harness string
	c := &cobra.Command{
		Use:   "bootstrap",
		Short: "Scaffold the mesh steward (interviews the user + builds the agent roster)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			sdir, notes, err := mesh.ScaffoldSteward(dir, harness, time.Now())
			if err != nil {
				return err
			}
			for _, n := range notes {
				fmt.Println("  " + n)
			}
			fmt.Printf("steward: %s\n", sdir)
			fmt.Println("next: run your harness on the steward dir — it will interview you and build the roster")
			return nil
		},
	}
	c.Flags().StringVar(&harness, "harness", "", "steward harness: claude|opencode (default: claude)")
	return c
}

func msgCmd() *cobra.Command {
	c := &cobra.Command{Use: "msg", Short: "Message a peer: ask (dispatch + block) or send (write-only)"}
	c.AddCommand(askCmd(), sendCmd())
	return c
}

// --- shared helpers ---

func agentsDir() (string, error) { return mesh.AgentsDir() }

// resolveContextAgent finds the agent whose workspace we act within (session,
// inbox, claim, kb). Order: --agent flag, else $AGENT_NAME/cwd basename.
func resolveContextAgent(dir, flagAgent string) (*mesh.Agent, error) {
	name := flagAgent
	if name == "" {
		name = mesh.CallerName()
	}
	if a, err := mesh.FindAgent(dir, name); err == nil {
		return a, nil
	}
	// No manifest for the resolved name. When no explicit --agent was given, cwd
	// may be a KB worktree (<agentsDir>/.worktrees/<agent>-<session>) whose
	// basename isn't an agent — recover the real agent from it before failing.
	if flagAgent == "" {
		if rec := mesh.AgentFromWorktreeCwd(dir); rec != "" {
			return mesh.FindAgent(dir, rec)
		}
	}
	return mesh.FindAgent(dir, name) // return the original "has no manifest" error
}

func parseRGB(s string) (r, g, b float64, err error) {
	if s == "" {
		return 0.5, 0.5, 0.5, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("--rgb wants r,g,b (0..1), got %q", s)
	}
	vals := make([]float64, 3)
	for i, p := range parts {
		vals[i], err = strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("--rgb component %q not a number", p)
		}
	}
	return vals[0], vals[1], vals[2], nil
}

// parseAccepts turns "guidance:desc,review:desc,fyi" into []Accept.
func parseAccepts(s string) []mesh.Accept {
	if s == "" {
		return nil
	}
	var out []mesh.Accept
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		t, d, _ := strings.Cut(item, ":")
		out = append(out, mesh.Accept{Type: strings.TrimSpace(t), Desc: strings.TrimSpace(d)})
	}
	return out
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// --- init ---

func poolInitCmd() *cobra.Command {
	var name, harness string
	var noSteward bool
	c := &cobra.Command{
		Use:   "init [path]",
		Short: "Create a new isolated pool at <path> (default: current directory), or repair it if it already is one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			// Target the given path, else the CURRENT directory. No path never
			// falls back to ~/agents (ADR-0037) — bare `pool init` means "here".
			target := "."
			if len(args) == 1 {
				target = args[0]
			}
			abs, err := filepath.Abs(target)
			if err != nil {
				return err
			}

			// Already a pool → repair (idempotent); never re-scaffold the steward.
			if mesh.IsPool(abs) {
				did, err := mesh.InitMesh(abs)
				if err != nil {
					return err
				}
				if _, err := mesh.RegisterPool(abs, name, now); err != nil {
					return err
				}
				fmt.Printf("pool (repaired): %s\n", abs)
				for _, d := range did {
					fmt.Println("  " + d)
				}
				return nil
			}

			// Fresh pool: emit doctrine + runtime + marker, register, scaffold steward.
			did, err := mesh.InitPool(abs, name, now)
			if err != nil {
				return err
			}
			if _, err := mesh.RegisterPool(abs, name, now); err != nil {
				return err
			}
			fmt.Printf("initialized pool: %s\n", abs)
			for _, d := range did {
				fmt.Println("  " + d)
			}
			if !noSteward {
				sdir, snotes, err := mesh.ScaffoldSteward(abs, harness, now)
				if err != nil {
					return err
				}
				for _, n := range snotes {
					fmt.Println("  " + n)
				}
				fmt.Printf("next: cd %s && run your harness on the steward — it will interview you and build the roster\n", sdir)
				return nil
			}
			fmt.Printf("next: cd %s && meshctl agent new <agent>\n", abs)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "pool identity name (default: pool dir basename)")
	c.Flags().StringVar(&harness, "harness", "", "harness for the scaffolded steward: claude|opencode (default: claude)")
	c.Flags().BoolVar(&noSteward, "no-steward", false, "do not scaffold the onboarding-hub steward agent")
	return c
}

func poolUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Re-emit the embedded doctrine into the current pool from this binary",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			did, err := mesh.UpgradeDoctrine(dir)
			if err != nil {
				return err
			}
			if len(did) == 0 {
				fmt.Println("doctrine already current")
				return nil
			}
			fmt.Printf("pool: %s\n", dir)
			for _, d := range did {
				fmt.Println("  " + d)
			}
			return nil
		},
	}
}

func poolListCmd() *cobra.Command {
	var current bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List all registered pools on this machine (--current shows just the pool you're in)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if current {
				dir, err := agentsDir()
				if err != nil {
					return err
				}
				fmt.Printf("pool root: %s\n", dir)
				if b, err := os.ReadFile(mesh.PoolMarkerPath(dir)); err == nil {
					fmt.Print(string(b))
				} else {
					fmt.Println("(no .agentmesh/pool.yaml marker — run `meshctl pool init`)")
				}
				return nil
			}
			pools, err := mesh.RegisteredPools()
			if err != nil {
				return err
			}
			if len(pools) == 0 {
				fmt.Println("(no pools registered — `meshctl pool init <path>` or `meshctl pool register <path>`)")
				return nil
			}
			for _, e := range pools {
				status := ""
				if !mesh.PoolIsLive(e) {
					status = "  ⚠ MISSING (`meshctl pool forget`)"
				}
				fmt.Printf("%-20s %s%s\n", e.Name, e.Path, status)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&current, "current", false, "show the pool the current directory belongs to")
	return c
}

func poolRegisterCmd() *cobra.Command {
	var name string
	c := &cobra.Command{
		Use:   "register [path]",
		Short: "Register an existing pool (default: current directory) into the machine registry",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) == 1 {
				target = args[0]
			}
			abs, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			if !mesh.IsPool(abs) {
				return fmt.Errorf("%s is not a pool (no .agentmesh/ marker) — `meshctl pool init` first", abs)
			}
			if _, err := mesh.RegisterPool(abs, name, time.Now()); err != nil {
				return err
			}
			fmt.Printf("registered pool: %s\n", abs)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "registry name (default: dir basename)")
	return c
}

func poolForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <name|path>",
		Short: "Remove a pool from the registry (does not touch the pool on disk)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ok, err := mesh.ForgetPool(args[0])
			if err != nil {
				return err
			}
			if !ok {
				fmt.Printf("no registry entry matching %q\n", args[0])
				return nil
			}
			fmt.Printf("forgot %q (pool on disk untouched)\n", args[0])
			return nil
		},
	}
}

// --- new ---

func newCmd() *cobra.Command {
	var (
		title, model, rgb, repo string
		badge, role             string
		owns, domains, accepts  string
		mutates                 string
		dispatchable            bool
		iterm                   bool
		harness                 string
		bare                    bool
	)
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a new agent workspace (manifest + intake + CLAUDE.md)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			r, g, b, err := parseRGB(rgb)
			if err != nil {
				return err
			}
			name := args[0]
			if badge == "" {
				badge = strings.ToUpper(name)
			}
			spec := mesh.AgentSpec{
				Name: name, Title: title, Role: role,
				Owns: splitCSV(owns), Domains: splitCSV(domains),
				Accepts: parseAccepts(accepts), Dispatchable: dispatchable,
				Mutates: splitCSV(mutates), Model: model, Repo: repo, Harness: harness, Bare: bare,
				Badge: mesh.Badge{Label: badge, R: r, G: g, B: b},
			}
			dest, err := mesh.New(dir, spec, mesh.ScaffoldOptions{ITerm: iterm})
			if err != nil {
				return err
			}
			fmt.Printf("scaffolded %s\n", dest)
			fmt.Println("next: (1) flesh out CLAUDE.md body  (2) cd " + dest +
				" && task install  (3) create its git remote  (4) for a per-agent terminal badge, " +
				"run `meshctl agent identity` on shell entry (portable — any terminal, any OS)")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "human title, e.g. \"Software Architect\"")
	f.StringVar(&role, "role", "", "one-paragraph role")
	f.StringVar(&badge, "badge", "", "badge label (default: UPPERCASE name)")
	f.StringVar(&rgb, "rgb", "0.5,0.5,0.5", "badge color r,g,b (0..1)")
	f.StringVar(&model, "model", "", "model pin, e.g. claude-haiku-4-5 (default: inherit)")
	f.StringVar(&owns, "owns", "", "comma-separated ownership tags")
	f.StringVar(&domains, "domains", "", "comma-separated capability/domain tags")
	f.StringVar(&accepts, "accepts", "", "accepted request types: type:desc,type:desc")
	f.StringVar(&mutates, "mutates", "", "external systems written (tracker,code,prod); non-empty ⇒ not dispatchable")
	f.BoolVar(&dispatchable, "dispatchable", false, "may `meshctl ask` run it headlessly (ignored if mutates set)")
	f.StringVar(&repo, "repo", "", "git remote URL")
	f.StringVar(&harness, "harness", "", "coding-agent harness: claude|opencode (default: claude for new; autodetect for onboard)")
	f.BoolVar(&iterm, "iterm2", false, "also write a macOS iTerm2 dynamic profile (.iterm2/) for window identity")
	f.BoolVar(&bare, "bare", false, "do not append the '-agent' repo suffix to the name")
	return cmd
}

// --- onboard ---

func onboardCmd() *cobra.Command {
	var (
		title, model, rgb, repo string
		badge, role             string
		owns, domains, accepts  string
		mutates                 string
		dispatchable            bool
		iterm                   bool
		harness                 string
	)
	cmd := &cobra.Command{
		Use:   "onboard <name|path>",
		Short: "Upgrade a pre-existing agent directory into a mesh member (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			r, g, b, err := parseRGB(rgb)
			if err != nil {
				return err
			}
			spec := mesh.AgentSpec{
				Title: title, Role: role,
				Owns: splitCSV(owns), Domains: splitCSV(domains),
				Accepts: parseAccepts(accepts), Dispatchable: dispatchable,
				Mutates: splitCSV(mutates), Model: model, Repo: repo, Harness: harness,
			}
			if badge != "" || cmd.Flags().Changed("rgb") {
				spec.Badge = mesh.Badge{Label: badge, R: r, G: g, B: b}
			}
			res, err := mesh.Onboard(dir, args[0], spec, mesh.ScaffoldOptions{ITerm: iterm})
			if err != nil {
				return err
			}
			fmt.Printf("onboard %s:\n", res.Dir)
			if res.AddedManifest {
				fmt.Println("  + wrote agent.yaml")
				if res.InferredTitle != "" {
					fmt.Printf("      inferred title: %s\n", res.InferredTitle)
				}
				if res.InferredRole {
					fmt.Println("      inferred role from CLAUDE.md ## Role")
				}
			}
			if res.AddedIntake {
				fmt.Println("  + created intake/ (+ README.md)")
			}
			if res.AddedCommsSec {
				fmt.Println("  + injected Agent Communication (intake) section into CLAUDE.md")
			}
			for _, n := range res.Notes {
				fmt.Println("  · " + n)
			}
			if !res.AddedManifest && !res.AddedIntake && !res.AddedCommsSec && len(res.Notes) == 0 {
				fmt.Println("  (nothing to add — already a full mesh member)")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "override inferred title")
	f.StringVar(&role, "role", "", "override inferred role")
	f.StringVar(&badge, "badge", "", "badge label")
	f.StringVar(&rgb, "rgb", "0.5,0.5,0.5", "badge color r,g,b (0..1)")
	f.StringVar(&model, "model", "", "model pin")
	f.StringVar(&owns, "owns", "", "comma-separated ownership tags")
	f.StringVar(&domains, "domains", "", "comma-separated domain tags")
	f.StringVar(&accepts, "accepts", "", "accepted request types: type:desc,type:desc")
	f.StringVar(&mutates, "mutates", "", "external systems written; non-empty ⇒ not dispatchable")
	f.BoolVar(&dispatchable, "dispatchable", false, "may be dispatched headlessly (ignored if mutates set)")
	f.StringVar(&repo, "repo", "", "git remote URL")
	f.StringVar(&harness, "harness", "", "coding-agent harness: claude|opencode (default: claude for new; autodetect for onboard)")
	f.BoolVar(&iterm, "iterm2", false, "also write a macOS iTerm2 dynamic profile (.iterm2/) if missing")
	return cmd
}

// --- directory ---

func directoryCmd() *cobra.Command {
	var asJSON, asMD bool
	var write string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Emit the roster on demand from a live scan of agent.yaml manifests",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			agents, loadErrs, err := mesh.ScanAgents(dir)
			if err != nil {
				return err
			}
			for name, e := range loadErrs {
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", name, e)
			}
			if asJSON {
				out, err := mesh.DirectoryJSON(agents)
				if err != nil {
					return err
				}
				fmt.Println(out)
				return nil
			}
			md := mesh.DirectoryMarkdown(agents)
			if write != "" {
				content := mesh.GeneratedBegin + "\n\n" + md + "\n" + mesh.GeneratedEnd + "\n"
				if err := os.WriteFile(write, []byte(content), 0o644); err != nil {
					return err
				}
				fmt.Printf("wrote generated roster snapshot to %s\n", write)
				return nil
			}
			fmt.Print(md)
			_ = asMD
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asMD, "md", false, "markdown table (default)")
	f.BoolVar(&asJSON, "json", false, "machine-readable JSON for routing")
	f.StringVar(&write, "write", "", "write a generated snapshot to a file (marked non-authoritative)")
	return cmd
}

// --- ask / send ---

func askCmd() *cobra.Command {
	var detach bool
	var priority, from, related string
	cmd := &cobra.Command{
		Use:   "ask <target> <type> <prompt>",
		Short: "Write intake + dispatch an advisory peer headlessly, block for the answer",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			target, err := mesh.FindAgent(dir, args[0])
			if err != nil {
				return &mesh.CodedError{Code: mesh.ExitUnknownTarget, Msg: err.Error()}
			}
			_, rc, err := mesh.Ask(dir, target, mesh.AskOptions{
				Type: args[1], Prompt: args[2], From: from,
				Priority: priority, Related: splitCSV(related), Detach: detach,
				Now: time.Now(),
			})
			if err != nil {
				return err
			}
			if rc != 0 {
				os.Exit(rc)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&detach, "detach", false, "write-only + return the id to poll later (no blocking dispatch)")
	f.StringVar(&priority, "priority", "P2", "P1|P2|P3")
	f.StringVar(&from, "from", "", "requester name (default: $AGENT_NAME or cwd)")
	f.StringVar(&related, "related", "", "comma-separated refs (decision/risk/tracker keys)")
	return cmd
}

func sendCmd() *cobra.Command {
	var priority, from, related string
	cmd := &cobra.Command{
		Use:   "send <target> <type> <prompt>",
		Short: "Write-only intake (no dispatch) — for state-mutating, human-triggered peers",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			target, err := mesh.FindAgent(dir, args[0])
			if err != nil {
				return &mesh.CodedError{Code: mesh.ExitUnknownTarget, Msg: err.Error()}
			}
			fromName := from
			if fromName == "" {
				fromName = mesh.CallerName()
			}
			req, err := mesh.WriteRequest(target, args[1], args[2], fromName, priority, splitCSV(related), time.Now())
			if err != nil {
				return err
			}
			fmt.Printf("wrote intake %s → %s (%s)\n", req.ID, target.Name, req.Path())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&priority, "priority", "P2", "P1|P2|P3")
	f.StringVar(&from, "from", "", "requester name (default: $AGENT_NAME or cwd)")
	f.StringVar(&related, "related", "", "comma-separated refs")
	return cmd
}

// --- doctor ---

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate every manifest; non-zero exit on any error",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			findings, ok := mesh.Doctor(dir)
			if len(findings) == 0 {
				fmt.Println("doctor: all manifests valid ✓")
				return nil
			}
			for _, f := range findings {
				mark := "  "
				if f.Level == "error" {
					mark = "✗ "
				} else if f.Level == "warn" {
					mark = "⚠ "
				}
				fmt.Printf("%s[%s] %s\n", mark, f.Agent, f.Message)
			}
			if !ok {
				return &mesh.CodedError{Code: 1, Msg: "doctor found errors"}
			}
			fmt.Println("doctor: no errors (warnings above)")
			return nil
		},
	}
}

// --- session ---

func sessionCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "session start|end|list",
		Short: "Register/deregister session presence; warn on workspace overlap",
		Args:  cobra.ExactArgs(1),
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
			id := mesh.ResolveSessionID(os.Getpid(), now)
			switch args[0] {
			case "start":
				warn, err := mesh.SessionStart(a, id, os.Getpid(), now)
				if err != nil {
					return err
				}
				fmt.Printf("session %s registered in %s\n", id, a.Name)
				if warn != "" {
					fmt.Fprintln(os.Stderr, warn)
				}
			case "end":
				if err := mesh.SessionEnd(a, id); err != nil {
					return err
				}
				fmt.Printf("session %s deregistered\n", id)
			case "beat":
				if err := mesh.SessionBeat(a, id, now); err != nil {
					return err
				}
			case "list":
				for _, s := range mesh.SessionList(a, now) {
					claim := s.CurrentClaim
					if claim == "" {
						claim = "-"
					}
					fmt.Printf("%s pid=%d claim=%s\n", s.ID, s.PID, claim)
				}
			default:
				return fmt.Errorf("session: want start|end|list|beat, got %q", args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	return cmd
}

// --- inbox ---

func inboxCmd() *cobra.Command {
	var agent string
	parent := &cobra.Command{Use: "inbox", Short: "Inbox: list pending, claim the next, or claim/release a request"}

	inboxList := func(cmd *cobra.Command, args []string) error {
		dir, err := agentsDir()
		if err != nil {
			return err
		}
		a, err := resolveContextAgent(dir, agent)
		if err != nil {
			return err
		}
		reqs, err := mesh.ListRequests(a.InboxPath())
		if err != nil {
			return err
		}
		if len(reqs) == 0 {
			fmt.Println("(inbox empty)")
			return nil
		}
		for _, r := range reqs {
			fmt.Printf("%s\tfrom=%s\ttype=%s\tstatus=%s\tpri=%s\n", r.ID, r.From, r.Type, r.Status, r.Priority)
		}
		return nil
	}

	list := &cobra.Command{Use: "list", Short: "List pending requests", RunE: inboxList}

	var claim bool
	next := &cobra.Command{
		Use:   "next",
		Short: "Return the next pending request (with --claim, atomically claim it)",
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
			id := mesh.ResolveAgentSessionID(a, os.Getpid(), now)
			r, err := mesh.InboxNext(a, id, os.Getpid(), claim, now)
			if err != nil {
				return err
			}
			if r == nil {
				fmt.Println("(no pending requests)")
				return nil
			}
			fmt.Printf("%s\t%s\tfrom=%s\ttype=%s\tstatus=%s\n", r.ID, r.Path(), r.From, r.Type, r.Status)
			return nil
		},
	}
	next.Flags().BoolVar(&claim, "claim", false, "atomically claim the returned request")

	var archiveAnswered bool
	archive := &cobra.Command{
		Use:   "archive [id]",
		Short: "Archive a closed request out of the inbox (distill its residue to the KB FIRST) — ADR-0043",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			if archiveAnswered {
				ids, err := mesh.ArchiveAnswered(a)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					fmt.Println("(no answered requests to archive)")
					return nil
				}
				fmt.Printf("archived %d answered request(s): %s\n", len(ids), strings.Join(ids, ", "))
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("give a request id, or use --answered")
			}
			out, err := mesh.ArchiveRequest(a, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("archived %s → %s\n", args[0], out)
			return nil
		},
	}
	archive.Flags().BoolVar(&archiveAnswered, "answered", false, "archive every request whose status is 'answered'")

	// Bare `inbox` lists.
	parent.RunE = inboxList
	parent.PersistentFlags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	parent.AddCommand(list, next, claimCmd(), releaseCmd(), archive)
	return parent
}

func claimCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "claim <id>",
		Short: "Explicitly claim a request in this workspace",
		Args:  cobra.ExactArgs(1),
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
			id := mesh.ResolveAgentSessionID(a, os.Getpid(), now)
			reqPath, err := requestPathByID(a, args[0])
			if err != nil {
				return err
			}
			if c, ok := mesh.ClaimRequest(reqPath, id, os.Getpid(), now); ok {
				fmt.Printf("claimed %s (session %s)\n", args[0], id)
				_ = c
				return nil
			} else {
				return fmt.Errorf("could not claim %s — held by %s", args[0], c.OwnerSession)
			}
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	return cmd
}

func releaseCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "release <id>",
		Short: "Release a claim you hold",
		Args:  cobra.ExactArgs(1),
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
			id := mesh.ResolveAgentSessionID(a, os.Getpid(), now)
			reqPath, err := requestPathByID(a, args[0])
			if err != nil {
				return err
			}
			if err := mesh.ReleaseClaim(reqPath, id, now); err != nil {
				return err
			}
			fmt.Printf("released %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	return cmd
}

func requestPathByID(a *mesh.Agent, id string) (string, error) {
	reqs, err := mesh.ListRequests(a.InboxPath())
	if err != nil {
		return "", err
	}
	for _, r := range reqs {
		if r.ID == id {
			return r.Path(), nil
		}
	}
	return "", fmt.Errorf("no request %q in %s", id, a.InboxPath())
}

// --- kb ---

// memoryCmd — the disposable, generated knowledge index (ADR-0014/0015).
func memoryCmd() *cobra.Command {
	var agent string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "memory rebuild|recall",
		Short: "Per-agent generated, disposable knowledge index (advisory; KB is authoritative)",
	}

	rebuild := &cobra.Command{
		Use:   "rebuild",
		Short: "Scan knowledge/ + agent.yaml; write .memory/index.json (atomic, pure rebuild)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			idx, err := mesh.MemoryRebuild(a)
			if err != nil {
				return err
			}
			fmt.Printf("rebuilt %s/.memory/index.json — %d topics from knowledge/\n", a.Name, len(idx.Topics))
			return nil
		},
	}
	rebuild.Flags().StringVar(&agent, "agent", "", "agent name (default: cwd/$AGENT_NAME)")

	var limit int
	recall := &cobra.Command{
		Use:   "recall <query>",
		Short: "Live unified recall over KB + session-records + git spine (compact pointers, no bodies)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			hits, err := mesh.Recall(a, strings.Join(args, " "), limit)
			if err != nil {
				return err
			}
			if asJSON {
				out, err := json.MarshalIndent(hits, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			if len(hits) == 0 {
				fmt.Println("no matching pointers — the KB may still cover it; try a broader query or read knowledge/ directly")
				return nil
			}
			for _, h := range hits {
				fmt.Printf("[%s] %s\n", h.Kind, h.Title)
				if len(h.Where) > 0 {
					fmt.Printf("  %s", strings.Join(h.Where, "  "))
					if h.Date != "" {
						fmt.Printf("  (%s)", h.Date)
					}
					fmt.Println()
				}
				if h.Detail != "" {
					fmt.Printf("  %s\n", h.Detail)
				}
			}
			return nil
		},
	}
	recall.Flags().StringVar(&agent, "agent", "", "agent name (default: cwd/$AGENT_NAME)")
	recall.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON for routing")
	recall.Flags().IntVar(&limit, "limit", 10, "max pointers")

	var sessLimit int
	sessions := &cobra.Command{
		Use:   "sessions",
		Short: "List captured session-records newest-first (the episodic timeline)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			recs := mesh.ListSessionRecords(a)
			if sessLimit > 0 && len(recs) > sessLimit {
				recs = recs[:sessLimit]
			}
			if asJSON {
				out, err := json.MarshalIndent(recs, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			if len(recs) == 0 {
				fmt.Println("(no session-records yet)")
				return nil
			}
			for _, r := range recs {
				fmt.Printf("%s  %s\n", r.Ended, truncateCLI(r.Summary, 90))
				meta := fmt.Sprintf("  files=%d cmds=%d tools=%d commits=%d", len(r.FilesTouched), len(r.Commands), len(r.Tools), len(r.Commits))
				if len(r.Refs) > 0 {
					meta += "  refs=" + strings.Join(r.Refs, ",")
				}
				fmt.Println(meta)
			}
			return nil
		},
	}
	sessions.Flags().StringVar(&agent, "agent", "", "agent name (default: cwd/$AGENT_NAME)")
	sessions.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	sessions.Flags().IntVar(&sessLimit, "limit", 15, "max records (0 = all)")

	cmd.AddCommand(rebuild, recall, sessions)
	return cmd
}

func truncateCLI(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// intuitionCmd — the owned intuition contract over the episodic engine (ADR-0016).
func intuitionCmd() *cobra.Command {
	var agent string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "intuition recall <query>",
		Short: "Compact past-work pointers (owned engine by default; claude-mem opt-in) — advisory",
	}
	recall := &cobra.Command{
		Use:   "recall <query>",
		Short: "Return compact past-work pointers for a query, without full narratives",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			hits := mesh.RecallIntuition(a, strings.Join(args, " "), limit)
			if asJSON {
				out, err := json.MarshalIndent(hits, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			if md := mesh.IntuitionMarkdown(hits); md != "" {
				fmt.Print(md)
			}
			return nil
		},
	}
	recall.Flags().StringVar(&agent, "agent", "", "agent name (default: cwd/$AGENT_NAME)")
	recall.Flags().IntVar(&limit, "limit", 3, "max pointers (1-10)")
	recall.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	cmd.AddCommand(recall)
	return cmd
}

// handoffCmd — the resumable session handoff (ADR-0017): where we are + next step.
func handoffCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "handoff show|set",
		Short: "Read/write the agent's authoritative session handoff (current focus + next step)",
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the current handoff (empty if none) — read this at session start",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			s, err := mesh.HandoffShow(a)
			if err != nil {
				return err
			}
			if strings.TrimSpace(s) != "" {
				fmt.Print(s)
			}
			return nil
		},
	}
	set := &cobra.Command{
		Use:   "set",
		Short: "Write the handoff body from stdin (stamps a timestamp; overwrites current)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(b)) == "" {
				return fmt.Errorf("handoff set: empty body on stdin")
			}
			if err := mesh.HandoffSet(a, string(b), time.Now()); err != nil {
				return err
			}
			fmt.Printf("wrote %s\n", mesh.HandoffPath(a))
			return nil
		},
	}
	show.Flags().StringVar(&agent, "agent", "", "agent (default: cwd/$AGENT_NAME)")
	set.Flags().StringVar(&agent, "agent", "", "agent (default: cwd/$AGENT_NAME)")
	cmd.AddCommand(show, set)
	return cmd
}

// hookCmd — the mesh's own Claude Code hook handlers, folded into the binary so
// there is no python3 dependency and no loose script files. Wired per-agent by
// `meshctl agent new`/`onboard` into <agent>/.claude/settings.json (relative to
// `meshctl` on $PATH). Every handler is fail-open: any error ⇒ no output,
// exit 0 (a hook must never break a turn).
func hookCmd() *cobra.Command {
	var harness string
	cmd := &cobra.Command{
		Use:   "hook session-start|user-prompt|session-end|pre-tool-use",
		Short: "Harness hook handlers (self-contained; wired per-agent by agent new/onboard)",
	}
	cmd.PersistentFlags().StringVar(&harness, "harness", "", "output envelope: claude|opencode (default: agent's harness)")

	// run reads the stdin payload, invokes fn, and prints the injected context
	// in the resolved harness's envelope. Fail-open: never errors to the caller.
	run := func(event string, fn func(dir string, p mesh.HookPayload, now time.Time) string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil
			}
			dir, err := agentsDir()
			if err != nil {
				return nil
			}
			p := mesh.ParseHookPayload(b)
			h := mesh.HarnessForPayload(dir, p, harness)
			_ = h.EmitContext(os.Stdout, event, fn(dir, p, time.Now()))
			return nil
		}
	}

	sessionStart := &cobra.Command{
		Use:   "session-start",
		Short: "SessionStart: register presence + inject the agent's handoff",
		RunE: run("SessionStart", func(dir string, p mesh.HookPayload, now time.Time) string {
			return mesh.HookSessionStart(dir, p, os.Getpid(), now)
		}),
	}
	userPrompt := &cobra.Command{
		Use:   "user-prompt",
		Short: "UserPromptSubmit: inject intuition pointers + a distillation boundary nudge",
		RunE:  run("UserPromptSubmit", mesh.HookUserPrompt),
	}
	sessionEnd := &cobra.Command{
		Use:   "session-end",
		Short: "SessionEnd: deregister presence",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil
			}
			dir, err := agentsDir()
			if err != nil {
				return nil
			}
			mesh.HookSessionEnd(dir, mesh.ParseHookPayload(b), os.Getpid(), time.Now())
			return nil
		},
	}

	// PreToolUse is the fail-CLOSED gate: it may emit a deny decision (not the
	// additionalContext envelope the others use), so it has its own handler.
	preToolUse := &cobra.Command{
		Use:   "pre-tool-use",
		Short: "PreToolUse: fail-closed deny-gate (blocks writes to competing memory stores + hand-written intake)",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil
			}
			// Pool resolution is best-effort: the gate's rules are path-shaped and
			// machine-global, so it must still fire when cwd is outside a pool.
			dir, _ := agentsDir()
			p := mesh.ParseHookPayload(b)
			_ = mesh.EmitGateDecision(os.Stdout, mesh.GatePreToolUse(dir, p))
			return nil
		},
	}

	cmd.AddCommand(sessionStart, userPrompt, sessionEnd, preToolUse)
	return cmd
}

func kbCmd() *cobra.Command {
	var agent string
	var abort bool
	var headless bool
	var session string
	cmd := &cobra.Command{
		Use:   "kb begin|finish",
		Short: "Worktree+branch for a KB write, then merge back to main and clean up",
		Args:  cobra.ExactArgs(1),
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
			id := mesh.ResolveSessionID(os.Getpid(), now)
			// A dispatched run is headless by definition.
			if os.Getenv("AGENT_DISPATCH_DEPTH") != "" && os.Getenv("AGENT_DISPATCH_DEPTH") != "0" {
				headless = true
			}
			switch args[0] {
			case "begin":
				sid := id
				if session != "" {
					sid = session
				}
				wt, err := mesh.KBBegin(dir, a, sid, now)
				if err != nil {
					return err
				}
				fmt.Printf("KB worktree ready: %s (branch session/%s)\n", wt, sid)
				fmt.Println("edit + commit there, then `meshctl kb finish`")
			case "finish":
				sid, err := mesh.ResolveKBSession(dir, a, id, session)
				if err != nil {
					return err
				}
				err = mesh.KBFinish(dir, a, sid, abort, headless)
				if errors.Is(err, mesh.ErrKBConflict) {
					fmt.Fprintln(os.Stderr, err.Error())
					return &mesh.CodedError{Code: 1, Msg: "kb finish: conflict — needs human"}
				}
				if err != nil {
					return err
				}
				if abort {
					fmt.Println("KB branch + worktree discarded")
				} else {
					fmt.Println("KB changes merged to main; worktree removed")
				}
			default:
				return fmt.Errorf("kb: want begin|finish, got %q", args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	cmd.Flags().BoolVar(&abort, "abort", false, "discard the branch + worktree (finish)")
	cmd.Flags().BoolVar(&headless, "headless", false, "never auto-resolve conflicts; leave needs-human (finish)")
	cmd.Flags().StringVar(&session, "session", "", "explicit KB session id (finish auto-detects the sole worktree if unset)")
	return cmd
}
