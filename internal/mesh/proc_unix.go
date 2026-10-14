//go:build !windows

package mesh

import (
	"errors"
	"os/exec"
	"syscall"
)

// pidAlive reports whether a process with the given pid exists. known is false
// when the platform cannot tell (callers then fall back to age-based staleness).
func pidAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, syscall.EPERM):
		return true, true // exists, owned by someone else
	default:
		return false, true
	}
}

// setProcGroup puts the child in its own process group so a timeout or an
// interrupt can take the whole tree (harness + the MCP servers it spawned) down,
// not just the direct child.
func setProcGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid = the process group created by Setpgid.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
