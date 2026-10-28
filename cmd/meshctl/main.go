// Command meshctl is the tool of record for the agent mesh:
// scaffold/onboard agents, emit the roster on demand, message peers
// via file-based intake, dispatch advisory peers headlessly with anti-cascade
// guards, and coordinate concurrent sessions (presence, atomic claims, KB
// worktrees).
package main

import (
	"context"
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

	"github.com/kevinhamon/meshctl/internal/console"
	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/kevinhamon/meshctl/internal/selfupdate"
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
			if n := selfupdate.Notice(resolveVersion(), time.Now()); n != "" {
				fmt.Println("ℹ " + n)
			}
			return nil
		},
	}
}

// updateCmd replaces this binary with a GitHub release (default: the latest).
func updateCmd() *cobra.Command {
	var check, force bool
	var tag string
	c := &cobra.Command{
		Use:   "update",
		Short: "Update meshctl itself to the latest GitHub release (checksum-verified, in place)",
		Long: "Fetches the latest release of " + selfupdate.Repo + ", downloads the binary for this platform,\n" +
			"verifies it against the release's .sha256, and replaces the running executable.\n" +
			"--check only reports whether an update exists. A local/dev build (e.g. from `task install` or a\n" +
			"source checkout) is never replaced without --force: it may carry code newer than any release.\n" +
			"After updating, run `meshctl pool upgrade --check` in each pool to pick up wiring/doctrine changes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cur := resolveVersion()
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			rel, err := selfupdate.Latest(ctx, tag)
			if err != nil {
				return fmt.Errorf("cannot reach releases: %w", err)
			}
			dev := !selfupdate.IsRelease(cur)
			newer := selfupdate.Newer(rel.Tag, cur)
			switch {
			case check:
				switch {
				case dev:
					fmt.Printf("meshctl %s is a local build; latest release is %s (`meshctl update --force` to switch to it)\n", cur, rel.Tag)
				case newer:
					fmt.Printf("update available: %s → %s (run `meshctl update`)\n", cur, rel.Tag)
					return &mesh.CodedError{Code: 1, Msg: "update available"}
				default:
					fmt.Printf("meshctl %s is up to date\n", cur)
				}
				return nil
			case dev && !force:
				return fmt.Errorf("meshctl %s is a local build, which may be newer than release %s — rerun with --force to replace it", cur, rel.Tag)
			case !dev && !newer && !force && tag == "":
				fmt.Printf("meshctl %s is up to date\n", cur)
				return nil
			}
			exe, err := selfupdate.Executable()
			if err != nil {
				return err
			}
			fmt.Printf("updating %s: %s → %s …\n", exe, cur, rel.Tag)
			if err := selfupdate.Install(ctx, rel, exe); err != nil {
				return err
			}
			fmt.Printf("meshctl %s installed. Next, in each pool: `meshctl pool upgrade --check`\n", rel.Tag)
			if console.RestartLoginItem() {
				fmt.Println("restarted the console login item on the new binary")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "only report whether an update is available (exit 1 if one is)")
	c.Flags().BoolVar(&force, "force", false, "replace a local/dev build, or reinstall the same version")
	c.Flags().StringVar(&tag, "version", "", "install this release tag instead of the latest (e.g. v1.0.0)")
	return c
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
	root.SetUsageTemplate(root.UsageTemplate() + "\nFriction, a gap, a bug or an idea? https://github.com/kevinhamon/meshctl/issues/new/choose\n")
	root.AddCommand(
		versionCmd(), updateCmd(),
		// Resource groups (noun → verb).
		poolCmd(),  // init | upgrade | list
		agentCmd(), // new | onboard | list
		msgCmd(),   // ask | send
		inboxCmd(), // list | next | claim | release | respond | decline | archive
		sentCmd(),  // requester side: responses awaiting read-back
		kbCmd(), workCmd(), pendingCmd(), consoleCmd(), memoryCmd(), sessionCmd(), handoffCmd(), stateCmd(), intuitionCmd(), hookCmd(),
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
		Short: "Reconcile a legacy pool layout to the consolidated pool directory (ADR-0038)",
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
	var purge, yes, dryRun bool
	c := &cobra.Command{
		Use:   "teardown",
		Short: "Remove the pool's mesh infra (the pool directory) + deregister; agent repos are kept unless --purge",
		Long: `Remove the pool marker, runtime state and registry entry. Agent workspaces are
kept unless --purge, which DELETES them (and any knowledge not pushed to a remote).

--purge is irreversible, so it needs confirmation: pass --yes, or type the pool's
directory name at the prompt. --dry-run lists what would be removed and exits.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Printf("would tear down pool: %s\n", dir)
				fmt.Printf("  remove %s/\n", mesh.PoolDirName(dir))
				if purge {
					agents, _, _ := mesh.ScanAgents(dir)
					for _, a := range agents {
						fmt.Printf("  DELETE agent workspace %s\n", a.Dir())
					}
				}
				return nil
			}
			if purge {
				fmt.Fprintln(os.Stderr, "⚠ --purge will DELETE agent workspaces (and any UNPUSHED knowledge) in "+dir)
				if !yes {
					if !isTerminal(os.Stdin) {
						return fmt.Errorf("refusing --purge without confirmation: add --yes (non-interactive) or run from a terminal")
					}
					fmt.Fprintf(os.Stderr, "Type the pool directory name (%s) to confirm: ", filepath.Base(dir))
					var typed string
					fmt.Fscanln(os.Stdin, &typed)
					if strings.TrimSpace(typed) != filepath.Base(dir) {
						return fmt.Errorf("confirmation did not match; nothing removed")
					}
				}
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
	c.Flags().BoolVar(&yes, "yes", false, "with --purge: skip the interactive confirmation")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be removed and exit")
	return c
}

// isTerminal reports whether f is an interactive terminal (character device).
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func agentCmd() *cobra.Command {
	c := &cobra.Command{Use: "agent", Short: "Agent lifecycle: scaffold (new), onboard existing, bootstrap the steward, list roster, print identity"}
	c.AddCommand(newCmd(), onboardCmd(), bootstrapCmd(), directoryCmd(), identityCmd(), briefCmd())
	return c
}

// briefCmd prints the context a peer consulted in-session gets at session
// start: identity, role, the request with recall pointers, handoff, and the
// write-back contract. The subagent's first command.
func briefCmd() *cobra.Command {
	var agent, request string
	c := &cobra.Command{
		Use:   "brief --agent <peer> [--request <id>]",
		Short: "Print an agent's session-start context on demand (identity, role, request + recall, handoff, write-back contract)",
		Long: "For a peer consulted in-session (`meshctl msg ask … --in-session`): its SessionStart/UserPromptSubmit\n" +
			"hooks do not fire inside a subagent, so this command delivers the same context in one shot. With\n" +
			"--request it includes the request's ask and recall pointers for it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			out, err := mesh.Brief(dir, a, request)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	c.Flags().StringVar(&agent, "agent", "", "the agent being briefed (default: $AGENT_NAME or cwd — pass it explicitly from a subagent)")
	c.Flags().StringVar(&request, "request", "", "intake request id to include (ask text + recall pointers)")
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
	var force, check, doctrineOnly bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the pool to this binary + the installed harnesses: pool directory, doctrine and every agent's wiring",
		Long: "Brings the whole pool up to this meshctl and the harness versions installed here, in three steps:\n" +
			"  0. pool directory — moves a legacy hidden pool directory to pool/, makes it a git repo on main with an\n" +
			"     ignore file for runtime state, writes pool/config.yaml (human-owned settings) and moves any `tools`\n" +
			"     still in pool/pool.yaml into it, and commits with your git identity (ADR-0066).\n" +
			"  1. doctrine — replaces doctrine files unchanged since meshctl wrote them. A file edited locally is\n" +
			"     left alone: the new version is written beside it as <file>.upstream for a manual merge, and the\n" +
			"     command exits non-zero. --force overwrites edited files after backing each up to <file>.bak-<timestamp>.\n" +
			"  2. agents — re-runs `agent onboard` for every agent: re-wires hooks/plugins for the installed harness\n" +
			"     (e.g. swaps an opencode V1 plugin for the V2 one), refreshes mesh-owned skills, adds missing\n" +
			"     CLAUDE.md stanzas. Never touches agent.yaml or an agent's role doctrine.\n" +
			"--check previews every step (plus warnings: live sessions, uncommitted changes, global harness config)\n" +
			"without writing anything; it exits 1 when an upgrade is pending.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			if check {
				plan, err := mesh.PlanPoolUpgrade(dir, time.Now())
				if err != nil {
					return err
				}
				printUpgradePlan(plan, doctrineOnly)
				if plan.Pending() {
					return &mesh.CodedError{Code: 1, Msg: "upgrade pending — run `meshctl pool upgrade` to apply"}
				}
				return nil
			}
			fmt.Printf("pool: %s\n", dir)
			// Pool directory first: doctrine and agents are written into its result.
			moved, err := mesh.UpgradePoolDir(dir, time.Now())
			for _, d := range moved {
				fmt.Println("  " + d)
			}
			if err != nil {
				return err
			}
			res, err := mesh.UpgradeDoctrine(dir, force, time.Now())
			if err != nil {
				return err
			}
			if len(res.Did) == 0 && len(res.Conflicts) == 0 {
				fmt.Println("  doctrine already current")
			}
			for _, d := range res.Did {
				fmt.Println("  " + d)
			}
			for _, c := range res.Conflicts {
				p := filepath.Join(dir, c)
				fmt.Printf("  ✗ %s has local edits — left untouched; new version at %s.upstream\n", c, c)
				fmt.Printf("      merge: diff -u %s %s.upstream\n", p, p)
			}
			if !doctrineOnly {
				agents, err := mesh.UpgradeAgents(dir)
				for _, a := range agents {
					if len(a.Actions) == 0 {
						fmt.Printf("  %s: already current\n", a.Name)
						continue
					}
					fmt.Printf("  %s (%s):\n", a.Name, a.Harness)
					for _, n := range a.Actions {
						fmt.Println("    · " + n)
					}
				}
				if err != nil {
					return err
				}
				fmt.Println("restart every open agent session — running harnesses keep the old wiring until restarted")
				// Record which meshctl upgraded the pool, so a newer one can say
				// the pool is behind (console and doctor).
				if err := mesh.WriteUpgradeStamp(dir, resolveVersion(), time.Now()); err != nil {
					fmt.Println("  (could not record the upgrade stamp: " + err.Error() + ")")
				}
				if p, err := console.LoginItemPath(); err == nil && runtime.GOOS == "darwin" {
					if _, err := os.Stat(p); err != nil {
						fmt.Println("tip: `meshctl console install` keeps a live view of this pool running from login")
					}
				}
			}
			// Hygiene last: an upgraded pool should also be a clean one. Automatic
			// repairs are applied; anything needing a human is listed.
			hyg := mesh.PoolHygiene(dir, time.Now())
			fixed, errs := mesh.ApplyHygiene(hyg)
			manual := mesh.ManualItems(hyg)
			if len(fixed) > 0 || len(errs) > 0 || len(manual) > 0 {
				fmt.Println("hygiene:")
				for _, f := range fixed {
					fmt.Println("  ✓ " + f)
				}
				for _, e := range errs {
					fmt.Println("  ✗ " + e.Error())
				}
				for _, m := range manual {
					fmt.Printf("  ⚠ [%s] %s\n", m.Agent, m.Message)
				}
			}
			if len(res.Conflicts) == 0 && len(manual) == 0 && len(errs) == 0 {
				fmt.Println("pool clean ✓")
				return nil
			}
			n := len(res.Conflicts) + len(manual) + len(errs)
			return &mesh.CodedError{Code: 1, Msg: fmt.Sprintf("%d item(s) need a human (listed above); the pool is upgraded but not yet clean", n)}
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite locally-edited doctrine (each is backed up first)")
	c.Flags().BoolVar(&check, "check", false, "preview the upgrade without writing anything (exit 1 if one is pending)")
	c.Flags().BoolVar(&doctrineOnly, "doctrine-only", false, "upgrade the pool doctrine only; leave agent wiring alone")
	return c
}

// printUpgradePlan renders a `pool upgrade --check` preview.
func printUpgradePlan(plan mesh.PoolUpgradePlan, doctrineOnly bool) {
	fmt.Printf("pool: %s\n", plan.PoolRoot)
	if plan.OpencodeMajor > 0 {
		fmt.Printf("opencode installed: V%d\n", plan.OpencodeMajor)
	}
	fmt.Println("pool directory:")
	if len(plan.Pool) == 0 {
		fmt.Println("  current")
	}
	for _, s := range plan.Pool {
		fmt.Println("  · " + s)
	}
	fmt.Println("doctrine:")
	if len(plan.Doctrine) == 0 && len(plan.Conflicts) == 0 {
		fmt.Println("  current")
	}
	for _, d := range plan.Doctrine {
		fmt.Println("  · update " + d)
	}
	for _, c := range plan.Conflicts {
		fmt.Println("  ✗ " + c + " — differs from this binary's copy: left alone, a .upstream copy is written for a manual merge")
	}
	if plan.DoctrineDiff != "" {
		// The doctrine change is read here, before `pool upgrade` commits it.
		fmt.Println("doctrine diff (committed as `doctrine: upgrade to <version>` on apply):")
		fmt.Print(plan.DoctrineDiff)
	}
	if !doctrineOnly {
		fmt.Println("agents:")
		for _, a := range plan.Agents {
			state := "current"
			if len(a.Actions) > 0 {
				state = fmt.Sprintf("%d change(s)", len(a.Actions))
			}
			fmt.Printf("  %s (%s): %s\n", a.Name, a.Harness, state)
			for _, x := range a.Actions {
				fmt.Println("    · " + x)
			}
			for _, w := range a.Warnings {
				fmt.Println("    ⚠ " + w)
			}
		}
	}
	for _, w := range plan.Warnings {
		fmt.Println("⚠ " + w)
	}
	// Pool repo nag (ADR-0066): last, so it is the line the human sees. The
	// apply path reports it through hygiene's manual items, in the same spot.
	if nag := mesh.PoolRepoNag(plan.PoolRoot); nag != "" {
		fmt.Println("⚠ " + nag)
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
					fmt.Printf("(no %s marker — run `meshctl pool init`)\n", mesh.PoolMarkerPath(dir))
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
				return fmt.Errorf("%s is not a pool (no %s/ marker) — `meshctl pool init` first", abs, mesh.PoolDirName(abs))
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
		title, model, rgb, repo, workdirs string
		badge, role                       string
		owns, domains, accepts            string
		mutates                           string
		dispatchable                      bool
		iterm                             bool
		harness                           string
		ocVersion                         int
		bare                              bool
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
			if ocVersion < 0 || ocVersion > 2 {
				return fmt.Errorf("--opencode-version must be 1 or 2")
			}
			name := args[0]
			if badge == "" {
				badge = strings.ToUpper(name)
			}
			spec := mesh.AgentSpec{
				Name: name, Title: title, Role: role,
				Owns: splitCSV(owns), Domains: splitCSV(domains),
				Accepts: parseAccepts(accepts), Dispatchable: dispatchable,
				Mutates: splitCSV(mutates), Model: model, Repo: repo, Workdirs: splitCSV(workdirs), Harness: harness, HarnessVersion: ocVersion, Bare: bare,
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
	f.StringVar(&workdirs, "workdirs", "", "comma-separated local repo paths writable in delegated hub-session work (~ ok)")
	f.StringVar(&harness, "harness", "", "coding-agent harness: claude|opencode (default: claude for new; autodetect for onboard)")
	f.IntVar(&ocVersion, "opencode-version", 0, "opencode major to wire for: 1|2 (default: the installed opencode's; 2 if none)")
	f.BoolVar(&iterm, "iterm2", false, "also write a macOS iTerm2 dynamic profile (.iterm2/) for window identity")
	f.BoolVar(&bare, "bare", false, "do not append the '-agent' repo suffix to the name")
	return cmd
}

// --- onboard ---

func onboardCmd() *cobra.Command {
	var (
		title, model, rgb, repo, workdirs string
		badge, role                       string
		owns, domains, accepts            string
		mutates                           string
		dispatchable                      bool
		iterm                             bool
		harness                           string
		ocVersion                         int
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
			if ocVersion < 0 || ocVersion > 2 {
				return fmt.Errorf("--opencode-version must be 1 or 2")
			}
			spec := mesh.AgentSpec{
				Title: title, Role: role,
				Owns: splitCSV(owns), Domains: splitCSV(domains),
				Accepts: parseAccepts(accepts), Dispatchable: dispatchable,
				Mutates: splitCSV(mutates), Model: model, Repo: repo, Workdirs: splitCSV(workdirs), Harness: harness,
				HarnessVersion: ocVersion,
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
	f.StringVar(&workdirs, "workdirs", "", "comma-separated local repo paths writable in delegated hub-session work (~ ok)")
	f.StringVar(&harness, "harness", "", "coding-agent harness: claude|opencode (default: claude for new; autodetect for onboard)")
	f.IntVar(&ocVersion, "opencode-version", 0, "opencode major to wire for: 1|2 (default: the installed opencode's; 2 if none)")
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
				out, err := mesh.DirectoryJSON(agents, mesh.LoadPoolConfig(dir))
				if err != nil {
					return err
				}
				fmt.Println(out)
				return nil
			}
			md := mesh.DirectoryMarkdown(agents, mesh.LoadPoolConfig(dir))
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
	var detach, inSession, work bool
	var priority, from, related string
	cmd := &cobra.Command{
		Use:   "ask <target> <type> <prompt>",
		Short: "Write intake + dispatch an advisory peer (headless process, or --in-session as a subagent of this session)",
		Long: "Writes the intake request, runs the anti-cascade guards, then either runs the peer headlessly in its own\n" +
			"workspace and blocks for the answer (default), or — with --in-session — records the dispatch and prints\n" +
			"the delegation to make next — Claude Code's Agent tool (subagent_type mesh-<peer>) or opencode V2's\n" +
			"subagent tool (agent mesh-<peer>) — so the peer runs inside this session instead of a second\n" +
			"terminal. Either way the answer lands in the request file (`meshctl sent`). --in-session needs the\n" +
			"peer's subagent definition in this workspace (.claude/agents/ or .opencode/agents/mesh-<peer>.md,\n" +
			"rendered by `agent onboard` / `pool upgrade`).\n\n" +
			"--in-session --work delegates write-capable work instead (mesh-<peer>-work): the peer\n" +
			"edits and commits as itself in its own workspace and declared workdirs, and ends with a receipt.\n" +
			"If the peer has a live session, the work goes to a mesh/work/<id> worktree that the peer lands with\n" +
			"`meshctl work merge <id>`. Delegating to a state-mutating peer asks the human for approval.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			target, err := mesh.FindAgent(dir, args[0])
			if err != nil {
				return &mesh.CodedError{Code: mesh.ExitUnknownTarget, Msg: err.Error()}
			}
			if work && !inSession {
				return fmt.Errorf("--work needs --in-session (delegated work runs as a subagent of this session)")
			}
			if inSession {
				if detach {
					return fmt.Errorf("--in-session and --detach are exclusive")
				}
				_, rc, err := mesh.AskInSession(dir, target, mesh.InSessionOptions{
					Type: args[1], Prompt: args[2], From: from,
					Priority: priority, Related: splitCSV(related), Work: work, Now: time.Now(),
				})
				if err != nil {
					return err
				}
				if rc != 0 {
					os.Exit(rc)
				}
				return nil
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
	f.BoolVar(&inSession, "in-session", false, "record the dispatch and print the subagent delegation (peer runs as a subagent of this Claude Code or opencode V2 session)")
	f.BoolVar(&work, "work", false, "with --in-session: delegate write-capable work (mesh-<peer>-work) instead of a read-only consult")
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
	var fix, asJSON bool
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Check manifests, wiring, KB consistency and pool hygiene; non-zero exit on any error",
		Long: `Check every agent's manifest, harness wiring, knowledge base and the pool's
runtime state. Hygiene items (stale claims and sessions, orphan worktrees, retired
files, legacy intake statuses, leftovers from earlier upgrades) are reported with
"fixable" when meshctl can repair them; --fix applies those repairs. Items that
need a human (text you wrote, doctrine you edited) are always reported, never changed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			now := time.Now()
			info := mesh.OutboundWiringLines(dir)
			if n := mesh.UpgradeNotice(dir, resolveVersion()); n != "" {
				info = append(info, n)
			}
			if n := selfupdate.Notice(resolveVersion(), now); n != "" {
				info = append(info, n)
			}
			findings, ok, fixed, errs := mesh.DoctorWithHygiene(dir, fix, now)
			for _, m := range console.LoginItemFindings(console.ReadSettings()) {
				findings = append(findings, mesh.DoctorFinding{Agent: "console", Level: "warn", Message: m})
			}
			if asJSON {
				if err := printJSON(mesh.NewDoctorReport(findings, ok, info, now)); err != nil {
					return err
				}
				if !ok {
					return &mesh.CodedError{Code: 1, Msg: "doctor found errors"}
				}
				return nil
			}
			for _, l := range info {
				fmt.Println("ℹ " + l)
			}
			for _, f := range fixed {
				fmt.Println("✓ fixed " + f)
			}
			for _, e := range errs {
				fmt.Println("✗ fix failed: " + e.Error())
			}
			for i, f := range findings {
				if f.Fixable {
					findings[i].Message += "  (fixable: meshctl doctor --fix)"
				}
			}
			if len(findings) == 0 {
				fmt.Println("doctor: clean ✓")
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
	c.Flags().BoolVar(&fix, "fix", false, "apply the automatic hygiene repairs (stale state, retired files, legacy statuses)")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable report (versioned schema); exit status as without it")
	return c
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
	parent := &cobra.Command{Use: "inbox", Short: "Inbox: list the open queue, claim a request by id, or release one"}

	var listAll bool
	inboxList := func(cmd *cobra.Command, args []string) error {
		dir, err := agentsDir()
		if err != nil {
			return err
		}
		a, err := resolveContextAgent(dir, agent)
		if err != nil {
			return err
		}
		q, err := mesh.InboxQueue(a, listAll)
		if err != nil {
			return err
		}
		if len(q) == 0 && listAll {
			fmt.Println("(inbox empty)")
			return nil
		}
		fmt.Print(mesh.InboxQueueMarkdown(q, time.Now()))
		return nil
	}

	list := &cobra.Command{Use: "list", Short: "List the open queue (pending + in-progress), priority then age; --all for every status", RunE: inboxList}
	list.Flags().BoolVar(&listAll, "all", false, "show every status, not just pending + in-progress")

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

	var archiveAnswered, archiveClosed, archiveForce bool
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
			if archiveClosed {
				ids, skipped, err := mesh.ArchiveClosed(a, archiveForce)
				if err != nil {
					return err
				}
				if len(skipped) > 0 {
					fmt.Printf("kept %d closed request(s) the requester has not read back yet (--force to archive anyway): %s\n", len(skipped), strings.Join(skipped, ", "))
				}
				if len(ids) == 0 {
					fmt.Println("(no closed requests to archive)")
					return nil
				}
				fmt.Printf("archived %d closed request(s): %s\n", len(ids), strings.Join(ids, ", "))
				return nil
			}
			if archiveAnswered {
				ids, skipped, err := mesh.ArchiveAnswered(a, archiveForce)
				if err != nil {
					return err
				}
				if len(skipped) > 0 {
					fmt.Printf("kept %d answered request(s) the requester has not read back yet (--force to archive anyway): %s\n", len(skipped), strings.Join(skipped, ", "))
				}
				if len(ids) == 0 {
					fmt.Println("(no answered requests to archive)")
					return nil
				}
				fmt.Printf("archived %d answered request(s): %s\n", len(ids), strings.Join(ids, ", "))
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("give a request id, or use --answered / --closed")
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
	archive.Flags().BoolVar(&archiveClosed, "closed", false, "archive every closed request (answered/filed/declined) the requester has read back")
	archive.Flags().BoolVar(&archiveForce, "force", false, "with --answered/--closed: also archive ones the requester has not read back")

	// Bare `inbox` lists.
	parent.RunE = inboxList
	parent.PersistentFlags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	parent.AddCommand(list, next, claimCmd(), releaseCmd(), respondCmd(), declineCmd(), archive, triageCmd(&agent))
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
		Short: "Remove a leftover .memory/index.json; recall is computed live",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			// Recall has computed live over knowledge/ since v1.1; the on-disk
			// index is retired. This verb now just removes a leftover one.
			p := filepath.Join(a.Dir(), mesh.MemoryDir, mesh.MemoryIndexFile)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Printf("memory is computed live from %s/knowledge/ — nothing to rebuild (removed any leftover %s)\n", a.Name, mesh.MemoryIndexFile)
			return nil
		},
	}
	rebuild.Flags().StringVar(&agent, "agent", "", "agent name (default: cwd/$AGENT_NAME)")

	var limit, budget int
	var scope string
	recall := &cobra.Command{
		Use:   "recall <query>",
		Short: "Live unified recall over KB + session-records + git spine (tiered pointers; --budget inlines current-tier bodies)",
		Long: `Rank KB topics, captured session-records and recent commits for a query.
KB hits carry a tier: "current" (architecture/, playbooks/, reference/, direction.md,
risks.md, state.md — what is true now) outranks "log" (decisions/, experience/ —
history) which outranks "inbox" (candidates/ — proposals). Decisions whose status is
superseded, deprecated or rejected are never returned. With --budget N the top
current-tier hits have their text inlined (≈N tokens in total) so the knowledge is
in view, not behind a pointer; log and inbox hits stay pointers.

--scope canonical also searches every canonical peer's current and log tiers
(read-only; never candidates, session records or git history; never perspective
peers), tagging hits "via <peer>", at most 5 of them, unconfirmed ones last. Peer
bodies inline only with --budget, after your own; an inlined body's mesh://
references are followed one hop to canonical targets, marked via the owner.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			hits, err := mesh.RecallScoped(a, strings.Join(args, " "), scope, limit, budget)
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
			fmt.Print(mesh.RecallMarkdown(hits))
			return nil
		},
	}
	recall.Flags().StringVar(&agent, "agent", "", "agent name (default: cwd/$AGENT_NAME)")
	recall.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON for routing")
	recall.Flags().IntVar(&limit, "limit", 10, "max pointers")
	recall.Flags().IntVar(&budget, "budget", 0, "inline current-tier KB bodies up to about this many tokens (0 = pointers only)")
	recall.Flags().StringVar(&scope, "scope", mesh.RecallScopeOwn, "own (default) or canonical (also canonical peers' current and log tiers, via <peer>)")

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
				for _, c := range r.Confirms {
					fmt.Println("  confirmed: " + c)
				}
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
		Short: "Compact past-work pointers from session records and git — advisory",
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
	var force bool
	cmd := &cobra.Command{
		Use:   "handoff show|template|set",
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
	template := &cobra.Command{
		Use:   "template",
		Short: "Print the handoff template to fill in (the one fixed shape a handoff takes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print(mesh.HandoffTemplate)
			return nil
		},
	}
	set := &cobra.Command{
		Use:   "set",
		Short: "Write the handoff body from stdin (stamps a timestamp; overwrites current)",
		Long: "Replaces knowledge/handoff.md with the body on stdin (meshctl adds the timestamp header).\n" +
			"Fill in `meshctl handoff template` — four fixed sections, at most " + fmt.Sprint(mesh.HandoffMaxLines) + " lines, facts only —\n" +
			"and rewrite the whole thing each time (don't append; git keeps prior handoffs). Pass it with a\n" +
			"quoted heredoc so nothing in the text is shell-expanded:\n\n" +
			"  meshctl handoff set <<'EOF'\n" +
			"  ## Current State\n" +
			"  Pool upgraded to opencode V2; all agents rewired and pushed.\n\n" +
			"  ## Next Step\n" +
			"  Restart every agent session, then smoke-test research-agent.\n\n" +
			"  ## Open Threads\n" +
			"  - none\n\n" +
			"  ## Pointers\n" +
			"  - knowledge/experience/opencode-v1-to-v2-pool-upgrade.md\n" +
			"  EOF\n\n" +
			"Problems (missing section, leftover placeholder, over budget) are reported but never block the write.",
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
			if problems := mesh.HandoffLint(string(b)); len(problems) > 0 && !force {
				for _, w := range problems {
					fmt.Fprintln(os.Stderr, "✗ handoff: "+w)
				}
				return &mesh.CodedError{Code: 1, Msg: "handoff set: body fails lint (fix it, or --force to write anyway)"}
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
	set.Flags().BoolVar(&force, "force", false, "write even when the body fails lint (missing section, placeholder, over budget)")
	cmd.AddCommand(show, template, set)
	return cmd
}

// stateCmd — the current-state digest (knowledge/state.md), the handoff's
// sibling: the handoff says where the last session stopped, the digest says
// what the world looks like. Same fixed shape, same one-command write.
func stateCmd() *cobra.Command {
	var agent, summaryFile string
	var force bool
	cmd := &cobra.Command{
		Use:   "state show|template|set",
		Short: "Read/write the agent's current-state digest (knowledge/state.md, ≤40 lines; injected at session start)",
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the current digest (empty if none)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agentsDir()
			if err != nil {
				return err
			}
			a, err := resolveContextAgent(dir, agent)
			if err != nil {
				return err
			}
			s, err := mesh.StateShow(a)
			if err != nil {
				return err
			}
			if strings.TrimSpace(s) != "" {
				fmt.Print(s)
			}
			return nil
		},
	}
	template := &cobra.Command{
		Use:   "template",
		Short: "Print the digest template to fill in (the one fixed shape)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print(mesh.StateTemplate)
			return nil
		},
	}
	set := &cobra.Command{
		Use:   "set",
		Short: "Write the digest from stdin as a promotion (--summary-file required; ledger + Confirm: commit)",
		Long: "Replaces knowledge/state.md with the body on stdin. Fill in `meshctl state template` — four fixed\n" +
			"sections, at most " + fmt.Sprint(mesh.StateMaxLines) + " lines, standing facts only — and rewrite the whole thing after each\n" +
			"consolidation (`meshctl kb consolidate` lists the decisions it has not absorbed). Pass it with a quoted\n" +
			"heredoc:\n\n" +
			"  meshctl state set --summary-file <you>/.memory/confirm/<session>.md <<'EOF'\n" +
			"  ## Now\n" +
			"  All new services are Go on the platform; the legacy stack is maintained, not extended.\n\n" +
			"  ## Active\n" +
			"  - API v1 → v2 migration: 5 of 7 services complete\n\n" +
			"  ## Recently decided\n" +
			"  - ADR-0012 one repository per service; shared code only via published modules\n\n" +
			"  ## Open questions\n" +
			"  - none\n" +
			"  EOF\n\n" +
			"A body with a missing section, a leftover placeholder or over budget is refused (--force overrides).\n\n" +
			"The digest is current-tier knowledge, so `state set` is a promotion (ADR-0067): the gate asks the human\n" +
			"with your confirmation summary, and the command writes one ledger hash per digest section and commits\n" +
			"state.md with the ledger as `Confirm: <Intent line>`. No --summary-file, or no human at the prompt\n" +
			"(dispatch depth, a distil run), is refused.",
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
				return fmt.Errorf("state set: empty body on stdin")
			}
			if problems := mesh.StateLint(string(b)); len(problems) > 0 && !force {
				for _, w := range problems {
					fmt.Fprintln(os.Stderr, "✗ state: "+w)
				}
				return &mesh.CodedError{Code: 1, Msg: "state set: body fails lint (fix it, or --force to write anyway)"}
			}
			now := time.Now()
			res, err := mesh.StateSetConfirmed(a, string(b), mesh.StateSetOptions{
				SummaryFile: summaryFile,
				Session:     mesh.ResolveAgentSessionID(a, os.Getpid(), now),
				Now:         now,
			})
			if err != nil {
				return &mesh.CodedError{Code: 1, Msg: err.Error()}
			}
			fmt.Printf("wrote %s; confirmed %d section(s) as %s Confirm: %s\n", res.Path, len(res.Ledgered), res.Confirm, res.Intent)
			return nil
		},
	}
	show.Flags().StringVar(&agent, "agent", "", "agent (default: cwd/$AGENT_NAME)")
	set.Flags().StringVar(&agent, "agent", "", "agent (default: cwd/$AGENT_NAME)")
	set.Flags().BoolVar(&force, "force", false, "write even when the body fails lint")
	set.Flags().StringVar(&summaryFile, "summary-file", "", "your confirmation summary, <you>/.memory/confirm/<session>.md (required)")
	cmd.AddCommand(show, template, set)
	return cmd
}

// hookCmd — the mesh's own Claude Code hook handlers, folded into the binary so
// there is no python3 dependency and no loose script files. Wired per-agent by
// `meshctl agent new`/`onboard` into <agent>/.claude/settings.json (relative to
// `meshctl` on $PATH). Every handler is fail-open: any error ⇒ no output,
// exit 0 (a hook must never break a turn).
func hookCmd() *cobra.Command {
	var harness, peer string
	var inSession, work bool
	cmd := &cobra.Command{
		Use:   "hook session-start|user-prompt|session-beat|session-end|pre-tool-use|post-tool-use|status-line",
		Short: "Harness hook handlers (self-contained; wired per-agent by agent new/onboard)",
	}
	cmd.PersistentFlags().StringVar(&harness, "harness", "", "output envelope: claude|opencode (default: agent's harness)")
	cmd.PersistentFlags().StringVar(&peer, "agent", "", "act for this agent instead of the payload cwd's (in-session peer subagents)")
	cmd.PersistentFlags().BoolVar(&inSession, "in-session", false, "the hook fires inside an in-session peer subagent (peer gate; Stop closes the dispatch)")
	cmd.PersistentFlags().BoolVar(&work, "work", false, "with --in-session: the peer does delegated work (work gate: writes in its own repos only)")
	// stamp applies the override flags to a parsed payload.
	stamp := func(p mesh.HookPayload) mesh.HookPayload {
		p.PeerAgent = peer
		p.InSession = inSession
		p.Work = inSession && work
		p.Harness = harness
		return p
	}

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
			p := stamp(mesh.ParseHookPayload(b))
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
	sessionBeat := &cobra.Command{
		Use:   "session-beat",
		Short: "Heartbeat: refresh presence for a session that is still open (no output)",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil
			}
			dir, err := agentsDir()
			if err != nil {
				return nil
			}
			mesh.HookSessionBeat(dir, mesh.ParseHookPayload(b), time.Now())
			return nil
		},
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
			p, err := mesh.ResolvePeerSubagent(dir, stamp(mesh.ParseHookPayload(b)))
			if err != nil {
				return nil // an unresolvable peer was never let run; nothing to capture
			}
			mesh.HookSessionEnd(dir, p, os.Getpid(), time.Now())
			return nil
		},
	}

	// PreToolUse is the fail-CLOSED gate: it may emit a deny decision (not the
	// additionalContext envelope the others use), so it has its own handler.
	preToolUse := &cobra.Command{
		Use:   "pre-tool-use",
		Short: "PreToolUse: fail-closed deny-gate (blocks writes to competing memory stores + hand-written intake)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Pool resolution is best-effort: the gate's rules are path-shaped and
			// machine-global, so it must still fire when cwd is outside a pool.
			dir, _ := agentsDir()
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				// Fail CLOSED: a gate that cannot read the call must not allow it.
				_ = mesh.HarnessForPayload(dir, mesh.HookPayload{}, harness).EmitGate(os.Stdout, mesh.GateUnavailable("could not read stdin"))
				return nil
			}
			p, perr := mesh.ParseHookPayloadStrict(b)
			p = stamp(p)
			if perr != nil {
				_ = mesh.HarnessForPayload(dir, p, harness).EmitGate(os.Stdout, mesh.GateUnavailable(perr.Error()))
				return nil
			}
			// An opencode child session is mapped to its peer here (ADR-0076);
			// a mesh- subagent that cannot be mapped is refused.
			if p, perr = mesh.ResolvePeerSubagent(dir, p); perr != nil {
				_ = mesh.HarnessForPayload(dir, p, harness).EmitGate(os.Stdout, mesh.GateUnavailable(perr.Error()))
				return nil
			}
			_ = mesh.HarnessForPayload(dir, p, harness).EmitGate(os.Stdout, mesh.HookPreToolUse(dir, p, time.Now()))
			return nil
		},
	}

	// PostToolUse runs after a gated call on the same matcher. No output ever:
	// it is the seam where pool commits (ADR-0066) and outbound acks (ADR-0068)
	// record what the gate let through. Fail-open, like the advisory hooks.
	postToolUse := &cobra.Command{
		Use:   "post-tool-use",
		Short: "PostToolUse: after a gated call, record what it did (pool commit, outbound ack); no output",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil
			}
			dir, _ := agentsDir()
			p, err := mesh.ResolvePeerSubagent(dir, stamp(mesh.ParseHookPayload(b)))
			if err != nil {
				return nil // the gate refused this subagent's calls; nothing ran
			}
			mesh.HookPostToolUse(dir, p, time.Now())
			return nil
		},
	}

	// The status line is not a hook event but rides the same wiring: Claude
	// Code runs it with its own stdin JSON and shows stdout verbatim (ADR-0074).
	statusLine := &cobra.Command{
		Use:   "status-line",
		Short: "Status line: the agent and the pending work waiting on you, one line",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, _ := io.ReadAll(os.Stdin)
			dir, _ := agentsDir()
			fmt.Println(mesh.StatusLine(dir, mesh.ParseStatusLinePayload(b), time.Now()))
			return nil
		},
	}

	cmd.AddCommand(sessionStart, userPrompt, sessionBeat, sessionEnd, preToolUse, postToolUse, statusLine)
	return cmd
}

func kbCmd() *cobra.Command {
	var agent string
	var abort bool
	var headless bool
	var session string
	var summaryFile string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "kb",
		Short: "Worktree+branch for a KB write, merge back to main and clean up; consolidate lists the KB's debt; reconcile walks existing knowledge",
		Long: `begin / finish: an isolated worktree + branch for a KB write, merged back ff-only.
consolidate: the KB's debt list — decisions without machine-readable status, accepted
decisions not landed in the docs they update or in the state digest, a missing or
over-budget state.md, candidates older than 14 days, handoff problems — in the order to
work it off. Nothing is changed; the agent does the consolidation in a kb begin/finish
worktree and rewrites the digest with "meshctl state set --summary-file <summary>".
reconcile: the reconciliation pass (ADR-0070) — the walk order, or one peer's worksheet.
confirm: bulk confirmation of reviewed entries from a reconcile report, five at a time.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("kb: want begin|finish|consolidate|reconcile|confirm, got %q", args[0])
		},
	}
	// One RunE for begin/finish/consolidate, switched on the subcommand name;
	// the inner block keeps the single-verb body as it was.
	run := func(cmd *cobra.Command, args []string) error {
		{
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
			switch cmd.Name() {
			case "consolidate":
				r := mesh.KBConsolidate(a, now)
				if asJSON {
					out, err := json.MarshalIndent(r, "", "  ")
					if err != nil {
						return err
					}
					fmt.Println(string(out))
					return nil
				}
				fmt.Print(r.Markdown())
				return nil
			case "begin":
				sid := id
				if session != "" {
					sid = session
				}
				wt, err := mesh.KBBegin(dir, a, sid, now)
				if err != nil {
					return err
				}
				fmt.Printf("KB worktree ready: %s (branch %s)\n", wt, mesh.KBBranch(sid))
				fmt.Println("edit + commit there, then `meshctl kb finish`")
			case "finish":
				sid, err := mesh.ResolveKBSession(dir, a, id, session)
				if err != nil {
					return err
				}
				res, err := mesh.KBFinish(dir, a, sid, mesh.KBFinishOptions{Abort: abort, Headless: headless, SummaryFile: summaryFile, Now: now})
				if errors.Is(err, mesh.ErrKBConflict) {
					fmt.Fprintln(os.Stderr, err.Error())
					return &mesh.CodedError{Code: 1, Msg: "kb finish: conflict — needs human"}
				}
				var nc *mesh.KBNeedsConfirmError
				if errors.As(err, &nc) {
					// Parked for the peer's own session: the receipt lines, then the
					// intake response body (status needs-human) for a delegated run.
					fmt.Print("Receipt:\n" + nc.Receipt() + "\nResponse body (`meshctl inbox respond <id> --agent " + a.Name +
						" --status needs-human --response-file -`):\n\n" + nc.ResponseBody())
					return &mesh.CodedError{Code: 1, Msg: err.Error()}
				}
				if err != nil {
					return err
				}
				if abort {
					fmt.Println("KB branch + worktree discarded")
				} else if res.Confirm != "" {
					fmt.Printf("KB changes merged to main and confirmed: %s Confirm: %s (%d ledger line(s) written, %d dropped); worktree removed\n",
						res.Confirm, res.Intent, len(res.Ledgered), len(res.Dropped))
				} else {
					fmt.Println("KB changes merged to main; worktree removed")
				}
			}
			return nil
		}
	}
	for _, sub := range []struct{ use, short string }{
		{"begin", "Open an isolated worktree + branch for a KB write"},
		{"finish", "Merge the KB branch back to main ff-only and remove the worktree (--abort discards)"},
		{"consolidate", "List the KB's debt in the order to work it off; changes nothing"},
	} {
		cmd.AddCommand(&cobra.Command{Use: sub.use, Short: sub.short, Args: cobra.NoArgs, RunE: run})
	}
	cmd.AddCommand(kbReconcileCmd(&asJSON))
	cmd.AddCommand(kbConfirmCmd(&agent, &session))
	// Shared by every kb subcommand (unchanged from the single-verb form).
	cmd.PersistentFlags().StringVar(&agent, "agent", "", "agent workspace (default: $AGENT_NAME or cwd)")
	cmd.PersistentFlags().BoolVar(&abort, "abort", false, "discard the branch + worktree (finish)")
	cmd.PersistentFlags().BoolVar(&headless, "headless", false, "no human at the prompt: never auto-resolve conflicts; a promotion parks as needs-confirm (finish)")
	cmd.PersistentFlags().StringVar(&session, "session", "", "explicit KB session id (finish auto-detects the sole worktree if unset)")
	// Read by the gate (the promotion ask) and by finish itself (the Confirm: commit, ADR-0067); candidates-only finishes ignore it.
	cmd.PersistentFlags().StringVar(&summaryFile, "summary-file", "", "your confirmation summary, <you>/.memory/confirm/<session>.md (finish; required when it promotes)")
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "machine-readable report (consolidate)")
	return cmd
}
