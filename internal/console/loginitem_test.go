package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoginItemPlist_ValidAndRoundTrips(t *testing.T) {
	exe := "/Users/x/.local/bin/meshctl & co"
	p := LoginItemPlist(exe, "/Users/x/agents", "/usr/bin:/bin", "/Users/x/Library/Logs/meshctl-console.log")
	for _, want := range []string{"<key>AGENTS_DIR</key><string>/Users/x/agents</string>", "<string>console</string>",
		"<key>KeepAlive</key><true/>", "&amp; co"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if got := plistProgram(p); got != exe {
		t.Errorf("program round trip: %q", got)
	}
	if lint, err := exec.LookPath("plutil"); err == nil {
		f := filepath.Join(t.TempDir(), "x.plist")
		_ = os.WriteFile(f, []byte(p), 0o644)
		if out, err := exec.Command(lint, "-lint", f).CombinedOutput(); err != nil {
			t.Fatalf("plutil: %v %s", err, out)
		}
	}
}

func TestLoginItem_InstallUninstallWithFakeLaunchctl(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if _, err := InstallLoginItem("/bin/true", "/tmp"); err != ErrNotMacOS {
			t.Fatalf("non-macOS: %v", err)
		}
		return
	}
	t.Setenv("HOME", t.TempDir())
	var calls []string
	old := Launchctl
	Launchctl = func(args ...string) ([]byte, error) { calls = append(calls, strings.Join(args, " ")); return nil, nil }
	defer func() { Launchctl = old }()

	exe := filepath.Join(t.TempDir(), "meshctl")
	_ = os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	p, err := InstallLoginItem(exe, "/pool")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || plistProgram(string(b)) != exe {
		t.Fatalf("plist: %v %s", err, b)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "bootout ") || !strings.HasPrefix(calls[1], "bootstrap ") {
		t.Fatalf("launchctl calls: %v", calls)
	}
	if !RestartLoginItem() || !strings.HasPrefix(calls[2], "kickstart -k ") {
		t.Fatalf("restart: %v", calls)
	}
	// Nothing listens on this port, and the program is removed: two findings.
	_ = os.Remove(exe)
	fs := LoginItemFindings(Settings{Port: 1})
	if len(fs) != 2 || !strings.Contains(fs[0], "no longer exists") || !strings.Contains(fs[1], "nothing answers") {
		t.Fatalf("findings: %v", fs)
	}
	if removed, err := UninstallLoginItem(); !removed || err != nil {
		t.Fatalf("uninstall: %v %v", removed, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("plist must be gone")
	}
	if removed, _ := UninstallLoginItem(); removed {
		t.Fatal("second uninstall must be a no-op")
	}
	if RestartLoginItem() {
		t.Fatal("no login item ⇒ no restart")
	}
	if fs := LoginItemFindings(Settings{Port: 1}); len(fs) != 0 {
		t.Fatalf("no login item ⇒ no findings: %v", fs)
	}
}

func TestReadSettings_DoesNotCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	s := ReadSettings()
	if s.Port != DefaultPort || s.Token != "" {
		t.Fatalf("defaults: %+v", s)
	}
	p, _ := SettingsPath()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("ReadSettings must not create the settings file")
	}
}
