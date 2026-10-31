package console

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// hub.go — one console for every pool on this machine (since
// v1.8.2). The hub runs one engine per live pool in the registry, plus the
// pool the console was started from, and tells each page about its own pool
// and a summary of all of them, so the switcher shows where work waits.
// Each engine still reads only its own pool; the hub only lists them.

// registrySync is how often the hub re-reads the pool registry.
const registrySync = time.Minute

// PoolSummary is one pool's line in the switcher.
type PoolSummary struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Ready    bool   `json:"ready"`     // pending computed at least once
	Pending  int    `json:"pending"`   // items waiting, across peers
	NeedsYou int    `json:"needs_you"` // peers with KB branches or conflicts
	Waiting  int    `json:"waiting"`   // other peers with work for the person (a phrase to say, or read-back)
	Agents   int    `json:"agents"`
	Jobs     int    `json:"jobs"` // scheduled jobs across open sessions
	Errors   int    `json:"errors"`
	Warnings int    `json:"warnings"`
	Sessions int    `json:"sessions"`
	Upgrade  bool   `json:"upgrade"`
	// Unrestorable counts agents doctor says could not be rebuilt on another
	// machine (no repo or no remote; pool-clone C6).
	Unrestorable int `json:"unrestorable"`
}

// Frame is what a page receives: its pool's snapshot, which pool that is,
// and every pool's summary. The snapshot's fields stay at the top level, so
// a reader of a single-pool snapshot reads a frame unchanged.
type Frame struct {
	Snapshot
	PoolKey string        `json:"pool_key"`
	Pools   []PoolSummary `json:"pools"`
	// Boot is new each time the console starts. Document links are signed
	// per process, so a page that sees it change refetches them.
	Boot string `json:"boot"`
}

// PoolRef is a pool the hub serves: its key in URLs, name and root.
type PoolRef struct {
	Key, Name, Path string
}

// ErrNoPool: the key names no pool this console serves.
var ErrNoPool = errors.New("no such pool in this console")

// Hub owns one engine per pool and the pages subscribed to them.
type Hub struct {
	start   string // the pool the console was started from; "" outside one
	version string
	boot    string
	// Entries lists the registered pools; tests replace it.
	Entries func() ([]mesh.PoolEntry, error)

	mu    sync.Mutex
	ctx   context.Context
	refs  []PoolRef
	pools map[string]*hubPool // by path
	subs  map[*hubSub]struct{}
}

type hubPool struct {
	eng  *Engine
	stop context.CancelFunc
}

type hubSub struct {
	key string
	ch  chan []byte
}

// NewHub prepares a hub; start is the pool the console opens on ("" for the
// first registered pool).
func NewHub(start, version string) *Hub {
	return &Hub{start: start, version: version, boot: strconv.FormatInt(time.Now().UnixNano(), 36), Entries: mesh.RegisteredPools,
		pools: map[string]*hubPool{}, subs: map[*hubSub]struct{}{}}
}

// PoolRefs orders and keys the pools to serve: the start pool first when it
// is not registered, then every live registered pool in registry order.
// Keys are the names made URL-safe; a repeated name gets -2, -3, ...
func PoolRefs(start string, entries []mesh.PoolEntry) []PoolRef {
	var refs []PoolRef
	seen := map[string]bool{}
	add := func(name, path string) {
		id := samePath(path)
		if seen[id] {
			return
		}
		seen[id] = true
		refs = append(refs, PoolRef{Name: name, Path: path})
	}
	if start != "" {
		registered := false
		for _, e := range entries {
			registered = registered || samePath(e.Path) == samePath(start)
		}
		if !registered {
			add(filepath.Base(start), start)
		}
	}
	for _, e := range entries {
		if mesh.PoolIsLive(e) || (start != "" && samePath(e.Path) == samePath(start)) {
			add(e.Name, e.Path)
		}
	}
	used := map[string]int{}
	for i := range refs {
		k := slug(refs[i].Name)
		used[k]++
		if used[k] > 1 {
			k += "-" + strconv.Itoa(used[k])
		}
		refs[i].Key = k
	}
	return refs
}

// samePath compares pool roots through symlinks (/tmp and /private/tmp).
func samePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "pool"
	}
	return b.String()
}

// Sync re-reads the registry: engines start for new pools and stop for
// pools that are gone. Before Run, it only records the pools.
func (h *Hub) Sync() {
	entries, _ := h.Entries()
	refs := PoolRefs(h.start, entries)
	h.mu.Lock()
	h.refs = refs
	want := map[string]bool{}
	for _, r := range refs {
		want[r.Path] = true
		if _, ok := h.pools[r.Path]; ok {
			continue
		}
		e := NewEngine(r.Path, h.version)
		e.OnUpdate = h.publish
		p := &hubPool{eng: e}
		if h.ctx != nil {
			ctx, cancel := context.WithCancel(h.ctx)
			p.stop = cancel
			go func() { _ = e.Run(ctx) }()
		}
		h.pools[r.Path] = p
	}
	for path, p := range h.pools {
		if !want[path] {
			if p.stop != nil {
				p.stop()
			}
			delete(h.pools, path)
		}
	}
	h.mu.Unlock()
	h.publish()
}

// Run starts every pool's engine and keeps the list current until ctx ends.
func (h *Hub) Run(ctx context.Context) error {
	h.mu.Lock()
	h.ctx = ctx
	for _, p := range h.pools { // pools recorded by an earlier Sync
		if p.stop == nil {
			c, cancel := context.WithCancel(ctx)
			p.stop = cancel
			go func() { _ = p.eng.Run(c) }()
		}
	}
	h.mu.Unlock()
	h.Sync()
	t := time.NewTicker(registrySync)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			h.Sync()
		}
	}
}

// Pools lists the pools served, in switcher order.
func (h *Hub) Pools() []PoolRef {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PoolRef(nil), h.refs...)
}

// DefaultKey is the pool a page opens on: the start pool, else the first.
func (h *Hub) DefaultKey() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.defaultKeyLocked()
}

func (h *Hub) defaultKeyLocked() string {
	for _, r := range h.refs {
		if h.start != "" && samePath(r.Path) == samePath(h.start) {
			return r.Key
		}
	}
	if len(h.refs) > 0 {
		return h.refs[0].Key
	}
	return ""
}

// Engine returns the engine for key ("" for the default pool) and the key.
func (h *Hub) Engine(key string) (*Engine, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if key == "" {
		key = h.defaultKeyLocked()
	}
	for _, r := range h.refs {
		if r.Key == key {
			if p, ok := h.pools[r.Path]; ok {
				return p.eng, key, nil
			}
		}
	}
	return nil, "", ErrNoPool
}

// Frame builds the frame for key ("" for the default pool).
func (h *Hub) Frame(key string) (Frame, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.frameLocked(key)
	if !ok {
		return Frame{}, ErrNoPool
	}
	return f, nil
}

// frameLocked builds key's frame; false when key names no pool served, in
// which case the frame still lists every pool.
func (h *Hub) frameLocked(key string) (Frame, bool) {
	if key == "" {
		key = h.defaultKeyLocked()
	}
	f := Frame{PoolKey: key, Pools: []PoolSummary{}, Boot: h.boot}
	found := false
	for _, r := range h.refs {
		p, ok := h.pools[r.Path]
		if !ok {
			continue
		}
		s := p.eng.Snapshot()
		if r.Key == key {
			f.Snapshot, found = s, true
		}
		f.Pools = append(f.Pools, Summarize(r, s))
	}
	return f, found
}

// Summarize is a pool's switcher line from its snapshot.
func Summarize(r PoolRef, s Snapshot) PoolSummary {
	ps := PoolSummary{Key: r.Key, Name: r.Name, Path: r.Path, Ready: s.Pending.Agents != nil, Sessions: len(s.Sessions), Agents: len(s.Agents)}
	for _, a := range s.Pending.Agents {
		ps.Pending += a.Total
		switch agentAttention(a) {
		case attentionNeedsYou:
			ps.NeedsYou++
		case attentionWaiting:
			ps.Waiting++
		}
	}
	for _, se := range s.Sessions {
		ps.Jobs += len(se.Jobs)
	}
	if s.Doctor != nil {
		for _, f := range s.Doctor.Findings {
			if f.Level == "error" {
				ps.Errors++
			} else {
				ps.Warnings++
			}
			if f.Kind == mesh.FindingNotRestorable {
				ps.Unrestorable++
			}
		}
	}
	for _, n := range s.Notices {
		if n.Kind == "upgrade" {
			ps.Upgrade = true
		}
	}
	return ps
}

// Subscribe returns a channel of frames for key ("" for the default pool),
// the current one first, and a cancel func. Any pool's change sends a new
// frame, since the summaries move with it.
func (h *Hub) Subscribe(key string) (<-chan []byte, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.frameLocked(key)
	if !ok {
		return nil, nil, ErrNoPool
	}
	sub := &hubSub{key: f.PoolKey, ch: make(chan []byte, 4)}
	h.subs[sub] = struct{}{}
	b, _ := json.Marshal(f)
	sub.ch <- b
	return sub.ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[sub]; ok {
			delete(h.subs, sub)
			close(sub.ch)
		}
		h.mu.Unlock()
	}, nil
}

// publish sends every page a fresh frame. A slow page loses its oldest
// frame, never the newest, as with a single engine.
func (h *Hub) publish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		// A pool that left the registry gets a frame with no snapshot; the
		// page then moves to a pool still listed.
		f, _ := h.frameLocked(sub.key)
		b, _ := json.Marshal(f)
		select {
		case sub.ch <- b:
		default:
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- b:
			default:
			}
		}
	}
}
