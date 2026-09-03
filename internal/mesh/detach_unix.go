//go:build unix

package mesh

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own process group so a background run
// outlives the short-lived hook process instead of being torn down with the
// parent's group. POSIX-only; see detach_other.go for the fallback.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
