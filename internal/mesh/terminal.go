package mesh

import (
	"os/exec"
	"strconv"
	"strings"
)

// terminal.go — which process and terminal run a session. Hooks run with no
// controlling terminal, so the walk goes up the process tree to the first
// ancestor that has one: the harness itself (mesh-console tickets 11–12).

// psParentTTY returns a process's parent pid and terminal name ("" when it
// has none); a var so tests can fake the process tree.
var psParentTTY = func(pid int) (ppid int, tty string, ok bool) {
	out, err := exec.Command("ps", "-o", "ppid=,tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", false
	}
	f := strings.Fields(string(out))
	if len(f) < 1 {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(f[0])
	if err != nil {
		return 0, "", false
	}
	if len(f) > 1 && f[1] != "??" && f[1] != "?" && f[1] != "-" {
		tty = f[1]
	}
	return ppid, tty, true
}

// harnessTerminal walks up from pid to the first ancestor with a terminal and
// returns it and the terminal's device path; 0, "" when none is found (a
// headless run, or a system without ps).
func harnessTerminal(pid int) (int, string) {
	for hops := 0; pid > 1 && hops < 16; hops++ {
		ppid, tty, ok := psParentTTY(pid)
		if !ok {
			return 0, ""
		}
		if tty != "" {
			if !strings.HasPrefix(tty, "/dev/") {
				tty = "/dev/" + tty
			}
			return pid, tty
		}
		pid = ppid
	}
	return 0, ""
}
