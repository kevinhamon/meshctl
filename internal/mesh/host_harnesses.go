package mesh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// host_harnesses.go — a host-wide cache of which registered harnesses are
// installed, so doctor and `--all-pools` can say "installed but not wired"
// without spawning a version probe for every harness on every call.
//
// The cache lives beside the pool registry ($XDG_CONFIG_HOME/meshctl/
// harnesses.json). Each harness's entry is keyed by its fingerprint: the
// resolved binary path, the binary's mtime and size, and the value of its
// probe-override env var. Only a harness whose fingerprint changed (binary
// upgraded, installed, removed, override set) is probed again. A missing or
// corrupt cache reads as empty and is rewritten.
//
// Even a cache hit stats every harness binary and reads a file, and a miss
// spawns a process: InstalledHarnesses belongs at wire, upgrade and doctor
// time, never on a hook's hot path (hooks read the running harness from env).

const hostHarnessCacheSchema = 1

// hostHarnessCache is the on-disk shape of harnesses.json.
type hostHarnessCache struct {
	Schema    int                         `json:"schema"`
	Harnesses map[string]hostHarnessEntry `json:"harnesses"`
}

// harnessFingerprint is what must be unchanged for a cached probe to stand.
type harnessFingerprint struct {
	Path     string `json:"path"`               // resolved binary path; "" ⇒ not on PATH
	MtimeNs  int64  `json:"mtime_ns,omitempty"` // binary mtime (unix ns)
	Size     int64  `json:"size,omitempty"`     // binary size in bytes
	Override string `json:"override,omitempty"` // value of the probe-override env var
}

// hostHarnessEntry is one harness's cached probe result.
type hostHarnessEntry struct {
	harnessFingerprint
	Installed bool   `json:"installed"`
	Variant   int    `json:"variant"`
	Probed    string `json:"probed"` // RFC 3339, when the probe ran
}

// Test seams: tests swap these so they never resolve or probe real binaries.
var (
	harnessLookPath = exec.LookPath
	probeHarness    = func(h HarnessSpec) (int, bool) { return h.InstalledVariant() }
)

// hostHarnessCachePath is harnesses.json beside the pool registry.
func hostHarnessCachePath() (string, error) {
	p, err := registryPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "harnesses.json"), nil
}

// harnessProbeOverrideEnv names the env var that overrides a harness's variant
// probe, by convention MESHCTL_<ID>_MAJOR (tests, CI, a box with two release
// lines installed). Its value is part of the fingerprint, so setting or
// changing it re-probes instead of serving a stale cached variant.
func harnessProbeOverrideEnv(id string) string {
	up := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, id)
	return "MESHCTL_" + up + "_MAJOR"
}

// fingerprintHarness resolves h's binary on PATH (following symlinks, so a
// re-pointed symlink is a change) and stats it.
func fingerprintHarness(h HarnessSpec) harnessFingerprint {
	fp := harnessFingerprint{Override: os.Getenv(harnessProbeOverrideEnv(h.ID))}
	if h.Binary == "" {
		return fp
	}
	p, err := harnessLookPath(h.Binary)
	if err != nil {
		return fp
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	fp.Path = p
	if fi, err := os.Stat(p); err == nil {
		fp.MtimeNs = fi.ModTime().UnixNano()
		fp.Size = fi.Size()
	}
	return fp
}

// loadHostHarnessCache reads the cache; missing or corrupt ⇒ empty.
func loadHostHarnessCache(path string) hostHarnessCache {
	c := hostHarnessCache{Schema: hostHarnessCacheSchema, Harnesses: map[string]hostHarnessEntry{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var disk hostHarnessCache
	if json.Unmarshal(b, &disk) != nil || disk.Schema != hostHarnessCacheSchema || disk.Harnesses == nil {
		return c
	}
	return disk
}

// saveHostHarnessCache writes the cache atomically (temp file + rename).
func saveHostHarnessCache(path string, c hostHarnessCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".harnesses-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// cachedInstalledHarnesses is InstalledHarnesses through the host cache. A
// cache that cannot be located or written only costs a re-probe next time.
func cachedInstalledHarnesses() []InstalledHarness {
	now := time.Now()
	path, perr := hostHarnessCachePath()
	cache := hostHarnessCache{Schema: hostHarnessCacheSchema, Harnesses: map[string]hostHarnessEntry{}}
	if perr == nil {
		cache = loadHostHarnessCache(path)
	}
	next := hostHarnessCache{Schema: hostHarnessCacheSchema, Harnesses: map[string]hostHarnessEntry{}}
	changed := false
	var out []InstalledHarness
	for _, h := range RegisteredHarnesses() {
		if h.InstalledVariant == nil {
			continue
		}
		fp := fingerprintHarness(h)
		e, hit := cache.Harnesses[h.ID]
		if !hit || e.harnessFingerprint != fp {
			v, ok := probeHarness(h)
			e = hostHarnessEntry{harnessFingerprint: fp, Installed: ok, Variant: v, Probed: now.UTC().Format(time.RFC3339)}
			changed = true
		}
		next.Harnesses[h.ID] = e
		if e.Installed {
			out = append(out, InstalledHarness{ID: h.ID, Variant: e.Variant})
		}
	}
	// Entries for harnesses no longer registered are dropped.
	if len(next.Harnesses) != len(cache.Harnesses) {
		changed = true
	}
	if changed && perr == nil {
		_ = saveHostHarnessCache(path, next)
	}
	return out
}
