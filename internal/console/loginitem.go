package console

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// loginitem.go — start `meshctl console` at login:
// a macOS LaunchAgent, installed and removed on request, restarted after
// `meshctl update` so an old binary never keeps serving.

// LoginItemLabel is the LaunchAgent's label.
const LoginItemLabel = "com.meshctl.console"

// ErrNotMacOS: the login item is a macOS LaunchAgent; other systems run
// `meshctl console` themselves (or from their own init system) for now.
var ErrNotMacOS = errors.New("starting the console at login is supported on macOS only for now; run `meshctl console` yourself")

// Launchctl runs launchctl; tests replace it.
var Launchctl = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

// LoginItemPath is ~/Library/LaunchAgents/com.meshctl.console.plist.
func LoginItemPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", LoginItemLabel+".plist"), nil
}

// LoginItemLog is where the login item's output goes.
func LoginItemLog() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "meshctl-console.log"), nil
}

// LoginItemPlist renders the LaunchAgent: run exe `console`, opening on the
// pool at poolRoot ("" for the first registered pool, run from home), at
// login and again if it exits, with a PATH that finds git.
func LoginItemPlist(exe, poolRoot, path, logFile string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	wd, agentsDir := poolRoot, ""
	if poolRoot == "" {
		wd, _ = os.UserHomeDir()
	} else {
		agentsDir = "\n\t\t<key>AGENTS_DIR</key><string>" + esc(poolRoot) + "</string>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + LoginItemLabel + `</string>
	<key>ProgramArguments</key>
	<array><string>` + esc(exe) + `</string><string>console</string></array>
	<key>WorkingDirectory</key><string>` + esc(wd) + `</string>
	<key>EnvironmentVariables</key>
	<dict>` + agentsDir + `
		<key>PATH</key><string>` + esc(path) + `</string>
	</dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>30</integer>
	<key>StandardOutPath</key><string>` + esc(logFile) + `</string>
	<key>StandardErrorPath</key><string>` + esc(logFile) + `</string>
</dict>
</plist>
`
}

func guiDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// InstallLoginItem writes the LaunchAgent for exe, opening on poolRoot, and
// starts it (replacing one already loaded).
func InstallLoginItem(exe, poolRoot string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", ErrNotMacOS
	}
	p, err := LoginItemPath()
	if err != nil {
		return "", err
	}
	logFile, err := LoginItemLog()
	if err != nil {
		return "", err
	}
	path := os.Getenv("PATH")
	for _, d := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin", "/opt/homebrew/bin"} {
		if !strings.Contains(":"+path+":", ":"+d+":") {
			path += ":" + d
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	_ = os.MkdirAll(filepath.Dir(logFile), 0o755)
	_, _ = Launchctl("bootout", guiDomain()+"/"+LoginItemLabel) // not loaded is fine
	if err := os.WriteFile(p, []byte(LoginItemPlist(exe, poolRoot, strings.TrimPrefix(path, ":"), logFile)), 0o644); err != nil {
		return "", err
	}
	if out, err := Launchctl("bootstrap", guiDomain(), p); err != nil {
		return "", fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return p, nil
}

// UninstallLoginItem stops and removes the LaunchAgent; nothing to do when
// it is not installed.
func UninstallLoginItem() (bool, error) {
	p, err := LoginItemPath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		return false, nil
	}
	_, _ = Launchctl("bootout", guiDomain()+"/"+LoginItemLabel)
	return true, os.Remove(p)
}

// RestartLoginItem restarts an installed login item, so it runs the binary
// now on disk (after `meshctl update`). False when none is installed.
func RestartLoginItem() bool {
	p, err := LoginItemPath()
	if err != nil || runtime.GOOS != "darwin" {
		return false
	}
	if _, err := os.Stat(p); err != nil {
		return false
	}
	_, err = Launchctl("kickstart", "-k", guiDomain()+"/"+LoginItemLabel)
	return err == nil
}

// LoginItemFindings reports problems with an installed login item for
// doctor: its program gone, or nothing answering on the console's port.
// None when no login item is installed.
func LoginItemFindings(s Settings) []string {
	p, err := LoginItemPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []string
	if exe := plistProgram(string(b)); exe != "" {
		if _, err := os.Stat(exe); err != nil {
			out = append(out, "the console login item runs "+exe+", which no longer exists — run `meshctl console install` again")
		}
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port)), 500*time.Millisecond)
	if err != nil {
		lf, _ := LoginItemLog()
		out = append(out, fmt.Sprintf("the console login item is installed but nothing answers on port %d — see %s", s.Port, lf))
	} else {
		_ = c.Close()
	}
	return out
}

// plistProgram reads the first ProgramArguments string from a plist this
// package wrote.
func plistProgram(plist string) string {
	i := strings.Index(plist, "<key>ProgramArguments</key>")
	if i < 0 {
		return ""
	}
	rest := plist[i:]
	j := strings.Index(rest, "<string>")
	k := strings.Index(rest, "</string>")
	if j < 0 || k < j {
		return ""
	}
	var v string
	if err := xml.Unmarshal([]byte(rest[j:k+len("</string>")]), &v); err != nil {
		return ""
	}
	return v
}
