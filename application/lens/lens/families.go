package lens

import (
	"strings"

	"github.com/mslxl/natcatty/application/lens/model"
)

// A family is one row of Terminal's ordered matcher table. Terminal walks the table in
// this order and the first family that returns a lens claims the command; a
// family with no matching lens returns nil and the next family is tried.
//
// The table is Terminal's, not the registry's priority order: git worktree before
// git history, gh/jj before files, build before JS, and docs.help last so every
// family gets its own command before the generic --help lens does.
type family struct {
	name string
	ids  []string
}

// families is Terminal's 13 family matchers, in call order. The IDs in each family
// are the built-in lenses that family owns; each lens's own Match/globs decide
// the exact subcommand (git status vs git diff, cargo test vs cargo build, ...).
var families = []family{
	{"git worktree", []string{
		"git.status", "git.diff", "git.branch", "git.remote",
		"git.tag", "git.files", "git.transfer", "git.stash",
	}},
	{"git history", []string{
		"git.log", "git.show", "git.reflog", "git.shortlog", "git.blame",
	}},
	{"gh / jj", []string{
		"gh.view", "gh.list", "jj.status", "jj.diff", "jj.log",
	}},
	{"files", []string{
		"files.pwd", "files.ls", "files.find", "files.stat",
		"files.which", "files.wc", "files.du", "files.tree",
	}},
	// Terminal's Viewers family, in the catalog's order. All six viewer lenses are
	// registered in builtin/viewfile.go and builtin/viewers.go.
	{"viewers", []string{
		"view.file", "view.follow", "view.diff", "view.json", "view.sql", "view.table",
	}},
	{"search", []string{"search.grep"}},
	{"build", []string{
		"build.diag", "build.make", "build.swift", "rust.test", "rust.cargo", "rust.tree",
	}},
	{"js / ts", []string{
		"js.install", "js.pkg", "js.test", "js.lint", "js.tsc", "pkg.outdated",
	}},
	{"python / go", []string{
		"py.test", "py.pkg", "py.lint", "go.test", "go.build",
	}},
	{"system", []string{
		"sys.ps", "sys.df", "sys.env", "sys.sockets", "sys.load",
		"sys.info", "sys.history", "sys.cal", "sys.brew",
	}},
	{"network", []string{
		"net.ping", "net.http", "net.trace", "net.dns",
	}},
	{"containers / infra", []string{
		"docker.build", "docker.logs", "docker.table",
		"k8s.get", "k8s.describe", "k8s.helm", "tf.plan", "cloud.json",
	}},
	{"docs / pager", []string{
		"docs.systemd", "docs.tldr", "docs.man", "docs.help",
	}},
}

// familyIDs is every ID the 13 families own. A definition outside this set is
// a test or plugin lens; it keeps the old registry walk (priority then globs)
// after the table, so plugins remain possible without changing Terminal's table.
var familyIDs = func() map[string]bool {
	m := make(map[string]bool)
	for _, f := range families {
		for _, id := range f.ids {
			m[id] = true
		}
	}
	return m
}()

// IsFamilyLens reports whether id is owned by one of Terminal's 13 families. Every
// built-in lens is; test and plugin definitions are not.
func IsFamilyLens(id string) bool { return familyIDs[id] }

// lookupFamily returns the first lens in the ordered family table that claims
// cmd, or nil when no family does.
func lookupFamily(cmd model.Cmd) Lens {
	if l := lookupHelp(cmd); l != nil {
		return l
	}
	for _, f := range families {
		if l := matchIDs(cmd, f.ids...); l != nil {
			return l
		}
	}
	return nil
}

// lookupHelp keeps the one shape rule the old priority encoded: a command
// asking for --help is showing the program's help, not its normal output, so
// docs.help wins even though its family is last in Terminal's table. The family
// matchers themselves do not claim a help request.
func lookupHelp(cmd model.Cmd) Lens {
	for _, a := range cmd.Args {
		if strings.Contains(a, "--help") {
			return matchID(cmd, "docs.help")
		}
	}
	return nil
}

// matchIDs asks each lens of one family in order. A definition with globs is
// checked through those globs, exactly as the old registry did; a definition
// without globs lets the lens's own Match decide.
func matchIDs(cmd model.Cmd, ids ...string) Lens {
	for _, id := range ids {
		if l := matchID(cmd, id); l != nil {
			return l
		}
	}
	return nil
}

func matchID(cmd model.Cmd, id string) Lens {
	for _, d := range registry {
		if d.ID != id {
			continue
		}
		if len(d.Match) == 0 {
			l := d.New()
			if l.Match(cmd) {
				return l
			}
			return nil
		}
		line := cmd.Program
		if len(cmd.Args) > 0 {
			line += " " + strings.Join(cmd.Args, " ")
		}
		for _, g := range d.Match {
			if glob(g, line) || glob(g, cmd.Program) {
				return d.New()
			}
		}
		return nil
	}
	return nil
}

// lookupExtra is the fallback for definitions that are not one of Terminal's 13
// families (tests and third-party plugin lenses). Built-ins never reach it:
// every built-in ID is owned by a family.
func lookupExtra(cmd model.Cmd) Lens {
	line := cmd.Program
	if len(cmd.Args) > 0 {
		line += " " + strings.Join(cmd.Args, " ")
	}
	for _, d := range registry {
		if familyIDs[d.ID] {
			continue
		}
		if len(d.Match) == 0 {
			l := d.New()
			if l.Match(cmd) {
				return l
			}
			continue
		}
		for _, g := range d.Match {
			if glob(g, line) || glob(g, cmd.Program) {
				return d.New()
			}
		}
	}
	return nil
}
