//go:build windows

package mesh

// processAlive is unknown on Windows; sessions there fall back to the
// heartbeat (OpenSessions treats an unknown harness like a stale one).
func processAlive(pid int) bool { return false }
