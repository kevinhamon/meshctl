//go:build !unix

package mesh

import "os/exec"

// detachProcess is a no-op on platforms without POSIX process groups (e.g.
// Windows). cmd.Process.Release already drops the parent's handle, and these
// platforms don't tear a child down with the parent's group the way POSIX does.
func detachProcess(cmd *exec.Cmd) {}
