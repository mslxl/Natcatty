package lens

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mslxl/natcatty/application/lens/model"
)

// Ctx is what a lens gets besides its command; kept small on purpose.
type Ctx struct {
	Logf func(format string, args ...any)
}

// Event is an event from a node of a lens view, as Terminal's lens events:
// {ev = "select"|"activate", id = <node id>, item = <row id>},
// {ev = "action", id = <node id>, act = <name>, item = <row id>}, and value
// events for inputs.
type Event struct {
	Action string // action, select, activate, change, toggle, focus, edit, undo
	ID     string // the node's id (a table id)
	Item   string // the row/item id
	Act    string // the action name for {ev = "action"}
	Value  string
	Mods   []string
}

// Lens is one command lens: match, then open → line* → finish → view.
// A lens cannot decline a command its definition matched.
type Lens interface {
	ID() string
	Match(model.Cmd) bool
	Open(cmd model.Cmd, cx *Ctx) (state any, err error)
	Line(state any, l model.Line) error
	Finish(state any, status int) error
	View(state any) (Node, error) // nil leaves the block showing raw output
	Event(state any, ev Event) error
}

// ByteSink is an optional interface a lens can implement to capture the raw
// byte stream of its command instead of line-shaped text. It is the right
// shape for commands whose output is binary — `cat image.png` is the canonical
// case: the bytes reaching the terminal already are the picture, so the lens
// should not have to re-read the file from disk, and textifying them only
// floods the screen with garbage rows.
//
// When a lens implements ByteSink and CaptureBytes reports true after Open,
// the engine and tap skip the rune/screen pipeline entirely for the rest of
// the command: Line is never called again, every output byte reaches Bytes in
// order, and Finish/View see the accumulation in whatever state the lens kept.
type ByteSink interface {
	Lens
	// CaptureBytes reports whether this command's byte stream should be
	// captured raw. It is consulted once, right after Open, so the lens can
	// decide from the command (path extension, subcommand, flags).
	CaptureBytes(state any) bool
	// Bytes receives the raw output bytes in order. The chunks are not
	// aligned to anything — they are the pieces the PTY delivered.
	Bytes(state any, b []byte) error
}

// Definition registers a lens: its id, the globs over the simple command
// line that claim a command, how to make it, and how early it is asked.
type Definition struct {
	ID    string
	Match []string // "git status*", "gst*", "ls*"; empty means the lens matches itself
	// Priority orders definitions outside Terminal's 13 families (tests and
	// plugins). Built-in lenses are claimed by families.go's ordered table, so
	// a built-in priority no longer decides the claim order.
	Priority int
	New      func() Lens
}

var registry []Definition

// Register adds a lens definition. Call it from an init in the lens package
// that provides it (built-in or a plugin's Go package). Definitions are kept in
// priority order (stable: same priority keeps registration order).
func Register(d Definition) {
	registry = append(registry, d)
	sort.SliceStable(registry, func(i, j int) bool {
		return registry[i].Priority > registry[j].Priority
	})
}

// Registered returns the definitions in priority order (stable): higher
// priorities first, then registration order.
func Registered() []Definition { return registry }

// Lookup returns the first lens in Terminal's ordered family table that claims the
// command. Test and plugin definitions outside the 13 families keep the old
// priority/glob walk as a fallback.
func Lookup(cmd model.Cmd) Lens {
	cmd = expandAlias(cmd)
	if l := lookupFamily(cmd); l != nil {
		return l
	}
	return lookupExtra(cmd)
}

// expandAlias applies the static alias table before matching. Normalize already
// does this for a raw shell line, but Lookup is also called with a hand-built
// Cmd (tests, integrations); `gst`/`ll` must reach the same family there.
func expandAlias(cmd model.Cmd) model.Cmd {
	expanded, ok := aliases[cmd.Program]
	if !ok {
		return cmd
	}
	fields := strings.Fields(expanded)
	if len(fields) == 0 {
		return cmd
	}
	cmd.Program = fields[0]
	cmd.Args = append(fields[1:], cmd.Args...)
	return cmd
}

// glob matches a shell-style pattern, treating a trailing space as a prefix
// match so that "git status" claims "git status --short".
func glob(pattern, s string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, " ") {
		return strings.HasPrefix(s, strings.TrimRight(pattern, " "))
	}
	if ok, err := path.Match(pattern, s); err == nil && ok {
		return true
	}
	if ok, err := path.Match(pattern+"*", s); err == nil && ok {
		return true
	}
	return false
}

// aliases maps common shell aliases to their commands, for the shells whose
// integration reports the typed line alone.
var aliases = map[string]string{
	"gst":   "git status",
	"gs":    "git status",
	"ga":    "git add",
	"gc":    "git commit",
	"gco":   "git checkout",
	"gd":    "git diff",
	"gl":    "git log",
	"gp":    "git push",
	"gpl":   "git pull",
	"ll":    "ls -l",
	"la":    "ls -la",
	"lsa":   "ls -la",
	"k":     "kubectl",
	"kgp":   "kubectl get pods",
	"dc":    "docker compose",
	"tf":    "terraform",
	"py":    "python3",
	"serve": "python3 -m http.server",
}

// wrappers are the programs a lens looks through to the real command.
var wrappers = map[string]bool{
	"command": true, "builtin": true, "sudo": true, "doas": true, "exec": true,
	"time": true, "nice": true, "nohup": true, "env": true, "caffeinate": true,
	"npx": true, "bunx": true, "pnpx": true, "uvx": true, "pipx": true,
}

// IsSimple reports whether a reported line is a simple command. Terminal only lets
// a lens take over a simple command: a pipeline, a sequence, a background job
// or a redirection means the output belongs to more than this one program, so
// the block stays raw.
func IsSimple(line string) bool {
	// A sequence is the first thing to settle, because the output that follows
	// the start mark is the last command's: `cd Pictures && cat a.png` runs two
	// commands and the picture comes from the second. Refusing the whole line
	// meant the most common way to run a command that needs another directory
	// first was never claimed, and the picture's bytes printed as text.
	last := lastCommandSegment(line)
	// A pipeline or a redirection changes the output the block will show, so a
	// lens must not claim it: `cat a.png | head` prints head's output, and
	// `cat a.png > copy` prints nothing.
	return !hasPipeOrRedirect(last)
}

// hasPipeOrRedirect reports whether a single command redirects its output or
// pipes it somewhere else. `||` is a sequence separator, not a pipe.
func hasPipeOrRedirect(line string) bool {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '|':
			if i+1 < len(line) && line[i+1] == '|' {
				i++ // `||` runs the next command instead; not a pipe
				continue
			}
			return true
		case c == '<' || c == '>':
			return true
		}
	}
	return false
}

// Normalize turns a reported command line into the program and its
// arguments, expanding an alias and looking through wrappers and VAR=
// prefixes, as Terminal's LensRun does.
func Normalize(line, typed, cwd string) model.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		line = strings.TrimSpace(typed)
	}
	// A line can hold several commands: `cd Pictures && cat a.png` runs two,
	// and only the last one prints the output the block will show. Taking the
	// line whole made its program `cd`, so the file lens never matched and the
	// picture's bytes were treated as text (they showed as "Binary file").
	// The last segment is the one whose output follows the start mark.
	line = lastCommandSegment(line)
	if e, ok := aliases[strings.ToLower(strings.TrimSpace(line))]; ok {
		line = e
	} else if f := splitCommandLine(line); len(f) > 0 {
		if e, ok := aliases[strings.ToLower(f[0])]; ok {
			line = e + " " + strings.Join(f[1:], " ")
		}
	}
	fields := splitCommandLine(line)
	for len(fields) > 0 {
		if isAssignment(fields[0]) || fields[0] == "" {
			fields = fields[1:]
			continue
		}
		if wrappers[filepath.Base(strings.ToLower(fields[0]))] {
			fields = fields[1:]
			for len(fields) > 0 {
				f := fields[0]
				if isAssignment(f) {
					fields = fields[1:]
					continue
				}
				if !strings.HasPrefix(f, "-") {
					break
				}
				fields = fields[1:]
				if takesArgument(f) && len(fields) > 0 {
					fields = fields[1:] // sudo -u root git status
				}
			}
			continue
		}
		break
	}
	cmd := model.Cmd{Line: line, Typed: typed, Cwd: cwd}
	if len(fields) == 0 {
		return cmd
	}
	cmd.Program = strings.TrimSuffix(filepath.Base(fields[0]), ".exe")
	cmd.Args = append([]string(nil), fields[1:]...)
	return cmd
}

// lastCommandSegment returns the last command in a line that runs several:
// `a && b`, `a; b`, `a || b`, `a & b`. The output that follows a start mark is
// the last command's, so that is the one a lens has to identify.
//
// A pipeline is left whole: `cat a.png | head` prints head's output, but the
// file lens still needs the cat that produced the bytes, and the existing
// handling of pipes keeps the first program. Redirections and subshells are
// left as text, which is all a lens reads.
func lastCommandSegment(line string) string {
	depth := 0
	var quote byte
	last := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(line) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(line):
			i++
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0 && c == ';':
			last = i + 1
		case depth == 0 && c == '&':
			// `&&` and `&` both start a new command; `||` is handled on the
			// second bar of a pair, and a lone `&` backgrounds what came before.
			if i+1 < len(line) && line[i+1] == '&' {
				i++
			}
			last = i + 1
		case depth == 0 && c == '|':
			// `|` pipes into the next command; `||` starts a new one.
			if i+1 < len(line) && line[i+1] == '|' {
				i++
				last = i + 1
			}
		}
	}
	seg := strings.TrimSpace(line[last:])
	if seg == "" {
		return strings.TrimSpace(line)
	}
	return seg
}

// reads it: quotes group a word that contains spaces, and a backslash escapes
// the next character. strings.Fields is not enough, because `cat "my logo.png"`
// then arrives as `"my` and `logo.png"`, and the file lens could not recognize
// a picture whose name has a space in it.
//
// It is not a shell parser: expansions, substitutions and redirections are
// left as text, which is all a lens needs to find the program and its file.
// splitCommandLine splits a shell command line into its words the way the shell
// reads it: quotes group a word that contains spaces, and a backslash escapes
// the next character.
func splitCommandLine(line string) []string {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case ' ', '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case '\'', '"':
			inWord = true
			quote := c
			i++
			for ; i < len(line); i++ {
				ch := line[i]
				if ch == '\\' && quote == '"' && i+1 < len(line) {
					// Inside double quotes only these escapes are special; any
					// other backslash is the shell's literal backslash.
					switch line[i+1] {
					case '"', '\\', '$', '`':
						cur.WriteByte(line[i+1])
						i++
						continue
					}
					cur.WriteByte(ch)
					continue
				}
				if ch == quote {
					break
				}
				cur.WriteByte(ch)
			}
		case '\\':
			inWord = true
			if i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// takesArgument reports whether a wrapper's flag takes the next token with it,
// so that `sudo -u root git status` still finds `git`.
func takesArgument(flag string) bool {
	switch flag {
	case "-u", "-g", "-p", "-C", "-n", "-o", "-t", "-c", "-r", "-S", "-U", "-D",
		"--user", "--group", "--prompt", "--chdir", "--nice", "--output", "--directory":
		return true
	}
	return false
}

func isAssignment(s string) bool {
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return false
	}
	for _, r := range s[:i] {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
