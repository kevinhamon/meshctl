//go:build windows

package mesh

import "os/exec"

// pidAlive: Windows has no cheap, reliable liveness probe from Go's stdlib
// (os.FindProcess always succeeds), so report "unknown" and let callers fall
// back to age-based staleness.
func pidAlive(pid int) (alive, known bool) { return false, false }

// setProcGroup is a no-op on Windows; exec.CommandContext kills the direct child.
func setProcGroup(cmd *exec.Cmd) {}

// noTTY is a no-op on Windows; GIT_TERMINAL_PROMPT=0 covers git's own prompts.
func noTTY(cmd *exec.Cmd) {}
