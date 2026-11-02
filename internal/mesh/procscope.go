package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
)

// A process ID is only valid in the process namespace that issued it: the same
// number elsewhere names a different process, or none. Records that carry a pid
// (sessions, scheduled-job lists, the dispatch lock) also record that
// namespace, as the host name plus, on Linux, the pid namespace. A reader in
// another scope does not probe the pid; it goes by heartbeat or age instead.

// procScope is this process's scope. A variable so tests can stand in another.
var procScope = sync.OnceValue(computeProcScope)

func computeProcScope() string {
	host, _ := os.Hostname()
	if ns, err := os.Readlink("/proc/self/ns/pid"); err == nil {
		return host + "/" + ns
	}
	return host
}

// scopeLocal reports whether a pid recorded under scope can be probed here. An
// empty scope is a record written before scopes existed, read as local.
func scopeLocal(scope string) bool { return scope == "" || scope == procScope() }

// scopeTag is a short, path-safe stand-in for a scope.
func scopeTag(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:4])
}
