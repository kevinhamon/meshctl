package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reserved owner: `pool` names the pool itself, so
// `mesh://pool/...` can never mean a peer.

func TestReservedOwner_CreateRefusesPeerNamedPool(t *testing.T) {
	dir := migratedRoot(t)
	spec := AgentSpec{Name: "pool", Title: "Pool", Role: "r", Bare: true}
	if _, err := New(dir, spec, ScaffoldOptions{}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("agent new must refuse the reserved name: err=%v", err)
	}
	// Case does not matter: a case-folding filesystem would collide with pool/.
	spec.Name = "Pool"
	if _, err := New(dir, spec, ScaffoldOptions{}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("agent new must refuse the reserved name regardless of case: err=%v", err)
	}
	if err := ValidAgentName("pool"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("ValidAgentName must say the name is reserved: %v", err)
	}
	if err := ValidAgentName("pool-agent"); err != nil {
		t.Fatalf("only the exact reserved name is refused: %v", err)
	}
}

func TestReservedOwner_OnboardRefusesPeerNamedPool(t *testing.T) {
	dir := meshRoot(t)
	ws := filepath.Join(dir, "pool")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Onboard(dir, ws, AgentSpec{Title: "Pool", Role: "r"}, ScaffoldOptions{})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("onboard must refuse the reserved name: err=%v", err)
	}
}

func TestReservedOwner_ManifestValidationRefusesPeerNamedPool(t *testing.T) {
	dir := meshRoot(t) // legacy: a pool/ folder here is just a workspace
	ws := filepath.Join(dir, "pool")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: pool\ntitle: Pool\naccepts:\n  - {type: x}\nbadge: {label: PL}\n"
	if err := os.WriteFile(filepath.Join(ws, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := FindAgent(dir, "pool")
	if err != nil {
		t.Fatal(err)
	}
	var reserved int
	for _, p := range a.Validate() {
		if strings.Contains(p, "reserved") {
			reserved++
		}
	}
	if reserved != 1 {
		t.Fatalf("validation must say the name is reserved: %v", a.Validate())
	}
	// Doctor reports it once, not once per check.
	findings, ok := Doctor(dir)
	if ok {
		t.Fatalf("doctor must error: %+v", findings)
	}
	n := 0
	for _, f := range findings {
		if f.Level == "error" && strings.Contains(f.Message, "reserved") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one reserved-name error, got %d: %+v", n, findings)
	}
}

func TestReservedOwner_DoctorErrorsOnExistingPeerNamedPool(t *testing.T) {
	dir := migratedRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	// A hand-made peer manifest inside the pool directory itself.
	manifest := "name: pool\ntitle: Pool\naccepts:\n  - {type: x}\nbadge: {label: PL}\n"
	if err := os.WriteFile(filepath.Join(dir, CurrentPoolDir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, ok := Doctor(dir)
	if ok {
		t.Fatalf("doctor must error on a peer named pool: %+v", findings)
	}
	var found bool
	for _, f := range findings {
		if f.Agent == ReservedOwner && f.Level == "error" && strings.Contains(f.Message, "reserved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error finding naming the reserved owner: %+v", findings)
	}
	// Without the stray manifest the migrated pool is clean of this finding.
	os.Remove(filepath.Join(dir, CurrentPoolDir, ManifestName))
	findings, _ = Doctor(dir)
	if hasFinding(findings, "reserved") {
		t.Fatalf("no reserved-name finding without a peer named pool: %+v", findings)
	}
}

// Resolver: `mesh://pool/<path>#<slug>` reads
// `pool/<path>` and matches a `**Term**:` line or a heading by kebab slug,
// case-insensitively.

const testPoolGlossary = `# Pool glossary

## Knowledge

**Knowledge base**: A peer's durable, versioned notes under knowledge/.
_Avoid_: "memory" — that is the episodic layer.

**Fact owner**: The peer whose knowledge base holds the confirmed entry.

## Process

**Confirmation round**: The agent reads the change out item by item before the finish.
`

func writePoolGlossary(t *testing.T, root, body string) string {
	t.Helper()
	p := PoolGlossaryPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveMeshRef_PoolGlossaryTermBySlug(t *testing.T) {
	root := migratedRoot(t)
	writePoolGlossary(t, root, testPoolGlossary)

	text, ok, err := ResolveMeshRef(root, "mesh://pool/glossary.md#knowledge-base")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(text, "**Knowledge base**:") || !strings.Contains(text, "episodic layer") {
		t.Fatalf("term text must be the whole entry:\n%s", text)
	}
	if strings.Contains(text, "Fact owner") {
		t.Fatalf("term text must stop at the next term:\n%s", text)
	}
	// Case-insensitive slug.
	upper, ok, err := ResolveMeshRef(root, "mesh://pool/glossary.md#Knowledge-Base")
	if err != nil || !ok || upper != text {
		t.Fatalf("case must not matter: ok=%v err=%v\n%s", ok, err, upper)
	}
	// A heading slug resolves to the section (through the next heading of the
	// same or a higher level).
	section, ok, err := ResolveMeshRef(root, "mesh://pool/glossary.md#process")
	if err != nil || !ok || !strings.Contains(section, "Confirmation round") || strings.Contains(section, "Fact owner") {
		t.Fatalf("heading section: ok=%v err=%v\n%s", ok, err, section)
	}
	// Unknown slug: unresolved, not an error.
	if _, ok, err := ResolveMeshRef(root, "mesh://pool/glossary.md#no-such-term"); ok || err != nil {
		t.Fatalf("unknown slug must be unresolved without error: ok=%v err=%v", ok, err)
	}
	// No slug: the whole file.
	whole, ok, err := ResolveMeshRef(root, "mesh://pool/glossary.md")
	if err != nil || !ok || !strings.HasPrefix(whole, "# Pool glossary") {
		t.Fatalf("bare path must resolve to the file: ok=%v err=%v", ok, err)
	}
}

func TestResolveMeshRef_LocalDoctrineHeading(t *testing.T) {
	root := migratedRoot(t)
	if err := os.WriteFile(DoctrineLocalPath(root), []byte("# House rules\n\n## Review Gate\n\nEvery PR has one reviewer.\n\n## Naming\n\nKebab everywhere.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, ok, err := ResolveMeshRef(root, "mesh://pool/doctrine/local.md#review-gate")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(text, "## Review Gate") || !strings.Contains(text, "one reviewer") || strings.Contains(text, "Kebab") {
		t.Fatalf("heading section text:\n%s", text)
	}
	// Missing file: unresolved, not an error.
	if _, ok, err := ResolveMeshRef(root, "mesh://pool/doctrine/nope.md#x"); ok || err != nil {
		t.Fatalf("missing file must be unresolved without error: ok=%v err=%v", ok, err)
	}
}

func TestResolveMeshRef_LegacyPoolAndMalformedRefs(t *testing.T) {
	root := meshRoot(t) // legacy hidden pool dir, no repo
	writePoolGlossary(t, root, testPoolGlossary)
	text, ok, err := ResolveMeshRef(root, "mesh://pool/glossary.md#fact-owner")
	if err != nil || !ok || !strings.HasPrefix(text, "**Fact owner**:") {
		t.Fatalf("legacy pool must resolve too: ok=%v err=%v\n%s", ok, err, text)
	}
	for _, bad := range []string{
		"pool/glossary.md#x",            // no scheme
		"mesh://pool",                   // no path
		"mesh://pool/../agent.yaml",     // escapes the owner
		"mesh://pool//etc/passwd",       // absolute path
		"mesh://architect/glossary.md",  // not the reserved owner
		"mesh://bad name/glossary.md#x", // invalid owner
	} {
		if _, ok, err := ResolveMeshRef(root, bad); err == nil || ok {
			t.Fatalf("%q must be an error: ok=%v err=%v", bad, ok, err)
		}
	}
	if KebabSlug("Knowledge Base") != "knowledge-base" || KebabSlug("  Fact/Owner! ") != "fact-owner" || KebabSlug("2026 Plan") != "2026-plan" {
		t.Fatalf("slug: %q %q %q", KebabSlug("Knowledge Base"), KebabSlug("  Fact/Owner! "), KebabSlug("2026 Plan"))
	}
}
