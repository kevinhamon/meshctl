// Package console is `meshctl console`: a near-real-time view of a pool,
// served on localhost. It is
// read-only except for doctor's automatic repairs.
//
// The engine keeps one snapshot of the pool and recomputes it when the
// pool's state files change. Files are the source of truth and the "message
// bus": there is no broker and no database. Pending work is recomputed on
// every change (debounced), doctor on its own slower cadence, and a minute
// tick covers state that changes with time alone (ages, the 14-day rule,
// presence expiry, stale claims).
package console

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/kevinhamon/meshctl/internal/mesh"
)

// Cadences.
const (
	debounce      = 250 * time.Millisecond
	tickEvery     = time.Minute
	doctorEvery   = 10 * time.Minute
	doctorMinGap  = time.Minute // a change reruns doctor at most this often
	watchRescanOn = time.Minute // new peers and folders are picked up on the tick
)

// Snapshot is what the console serves: GET /api/snapshot and every
// server-sent event carry one.
type Snapshot struct {
	Schema    int                    `json:"schema"`
	Generated string                 `json:"generated"`
	Pool      string                 `json:"pool"`
	Version   string                 `json:"version"`
	Pending   mesh.PendingReport     `json:"pending"`
	Doctor    *mesh.DoctorReportJSON `json:"doctor"` // nil until the first run finishes
	Notices   []Notice               `json:"notices"`
	Sessions  []SessionReport        `json:"sessions"`
	Agents    []AgentInfo            `json:"agents"`
	Console   mesh.ConsoleSettings   `json:"console"` // how the console looks for this pool (pool/console.yaml)
	// PoolPush is the doctor line the Push button sits on: the pool repo is
	// clean and ahead of its remote. Empty otherwise.
	PoolPush string `json:"pool_push,omitempty"`
}

// Notice is a pool-level message: not any one peer's work.
type Notice struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Engine owns the snapshot and its subscribers.
type Engine struct {
	dir     string
	version string
	now     func() time.Time

	mu   sync.Mutex
	snap Snapshot
	subs map[chan []byte]struct{}

	recheck   chan struct{}
	fixing    sync.Mutex // one doctor repair at a time
	upgrading sync.Mutex // one pool upgrade at a time
	pushing   sync.Mutex // one pool push at a time
	repoGit   sync.Mutex // one agent repo commit or push at a time

	// OnUpdate, when set before Run, is called after each new snapshot is
	// published (outside the engine's lock): the hub's cue to tell every
	// page that the pool summaries moved.
	OnUpdate func()
}

// NewEngine prepares an engine for the pool at dir; Run starts it.
func NewEngine(dir, version string) *Engine {
	return &Engine{dir: dir, version: version, now: time.Now, subs: map[chan []byte]struct{}{},
		recheck: make(chan struct{}, 1)}
}

// Dir is the pool root the engine watches.
func (e *Engine) Dir() string { return e.dir }

// Snapshot returns the current snapshot.
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snap
}

// Subscribe returns a channel that receives each new snapshot as JSON, the
// current one first, and a cancel func.
func (e *Engine) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 4)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	b, _ := json.Marshal(e.snap)
	e.mu.Unlock()
	ch <- b
	return ch, func() {
		e.mu.Lock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
		e.mu.Unlock()
	}
}

// Recheck asks for a doctor run now (the read-only "re-check" button).
func (e *Engine) Recheck() {
	select {
	case e.recheck <- struct{}{}:
	default: // one already queued
	}
}

// update applies f to the snapshot and publishes the result.
func (e *Engine) update(f func(*Snapshot)) {
	e.mu.Lock()
	f(&e.snap)
	e.snap.Schema = mesh.ReportSchema
	e.snap.Generated = e.now().UTC().Format(time.RFC3339)
	e.snap.Pool = e.dir
	e.snap.Version = e.version
	if e.snap.Notices == nil {
		e.snap.Notices = []Notice{}
	}
	if e.snap.Sessions == nil {
		e.snap.Sessions = []SessionReport{}
	}
	if e.snap.Agents == nil {
		e.snap.Agents = []AgentInfo{}
	}
	b, _ := json.Marshal(e.snap)
	for ch := range e.subs {
		// A slow reader loses its oldest frame, never the newest: each frame
		// carries the whole snapshot, so the latest one is all it needs.
		select {
		case ch <- b:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- b:
			default:
			}
		}
	}
	e.mu.Unlock()
	if e.OnUpdate != nil {
		e.OnUpdate()
	}
}

// RefreshPending recomputes every peer's pending work.
func (e *Engine) RefreshPending() {
	now := e.now()
	blocks, err := mesh.PendingAll(e.dir, now)
	if err != nil {
		return
	}
	rep := mesh.NewPendingReport(blocks, now)
	notices := []Notice{}
	if n := mesh.UpgradeNotice(e.dir, e.version); n != "" {
		notices = append(notices, Notice{Kind: "upgrade", Message: n})
	}
	sessions := Sessions(e.dir, now)
	roster := Roster(e.dir)
	console := mesh.ReadConsoleSettings(e.dir)
	e.update(func(s *Snapshot) {
		s.Pending, s.Notices, s.Sessions, s.Agents, s.Console = rep, notices, sessions, roster, console
	})
}

// Fix applies doctor's automatic repairs, as `meshctl doctor --fix` does,
// and publishes the report that follows. ran is false when a repair is
// already running for this pool.
func (e *Engine) Fix() (fixed, failed []string, ran bool) {
	if !e.fixing.TryLock() {
		return nil, nil, false
	}
	defer e.fixing.Unlock()
	now := e.now()
	findings, ok, fixed, errs := mesh.DoctorWithHygiene(e.dir, true, now)
	rep := mesh.NewDoctorReport(findings, ok, mesh.OutboundWiringLines(e.dir), now)
	push := mesh.PoolPushPreview(e.dir).Finding
	e.update(func(s *Snapshot) { s.Doctor, s.PoolPush = &rep, push })
	e.RefreshPending() // a repair can drop stale claims
	failed = []string{}
	for _, err := range errs {
		failed = append(failed, err.Error())
	}
	return append([]string{}, fixed...), failed, true
}

// RefreshDoctor runs doctor (with the hygiene items, never --fix).
func (e *Engine) RefreshDoctor() {
	now := e.now()
	findings, ok, _, _ := mesh.DoctorWithHygiene(e.dir, false, now)
	rep := mesh.NewDoctorReport(findings, ok, mesh.OutboundWiringLines(e.dir), now)
	push := mesh.PoolPushPreview(e.dir).Finding
	e.update(func(s *Snapshot) { s.Doctor, s.PoolPush = &rep, push })
}

// Run computes the first snapshot, then keeps it current until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	// Watch first, compute second: a change made right after the first
	// snapshot is then always seen.
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	watched := map[string]bool{}
	rescan := func() {
		for _, p := range WatchPaths(e.dir) {
			if !watched[p] && w.Add(p) == nil {
				watched[p] = true
			}
		}
	}
	rescan()
	e.RefreshPending()

	var (
		changeTimer  *time.Timer
		changeC      <-chan time.Time
		lastDoctor   = e.now()
		doctorWanted bool
	)
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	doctorTick := time.NewTicker(doctorEvery)
	defer doctorTick.Stop()
	doctorDone := make(chan struct{}, 1)
	doctorRunning := true
	go func() { e.RefreshDoctor(); doctorDone <- struct{}{} }()
	runDoctor := func() {
		if doctorRunning {
			doctorWanted = true
			return
		}
		doctorRunning, doctorWanted, lastDoctor = true, false, e.now()
		go func() { e.RefreshDoctor(); doctorDone <- struct{}{} }()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if !Relevant(ev.Name) {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					rescan()
				}
			}
			if changeTimer == nil {
				changeTimer = time.NewTimer(debounce)
				changeC = changeTimer.C
			} else {
				changeTimer.Reset(debounce)
			}
		case <-w.Errors:
			// A lost watch is covered by the minute tick.
		case <-changeC:
			changeTimer, changeC = nil, nil
			e.RefreshPending()
			if e.now().Sub(lastDoctor) >= doctorMinGap {
				runDoctor()
			} else {
				doctorWanted = true
			}
		case <-tick.C:
			rescan()
			e.RefreshPending()
			if doctorWanted && e.now().Sub(lastDoctor) >= doctorMinGap {
				runDoctor()
			}
		case <-doctorTick.C:
			runDoctor()
		case <-e.recheck:
			runDoctor()
		case <-doctorDone:
			doctorRunning = false
		}
	}
}

// WatchPaths lists the directories whose changes can move a snapshot: the
// pool directory and its worktrees, and per peer its workspace root, inbox,
// presence, runtime state (session records, confirmations, reconcile reports,
// outbound audit) and knowledge top level (with candidates), plus the git
// files that name branches. Directories only (kqueue opens one descriptor
// per watched file); knowledge sub-folders are covered by the minute tick and
// by commits, which move the refs.
func WatchPaths(dir string) []string {
	var out []string
	add := func(p string) {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	pool := mesh.PoolDir(dir)
	add(pool)
	add(filepath.Join(pool, mesh.WorktreesDirName))
	add(filepath.Join(pool, mesh.MemoryDir)) // the upgrade stamp
	agents, _, err := mesh.ScanAgents(dir)
	if err != nil {
		return out
	}
	for _, a := range agents {
		d := a.Dir()
		add(d)
		add(a.InboxPath())
		add(filepath.Join(d, mesh.SessionsDir))         // presence
		add(filepath.Join(d, mesh.SessionsDir, "jobs")) // scheduled jobs, per harness process
		add(filepath.Join(d, mesh.MemoryDir))
		for _, sub := range []string{"sessions", "confirm", "reconcile", mesh.OutboundDir} {
			add(filepath.Join(d, mesh.MemoryDir, sub))
		}
		add(filepath.Join(d, mesh.KnowledgeDir))
		add(filepath.Join(d, mesh.KnowledgeDir, "candidates"))
		for _, f := range a.DocFolders() { // documents (top level; sub-folders on the tick)
			if root, ok := docRoot(d, f); ok {
				add(root)
			}
		}
		add(filepath.Join(d, ".git"))
		refs := filepath.Join(d, ".git", "refs", "heads")
		add(refs)
		_ = filepath.WalkDir(filepath.Join(refs, "mesh"), func(p string, de os.DirEntry, err error) error {
			if err == nil && de.IsDir() {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

// gitRelevant are the files inside .git whose change means a branch moved
// or appeared. Everything else there (the index, locks, logs) changes on
// every git command, including the console's own, and would loop.
var gitRelevant = map[string]bool{"HEAD": true, "packed-refs": true, "config": true}

// Relevant reports whether a change to path can move the snapshot.
func Relevant(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".tmp") || strings.HasPrefix(base, ".#") {
		return false
	}
	if filepath.Base(filepath.Dir(path)) == ".git" {
		return gitRelevant[base]
	}
	return true
}
