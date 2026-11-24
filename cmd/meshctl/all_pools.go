package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kevinhamon/meshctl/internal/console"
	"github.com/kevinhamon/meshctl/internal/mesh"
	"github.com/kevinhamon/meshctl/internal/selfupdate"
)

// all_pools.go — `--all-pools` on doctor and pool upgrade: the same command
// run in every pool registered on this machine, one pool at a time. A pool
// whose path is gone or holds no pool is skipped and listed, never forgotten
// (`meshctl pool forget` stays the human's or the steward's call).

// doctorRun is one pool's doctor result, before printing.
type doctorRun struct {
	info     []string
	findings []mesh.DoctorFinding
	ok       bool
	fixed    []string
	errs     []error
}

// collectDoctor runs the checks for the pool at dir. Host-wide items (a newer
// meshctl, the console login item) are not included: see hostDoctorInfo and
// hostDoctorFindings.
func collectDoctor(dir string, fix bool, now time.Time) doctorRun {
	r := doctorRun{info: mesh.OutboundWiringLines(dir)}
	if n := mesh.UpgradeNotice(dir, resolveVersion()); n != "" {
		r.info = append(r.info, n)
	}
	r.findings, r.ok, r.fixed, r.errs = mesh.DoctorWithHygiene(dir, fix, now)
	return r
}

// hostDoctorInfo is the host-wide info doctor prints (a newer meshctl).
func hostDoctorInfo(now time.Time) []string {
	if n := selfupdate.Notice(resolveVersion(), now); n != "" {
		return []string{n}
	}
	return nil
}

// hostDoctorFindings are the host-wide findings (the console login item).
func hostDoctorFindings() []mesh.DoctorFinding {
	var out []mesh.DoctorFinding
	for _, m := range console.LoginItemFindings(console.ReadSettings()) {
		out = append(out, mesh.DoctorFinding{Agent: "console", Level: "warn", Message: m})
	}
	return out
}

// printDoctorRun prints a doctor result as text.
func printDoctorRun(r doctorRun) {
	for _, l := range r.info {
		fmt.Println("ℹ " + l)
	}
	for _, f := range r.fixed {
		fmt.Println("✓ fixed " + f)
	}
	for _, e := range r.errs {
		fmt.Println("✗ fix failed: " + e.Error())
	}
	if len(r.findings) == 0 {
		fmt.Println("doctor: clean ✓")
		return
	}
	for _, f := range r.findings {
		mark := "  "
		if f.Level == "error" {
			mark = "✗ "
		} else if f.Level == "warn" {
			mark = "⚠ "
		}
		msg := f.Message
		if f.Fixable {
			msg += "  (fixable: meshctl doctor --fix)"
		}
		fmt.Printf("%s[%s] %s\n", mark, f.Agent, msg)
	}
	if r.ok {
		fmt.Println("doctor: no errors (warnings above)")
	}
}

// allPoolsDoctorJSON is one pool's element of `doctor --all-pools --json`.
type allPoolsDoctorJSON struct {
	Pool   string                 `json:"pool"`
	Path   string                 `json:"path"`
	State  mesh.PoolState         `json:"state"`            // ok | missing | not-a-pool
	Reason string                 `json:"reason,omitempty"` // why the pool was skipped
	Report *mesh.DoctorReportJSON `json:"report,omitempty"` // what `meshctl doctor --json` prints there
	Agents []allPoolsAgentJSON    `json:"agents,omitempty"` // the harness matrix rows
	Error  string                 `json:"error,omitempty"`
}

// allPoolsAgentJSON is one agent's row of the harness matrix.
type allPoolsAgentJSON struct {
	Agent     string                      `json:"agent"`
	Harnesses map[string]mesh.HarnessCell `json:"harnesses"`
}

// runDoctorAllPools is `doctor --all-pools`: doctor in every registered pool
// that is present, then the skipped pools and the harness matrix. Non-zero
// exit when any pool's doctor has errors; a skipped pool alone is not one.
func runDoctorAllPools(fix, asJSON bool, now time.Time) error {
	pools, err := mesh.AllPools()
	if err != nil {
		return err
	}
	installed := mesh.InstalledHarnesses()
	ids := mesh.RegisteredHarnessIDs()
	hostInfo, hostFindings := hostDoctorInfo(now), hostDoctorFindings()
	type matrixRow struct {
		pool string
		row  mesh.AgentHarnessRow
	}
	var (
		failed  []string
		skipped []mesh.WalkedPool
		matrix  []matrixRow
		out     = []allPoolsDoctorJSON{}
	)
	if !asJSON && (len(hostInfo) > 0 || len(hostFindings) > 0) {
		fmt.Println("=== this machine ===")
		printDoctorRun(doctorRun{info: hostInfo, findings: hostFindings, ok: true})
		fmt.Println()
	}
	for _, p := range pools {
		el := allPoolsDoctorJSON{Pool: p.Name, Path: p.Path, State: p.State, Reason: p.Reason()}
		if p.State != mesh.PoolOK {
			skipped = append(skipped, p)
			out = append(out, el)
			continue
		}
		r := collectDoctor(p.Path, fix, now)
		if !r.ok {
			failed = append(failed, p.Name)
		}
		rows, merr := mesh.HarnessMatrix(p.Path, installed)
		for _, row := range rows {
			matrix = append(matrix, matrixRow{pool: p.Name, row: row})
			el.Agents = append(el.Agents, allPoolsAgentJSON{Agent: row.Agent, Harnesses: row.Cells})
		}
		if merr != nil {
			el.Error = "harness matrix: " + merr.Error()
		}
		if asJSON {
			// Each report is what `meshctl doctor --json` prints in that pool.
			r.info = append(r.info, hostInfo...)
			r.findings = append(r.findings, hostFindings...)
			rep := mesh.NewDoctorReport(r.findings, r.ok, r.info, now)
			el.Report = &rep
			out = append(out, el)
			continue
		}
		fmt.Printf("=== pool %s (%s) ===\n", p.Name, p.Path)
		printDoctorRun(r)
		if merr != nil {
			fmt.Println("✗ " + el.Error)
		}
		fmt.Println()
	}
	if asJSON {
		if err := printJSON(out); err != nil {
			return err
		}
	} else {
		fmt.Println("=== all pools ===")
		if len(pools) == 0 {
			fmt.Println("no pools registered on this machine (`meshctl pool list`)")
		}
		printSkippedPools(skipped)
		if len(matrix) > 0 {
			fmt.Println("harnesses (wired; missing = installed here, not wired; n/a = not installed here):")
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "  POOL\tAGENT\t%s\n", strings.Join(ids, "\t"))
			for _, m := range matrix {
				cells := make([]string, 0, len(ids))
				for _, id := range ids {
					cells = append(cells, string(m.row.Cells[id]))
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.pool, m.row.Agent, strings.Join(cells, "\t"))
			}
			tw.Flush()
		}
	}
	if len(failed) > 0 {
		return &mesh.CodedError{Code: 1, Msg: "doctor found errors in: " + strings.Join(failed, ", ")}
	}
	return nil
}

// printSkippedPools lists the registry entries a host-wide walk skipped.
func printSkippedPools(skipped []mesh.WalkedPool) {
	if len(skipped) == 0 {
		return
	}
	fmt.Println("skipped pools:")
	for _, p := range skipped {
		fmt.Printf("  ⚠ %s (%s): %s\n", p.Name, p.Path, p.Reason())
	}
}

// allPoolsCheckJSON is one pool's element of `pool upgrade --all-pools --check --json`.
type allPoolsCheckJSON struct {
	Pool   string                 `json:"pool"`
	Path   string                 `json:"path"`
	State  mesh.PoolState         `json:"state"`
	Reason string                 `json:"reason,omitempty"`
	Check  *mesh.UpgradeCheckJSON `json:"check,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// runPoolUpgradeAllPools is `pool upgrade --all-pools`: the upgrade (or its
// --check) in every registered pool that is present. Each pool stands alone:
// a failure is reported and the walk goes on; the exit is non-zero when any
// pool failed, needs a human, or (with --check) has an upgrade pending.
func runPoolUpgradeAllPools(check, doctrineOnly, asJSON, force, headless bool) error {
	// --force discards local doctrine edits: a per-pool decision. --headless
	// applies under one pool's own grant; a grant never reaches other pools.
	if force {
		return fmt.Errorf("--all-pools does not take --force: overwriting edited doctrine is a per-pool decision — run `meshctl pool upgrade --force` in that pool")
	}
	if headless {
		return fmt.Errorf("--all-pools does not take --headless: a pool's upgrade grant covers that pool only — run `meshctl pool upgrade --headless` in each pool")
	}
	if !check {
		if cause := mesh.CLINoHumanCause(false); cause != "" {
			return &mesh.CodedError{Code: 1, Msg: "refused: `pool upgrade --all-pools` needs a human at the prompt (" + cause + "); --check is allowed"}
		}
	}
	pools, err := mesh.AllPools()
	if err != nil {
		return err
	}
	var (
		failed, pending []string
		skipped         []mesh.WalkedPool
		out             = []allPoolsCheckJSON{}
	)
	for _, p := range pools {
		if p.State != mesh.PoolOK {
			skipped = append(skipped, p)
			out = append(out, allPoolsCheckJSON{Pool: p.Name, Path: p.Path, State: p.State, Reason: p.Reason()})
			continue
		}
		if check {
			el := allPoolsCheckJSON{Pool: p.Name, Path: p.Path, State: p.State}
			plan, err := mesh.PlanPoolUpgrade(p.Path, time.Now())
			if err != nil {
				failed = append(failed, p.Name)
				el.Error = err.Error()
				if !asJSON {
					fmt.Printf("=== pool %s (%s) ===\n✗ %s\n\n", p.Name, p.Path, err)
				}
				out = append(out, el)
				continue
			}
			if plan.Pending() {
				pending = append(pending, p.Name)
			}
			if asJSON {
				cj := mesh.NewUpgradeCheckJSON(plan, doctrineOnly, time.Now())
				el.Check = &cj
				out = append(out, el)
				continue
			}
			fmt.Printf("=== pool %s (%s) ===\n", p.Name, p.Path)
			printUpgradePlan(plan, doctrineOnly)
			fmt.Println()
			continue
		}
		fmt.Printf("=== pool %s (%s) ===\n", p.Name, p.Path)
		if err := runPoolUpgrade(p.Path, false, doctrineOnly); err != nil {
			failed = append(failed, p.Name)
			fmt.Println("✗ " + errText(err))
		}
		fmt.Println()
	}
	if check && asJSON {
		if err := printJSON(out); err != nil {
			return err
		}
	} else {
		fmt.Println("=== all pools ===")
		if len(pools) == 0 {
			fmt.Println("no pools registered on this machine (`meshctl pool list`)")
		}
		printSkippedPools(skipped)
		if len(pending) > 0 {
			fmt.Println("upgrade pending in: " + strings.Join(pending, ", "))
		}
		if len(failed) > 0 {
			fmt.Println("failed or need a human: " + strings.Join(failed, ", ") + " (each listed above)")
		}
	}
	switch {
	case len(failed) > 0:
		return &mesh.CodedError{Code: 1, Msg: fmt.Sprintf("%d pool(s) failed or need a human: %s", len(failed), strings.Join(failed, ", "))}
	case len(pending) > 0:
		return &mesh.CodedError{Code: 1, Msg: "upgrade pending in " + strings.Join(pending, ", ") + " — run `meshctl pool upgrade --all-pools` to apply"}
	}
	return nil
}

// errText is an error's message, CodedError's without the code.
func errText(err error) string {
	if ce, ok := err.(*mesh.CodedError); ok {
		return ce.Msg
	}
	return err.Error()
}
