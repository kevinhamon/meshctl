package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// harness_registry.go — the harnesses meshctl can wire and run, and what each
// one can do. Core code asks a harness what it CAN DO (HarnessCaps), never
// which harness it IS: adding a harness is one RegisterHarness call plus its
// adapter and bridge, with no edits to the gate, doctor, dispatch or upgrade
// paths. See the "Adding a harness" doc.

// HarnessCaps is what one harness variant can do. Every field is a behaviour
// the mesh degrades explicitly when absent — the parity table is built from
// these, and the conformance suite checks each adapter against its own caps.
type HarnessCaps struct {
	// PreToolGate: a pre-tool hook the mesh gate runs in; a deny blocks the call.
	PreToolGate bool
	// GateAsk: the gate can put an arbitrary ask to the human (outbound writes,
	// agent writes to pool/). Without it an ask-class decision is a deny.
	GateAsk bool
	// PromotionAsk: the promotion commands (kb finish, kb confirm, state set,
	// inbox triage apply) are asked by the harness before they run.
	PromotionAsk bool
	// PostToolHook: an after-call hook (pool commits, outbound acks).
	PostToolHook bool
	// SessionEndEvent: the harness signals the end of a session. Without it
	// meshctl closes a quiet session after an idle window.
	SessionEndEvent bool
	// InSessionPeers: a subagent tool that runs peer definitions under the
	// peer gate (`ask --in-session [--work]`).
	InSessionPeers bool
	// InSessionDistill: a background in-session subagent runs the distiller,
	// so DistillCmd is nil. Without it distillation is a separate process.
	InSessionDistill bool
	// StatusLine: a status surface meshctl feeds (`hook status-line`).
	StatusLine bool
}

// ToolKind is the neutral class of a harness tool call. The gate matches
// kinds, never raw tool names, so a new harness cannot slip a write past the
// gate under an unfamiliar name or argument key.
type ToolKind string

const (
	ToolWrite    ToolKind = "write"    // creates or overwrites a file (path in input)
	ToolEdit     ToolKind = "edit"     // edits a file in place (path in input)
	ToolPatch    ToolKind = "patch"    // patch text naming the files it touches
	ToolShell    ToolKind = "shell"    // a shell command line
	ToolRead     ToolKind = "read"     // reads files
	ToolFetch    ToolKind = "fetch"    // network fetch
	ToolSubagent ToolKind = "subagent" // starts a subagent
)

// HarnessVariant is one release line of a harness whose wiring or caps differ
// from its others (opencode V1 vs V2). Num is what the adapter is built with.
type HarnessVariant struct {
	Num   int
	Label string // column label, e.g. "opencode V2 (2.x)"
}

// HarnessSpec is one registry entry.
type HarnessSpec struct {
	// ID is the stable id stored in agent.yaml and pool config.
	ID string
	// Label is the human name ("Claude Code").
	Label string
	// Binary is the executable that must be on PATH for the harness to run.
	Binary string
	// RunningEnv are environment markers the harness exports to the tool
	// subprocesses it starts; any one set ⇒ "this process runs under it".
	RunningEnv []string
	// WorkspaceMarkers are workspace-relative files or dirs whose presence
	// shows the workspace is wired for this harness.
	WorkspaceMarkers []string
	// Variants are the release lines with distinct wiring or caps, newest
	// first; nil ⇒ the harness has one line (variant 0).
	Variants []HarnessVariant
	// InstalledVariant probes the installed binary: (variant, true) when it is
	// installed; variant 0 for a harness without variants.
	InstalledVariant func() (int, bool)
	// New builds the adapter for a variant; 0 ⇒ detect from the installed binary.
	New func(variant int) Harness
	// Tools maps the harness's tool names to neutral kinds; a name missing
	// here is not a file write, edit, patch or shell call to the gate.
	Tools map[string]ToolKind
	// PathKeys are the tool-input keys that carry a write/edit target path,
	// in the order the gate tries them.
	PathKeys []string
}

// DefaultHarness is the harness an agent with no recorded preference runs
// under, and the one a bare workspace is assumed to be wired for.
const DefaultHarness = HarnessClaude

var (
	harnessRegMu sync.RWMutex
	harnessReg   []HarnessSpec
)

// RegisterHarness adds a harness to the registry. Registration order is
// display order (parity columns, doctor rows) and the tie-break order for
// RunningHarness. A duplicate id panics: it is a programming error.
func RegisterHarness(s HarnessSpec) {
	id := normHarnessID(s.ID)
	if id == "" || s.New == nil {
		panic("mesh: RegisterHarness needs an ID and a New func")
	}
	s.ID = id
	harnessRegMu.Lock()
	defer harnessRegMu.Unlock()
	for _, h := range harnessReg {
		if h.ID == id {
			panic("mesh: harness " + id + " registered twice")
		}
	}
	harnessReg = append(harnessReg, s)
}

// unregisterHarness removes a harness; tests use it to drop a fake.
func unregisterHarness(id string) {
	harnessRegMu.Lock()
	defer harnessRegMu.Unlock()
	id = normHarnessID(id)
	out := harnessReg[:0]
	for _, h := range harnessReg {
		if h.ID != id {
			out = append(out, h)
		}
	}
	harnessReg = out
}

// RegisteredHarnesses returns every registered harness, in registration order.
func RegisteredHarnesses() []HarnessSpec {
	harnessRegMu.RLock()
	defer harnessRegMu.RUnlock()
	return append([]HarnessSpec(nil), harnessReg...)
}

// RegisteredHarnessIDs is RegisteredHarnesses' ids.
func RegisteredHarnessIDs() []string {
	var ids []string
	for _, h := range RegisteredHarnesses() {
		ids = append(ids, h.ID)
	}
	return ids
}

// LookupHarness finds a registered harness by id (case- and space-insensitive).
func LookupHarness(id string) (HarnessSpec, bool) {
	id = normHarnessID(id)
	for _, h := range RegisteredHarnesses() {
		if h.ID == id {
			return h, true
		}
	}
	return HarnessSpec{}, false
}

// ResolveHarness returns the adapter for id at variant (0 ⇒ detect). Empty id
// ⇒ DefaultHarness. An unknown id is an error — use this at every input
// boundary (flags, manifests, pool config) so a typo never silently becomes
// the default harness.
func ResolveHarness(id string, variant int) (Harness, error) {
	if normHarnessID(id) == "" {
		id = DefaultHarness
	}
	s, ok := LookupHarness(id)
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (registered: %s)", strings.TrimSpace(id), strings.Join(RegisteredHarnessIDs(), ", "))
	}
	return s.New(variant), nil
}

func normHarnessID(id string) string { return strings.ToLower(strings.TrimSpace(id)) }

// RunningHarness names the harness whose tool call runs this process, from the
// marker each exports to tool subprocesses. "" for a human's own terminal.
// A nested harness inherits its parent's marker too, so a bridge always
// passes --harness explicitly; this is the fallback for plain CLI calls an
// agent makes through its shell tool.
func RunningHarness() string { return runningHarnessIn(os.Getenv) }

func runningHarnessIn(getenv func(string) string) string {
	for _, h := range RegisteredHarnesses() {
		for _, k := range h.RunningEnv {
			if getenv(k) != "" {
				return h.ID
			}
		}
	}
	return ""
}

// WiredHarnesses lists the registered harnesses whose workspace markers exist
// in dest, in registration order.
func WiredHarnesses(dest string) []string {
	var out []string
	for _, h := range RegisteredHarnesses() {
		for _, m := range h.WorkspaceMarkers {
			if pathExists(filepath.Join(dest, m)) {
				out = append(out, h.ID)
				break
			}
		}
	}
	return out
}

// DetectHarness infers the harness a pre-existing workspace was set up for:
// the first non-default harness whose markers exist, else DefaultHarness.
// Non-default first, because the default's markers (.claude/) are common in
// workspaces that were later moved to another harness.
func DetectHarness(dest string) string {
	for _, id := range WiredHarnesses(dest) {
		if id != DefaultHarness {
			return id
		}
	}
	return DefaultHarness
}

// InstalledHarness is a registered harness found on this host.
type InstalledHarness struct {
	ID      string
	Variant int
}

// InstalledHarnesses lists the registered harnesses installed on this host,
// in registration order, through the host cache (host_harnesses.go): only a
// harness whose binary changed since the last call is probed again. A miss
// spawns a process (version probe): call it at wire, upgrade and doctor time,
// never from a hook's hot path.
func InstalledHarnesses() []InstalledHarness {
	return cachedInstalledHarnesses()
}

// HarnessColumn is one parity-table column: a harness, or one variant of it.
type HarnessColumn struct {
	ID      string
	Variant int
	Label   string
}

// HarnessColumns lists one column per registered harness variant (or one per
// harness without variants), in registration order, newest variant first.
func HarnessColumns() []HarnessColumn {
	var cols []HarnessColumn
	for _, h := range RegisteredHarnesses() {
		if len(h.Variants) == 0 {
			cols = append(cols, HarnessColumn{ID: h.ID, Label: h.Label})
			continue
		}
		for _, v := range h.Variants {
			cols = append(cols, HarnessColumn{ID: h.ID, Variant: v.Num, Label: v.Label})
		}
	}
	return cols
}

// Key is the column's stable key: id, or id@variant.
func (c HarnessColumn) Key() string {
	if c.Variant == 0 {
		return c.ID
	}
	return fmt.Sprintf("%s@%d", c.ID, c.Variant)
}

// ToolKindOf maps a raw tool name to its neutral kind across every registered
// harness ("" ⇒ not a kind the gate inspects). The union is safe because a
// name means the same thing on every harness that uses it; the conformance
// suite fails a registration that maps one name to two kinds.
func ToolKindOf(name string) ToolKind {
	for _, h := range RegisteredHarnesses() {
		if k, ok := h.Tools[name]; ok {
			return k
		}
	}
	return ""
}

// ToolPathKeys is the union of every registered harness's PathKeys, in
// registration order, deduplicated.
func ToolPathKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range RegisteredHarnesses() {
		for _, k := range h.PathKeys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

func init() {
	RegisterHarness(HarnessSpec{
		ID:               HarnessClaude,
		Label:            "Claude Code",
		Binary:           "claude",
		RunningEnv:       []string{"CLAUDECODE"},
		WorkspaceMarkers: []string{".claude/settings.json"},
		InstalledVariant: func() (int, bool) { return 0, ToolOnPath("claude") },
		New:              func(int) Harness { return claudeHarness{} },
		Tools: map[string]ToolKind{
			"Write": ToolWrite, "Edit": ToolEdit, "MultiEdit": ToolEdit, "NotebookEdit": ToolEdit,
			"Bash": ToolShell, "Read": ToolRead, "WebFetch": ToolFetch, "Agent": ToolSubagent, "Task": ToolSubagent,
		},
		PathKeys: []string{"file_path", "notebook_path"},
	})
	RegisterHarness(HarnessSpec{
		ID:               HarnessOpencode,
		Label:            "opencode",
		Binary:           "opencode",
		RunningEnv:       []string{"OPENCODE"},
		WorkspaceMarkers: []string{"opencode.json", ".opencode"},
		Variants: []HarnessVariant{
			{Num: 2, Label: "opencode V2 (2.x)"},
			{Num: 1, Label: "opencode V1 (1.x)"},
		},
		InstalledVariant: func() (int, bool) {
			m := InstalledOpencodeMajor()
			return m, m > 0
		},
		New: func(v int) Harness { return opencodeHarness{major: v} },
		Tools: map[string]ToolKind{
			"write": ToolWrite, "edit": ToolEdit, "multiedit": ToolEdit,
			"apply_patch": ToolPatch, "patch": ToolPatch,
			"bash": ToolShell, "shell": ToolShell, "read": ToolRead, "webfetch": ToolFetch,
			"task": ToolSubagent, "subagent": ToolSubagent,
		},
		PathKeys: []string{"filePath", "path"},
	})
}
