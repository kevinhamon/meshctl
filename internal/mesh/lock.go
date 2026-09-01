package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Dispatch guard tunables, env-overridable.
func hourlyMax() int      { return envInt("AGENT_DISPATCH_HOURLY_MAX", 12) }
func staleSec() int       { return envInt("AGENT_DISPATCH_STALE_SEC", 900) }
func timeoutSec() int     { return envInt("AGENT_DISPATCH_TIMEOUT_SEC", 420) }

// lockWaitSec bounds how long a caller queues behind an in-flight dispatch
// before giving up. Most dispatches finish in seconds, so this only has to
// cover the common case; past a couple of minutes an honest "busy, retry" beats
// blocking an interactive session for the full dispatch timeout (420s).
func lockWaitSec() int { return envInt("AGENT_DISPATCH_LOCK_WAIT_SEC", 120) }

// var, not const, so tests can poll faster than a real caller needs to.
var lockPollInterval = 2 * time.Second
func dispatchModel() string { return os.Getenv("AGENT_DISPATCH_MODEL") }

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// dispatchLog appends an audit line to $AGENT_DISPATCH_LOG or .dispatch.log.
func dispatchLog(agentsDir, caller, target string, depth int, now time.Time, format string, args ...any) {
	logPath := os.Getenv("AGENT_DISPATCH_LOG")
	if logPath == "" {
		logPath = filepath.Join(agentsDir, DispatchLog)
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line := fmt.Sprintf("%s caller=%s target=%s depth=%d %s\n",
		now.UTC().Format("2006-01-02T15:04:05Z"), caller, target, depth,
		fmt.Sprintf(format, args...))
	_, _ = f.WriteString(line)
}

// hourlyCount trims .dispatch.count to the last rolling hour and returns the
// count within the window (awk-trim behavior).
func hourlyCount(agentsDir string, now time.Time) (int, error) {
	countPath := filepath.Join(agentsDir, DispatchCount)
	cutoff := now.Unix() - 3600
	b, err := os.ReadFile(countPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var kept []string
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		// count file line is "<epoch>" (possibly with trailing fields); first field is epoch.
		fields := strings.Fields(ln)
		ts, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if ts >= cutoff {
			kept = append(kept, fields[0])
		}
	}
	// Rewrite the trimmed window back (mv of the awk output).
	_ = os.WriteFile(countPath, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
	return len(kept), nil
}

func recordDispatch(agentsDir string, now time.Time) error {
	countPath := filepath.Join(agentsDir, DispatchCount)
	_ = os.MkdirAll(filepath.Dir(countPath), 0o755)
	f, err := os.OpenFile(countPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", now.Unix())
	return err
}

// singleFlightLock is the mkdir-based dispatch lock. It is atomic on POSIX and
// independent of the model obeying or of env propagation. Held returns whether
// this call created the lock (so the caller knows to release it).
type singleFlightLock struct {
	path string
	held bool
	// waitedSec is how long acquisition queued behind an in-flight dispatch.
	// Logged so a mesh that spends its time queuing is visible as such.
	waitedSec int
}

// acquire takes the single-flight lock, WAITING for an in-flight dispatch to
// finish rather than refusing immediately. Reclaims the lock only if stale.
//
// Returning straight away was the wrong contract. "A dispatch is in flight" is
// a busy signal — the peer exists and will be free shortly — but the caller
// only saw a refusal, and a model that cannot tell "busy" from "unavailable"
// concludes the peer is unreachable and carries on without it. That is the
// worst possible outcome: work proceeds unreviewed, and the operator is told
// the peer could not be reached. Queuing removes the judgement call entirely
// for the common case, which is a dispatch that ends within seconds.
func acquireDispatchLock(agentsDir, caller, target string, pid int, now time.Time) (*singleFlightLock, error) {
	lockPath := filepath.Join(agentsDir, DispatchLock)
	l := &singleFlightLock{path: lockPath}
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755) // ensure .agentmesh/ exists (ADR-0038)

	mk := func() error { return os.Mkdir(lockPath, 0o755) }

	deadline := now.Add(time.Duration(lockWaitSec()) * time.Second)
	waited := 0
	for {
		err := mk()
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, err
		}

		// Held. Reclaim if the holder died or overran; otherwise wait it out.
		if age := lockAge(lockPath, time.Now()); age > staleSec() {
			_ = os.RemoveAll(lockPath)
			if err := mk(); err != nil {
				return nil, coded(ExitLockReclaim, "could not acquire lock after reclaim: %v", err)
			}
			break
		}

		if !time.Now().Before(deadline) {
			// Still busy after the full wait. Say so in terms that cannot be
			// read as "this peer does not exist" -- that misreading is the
			// whole reason this path was rewritten.
			return nil, coded(ExitLockHeld,
				"BUSY — %s is reachable but another dispatch has been in flight for the whole %ds wait.\n"+
					"  Held by: %s\n"+
					"  This is a temporary queue, NOT an unavailable peer. Do NOT continue as though %s\n"+
					"  could not be reached, and do NOT re-send the same request — it will queue behind the\n"+
					"  same lock. Either retry once after a pause, or tell the operator that %s is busy and\n"+
					"  stop. If the holder is genuinely stuck: rm -rf %q.",
				target, lockWaitSec(), readLockInfo(lockPath), target, target, lockPath)
		}

		time.Sleep(lockPollInterval)
		waited += int(lockPollInterval / time.Second)
	}

	l.waitedSec = waited
	l.held = true
	// Write ownership info ($LOCK/info).
	_ = os.WriteFile(filepath.Join(lockPath, "info"),
		[]byte(fmt.Sprintf("pid=%d caller=%s target=%s started=%s\n",
			pid, caller, target, now.UTC().Format("2006-01-02T15:04:05Z"))), 0o644)
	return l, nil
}

// release removes the lock, but only if this process created it (never delete a
// lock you did not create).
func (l *singleFlightLock) release() {
	if l != nil && l.held {
		_ = os.RemoveAll(l.path)
		l.held = false
	}
}

func lockAge(lockPath string, now time.Time) int {
	info, err := os.Stat(lockPath)
	if err != nil {
		return 0
	}
	return int(now.Sub(info.ModTime()).Seconds())
}

func readLockInfo(lockPath string) string {
	b, err := os.ReadFile(filepath.Join(lockPath, "info"))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}
