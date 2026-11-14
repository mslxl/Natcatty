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
		case c == '|' || c == ';' || c == '&' || c == '<' || c == '>':
			return false
		}
	}
	return true
}

// Normalize turns a reported command line into the program and its
// arguments, expanding an alias and looking through wrappers and VAR=
// prefixes, as Terminal's LensRun does.
func Normalize(line, typed, cwd string) model.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		line = strings.TrimSpace(typed)
	}
	if e, ok := aliases[strings.ToLower(strings.TrimSpace(line))]; ok {
		line = e
	} else if f := strings.Fields(line); len(f) > 0 {
		if e, ok := aliases[strings.ToLower(f[0])]; ok {
			line = e + " " + strings.Join(f[1:], " ")
		}
	}
	fields := strings.Fields(line)
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
