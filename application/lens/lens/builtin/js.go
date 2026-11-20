// js.go implements the js family of Terminal's command lenses.
//
// Mined from payload\flow.exe, lens id cluster (findings/lens-conditions):
//   - offset 149,994,400: "+  with binariesjs.installjs.pkgjs.test--cwd--modules-folder"
//     → the glued ids js.install, js.pkg, js.test and the flags --cwd,
//     --modules-folder [observed].
//   - offset 149,994,496–149,994,760: -C --dir --filter -F --prefix --workspace
//     -w --loglevel -c --config --watch [observed, tool flags].
//   - offset 149,994,090/272: "npm warn npm WARN warn: warning: WARN" and
//     "npm error npm ERR! error: ERR! ERROR" [observed].
//   - offset 149,994,784: "vulnerabilit" [observed, vuln summary].
//   - offset 149,995,080: "--watchAll--listTests--showConfig--check--list-different-ljs.lint
//     --init--print-config--rules--env-info--format-f" → glued id js.lint and
//     eslint flags [observed].
//   - offset 149,995,224: "--all--listFilesOnlyjs.tsc" → glued id js.tsc and
//     tsc flags [observed].
//   - offset 149,993,680: "All test suites", "Ran all test suites",
//     "peerDependencies", "optionalDependencies", "devDependencies";
//     149,993,943 "PASS Package"; 149,993,994 "error warning info problem file
//     potentially fixable  No problems success"; 149,993,896/149,995,783-803
//     " passed", " failed", " skipped"; 149,994,936/149,995,000 "-V
//     --list-reporters --reporter -R --test --test-reporter --run" [observed].
//
// Program names: npm, bun, node, npx appear in the tool-name table at
// 149,988,267 [observed]; pnpm and yarn do not appear there, so their use as
// triggers is [推断]. eslint, tsc, jest and vitest program names are likewise
// not literals in the lens region: their flags are observed, the program name
// is [推断] from the tool the flags belong to (--rules/--print-config for
// eslint, --listFilesOnly for tsc, --listTests/--showConfig for jest/vitest).
//
// The parsers are tolerant: unrecognized lines are ignored and a lens that saw
// nothing it recognizes returns a nil view, so the block falls back to raw.
package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// jsToolPrograms are the package managers the js lenses claim. npm and bun are
// observed in the tool table; pnpm and yarn are inferred.
var jsToolPrograms = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true}

// jsSub returns the package manager and its first non-flag argument ("" when
// there is none, as for a bare `yarn`).
func jsSub(cmd model.Cmd) (tool, sub string, ok bool) {
	if !jsToolPrograms[cmd.Program] {
		return "", "", false
	}
	for _, a := range cmd.Args {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return cmd.Program, a, true
	}
	return cmd.Program, "", true
}

// jsRunScript returns the script name of `npm run <name>` and friends.
func jsRunScript(cmd model.Cmd) string {
	if !jsToolPrograms[cmd.Program] {
		return ""
	}
	seenRun := false
	for _, a := range cmd.Args {
		if !seenRun {
			if a == "run" || a == "run-script" {
				seenRun = true
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

func init() {
	// No globs: the Match methods below match the package manager and the exact
	// subcommand, so `npm ls --outdated` can go to pkg.outdated and `npm test`
	// to js.test without a prefix glob stealing it.
	lens.Register(lens.Definition{ID: "js.install", New: func() lens.Lens { return &jsInstall{} }})
	lens.Register(lens.Definition{ID: "js.pkg", New: func() lens.Lens { return &jsPkg{} }})
	lens.Register(lens.Definition{ID: "js.test", New: func() lens.Lens { return &jsTest{} }})
	lens.Register(lens.Definition{ID: "js.lint", New: func() lens.Lens { return &jsLint{} }})
	lens.Register(lens.Definition{ID: "js.tsc", New: func() lens.Lens { return &jsTsc{} }})
}

// ---------------------------------------------------------------------------
// js.install

type jsInstall struct{}

// jsInstallState keeps npm-style install counts and the lines it could not
// summarize.
type jsInstallState struct {
	added, changed, removed, audited, vulns int
	packages                                []string
	warns, errs                             []string
	styled                                  []string // output lines with SGR escapes
	sawAny                                  bool
}

func (l *jsInstall) ID() string { return "js.install" }

func (l *jsInstall) Match(cmd model.Cmd) bool {
	_, sub, ok := jsSub(cmd)
	if !ok {
		return false
	}
	switch sub {
	case "install", "i", "ci", "add", "remove", "rm", "uninstall", "update",
		"upgrade", "prune", "publish", "pack", "link", "dedupe", "audit", "fund", "":
		return true
	}
	return false
}

func (l *jsInstall) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &jsInstallState{}, nil }
func (l *jsInstall) Finish(state any, status int) error            { return nil }
func (l *jsInstall) Event(state any, ev lens.Event) error          { return nil }

func (l *jsInstall) Line(state any, ln model.Line) error {
	st, ok := state.(*jsInstallState)
	if !ok {
		return errBadState
	}
	if ln.Styled != "" {
		st.styled = append(st.styled, ln.Styled)
	}
	st.observe(ln.Text)
	return nil
}

func (st *jsInstallState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	lower := strings.ToLower(t)
	switch {
	case strings.HasPrefix(lower, "npm warn"), strings.HasPrefix(lower, "npm warn "),
		strings.HasPrefix(t, "npm WARN"), strings.HasPrefix(lower, "warn:"),
		strings.HasPrefix(lower, "warning:"):
		st.warns = append(st.warns, t)
		st.sawAny = true
		return
	case strings.HasPrefix(lower, "npm error"), strings.HasPrefix(t, "npm ERR!"),
		strings.HasPrefix(lower, "error:"), strings.HasPrefix(t, "ERR!"),
		strings.HasPrefix(t, "ERROR"):
		st.errs = append(st.errs, t)
		st.sawAny = true
		return
	case strings.HasPrefix(t, "+ "):
		pkg := strings.TrimPrefix(t, "+ ")
		pkg = strings.TrimSuffix(pkg, " with binaries")
		if pkg != "" {
			st.packages = append(st.packages, pkg)
			st.sawAny = true
		}
		return
	}
	if added, changed, removed, audited, ok := jsInstallCounts(lower); ok {
		st.added, st.changed, st.removed, st.audited = added, changed, removed, audited
		st.sawAny = true
		return
	}
	if strings.Contains(lower, "vulnerabilit") {
		if n := firstInt(t); n >= 0 {
			st.vulns = n
		}
		st.sawAny = true
		return
	}
	if strings.Contains(lower, "up to date") {
		st.sawAny = true
	}
}

// jsInstallCounts reads "added 42 packages, and audited 43 packages in 3s",
// which carries more than one count.
func jsInstallCounts(lower string) (added, changed, removed, audited int, ok bool) {
	out := []*int{&added, &changed, &removed, &audited}
	words := []string{"added", "changed", "removed", "audited"}
	for i, w := range words {
		if at := strings.Index(lower, w+" "); at >= 0 {
			if n := firstInt(lower[at+len(w)+1:]); n >= 0 {
				*out[i] = n
				ok = true
			}
		}
	}
	return added, changed, removed, audited, ok
}

func (l *jsInstall) View(state any) (lens.Node, error) {
	st, ok := state.(*jsInstallState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	var children []lens.Node
	badges := make([]lens.Node, 0, 5)
	if st.added > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.added) + " added", Tone: toneSuccess})
	}
	if st.changed > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.changed) + " changed", Tone: toneWarning})
	}
	if st.removed > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.removed) + " removed", Tone: toneError})
	}
	if st.audited > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.audited) + " audited", Tone: toneMuted})
	}
	if st.vulns > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.vulns) + " vulnerabilities", Tone: toneError})
	} else if st.audited > 0 {
		badges = append(badges, lens.Badge{Text: "0 vulnerabilities", Tone: toneSuccess})
	}
	if len(badges) > 0 {
		children = append(children, lens.Row{Children: badges})
	}
	if len(st.packages) > 0 {
		items := make([]lens.Item, 0, len(st.packages))
		for _, p := range st.packages {
			items = append(items, lens.Item{Label: []lens.Span{{Text: p, Style: toneSuccess}}})
		}
		children = append(children, lens.List{Items: items})
	}
	// npm colourises its progress; the raw stream (with CR redraws) goes through
	// Terminal's `ansi(text)` mini terminal so only the final frame shows.
	if len(st.styled) > 0 {
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: "Output", Style: toneMuted}},
			Children: []lens.Node{lens.ANSI{Text: strings.Join(st.styled, "\n")}},
		})
	}
	if len(st.errs) > 0 {
		items := make([]lens.Item, 0, len(st.errs))
		for _, e := range st.errs {
			items = append(items, lens.Item{Label: []lens.Span{{Text: e, Style: toneError}}, Tone: toneError})
		}
		children = append(children, lens.List{Items: items})
	}
	if len(st.warns) > 0 {
		items := make([]lens.Item, 0, len(st.warns))
		for _, w := range st.warns {
			items = append(items, lens.Item{Label: []lens.Span{{Text: w, Style: toneWarning}}, Tone: toneWarning})
		}
		children = append(children, lens.List{Items: items})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------------------
// js.pkg (npm ls and friends: the installed dependency tree)

type jsPkg struct{}

type jsDep struct {
	name, version, section string
	depth                  int
	tree                   bool
	unmet                  bool
}

// jsPkgProblem is an npm logger line (npm error / warn / notice) seen in
// npm ls output, kept as a list row instead of forcing a raw fallback.
type jsPkgProblem struct {
	text, tone string
}

type jsPkgState struct {
	root     string
	deps     []jsDep
	problems []jsPkgProblem
	section  string
	sawAny   bool
}

func (l *jsPkg) ID() string { return "js.pkg" }

func (l *jsPkg) Match(cmd model.Cmd) bool {
	_, sub, ok := jsSub(cmd)
	if !ok {
		return false
	}
	// `npm ls --outdated` belongs to the pkg family (pkg.outdated).
	for _, a := range cmd.Args {
		if a == "--outdated" {
			return false
		}
	}
	switch sub {
	case "ls", "list", "ll", "la", "why", "explain":
		return true
	}
	return false
}

func (l *jsPkg) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &jsPkgState{}, nil }
func (l *jsPkg) Finish(state any, status int) error            { return nil }
func (l *jsPkg) Event(state any, ev lens.Event) error          { return nil }

func (l *jsPkg) Line(state any, ln model.Line) error {
	st, ok := state.(*jsPkgState)
	if !ok {
		return errBadState
	}
	st.observe(ln.Text)
	return nil
}

var jsDepSections = map[string]bool{
	"dependencies:": true, "devdependencies:": true,
	"peerdependencies:": true, "optionaldependencies:": true,
}

func (st *jsPkgState) observe(text string) {
	// npm ls writes the tree and its logger lines to the same stream, and the
	// last tree line can run into the first error without a newline:
	// "`-- UNMET DEPENDENCY left-pad@^1.3.0npm error code ELSPROBLEMS".
	// Split those apart so neither part is lost.
	for _, part := range jsSplitNPMLog(strings.TrimSpace(text)) {
		st.observeLine(part)
	}
}

func (st *jsPkgState) observeLine(t string) {
	t = strings.TrimSpace(t)
	if t == "" {
		return
	}
	lower := strings.ToLower(t)
	switch {
	case strings.HasPrefix(lower, "npm error"), strings.HasPrefix(t, "npm ERR!"):
		st.problems = append(st.problems, jsPkgProblem{text: t, tone: toneError})
		st.sawAny = true
		return
	case strings.HasPrefix(lower, "npm warn"), strings.HasPrefix(t, "npm WARN"):
		st.problems = append(st.problems, jsPkgProblem{text: t, tone: toneWarning})
		st.sawAny = true
		return
	case strings.HasPrefix(lower, "npm notice"):
		st.problems = append(st.problems, jsPkgProblem{text: t, tone: toneMuted})
		st.sawAny = true
		return
	}
	if jsDepSections[lower] {
		st.section = strings.TrimSuffix(t, ":")
		st.sawAny = true
		return
	}
	if i := strings.Index(t, "node_modules/"); i >= 0 {
		path := t[i+len("node_modules/"):]
		depth := strings.Count(path, "node_modules/")
		name, version := jsNameVersion(path)
		if name != "" {
			st.deps = append(st.deps, jsDep{name: name, version: version, depth: depth, section: st.section})
			st.sawAny = true
		}
		return
	}
	if dep, ok := jsPkgTreeDep(t); ok {
		dep.section = st.section
		st.deps = append(st.deps, dep)
		st.sawAny = true
		return
	}
	if kind, ok := jsPkgProblemWord(lower); ok {
		tone := toneError
		if kind == "extraneous" || kind == "deprecated" {
			tone = toneWarning
		}
		st.problems = append(st.problems, jsPkgProblem{text: t, tone: tone})
		st.sawAny = true
		return
	}
	// The project header has the form "package@version C:\...\001"
	// (the first line emitted by npm ls).
	if f := strings.Fields(t); len(f) >= 2 && strings.Contains(f[0], "@") &&
		strings.ContainsAny(f[1], `:/\`) {
		st.root = f[0] + " " + f[1]
	}
}

// jsSplitNPMLog splits a line where npm's logger output ran into other text.
func jsSplitNPMLog(s string) []string {
	markers := []string{"npm error", "npm ERR!", "npm warn", "npm WARN", "npm notice"}
	var out []string
	for i := 0; i < 8; i++ {
		at := -1
		for _, m := range markers {
			if j := strings.Index(s, m); j > 0 && (at < 0 || j < at) {
				at = j
			}
		}
		if at < 0 {
			break
		}
		if head := strings.TrimSpace(s[:at]); head != "" {
			out = append(out, head)
		}
		s = s[at:]
		next := -1
		for _, m := range markers {
			if j := strings.Index(s[1:], m); j >= 0 && (next < 0 || j+1 < next) {
				next = j + 1
			}
		}
		if next < 0 {
			break
		}
		out = append(out, strings.TrimSpace(s[:next]))
		s = s[next:]
	}
	if tail := strings.TrimSpace(s); tail != "" {
		out = append(out, tail)
	}
	return out
}

// jsPkgTreeDep reads a decorated dependency line: the unicode tree (├──, └──)
// or npm's ascii fallback on Windows (`-- , |-- ), with "UNMET DEPENDENCY"
// marking a package that is required but not installed.
func jsPkgTreeDep(t string) (jsDep, bool) {
	i := strings.IndexAny(t, "├└`+")
	if i < 0 {
		return jsDep{}, false
	}
	// Only a tree marker: the character after it must lead into a dash run, so
	// a backtick in prose does not count.
	tail := t[i:]
	if len(tail) > 6 {
		tail = tail[:6]
	}
	if !strings.ContainsAny(tail, "-─") {
		return jsDep{}, false
	}
	depth := (i + 3) / 4
	rest := strings.TrimSpace(t[i:])
	rest = strings.TrimLeft(rest, "├└─┬`+|\\- \t")
	if rest == "" {
		return jsDep{}, false
	}
	unmet := false
	lower := strings.ToLower(rest)
	switch {
	case strings.HasPrefix(lower, "unmet dependency "):
		rest = strings.TrimSpace(rest[len("unmet dependency "):])
		unmet = true
	case strings.HasPrefix(lower, "unmet "):
		rest = strings.TrimSpace(rest[len("unmet "):])
		unmet = true
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return jsDep{}, false
	}
	name, version := jsNameVersion(fields[0])
	if name == "" {
		return jsDep{}, false
	}
	return jsDep{name: name, version: version, depth: depth, tree: true, unmet: unmet}, true
}

// jsPkgProblemWord recognizes npm's dependency problem lines.
func jsPkgProblemWord(lower string) (string, bool) {
	for _, kind := range []string{"invalid", "unmet", "extraneous", "missing", "deprecated"} {
		if strings.HasPrefix(lower, kind+":") || strings.HasPrefix(lower, kind+" ") {
			return kind, true
		}
	}
	if strings.Contains(lower, "unmet dependency") {
		return "unmet", true
	}
	return "", false
}

// jsNameVersion splits "foo@1.2.3" and "@scope/foo@1.2.3".
func jsNameVersion(s string) (name, version string) {
	s = strings.TrimSpace(strings.TrimSuffix(s, ":"))
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return s, ""
	}
	name, version = s[:at], s[at+1:]
	if v := strings.Fields(version); len(v) > 0 {
		version = v[0]
	}
	return name, version
}

func (l *jsPkg) View(state any) (lens.Node, error) {
	st, ok := state.(*jsPkgState)
	if !ok {
		return nil, errBadState
	}
	// An incomplete `npm ls` (an unmet dependency, ELSPROBLEMS) still shows a
	// view: the packages, the problem lines and the errors. Only text that is
	// neither packages nor npm problems falls back to raw.
	if len(st.deps) == 0 && len(st.problems) == 0 {
		return nil, nil
	}
	unmet := 0
	deep := false
	for _, d := range st.deps {
		if d.unmet {
			unmet++
		}
		if d.depth > 0 || d.tree {
			deep = true
		}
	}
	badges := []lens.Node{
		lens.Badge{Text: jsCount(len(st.deps), "package"), Tone: toneNeutral},
	}
	if unmet > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(unmet) + " unmet", Tone: toneError})
	}
	children := []lens.Node{lens.Row{Children: badges}}
	if st.root != "" {
		children = append(children, lens.Text{Spans: []lens.Span{{Text: st.root, Style: "path"}}})
	}
	if len(st.deps) > 0 {
		if deep {
			children = append(children, jsDepTree(st.deps))
		} else {
			items := make([]lens.Item, 0, len(st.deps))
			for _, d := range st.deps {
				style, tone := "strong", toneNeutral
				if d.unmet {
					style, tone = "strong "+toneError, toneError
				}
				item := lens.Item{Label: []lens.Span{{Text: d.name, Style: style}}, Tone: tone}
				if d.version != "" {
					vstyle := toneMuted
					if d.unmet {
						vstyle = toneError
					}
					item.Value = []lens.Span{{Text: d.version, Style: vstyle}}
				}
				if d.section != "" {
					item.Detail = []lens.Span{{Text: d.section, Style: toneMuted}}
				}
				items = append(items, item)
			}
			children = append(children, lens.List{Items: items})
		}
	}
	if len(st.problems) > 0 {
		items := make([]lens.Item, 0, len(st.problems))
		for _, p := range st.problems {
			style := p.tone
			if p.tone == toneError {
				style = "strong " + toneError
			}
			items = append(items, lens.Item{
				Label: []lens.Span{{Text: p.text, Style: style}},
				Tone:  p.tone,
			})
		}
		children = append(children, lens.List{Items: items})
	}
	return lens.Cols(children...), nil
}

// jsDepTree rebuilds the disclosure tree from a flat depth list.
func jsDepTree(deps []jsDep) lens.Tree {
	type frame struct {
		node  *lens.TreeNode
		depth int
	}
	var roots []lens.TreeNode
	var stack []*frame
	for _, d := range deps {
		style, vstyle := "strong", toneMuted
		if d.unmet {
			style, vstyle = "strong "+toneError, toneError
		}
		label := []lens.Span{{Text: d.name, Style: style}}
		if d.version != "" {
			label = append(label, lens.Span{Text: d.version, Style: vstyle})
		}
		node := lens.TreeNode{Label: label, Open: true}
		for len(stack) > 0 && stack[len(stack)-1].depth >= d.depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, node)
			stack = append(stack, &frame{node: &roots[len(roots)-1], depth: d.depth})
			continue
		}
		parent := stack[len(stack)-1].node
		parent.Children = append(parent.Children, node)
		stack = append(stack, &frame{node: &parent.Children[len(parent.Children)-1], depth: d.depth})
	}
	return lens.Tree{Nodes: roots}
}

// ---------------------------------------------------------------------------
// js.test (jest and vitest)

type jsTest struct{}

type jsTestFile struct {
	path string
	fail bool
}

type jsTestState struct {
	passed, failed, skipped, total int
	took                           string
	files                          []jsTestFile
	sawAny                         bool
}

func (l *jsTest) ID() string { return "js.test" }

func (l *jsTest) Match(cmd model.Cmd) bool {
	if cmd.Program == "jest" || cmd.Program == "vitest" {
		return true
	}
	if _, _, ok := jsSub(cmd); !ok {
		return false
	}
	if script := jsRunScript(cmd); script == "test" || strings.HasPrefix(script, "test:") {
		return true
	}
	_, sub, _ := jsSub(cmd)
	return sub == "test" || sub == "t"
}

func (l *jsTest) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &jsTestState{}, nil }
func (l *jsTest) Finish(state any, status int) error            { return nil }
func (l *jsTest) Event(state any, ev lens.Event) error          { return nil }

func (l *jsTest) Line(state any, ln model.Line) error {
	st, ok := state.(*jsTestState)
	if !ok {
		return errBadState
	}
	st.observe(ln.Text)
	return nil
}

func (st *jsTestState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, "PASS "):
		st.files = append(st.files, jsTestFile{path: strings.TrimSpace(t[5:])})
		st.sawAny = true
		return
	case strings.HasPrefix(t, "FAIL "):
		st.files = append(st.files, jsTestFile{path: strings.TrimSpace(t[5:]), fail: true})
		st.sawAny = true
		return
	}
	if strings.HasPrefix(t, "Ran all test suites") {
		st.sawAny = true
		return
	}
	if strings.HasPrefix(t, "Time:") || strings.HasPrefix(t, "Duration") ||
		strings.HasPrefix(t, "Tests:") || strings.HasPrefix(t, "Tests ") ||
		strings.HasPrefix(t, "Test Files") || strings.HasPrefix(t, "Test Suites:") {
		if strings.HasPrefix(t, "Time:") || strings.HasPrefix(t, "Duration") {
			switch {
			case strings.HasPrefix(t, "Time:"):
				st.took = strings.TrimSpace(t[len("Time:"):])
			default:
				st.took = strings.TrimSpace(t[len("Duration"):])
			}
		}
		if p, fl, sk, total, ok := jsTestCounts(t); ok {
			if strings.HasPrefix(t, "Tests") {
				st.passed, st.failed, st.skipped, st.total = p, fl, sk, total
			}
			st.sawAny = true
		}
		return
	}
}

// jsTestCounts reads jest's "Tests: 1 failed, 2 passed, 3 total" and vitest's
// "Tests  1 failed | 2 passed (3)".
func jsTestCounts(line string) (passed, failed, skipped, total int, ok bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		if i+1 >= len(fields) {
			break
		}
		switch strings.TrimRight(fields[i+1], ",") {
		case "passed":
			passed, ok = n, true
		case "failed":
			failed, ok = n, true
		case "skipped", "todo":
			skipped, ok = n, true
		case "total":
			total, ok = n, true
		}
	}
	return passed, failed, skipped, total, ok
}

func (l *jsTest) View(state any) (lens.Node, error) {
	st, ok := state.(*jsTestState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	passed, failed, skipped := st.passed, st.failed, st.skipped
	if passed == 0 && failed == 0 && len(st.files) > 0 {
		for _, f := range st.files {
			if f.fail {
				failed++
			} else {
				passed++
			}
		}
	}
	if passed == 0 && failed == 0 && skipped == 0 {
		return nil, nil
	}
	children := []lens.Node{lens.TestSummary{Passed: passed, Failed: failed, Skipped: skipped, Took: st.took}}
	if len(st.files) > 0 {
		items := make([]lens.Item, 0, len(st.files))
		for _, f := range st.files {
			tone, style := toneSuccess, toneSuccess
			if f.fail {
				tone, style = toneError, "strong "+toneError
			}
			items = append(items, lens.Item{
				Label: []lens.Span{{Text: f.path, Style: style}},
				Tone:  tone,
			})
		}
		children = append(children, lens.List{Items: items})
	}
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------------------
// js.lint (eslint's stylish output)

type jsLint struct{}

type jsProblem struct {
	file, line, col, severity, msg, rule string
}

type jsLintState struct {
	problems   []jsProblem
	errors     int
	warnings   int
	noProblems bool
	current    string
	sawAny     bool
}

func (l *jsLint) ID() string { return "js.lint" }

func (l *jsLint) Match(cmd model.Cmd) bool { return cmd.Program == "eslint" }

func (l *jsLint) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &jsLintState{}, nil }
func (l *jsLint) Finish(state any, status int) error            { return nil }
func (l *jsLint) Event(state any, ev lens.Event) error          { return nil }

func (l *jsLint) Line(state any, ln model.Line) error {
	st, ok := state.(*jsLintState)
	if !ok {
		return errBadState
	}
	st.observe(ln.Text)
	return nil
}

func (st *jsLintState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	lower := strings.ToLower(t)
	if strings.Contains(lower, "problem") && strings.Contains(t, "(") {
		if n := firstInt(t); n >= 0 {
			st.errors, st.warnings = jsProblemCounts(t)
			st.sawAny = true
		}
		return
	}
	if strings.HasPrefix(lower, "no problems") || strings.Contains(lower, "no problems") {
		st.noProblems = true
		st.sawAny = true
		return
	}
	if p, ok := jsProblemLine(t); ok {
		p.file = st.current
		st.problems = append(st.problems, p)
		st.sawAny = true
		return
	}
	// A file header in the stylish form: a path with no leading spaces.
	if !strings.HasPrefix(text, " ") && !strings.HasPrefix(text, "\t") &&
		!strings.Contains(t, " ") && (strings.Contains(t, "/") || strings.Contains(t, ".")) {
		st.current = t
	}
}

// jsProblemLine reads "12:5  error  message  rule-name".
func jsProblemLine(t string) (jsProblem, bool) {
	cols := jsSplitColumns(t)
	if len(cols) < 3 {
		return jsProblem{}, false
	}
	pos := strings.SplitN(cols[0], ":", 2)
	if len(pos) != 2 || !allDigits(pos[0]) || !allDigits(pos[1]) {
		return jsProblem{}, false
	}
	sev := strings.ToLower(cols[1])
	if sev != "error" && sev != "warning" && sev != "info" {
		return jsProblem{}, false
	}
	p := jsProblem{line: pos[0], col: pos[1], severity: sev, msg: cols[2]}
	if len(cols) >= 4 {
		p.rule = cols[3]
	}
	return p, true
}

// jsSplitColumns splits on runs of two or more spaces (eslint's columns).
func jsSplitColumns(s string) []string {
	var out []string
	start := 0
	spaces := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			spaces++
			continue
		}
		if spaces >= 2 {
			if part := strings.TrimSpace(s[start : i-spaces]); part != "" {
				out = append(out, part)
			}
			start = i
		}
		spaces = 0
	}
	if part := strings.TrimSpace(s[start:]); part != "" {
		out = append(out, part)
	}
	return out
}

// jsProblemCounts reads "(1 error, 2 warnings)".
func jsProblemCounts(s string) (errors, warnings int) {
	fields := strings.Fields(strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(s))
	for i, f := range fields {
		if n, err := strconv.Atoi(f); err == nil && i+1 < len(fields) {
			switch {
			case strings.HasPrefix(fields[i+1], "error"):
				errors = n
			case strings.HasPrefix(fields[i+1], "warning"):
				warnings = n
			}
		}
	}
	return errors, warnings
}

func (l *jsLint) View(state any) (lens.Node, error) {
	st, ok := state.(*jsLintState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	if st.noProblems && len(st.problems) == 0 {
		return lens.Row{Children: []lens.Node{
			lens.Badge{Text: "no problems", Tone: toneSuccess},
		}}, nil
	}
	if len(st.problems) == 0 && st.errors == 0 && st.warnings == 0 {
		return nil, nil
	}
	if st.errors == 0 && st.warnings == 0 {
		for _, p := range st.problems {
			if p.severity == "error" {
				st.errors++
			} else {
				st.warnings++
			}
		}
	}
	badges := []lens.Node{}
	if st.errors > 0 {
		badges = append(badges, lens.Badge{Text: jsCount(st.errors, "error"), Tone: toneError})
	}
	if st.warnings > 0 {
		badges = append(badges, lens.Badge{Text: jsCount(st.warnings, "warning"), Tone: toneWarning})
	}
	children := []lens.Node{lens.Row{Children: badges}}
	if len(st.problems) > 0 {
		// An eslint problem is a diagnostic: Terminal renders it with
		// `flow.ui.diagnostic`, the rule as its code.
		nodes := make([]lens.Node, 0, len(st.problems))
		for _, p := range st.problems {
			sev := lens.SeverityWarning
			if p.severity == "error" {
				sev = lens.SeverityError
			}
			notes := []string{}
			if p.rule != "" {
				notes = append(notes, p.rule)
			}
			nodes = append(nodes, lens.Diagnostic{
				Severity: sev,
				Code:     p.rule,
				Message:  p.msg,
				Path:     p.file,
				Line:     jsAtoi(p.line),
				Col:      jsAtoi(p.col),
				Notes:    notes,
			})
		}
		children = append(children, lens.Column{Children: nodes})
	}
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------------------
// js.tsc

type jsTsc struct{}

type jsTscDiag struct {
	file, line, col, severity, code, msg string
}

type jsTscState struct {
	diags  []jsTscDiag
	errors int
	sawAny bool
}

func (l *jsTsc) ID() string { return "js.tsc" }

func (l *jsTsc) Match(cmd model.Cmd) bool { return cmd.Program == "tsc" }

func (l *jsTsc) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &jsTscState{}, nil }
func (l *jsTsc) Finish(state any, status int) error            { return nil }
func (l *jsTsc) Event(state any, ev lens.Event) error          { return nil }

func (l *jsTsc) Line(state any, ln model.Line) error {
	st, ok := state.(*jsTscState)
	if !ok {
		return errBadState
	}
	st.observe(ln.Text)
	return nil
}

func (st *jsTscState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "Found ") && strings.Contains(t, "error") {
		st.errors = firstInt(t)
		st.sawAny = true
		return
	}
	if d, ok := parseJSTscDiag(t); ok {
		st.diags = append(st.diags, d)
		if d.severity == "error" {
			st.errors++
		}
		st.sawAny = true
	}
}

// parseJSTscDiag reads "src/a.ts(12,5): error TS2322: message" and the location-less
// "error TS2322: message".
func parseJSTscDiag(t string) (jsTscDiag, bool) {
	var d jsTscDiag
	rest := t
	if open := strings.Index(t, "("); open > 0 {
		close := strings.Index(t, "):")
		if close > open {
			d.file = strings.TrimSpace(t[:open])
			pos := strings.SplitN(t[open+1:close], ",", 2)
			if len(pos) == 2 {
				d.line, d.col = strings.TrimSpace(pos[0]), strings.TrimSpace(pos[1])
			}
			rest = strings.TrimSpace(t[close+2:])
		}
	}
	for _, sev := range []string{"error", "warning", "info"} {
		if strings.HasPrefix(rest, sev) {
			d.severity = sev
			rest = strings.TrimSpace(rest[len(sev):])
			break
		}
	}
	if d.severity == "" {
		return jsTscDiag{}, false
	}
	if strings.HasPrefix(rest, "TS") {
		code, msg, found := strings.Cut(rest, ":")
		if !found {
			return jsTscDiag{}, false
		}
		d.code = strings.TrimSpace(code)
		d.msg = strings.TrimSpace(msg)
	} else {
		d.msg = rest
	}
	if d.msg == "" {
		return jsTscDiag{}, false
	}
	return d, true
}

func (l *jsTsc) View(state any) (lens.Node, error) {
	st, ok := state.(*jsTscState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny || len(st.diags) == 0 {
		return nil, nil
	}
	badges := []lens.Node{}
	if st.errors > 0 {
		badges = append(badges, lens.Badge{Text: jsCount(st.errors, "error"), Tone: toneError})
	}
	children := []lens.Node{lens.Row{Children: badges}}
	// One diagnostic node per tsc diagnostic, Terminal's `flow.ui.diagnostic`.
	nodes := make([]lens.Node, 0, len(st.diags))
	for _, d := range st.diags {
		sev := lens.SeverityWarning
		if d.severity == "error" {
			sev = lens.SeverityError
		}
		nodes = append(nodes, lens.Diagnostic{
			Severity: sev,
			Code:     d.code,
			Message:  d.msg,
			Path:     d.file,
			Line:     jsAtoi(d.line),
			Col:      jsAtoi(d.col),
		})
	}
	children = append(children, lens.Column{Children: nodes})
	return lens.Cols(children...), nil
}

// jsAtoi parses a decimal string, returning 0 when it is not a number.
func jsAtoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// jsCount renders a count with a singular or plural word: "1 error", "2 errors".
func jsCount(n int, word string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
