package mesh

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// harness_conformance_test.go — the contract every registered harness meets,
// a fake harness that proves a new one needs only its own registration and
// adapter, and the proof that the gate classifies tool calls by neutral kind.

// TestHarnessConformance runs the contract against every registered harness.
func TestHarnessConformance(t *testing.T) {
	// Variants are pinned below; this keeps any lazy probe off the installed binary.
	t.Setenv(opencodeMajorEnv, "2")
	for _, spec := range RegisteredHarnesses() {
		t.Run(spec.ID, func(t *testing.T) { runHarnessContract(t, spec) })
	}
	checkRegistryConsistent(t)
}

// variantNums lists the variants a spec renders for (0 for a harness without).
func variantNums(spec HarnessSpec) []int {
	if len(spec.Variants) == 0 {
		return []int{0}
	}
	var out []int
	for _, v := range spec.Variants {
		out = append(out, v.Num)
	}
	return out
}

// runHarnessContract checks one registered harness, every variant: its
// registration, wiring, skill rendering, hook output and caps.
func runHarnessContract(t *testing.T, spec HarnessSpec) {
	t.Helper()
	if spec.Label == "" || spec.Binary == "" || len(spec.WorkspaceMarkers) == 0 || len(spec.RunningEnv) == 0 {
		t.Errorf("%s: spec needs a Label, Binary, WorkspaceMarkers and RunningEnv: %+v", spec.ID, spec)
	}
	writes := false
	for _, k := range spec.Tools {
		writes = writes || k == ToolWrite || k == ToolEdit
	}
	if writes && len(spec.PathKeys) == 0 {
		t.Errorf("%s: maps a write/edit tool but declares no PathKeys, so the gate cannot read its target", spec.ID)
	}
	for _, v := range variantNums(spec) {
		t.Run(strconv.Itoa(v), func(t *testing.T) {
			h, err := ResolveHarness(spec.ID, v)
			if err != nil {
				t.Fatal(err)
			}
			if h.Name() != spec.ID {
				t.Fatalf("ResolveHarness(%q).Name() = %q", spec.ID, h.Name())
			}
			if v != 0 && h.Variant() != v {
				t.Fatalf("variant %d adapter reports Variant() = %d", v, h.Variant())
			}

			// Wiring: creates a marker (so WiredHarnesses detects it), only its
			// own, and a second run adds nothing.
			dest := filepath.Join(t.TempDir(), "agent")
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := h.WireAgent(dest); err != nil {
				t.Fatalf("WireAgent: %v", err)
			}
			if got := WiredHarnesses(dest); !reflect.DeepEqual(got, []string{spec.ID}) {
				t.Fatalf("after wiring %s, WiredHarnesses = %v (markers %v)", spec.ID, got, spec.WorkspaceMarkers)
			}
			if notes, err := h.WireAgent(dest); err != nil || len(notes) != 0 {
				t.Fatalf("WireAgent is not idempotent: notes=%v err=%v", notes, err)
			}

			// Skills: written under SkillDir; re-rendering the same body is a
			// no-op, and files beside it are left alone.
			const skill, body = "conformance-skill", "---\nname: conformance-skill\n---\nconformance body\n"
			dir := h.SkillDir(dest, skill)
			if !strings.HasPrefix(dir, dest+string(filepath.Separator)) {
				t.Fatalf("SkillDir %q is outside the workspace %q", dir, dest)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			sibling := filepath.Join(dir, "notes.md")
			if err := os.WriteFile(sibling, []byte("mine"), 0o644); err != nil {
				t.Fatal(err)
			}
			if wrote, err := h.RenderSkill(skill, body, dest); err != nil || !wrote {
				t.Fatalf("RenderSkill = %v, %v; want written", wrote, err)
			}
			if !dirHolds(t, dir, "conformance body") {
				t.Fatalf("RenderSkill wrote nothing under SkillDir %s", dir)
			}
			if wrote, err := h.RenderSkill(skill, body, dest); err != nil || wrote {
				t.Fatalf("re-rendering the same skill = %v, %v; want a no-op", wrote, err)
			}
			if b, _ := os.ReadFile(sibling); string(b) != "mine" {
				t.Fatal("RenderSkill clobbered a file beside the skill")
			}

			// Context: empty ⇒ nothing; otherwise output carrying the text.
			if out := emit(t, func(w io.Writer) error { return h.EmitContext(w, "SessionStart", "  \n") }); out != "" {
				t.Fatalf("EmitContext with no context wrote %q", out)
			}
			if out := emit(t, func(w io.Writer) error { return h.EmitContext(w, "SessionStart", "conformance context") }); !strings.Contains(out, "conformance context") {
				t.Fatalf("EmitContext output %q lacks the context", out)
			}

			// Gate: allow ⇒ nothing; deny and ask ⇒ output (an ask a harness
			// cannot put to the human is a deny in its own form).
			if out := emit(t, func(w io.Writer) error { return h.EmitGate(w, GateDecision{Check: GateCheckNone}) }); out != "" {
				t.Fatalf("EmitGate allow wrote %q", out)
			}
			for _, d := range []GateDecision{{Deny: true, Reason: "conformance deny"}, {Ask: true, Reason: "conformance ask"}} {
				if out := emit(t, func(w io.Writer) error { return h.EmitGate(w, d) }); out == "" {
					t.Fatalf("EmitGate %+v wrote nothing", d)
				}
			}

			// Caps agree with each other and with the adapter.
			c := h.Caps()
			if c.GateAsk && !c.PreToolGate {
				t.Errorf("caps %+v: GateAsk without PreToolGate", c)
			}
			if c.PromotionAsk && !c.PreToolGate {
				t.Errorf("caps %+v: PromotionAsk without PreToolGate", c)
			}
			if c.InSessionDistill != (h.DistillCmd("") == nil) {
				t.Errorf("caps %+v: InSessionDistill must hold exactly when DistillCmd is nil (got %v)", c, h.DistillCmd(""))
			}
		})
	}
}

// checkRegistryConsistent: across every registered harness, a tool name means
// one kind (ToolKindOf takes the union) and no workspace marker is shared
// (WiredHarnesses could not tell them apart).
func checkRegistryConsistent(t *testing.T) {
	t.Helper()
	kinds, markers := map[string]string{}, map[string]string{}
	for _, s := range RegisteredHarnesses() {
		for name, k := range s.Tools {
			if prev, ok := kinds[name]; ok && prev != string(k) {
				t.Errorf("tool %q maps to %s and %s across registered harnesses", name, prev, k)
			}
			kinds[name] = string(k)
		}
		for _, m := range s.WorkspaceMarkers {
			if other, ok := markers[m]; ok {
				t.Errorf("workspace marker %q is claimed by %s and %s", m, other, s.ID)
			}
			markers[m] = s.ID
		}
	}
}

func emit(t *testing.T, f func(io.Writer) error) string {
	t.Helper()
	var b bytes.Buffer
	if err := f(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// dirHolds reports whether any file under dir contains text.
func dirHolds(t *testing.T, dir, text string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), text) {
				found = true
			}
		}
		return nil
	})
	return found
}

// --- the fake harness ---

// fakeHarness is a harness meshctl has never heard of. Registering it must be
// enough: the contract holds, and core detection, resolution and the gate
// handle it with no edit outside this adapter.
type fakeHarness struct{}

const (
	fakeHarnessID     = "fakeharness"
	fakeHarnessEnv    = "MESHCTL_TEST_FAKEHARNESS"
	fakeHarnessMarker = ".fake/mesh.json"
)

func (fakeHarness) Name() string { return fakeHarnessID }
func (fakeHarness) Variant() int { return 0 }
func (fakeHarness) Caps() HarnessCaps {
	return HarnessCaps{PreToolGate: true, PostToolHook: true}
}
func (fakeHarness) WireAgent(dest string) ([]string, error) {
	out := filepath.Join(dest, filepath.FromSlash(fakeHarnessMarker))
	if fileExists(out) {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	hooks := `{"pre-tool":"meshctl hook pre-tool-use --harness ` + fakeHarnessID + `"}` + "\n"
	return []string{"wrote " + fakeHarnessMarker}, os.WriteFile(out, []byte(hooks), 0o644)
}
func (fakeHarness) SkillDir(dest, name string) string {
	return filepath.Join(dest, ".fake", "skills", name)
}
func (h fakeHarness) RenderSkill(name, body, dest string) (bool, error) {
	return writeSkillFile(filepath.Join(h.SkillDir(dest, name), "SKILL.md"), body)
}
func (fakeHarness) EmitContext(w io.Writer, _, ctx string) error {
	if strings.TrimSpace(ctx) == "" {
		return nil
	}
	_, err := io.WriteString(w, ctx+"\n")
	return err
}
func (fakeHarness) DistillCmd(prompt string) []string {
	return []string{"fakeharness-bin", "distill", prompt}
}
func (fakeHarness) EmitGate(w io.Writer, d GateDecision) error {
	if !d.Deny && !d.Ask {
		return nil
	}
	b, err := json.Marshal(map[string]string{"block": d.Reason})
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func fakeHarnessSpec() HarnessSpec {
	return HarnessSpec{
		ID:               fakeHarnessID,
		Label:            "Fake Harness",
		Binary:           "fakeharness-bin",
		RunningEnv:       []string{fakeHarnessEnv},
		WorkspaceMarkers: []string{fakeHarnessMarker},
		InstalledVariant: func() (int, bool) { return 0, false },
		New:              func(int) Harness { return fakeHarness{} },
		Tools: map[string]ToolKind{
			"fake_write": ToolWrite, "fake_patch": ToolPatch, "fake_sh": ToolShell, "fake_read": ToolRead,
		},
		PathKeys: []string{"target"},
	}
}

// registerFakeHarness adds the fake for one test.
func registerFakeHarness(t *testing.T) {
	t.Helper()
	RegisterHarness(fakeHarnessSpec())
	t.Cleanup(func() { unregisterHarness(fakeHarnessID) })
}

func TestHarnessConformance_FakeHarness(t *testing.T) {
	registerFakeHarness(t)
	spec, ok := LookupHarness(fakeHarnessID)
	if !ok {
		t.Fatal("fake harness not registered")
	}
	runHarnessContract(t, spec)
	checkRegistryConsistent(t)

	// Detection: a workspace wired only for the fake is the fake's.
	dest := filepath.Join(t.TempDir(), "agent")
	if _, err := (fakeHarness{}).WireAgent(dest); err != nil {
		t.Fatal(err)
	}
	if got := WiredHarnesses(dest); !reflect.DeepEqual(got, []string{fakeHarnessID}) {
		t.Fatalf("WiredHarnesses = %v", got)
	}
	if got := DetectHarness(dest); got != fakeHarnessID {
		t.Fatalf("DetectHarness = %q", got)
	}
	// Wired alongside the default, the non-default one is detected.
	if _, err := (claudeHarness{}).WireAgent(dest); err != nil {
		t.Fatal(err)
	}
	if got := WiredHarnesses(dest); !reflect.DeepEqual(got, []string{HarnessClaude, fakeHarnessID}) {
		t.Fatalf("WiredHarnesses with both = %v", got)
	}
	if got := DetectHarness(dest); got != fakeHarnessID {
		t.Fatalf("DetectHarness with both = %q", got)
	}

	// Resolution at input boundaries and in the lenient lookup.
	if h, err := ResolveHarness(" FakeHarness ", 0); err != nil || h.Name() != fakeHarnessID {
		t.Fatalf("ResolveHarness = %v, %v", h, err)
	}
	if HarnessByName(fakeHarnessID).Name() != fakeHarnessID {
		t.Fatal("HarnessByName does not find the fake")
	}
	if HarnessForPayload("", HookPayload{}, fakeHarnessID).Name() != fakeHarnessID {
		t.Fatal("the bridge's --harness flag does not select the fake")
	}

	// Running-harness marker.
	if RunningHarness() != "" {
		t.Fatalf("RunningHarness = %q with no marker set", RunningHarness())
	}
	t.Setenv(fakeHarnessEnv, "1")
	if RunningHarness() != fakeHarnessID {
		t.Fatalf("RunningHarness = %q with the fake's marker set", RunningHarness())
	}

	// Columns: the fake gets a parity column of its own.
	found := false
	for _, c := range HarnessColumns() {
		found = found || (c.ID == fakeHarnessID && c.Key() == fakeHarnessID && c.Label == "Fake Harness")
	}
	if !found {
		t.Fatalf("no parity column for the fake: %+v", HarnessColumns())
	}

	// The gate: the fake's tool names and path key are gated by kind.
	if ToolKindOf("fake_write") != ToolWrite || ToolKindOf("fake_sh") != ToolShell || ToolKindOf("fake_patch") != ToolPatch {
		t.Fatal("ToolKindOf does not map the fake's tools")
	}
	memory := "/u/.claude/projects/p/memory/note.md"
	intake := "/pool/agent/" + IntakeDir + "/a.md"
	deny := []HookPayload{
		{ToolName: "fake_write", ToolInput: json.RawMessage(`{"target":` + strconv.Quote(memory) + `}`)},
		{ToolName: "fake_write", ToolInput: json.RawMessage(`{"target":` + strconv.Quote(intake) + `}`)},
		{ToolName: "fake_patch", ToolInput: json.RawMessage(`{"diff":` + strconv.Quote("*** Begin Patch\n*** Add File: "+intake+"\n*** End Patch\n") + `}`)},
		{ToolName: "fake_sh", ToolInput: json.RawMessage(`{"command":"echo x > ` + intake + `"}`)},
	}
	for _, p := range deny {
		if d := GatePreToolUse("", p); !d.Deny {
			t.Errorf("gate let the fake's %s through: %s", p.ToolName, p.ToolInput)
		}
	}
	allow := []HookPayload{
		{ToolName: "fake_write", ToolInput: json.RawMessage(`{"target":"/w/src/main.go"}`)},
		{ToolName: "fake_read", ToolInput: json.RawMessage(`{"target":` + strconv.Quote(memory) + `}`)},
	}
	for _, p := range allow {
		if d := GatePreToolUse("", p); d.Deny || d.Ask {
			t.Errorf("gate stopped the fake's %s: %+v", p.ToolName, d)
		}
	}
	// A consulted peer under the fake: an edit tool is refused by kind.
	if deniedPeerAction(HookPayload{ToolName: "fake_write"}) == "" {
		t.Error("the peer contract does not refuse the fake's write tool")
	}

	// Unregistered, it is gone everywhere.
	unregisterHarness(fakeHarnessID)
	if _, err := ResolveHarness(fakeHarnessID, 0); err == nil || ToolKindOf("fake_write") != "" || RunningHarness() != "" {
		t.Fatal("unregistering the fake left traces in the registry")
	}
}

// TestGate_ToolNameClassification pins how the gate classifies every tool
// name it inspected before the gate read kinds from the registry: a file
// edit carries a target path, a patch carries paths in its text, a shell call
// carries a command line. Anything else is none of these.
func TestGate_ToolNameClassification(t *testing.T) {
	const patch = "*** Begin Patch\n*** Update File: /w/a.go\n*** End Patch\n"
	cases := []struct {
		name  string
		class string // "edit" | "patch" | "shell" | ""
	}{
		{"Write", "edit"}, {"Edit", "edit"}, {"MultiEdit", "edit"}, {"NotebookEdit", "edit"},
		{"write", "edit"}, {"edit", "edit"}, {"multiedit", "edit"},
		{"apply_patch", "patch"}, {"patch", "patch"},
		{"Bash", "shell"}, {"bash", "shell"}, {"shell", "shell"},
		// Not inspected as writes: reads, fetches, delegation, unknown or miscased names.
		{"Read", ""}, {"read", ""}, {"Glob", ""}, {"WebFetch", ""}, {"Agent", ""}, {"Task", ""},
		{"task", ""}, {"subagent", ""}, {"WRITE", ""}, {"BASH", ""}, {"Patch", ""}, {"", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// File edit: every path key the gate reads yields the path.
			for _, key := range []string{"file_path", "notebook_path", "filePath", "path"} {
				p := HookPayload{ToolName: c.name, ToolInput: json.RawMessage(`{` + strconv.Quote(key) + `:"/w/x.md"}`)}
				got := gateEditPaths(p)
				if want := c.class == "edit"; want != reflect.DeepEqual(got, []string{"/w/x.md"}) {
					t.Fatalf("%s with %s: gateEditPaths = %v, file edit = %v", c.name, key, got, want)
				}
			}
			// Patch: the paths come from the patch text.
			p := HookPayload{ToolName: c.name, ToolInput: json.RawMessage(`{"patchText":` + strconv.Quote(patch) + `}`)}
			if got, want := gateEditPaths(p), c.class == "patch"; want != reflect.DeepEqual(got, []string{"/w/a.go"}) {
				t.Fatalf("%s: patch paths = %v, patch = %v", c.name, got, want)
			}
			// Shell: a command line writing intake is denied; only shell tools carry one.
			sh := HookPayload{ToolName: c.name, ToolInput: json.RawMessage(`{"command":"echo x > /w/` + IntakeDir + `/a.md"}`)}
			if got, want := deniedShellWrite("", sh) != "", c.class == "shell"; got != want {
				t.Fatalf("%s: shell write denied = %v, shell = %v", c.name, got, want)
			}
			// A consulted peer: any edit or patch tool by name, a mutating shell command.
			peer := HookPayload{ToolName: c.name, ToolInput: json.RawMessage(`{"command":"rm -rf /w/src"}`)}
			if got, want := deniedPeerAction(peer) != "", c.class != ""; got != want {
				t.Fatalf("%s: peer denied = %v, want %v", c.name, got, want)
			}
			if got, want := OutboundCandidate(c.name), c.class != "edit" && c.class != "patch"; got != want {
				t.Fatalf("%s: OutboundCandidate = %v, want %v", c.name, got, want)
			}
		})
	}
}

// TestGate_FilePathKeys pins gateFilePath: the key order, tolerance of a
// wrong-typed key, and JSON's case-insensitive key match.
func TestGate_FilePathKeys(t *testing.T) {
	cases := []struct{ in, want string }{
		{``, ""},
		{`not json`, ""},
		{`[]`, ""},
		{`{}`, ""},
		{`{"file_path":"/a"}`, "/a"},
		{`{"notebook_path":"/n"}`, "/n"},
		{`{"filePath":"/c"}`, "/c"},
		{`{"path":"/p"}`, "/p"},
		{`{"path":"/p","filePath":"/c","notebook_path":"/n","file_path":"/a"}`, "/a"},
		{`{"path":"/p","filePath":"/c","notebook_path":"/n"}`, "/n"},
		{`{"path":"/p","filePath":"/c"}`, "/c"},
		{`{"file_path":"","path":"/p"}`, "/p"},
		{`{"file_path":5,"path":"/p"}`, "/p"},
		{`{"file_path":null,"path":"/p"}`, "/p"},
		{`{"FILE_PATH":"/upper"}`, "/upper"},
		{`{"Path":"/title"}`, "/title"},
		{`{"filepath":"/lower"}`, "/lower"},
		{`{"target":"/t"}`, ""},
	}
	for _, c := range cases {
		if got := gateFilePath(json.RawMessage(c.in)); got != c.want {
			t.Errorf("gateFilePath(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every registered harness's subagent tool reaches the delegation check: a
// state-mutating work peer is asked, an in-session peer may not delegate.
func TestGate_DelegationByToolKind(t *testing.T) {
	for _, name := range []string{"Agent", "Task", "task", "subagent"} {
		if !isDelegationTool(name) {
			t.Fatalf("%s must be a delegation", name)
		}
		p := HookPayload{ToolName: name, InSession: true, ToolInput: json.RawMessage(`{}`)}
		if d := GatePreToolUse(t.TempDir(), p); !d.Deny || d.Check != GateCheckDelegation {
			t.Fatalf("%s from an in-session peer: %+v, want a delegation deny", name, d)
		}
	}
	for _, name := range []string{"Read", "bash", "Glob", ""} {
		if isDelegationTool(name) {
			t.Fatalf("%s is not a delegation", name)
		}
	}
}
