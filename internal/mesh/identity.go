package mesh

import "fmt"

// TerminalIdentity returns an ANSI string that gives an agent's terminal a portable,
// cross-platform visual identity: it sets the window/tab title to the agent name and
// prints a colored badge banner built from the manifest (badge label + title, in the
// agent's badge color). It uses only OSC title-setting and SGR truecolor, so it works
// in any terminal on any OS (Windows Terminal, gnome-terminal, kitty, wezterm,
// Terminal.app, iTerm2) — no terminal-specific profile required.
//
// Intended use: run `meshctl agent identity` on shell entry in an agent workspace
// (e.g. from the shell rc or a direnv `.envrc`), or just invoke it once in the session.
func TerminalIdentity(a *Agent) string {
	name := a.Name
	label := a.Badge.Label
	if label == "" {
		label = name
	}
	r, g, b := to255(a.Badge.R), to255(a.Badge.G), to255(a.Badge.B)

	// OSC 0 sets both the icon name and the window title; BEL-terminated for the
	// widest terminal support.
	title := fmt.Sprintf("\x1b]0;%s\x07", name)

	// Badge: the label on a block of the agent's color, with a foreground picked for
	// contrast against that color (perceived luminance threshold).
	fg := 30 // black text on light badges
	if luminance(r, g, b) < 140 {
		fg = 97 // bright-white text on dark badges
	}
	badge := fmt.Sprintf("\x1b[48;2;%d;%d;%dm\x1b[%dm %s \x1b[0m", r, g, b, fg, label)

	line := badge + "  " + name
	if a.Title != "" {
		line += " — " + a.Title
	}
	return title + line + "\n"
}

// to255 maps a 0..1 color component (the manifest's scale) to 0..255, clamped.
func to255(v float64) int {
	n := int(v*255 + 0.5)
	if n < 0 {
		return 0
	}
	if n > 255 {
		return 255
	}
	return n
}

// luminance is the Rec. 601 perceived brightness of an RGB triple (0..255).
func luminance(r, g, b int) int {
	return (r*299 + g*587 + b*114) / 1000
}
