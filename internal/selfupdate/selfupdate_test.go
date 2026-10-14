package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewerAndIsRelease(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.1.0", "v1.0.0", true},
		{"v1.0.10", "v1.0.9", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.1.0", false},
		{"v1.1.0", "v0.0.0-20261001161931-36c1ee36efad+dirty", false}, // dev build: never "older"
		{"v1.1.0", "devel+36c1ee36efad", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
	if !IsRelease("v1.0.0") || IsRelease("v0.0.0-2026-abc") || IsRelease("devel") {
		t.Fatal("IsRelease misclassifies")
	}
}

// fakeGitHub serves a latest release whose platform asset is body and whose
// checksum file holds sum.
func fakeGitHub(t *testing.T, body, sum string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			json.NewEncoder(w).Encode(Release{Tag: "v9.9.9", Assets: []Asset{
				{Name: AssetName(), URL: srv.URL + "/dl/bin"},
				{Name: AssetName() + ".sha256", URL: srv.URL + "/dl/sum"},
			}})
		case r.URL.Path == "/dl/bin":
			w.Write([]byte(body))
		case r.URL.Path == "/dl/sum":
			w.Write([]byte(sum + "  " + AssetName() + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = old })
}

func sumOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestInstall_VerifiesAndReplaces(t *testing.T) {
	fakeGitHub(t, "NEW-BINARY", sumOf("NEW-BINARY"))
	exe := filepath.Join(t.TempDir(), "meshctl")
	os.WriteFile(exe, []byte("OLD"), 0o755)

	rel, err := Latest(context.Background(), "")
	if err != nil || rel.Tag != "v9.9.9" {
		t.Fatalf("Latest: %v %+v", err, rel)
	}
	if err := Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	fi, _ := os.Stat(exe)
	if string(b) != "NEW-BINARY" || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binary not replaced/executable: %q %v", b, fi.Mode())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".meshctl-update-*")); len(m) != 0 {
		t.Fatalf("temp files left behind: %v", m)
	}
}

func TestInstall_ChecksumMismatchLeavesBinaryAlone(t *testing.T) {
	fakeGitHub(t, "TAMPERED", sumOf("NEW-BINARY"))
	exe := filepath.Join(t.TempDir(), "meshctl")
	os.WriteFile(exe, []byte("OLD"), 0o755)
	rel, _ := Latest(context.Background(), "")
	err := Install(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatal("a failed update must leave the old binary in place")
	}
}

func TestInstall_RefusesUnverifiedAsset(t *testing.T) {
	rel := &Release{Tag: "v9.9.9", Assets: []Asset{{Name: AssetName(), URL: "http://x/bin"}}}
	if err := Install(context.Background(), rel, filepath.Join(t.TempDir(), "m")); err == nil ||
		!strings.Contains(err.Error(), "sha256") {
		t.Fatalf("want refusal without checksum, got %v", err)
	}
}

func TestNotice_SilentForDevBuildsAndWhenDisabled(t *testing.T) {
	if n := Notice("v0.0.0-2026-abc+dirty", timeNow()); n != "" {
		t.Fatalf("dev build must get no notice: %q", n)
	}
	t.Setenv("MESHCTL_NO_UPDATE_CHECK", "1")
	if n := Notice("v1.0.0", timeNow()); n != "" {
		t.Fatalf("disabled check must be silent: %q", n)
	}
}

func timeNow() time.Time { return time.Now() }
