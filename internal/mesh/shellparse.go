package mesh

import (
	"encoding/json"
	"os"
	"path"
	"regexp"
	"strings"
)

// shellparse.go — the gate's one view of a shell command line (spec: shared
// understanding, Phase 1 decision 4; ticket 14 prototype findings).
//
// Every gate rule that reads a Bash command goes through these helpers so the
// same three false positives never come back:
//
//   - `cat x 2>/dev/null`, `cmd 2>&1`, `cmd >/dev/null 2>&1` are not writes:
//     a /dev/null redirection or a file-descriptor duplication is not an
//     argument the command acts on (ShellOwnArguments drops them).
//   - a heredoc body is prose, not arguments: a handoff or a response whose
//     text names a private path, a branch or a `>` is not a write to it
//     (ShellOwnArguments drops the body and its `<<DELIM` operator).
//   - a mention is not an invocation: `cat tools/wrapper.sh`, `grep "meshctl kb
//     finish" …` or an echoed string never match a subcommand; only the words
//     at a command start do (ShellInvokes).
//
// Later gates (promotion, pool-path, branch-name, triage apply) call
// ShellInvokes and ShellSimpleCommands rather than re-implementing a matcher.
// Parsing is best-effort, as everywhere in the gate: the contract is "the
// patterns it recognises, reliably", and what it cannot read falls through to
// the caller's rule.

// ShellOwnArguments returns the command line reduced to what the command acts
// on: heredoc bodies (with their `<<DELIM` operators) are dropped, and so are
// redirections to or from /dev/null (`>/dev/null`, `2> /dev/null`, `&>/dev/null`,
// `</dev/null`) and file-descriptor duplications or closes (`2>&1`, `>&2`,
// `3>&-`). Quoting is preserved, so regexes written against a command line
// still apply, and anything inside quotes is left alone (`echo "a 2>/dev/null"`
// keeps its text). A removed operator takes the whitespace before it; trailing
// whitespace is trimmed. Every other redirection (`> out.txt`, `>> log`) stays:
// it is a write, and the caller's rule decides what to make of it.
func ShellOwnArguments(command string) string {
	s := command
	n := len(s)
	out := make([]byte, 0, n)
	trimRight := func() {
		for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\t') {
			out = out[:len(out)-1]
		}
	}
	type heredoc struct {
		delim     string
		stripTabs bool
	}
	var pending []heredoc
	inSingle, inDouble := false, false
	for i := 0; i < n; {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			out = append(out, c)
			i++
		case inDouble:
			if c == '\\' && i+1 < n {
				out = append(out, c, s[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inDouble = false
			}
			out = append(out, c)
			i++
		case c == '\\' && i+1 < n:
			out = append(out, c, s[i+1])
			i += 2
		case c == '\'':
			inSingle = true
			out = append(out, c)
			i++
		case c == '"':
			inDouble = true
			out = append(out, c)
			i++
		case c == '\n':
			out = append(out, c)
			i++
			for _, h := range pending {
				i = skipHeredocBody(s, i, h.delim, h.stripTabs)
			}
			pending = nil
		case strings.HasPrefix(s[i:], "<<") && !strings.HasPrefix(s[i:], "<<<") && (i == 0 || s[i-1] != '<'):
			j := i + 2
			strip := false
			if j < n && s[j] == '-' {
				strip = true
				j++
			}
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			delim, end := heredocDelimiter(s, j)
			if delim == "" {
				out = append(out, c)
				i++
				continue
			}
			trimRight()
			pending = append(pending, heredoc{delim, strip})
			i = end
		case c == '>' || c == '<' || c == '&' || (c >= '0' && c <= '9' && atWordStart(s, i)):
			if end, ok := nullOrDupRedirect(s, i); ok {
				trimRight()
				i = end
				continue
			}
			out = append(out, c)
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return strings.TrimRight(string(out), " \t\n")
}

// heredocDelimiter reads a heredoc delimiter word at s[i:] (bare, quoted or
// backslash-escaped) and returns it with the index past it; "" if none.
func heredocDelimiter(s string, i int) (string, int) {
	n := len(s)
	if i >= n {
		return "", i
	}
	switch s[i] {
	case '\'', '"':
		q := s[i]
		j := strings.IndexByte(s[i+1:], q)
		if j < 0 {
			return "", i
		}
		return s[i+1 : i+1+j], i + 2 + j
	case '\\':
		i++
	}
	j := i
	for j < n && !strings.ContainsRune(" \t\n;|&<>()", rune(s[j])) {
		j++
	}
	return s[i:j], j
}

// skipHeredocBody returns the index just past the terminator line of a heredoc
// body starting at s[i] (the end of the string when there is no terminator).
func skipHeredocBody(s string, i int, delim string, stripTabs bool) int {
	for i < len(s) {
		end := strings.IndexByte(s[i:], '\n')
		line := ""
		next := len(s)
		if end >= 0 {
			line = s[i : i+end]
			next = i + end + 1
		} else {
			line = s[i:]
		}
		if stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		i = next
		if line == delim {
			return i
		}
	}
	return i
}

// atWordStart reports whether s[i] begins a word (start of string, or after
// whitespace or a control operator), so that `2>` is an fd prefix but `file2>`
// is not.
func atWordStart(s string, i int) bool {
	return i == 0 || strings.ContainsRune(" \t\n;|&()", rune(s[i-1]))
}

// nullOrDupRedirect parses a redirection at s[i:] (optional fd number, operator,
// optional spaces, target) and reports its end when it is a /dev/null
// redirection or a descriptor duplication or close; ok=false for anything else,
// including a heredoc, a here-string, a control operator and a file target.
func nullOrDupRedirect(s string, i int) (int, bool) {
	n := len(s)
	j := i
	for j < n && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j >= n {
		return i, false
	}
	op := ""
	for _, cand := range []string{"&>>", "&>", ">>", ">&", ">|", "<&", ">", "<"} {
		if strings.HasPrefix(s[j:], cand) {
			op = cand
			break
		}
	}
	if op == "" || strings.HasPrefix(s[j:], "<<") {
		return i, false
	}
	j += len(op)
	for j < n && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	k := j
	for k < n && !strings.ContainsRune(" \t\n;|&()<>\"'", rune(s[k])) {
		k++
	}
	target := s[j:k]
	switch {
	case target == "/dev/null":
		return k, true
	case op == ">&" || op == "<&":
		if target == "-" || (target != "" && strings.Trim(target, "0123456789") == "") {
			return k, true
		}
	}
	return i, false
}

// shellInterpreters are the words whose `-c <string>` argument is itself a
// command line, parsed in turn.
var shellInterpreters = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}

// shellPrefixWords are leading words that do not name the command: shell
// keywords, grouping and the usual wrappers. They are dropped along with any
// `-flags` that follow them and any `VAR=value` assignments.
var shellPrefixWords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "while": true, "until": true,
	"do": true, "done": true, "!": true, "{": true, "}": true, "time": true, "exec": true,
	"command": true, "builtin": true, "env": true, "nohup": true, "sudo": true, "xargs": true, "nice": true,
}

var reShellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// ShellSimpleCommands splits a command line into its simple commands — one
// word list per command, split at `;`, `&&`, `||`, `|`, `&`, newlines,
// parentheses and backticks — with quotes removed from each word. It works on
// ShellOwnArguments, so heredoc bodies and /dev/null noise are gone. Leading
// `VAR=value` assignments, shell keywords and wrappers (`env`, `sudo`, `exec`,
// `time`, `xargs`…) are dropped so the first word names the command. A
// `bash -c "…"`/`sh -c '…'` (or `eval "…"`) string is parsed too and its
// commands appended after the interpreter's own. Redirection operators stay as
// words (`>`, `>>file`) for callers that look at them. Empty commands are
// omitted.
func ShellSimpleCommands(command string) [][]string {
	var cmds [][]string
	for _, words := range shellSplit(ShellOwnArguments(command)) {
		words = stripShellPrefix(words)
		if len(words) == 0 {
			continue
		}
		cmds = append(cmds, words)
		if inner := interpreterScript(words); inner != "" {
			cmds = append(cmds, ShellSimpleCommands(inner)...)
		}
	}
	return cmds
}

// shellSplit tokenises a command line into simple commands, quote-aware.
func shellSplit(s string) [][]string {
	var (
		cmds    [][]string
		words   []string
		word    []byte
		started bool
	)
	endWord := func() {
		if started {
			words = append(words, string(word))
		}
		word, started = word[:0], false
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
		}
		words = nil
	}
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		switch c {
		case '\'':
			started = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				word = append(word, s[i+1:]...)
				i = n
				continue
			}
			word = append(word, s[i+1:i+1+j]...)
			i += 1 + j
		case '"':
			started = true
			i++
			for i < n && s[i] != '"' {
				if s[i] == '\\' && i+1 < n && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
					i++
				}
				word = append(word, s[i])
				i++
			}
		case '\\':
			if i+1 < n {
				i++
				if s[i] != '\n' {
					started = true
					word = append(word, s[i])
				}
			}
		case ' ', '\t':
			endWord()
		case ';', '|', '&', '\n', '(', ')', '`':
			endCmd()
		default:
			started = true
			word = append(word, c)
		}
	}
	endCmd()
	return cmds
}

// stripShellPrefix drops assignments, keywords and wrapper words (with their
// flags) from the front of a simple command.
func stripShellPrefix(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		switch {
		case reShellAssignment.MatchString(w):
			words = words[1:]
		case shellPrefixWords[w]:
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
		default:
			return words
		}
	}
	return words
}

// interpreterScript returns the command string a `bash -c`, `sh -lc`, … or
// `eval` simple command runs; "" when the command is not one.
func interpreterScript(words []string) string {
	if len(words) == 0 {
		return ""
	}
	name := path.Base(words[0])
	if name == "eval" {
		return strings.Join(words[1:], " ")
	}
	if !shellInterpreters[name] {
		return ""
	}
	for i := 1; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") {
			return ""
		}
		if !strings.HasPrefix(w, "--") && strings.Contains(w, "c") && i+1 < len(words) {
			return words[i+1]
		}
	}
	return ""
}

// ShellInvokes reports whether a command line INVOKES the given subcommand —
// its space-separated words (e.g. "meshctl kb finish") appearing in order at a
// command start: the start of the line, after `;`, `&&`, `||`, `|`, `&`, `(`,
// a newline or a backtick, or inside a `bash -c "…"`/`sh -c '…'` string. The
// first word matches by basename, so `./bin/meshctl` counts. Flags between the
// subcommand words are skipped together with a separate value
// (`meshctl --agent a kb --session s finish`), the way cobra reads them; a
// flag word is read both as a boolean and as taking the next word, so the
// match is a superset of what cobra could run: a gate over-asks, it never
// misses. The words appearing as a file argument, a quoted string, an echoed
// mention or inside a heredoc body are never an invocation. This is the one
// matcher the gates share: a promotion, pool-path, branch-name or triage rule
// asks it instead of writing its own regex.
func ShellInvokes(command, subcommand string) bool {
	want := strings.Fields(subcommand)
	if len(want) == 0 {
		return false
	}
	for _, words := range ShellSimpleCommands(command) {
		if shellWordsMatch(words, want) {
			return true
		}
	}
	return false
}

// shellWordsMatch reports whether one simple command's words are an
// invocation of want (see ShellInvokes). reach[i] marks where the next wanted
// word may stand; a flag word passes reach on by one (a boolean or
// `--flag=value`) and, without `=`, also by two (a separate value). Linear in
// the words per wanted word, however many flags there are.
func shellWordsMatch(words, want []string) bool {
	if len(want) == 0 || len(words) == 0 || strings.HasSuffix(words[0], "/") || path.Base(words[0]) != want[0] {
		return false
	}
	reach := make([]bool, len(words)+2)
	reach[1] = true
	for _, w := range want[1:] {
		next := make([]bool, len(words)+2)
		found := false
		for i := 1; i < len(words); i++ {
			if !reach[i] {
				continue
			}
			if strings.HasPrefix(words[i], "-") {
				reach[i+1] = true
				if !strings.Contains(words[i], "=") {
					reach[i+2] = true
				}
				continue
			}
			if words[i] == w {
				next[i+1] = true
				found = true
			}
		}
		if !found {
			return false
		}
		reach = next
	}
	return true
}

// meshctlBoolFlags are meshctl's boolean flags (cobra BoolVar, plus help):
// the only flag words that do not take the next word as their value. Kept in
// step with cmd/meshctl by a test there (IsMeshctlBoolFlag).
var meshctlBoolFlags = map[string]bool{
	"abort": true, "all": true, "answered": true, "bare": true, "check": true,
	"claim": true, "closed": true, "current": true, "detach": true,
	"dispatchable": true, "doctrine-only": true, "dry-run": true, "fix": true,
	"force": true, "headless": true, "help": true, "h": true, "in-session": true,
	"iterm2": true, "json": true, "md": true, "no-steward": true, "open": true, "print-url": true, "purge": true,
	"work": true, "yes": true,
}

// IsMeshctlBoolFlag reports whether a meshctl flag name is boolean, i.e. never
// takes the next word as its value when a gate reads a meshctl command line.
func IsMeshctlBoolFlag(name string) bool { return meshctlBoolFlags[name] }

// meshctlCall is one parsed meshctl invocation: its flags (every value, in
// order; a bare boolean reads "true") and its positional arguments after the
// subcommand words.
type meshctlCall struct {
	flags map[string][]string
	args  []string
}

// flag is the last value given for a flag, unquoted ("" when absent).
func (c meshctlCall) flag(name string) string {
	v := c.flags[name]
	if len(v) == 0 {
		return ""
	}
	return unquote(v[len(v)-1])
}

// boolFlag reports whether a boolean flag is set true (bare, =true or =1).
func (c meshctlCall) boolFlag(name string) bool {
	v := c.flag(name)
	return v == "true" || v == "1"
}

// parseMeshctlCall finds the first simple command of a shell line that
// invokes subcommand (ShellInvokes' matcher) and reads its flags wherever they
// stand — before, between or after the subcommand words — the way cobra
// does (readMeshctlCall). ok=false when the line does not invoke subcommand.
func parseMeshctlCall(command, subcommand string) (c meshctlCall, ok bool) {
	want := strings.Fields(subcommand)
	for _, words := range ShellSimpleCommands(command) {
		if !shellWordsMatch(words, want) {
			continue
		}
		c = readMeshctlCall(words)
		if skip := len(want) - 1; len(c.args) >= skip {
			c.args = c.args[skip:]
		} else {
			c.args = nil
		}
		return c, true
	}
	return c, false
}

// readMeshctlCall reads one simple command's words after the program name:
// a flag without `=` takes the next word as its value unless it is one of
// meshctl's booleans; every other word is positional (subcommand words
// first, then arguments), unquoted.
func readMeshctlCall(words []string) meshctlCall {
	c := meshctlCall{flags: map[string][]string{}}
	for i := 1; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") && w != "-" {
			name, val, hasVal := strings.Cut(strings.TrimLeft(w, "-"), "=")
			if !hasVal {
				switch {
				case meshctlBoolFlags[name]:
					val = "true"
				case i+1 < len(words):
					i++
					val = words[i]
				}
			}
			c.flags[name] = append(c.flags[name], val)
			continue
		}
		c.args = append(c.args, unquote(w))
	}
	return c
}

// shellCommandInput is the command of a Bash-like tool call ("" when the
// payload is not one or carries none).
func shellCommandInput(p HookPayload) string {
	if !shellTools[p.ToolName] || len(p.ToolInput) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(p.ToolInput, &in) != nil {
		return ""
	}
	return in.Command
}

// shellAnyCommand reports whether re matches any simple command of the line,
// each rendered as its words joined by single spaces, so a `^`-anchored regex
// matches at a command start only (never a mention in an argument).
func shellAnyCommand(command string, re *regexp.Regexp) bool {
	for _, words := range ShellSimpleCommands(command) {
		if re.MatchString(strings.Join(words, " ")) {
			return true
		}
	}
	return false
}

// shellRedirects reports whether the line carries a redirection outside quotes.
// Run it on ShellOwnArguments: /dev/null targets and descriptor duplications
// are already gone, so what remains is a file write.
func shellRedirects(args string) bool {
	inSingle, inDouble := false, false
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case inSingle:
			inSingle = c != '\''
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case c == '\\':
			i++
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '>':
			return true
		}
	}
	return false
}

// shellWalk calls fn for each simple command of a line (ShellSimpleCommands)
// with the directory it runs in: cwd ("" = the process's) as moved by each
// `cd` before it. A `cd` itself is not passed to fn.
func shellWalk(command, cwd string, fn func(words []string, base string)) {
	base := cwd
	if base == "" {
		base, _ = os.Getwd()
	}
	for _, words := range ShellSimpleCommands(command) {
		if path.Base(words[0]) == "cd" {
			if len(words) > 1 {
				base = resolveShellPath(words[1], base)
			} else if home, err := os.UserHomeDir(); err == nil {
				base = home
			}
			continue
		}
		fn(words, base)
	}
}

var shellWriteCommands = map[string]bool{
	"tee": true, "mv": true, "cp": true, "rm": true, "touch": true, "truncate": true, "dd": true,
	"install": true, "ln": true, "rsync": true, "mkdir": true, "chmod": true, "chown": true, "patch": true,
}

// shellWriteTargets returns the absolute paths a shell command line writes —
// the one reading every write rule shares (pool knowledge, KB main checkout):
// redirection targets; the destination (last operand) of cp, ln, install and
// rsync, never their sources; `dd of=`; every operand of the other mutating
// utilities (mv's and rm's sources too, which they remove) and of `sed -i`.
// Relative paths resolve against cwd as moved by each `cd` (shellWalk).
func shellWriteTargets(command, cwd string) []string {
	var out []string
	shellWalk(command, cwd, func(words []string, base string) {
		name := path.Base(words[0])
		var operands []string
		for i := 1; i < len(words); i++ {
			w := words[i]
			switch {
			case strings.HasPrefix(w, ">"):
				target := strings.TrimLeft(w, ">")
				if target == "" && i+1 < len(words) {
					i++
					target = words[i]
				}
				if target != "" {
					out = append(out, resolveShellPath(target, base))
				}
			case w != "" && !strings.HasPrefix(w, "-"):
				operands = append(operands, w)
			}
		}
		switch {
		case name == "cp" || name == "ln" || name == "install" || name == "rsync":
			if len(operands) > 0 {
				out = append(out, resolveShellPath(operands[len(operands)-1], base))
			}
		case name == "dd":
			for _, o := range operands {
				if strings.HasPrefix(o, "of=") {
					out = append(out, resolveShellPath(strings.TrimPrefix(o, "of="), base))
				}
			}
		case shellWriteCommands[name] || (name == "sed" && sedInPlace(words)):
			for _, o := range operands {
				out = append(out, resolveShellPath(o, base))
			}
		}
	})
	return out
}

// reScriptWrite finds a file write in interpreter source: Python open() in a
// write or append mode, pathlib's write_text/write_bytes, Node's fs writes,
// Ruby's File.write, Perl's open with '>' and the usual rename/remove calls.
var reScriptWrite = regexp.MustCompile(`open\([^)]*['"][wax]\+?b?['"]|\.write_(text|bytes)\(|` +
	`(writeFile|appendFile|writeFileSync|appendFileSync)\(|File\.(write|open\([^)]*['"][wa])|` +
	`open\s*\(?\s*(my\s+)?\$?\w*\s*,\s*['"]\+?>|shutil\.|os\.(replace|rename|remove|unlink)\(|\.unlink\(|\.rename\(`)

// reScriptPath finds quoted string literals that look like paths.
var reScriptPath = regexp.MustCompile(`['"]([^'"\s]*/[^'"\s]*)['"]`)

// interpreterWriteTargets returns the paths a script run by a code interpreter
// (`python3 - <<'EOF' … EOF`, `node -e '…'`) may write: when the line runs an
// interpreter and its text holds a file-write call, every quoted path literal,
// resolved against the directory the interpreter runs in. The script is code
// the gate cannot follow, so this over-reads (a path that is only read counts
// when a write appears elsewhere in the script); a gate over-asks, it never
// misses (issue #13). Heredoc bodies are read on purpose: here they are the
// program, not prose.
func interpreterWriteTargets(command, cwd string) []string {
	base, found := "", false
	shellWalk(command, cwd, func(words []string, dir string) {
		if !found && codeInterpreter(path.Base(words[0])) {
			base, found = dir, true
		}
	})
	if !found || !reScriptWrite.MatchString(command) {
		return nil
	}
	var out []string
	for _, m := range reScriptPath.FindAllStringSubmatch(command, -1) {
		out = append(out, resolveShellPath(m[1], base))
	}
	return out
}

// codeInterpreter reports whether a program name runs source code it is given
// (python3.12 and the like included).
func codeInterpreter(name string) bool {
	switch name {
	case "node", "ruby", "perl", "php", "deno", "bun", "osascript":
		return true
	}
	return name == "python" || strings.HasPrefix(name, "python3") || strings.HasPrefix(name, "python2")
}

// sedInPlace reports whether a sed command carries -i (edit in place).
func sedInPlace(words []string) bool {
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "-i") || w == "--in-place" || strings.HasPrefix(w, "--in-place=") {
			return true
		}
		if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "i") {
			return true
		}
	}
	return false
}
