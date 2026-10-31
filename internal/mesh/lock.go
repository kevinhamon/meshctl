package mesh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Dispatch guard tunables, env-overridable.
func hourlyMax() int  { return envInt("AGENT_DISPATCH_HOURLY_MAX", 12) }
func staleSec() int   { return envInt("AGENT_DISPATCH_STALE_SEC", 900) }
func timeoutSec() int { return envInt("AGENT_DISPATCH_TIMEOUT_SEC", 420) }

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
		logPath = DispatchLogPath(agentsDir)
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
	countPath := DispatchCountPath(agentsDir)
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
	// Rewrite the trimmed window back, atomically, so a concurrent reader never
	// sees a half-written file. Only rewrite when something was trimmed: a
	// rewrite races with a concurrent append, and most calls trim nothing.
	if len(kept) != len(strings.Fields(string(b))) {
		_ = writeFileAtomic(countPath, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
	}
	return len(kept), nil
}

func recordDispatch(agentsDir string, now time.Time) error {
	countPath := DispatchCountPath(agentsDir)
	_ = os.MkdirAll(filepath.Dir(countPath), 0o755)
	f, err := os.OpenFile(countPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", now.Unix())
	return err
}

// unrecordDispatch removes one entry stamped `now` from the count file: the
// cap is charged BEFORE the lock wait (so concurrent callers cannot all pass
// at max-1), and refunded when the dispatch never ran (BUSY, interrupted).
func unrecordDispatch(agentsDir string, now time.Time) {
	countPath := DispatchCountPath(agentsDir)
	b, err := os.ReadFile(countPath)
	if err != nil {
		return
	}
	want := fmt.Sprintf("%d", now.Unix())
	var kept []string
	removed := false
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if !removed && strings.Fields(ln)[0] == want {
			removed = true
			continue
		}
		kept = append(kept, ln)
	}
	if removed {
		_ = writeFileAtomic(countPath, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
	}
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
func acquireDispatchLock(ctx context.Context, agentsDir, caller, target string, pid int, now time.Time) (*singleFlightLock, error) {
	lockPath := DispatchLockPath(agentsDir)
	l := &singleFlightLock{path: lockPath}
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755) // ensure the pool dir exists

	mk := func() error { return os.Mkdir(lockPath, 0o755) }
	reclaim := lockPath + ".reclaim"

	deadline := now.Add(time.Duration(lockWaitSec()) * time.Second)
	waited := 0
	for {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err := mk()
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, err
		}

		// Held. Reclaim only if the holder is gone: its process is dead (when the
		// platform can tell), or it is past the stale window. A live holder past
		// the window is left alone — the BUSY message names it for the operator.
		if lockReclaimable(lockPath, time.Now()) {
			// Reclaim under a mutex, re-checking staleness once inside it. A
			// waiter that judged the old lock stale can otherwise lose the race,
			// then rename away the fresh lock the winner just made — two holders.
			if reclaimLocked(reclaim) {
				if lockReclaimable(lockPath, time.Now()) {
					tomb := fmt.Sprintf("%s.stale-%d", lockPath, time.Now().UnixNano())
					if err := os.Rename(lockPath, tomb); err == nil {
						_ = os.RemoveAll(tomb)
					}
				}
				err := mk()
				_ = os.Remove(reclaim)
				if err == nil {
					break
				}
			}
			time.Sleep(lockPollInterval / 4)
			continue
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

// lockReclaimable decides whether a held lock may be taken over. The holder's
// pid is recorded in $LOCK/info; a dead pid is reclaimable at once, a live one
// never is (a hung holder is the operator's call, see the BUSY message), and an
// unknown one (no info yet, or a platform that cannot probe) falls back to age.
func lockReclaimable(lockPath string, now time.Time) bool {
	if pid := lockHolderPID(lockPath); pid > 0 {
		if alive, known := pidAlive(pid); known {
			return !alive
		}
	}
	return lockAge(lockPath, now) > staleSec()
}

// reclaimLocked takes the stale-reclaim mutex. The mutex is held for a rename
// and a mkdir only, so one older than the stale window was left by a crashed
// reclaimer and is cleared for the next attempt.
func reclaimLocked(reclaim string) bool {
	if os.Mkdir(reclaim, 0o755) == nil {
		return true
	}
	if lockAge(reclaim, time.Now()) > staleSec() {
		_ = os.Remove(reclaim)
	}
	return false
}

// lockHolderPID parses `pid=N` out of $LOCK/info; 0 if absent.
func lockHolderPID(lockPath string) int {
	for _, f := range strings.Fields(readLockInfo(lockPath)) {
		if v, ok := strings.CutPrefix(f, "pid="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

func readLockInfo(lockPath string) string {
	b, err := os.ReadFile(filepath.Join(lockPath, "info"))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}
