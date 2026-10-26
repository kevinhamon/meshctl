package mesh

import (
	"strings"
	"testing"
	"time"
)

func TestUpgradeNotice(t *testing.T) {
	dir := meshRoot(t)
	newTestAgent(t, dir, "architect", true, nil)
	if n := UpgradeNotice(dir, "v1.8.0"); n != "" {
		t.Fatalf("no stamp yet ⇒ no notice, got %q", n)
	}
	if err := WriteUpgradeStamp(dir, "v1.7.2", time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if s, ok := ReadUpgradeStamp(dir); !ok || s.Version != "v1.7.2" {
		t.Fatalf("stamp: %+v %v", s, ok)
	}
	n := UpgradeNotice(dir, "v1.8.0")
	if !strings.Contains(n, "v1.8.0 is installed") || !strings.Contains(n, "upgraded with v1.7.2") || !strings.Contains(n, "meshctl pool upgrade") {
		t.Fatalf("notice: %q", n)
	}
	for _, running := range []string{"v1.7.2", "v1.7.1", "v1.8.0-0.20261026-abc+dirty", "dev"} {
		if n := UpgradeNotice(dir, running); n != "" {
			t.Errorf("running %s ⇒ no notice, got %q", running, n)
		}
	}
}
