package builtin

// go family: Terminal's Go toolchain lenses, `go.build` and `go.test`.
//
// Mined evidence (payload\flow.exe offset 149986288, per findings\
// lens-conditions\raw-lens-region-runs2.txt line 365):
//
//	"go.build" + "go.test" + "--directory --project -p --python --cache-dir
//	 --config-file --color --package"                                  [观察]
//
// the two ids are the whole readable run before the flag table; the glue at
// 149986504 is "py.pkg"+"py.lint", so `go.*` is exactly these two. The flag
// table is a neighbouring cluster: `--directory --cache-dir --config-file
// --color --package` read like a package manager's, not the go command's, and
// findings §1.2 warns that same-pool neighbours are not necessarily the same
// lens — 【推断】 only `-p -o -v -run -bench -count` are taken as go's own, and
// no flag is required to match.
//
// The output side is ours (the constants region holds no parsing code): `go
// build` prints "file.go:12:3: undefined: x" — the same "location: message"
// shape build.diag splits, minus any severity word, which is why this file
// reuses buildDiagSplitLocation/buildDiagParse from build.go — and `go test`
// prints "ok pkg 0.123s", "FAIL pkg [build failed]", "--- FAIL: TestX (0.00s)",
// "=== RUN", "PASS" and "coverage: 42.0% of statements". Tolerant as ever: an
// unrecognized line is ignored and a view that recognized nothing returns nil,
// which leaves the block raw.

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	// The globs are left empty on purpose: the gate is goSubcommand, which can
	// look past go's own `-C dir` argument and never claims `go vet` or any
	// other family's program.
	lens.Register(lens.Definition{
		ID:    "go.test",
		Match: nil,
		New:   func() lens.Lens { return &goTest{} },
	})
	lens.Register(lens.Definition{
		ID:    "go.build",
		Match: nil,
		New:   func() lens.Lens { return &goBuild{} },
	})
}

// goSubcommand reports whether the command is `go <sub>`, looking past go's own
// leading `-C dir` argument.
func goSubcommand(cmd model.Cmd, sub string) bool {
	if cmd.Program != "go" {
		return false
	}
	args := cmd.Args
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-C" || a == "--C" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a == sub
	}
	return false
}

// goDiagParse reads the go tool's own error shapes: the compiler's
// "file.go:12:3: undefined: x" (no severity word), the "go: cannot find main
// module ..." prefix, and the severity-word forms build.diag reads.
func goDiagParse(t string) (buildDiagItem, bool) {
	if strings.HasPrefix(t, "go: ") {
		msg := strings.TrimSpace(strings.TrimPrefix(t, "go: "))
		if msg == "" {
			return buildDiagItem{}, false
		}
		return buildDiagItem{sev: "error", msg: msg}, true
	}
	if d, ok := buildDiagParse(t); ok {
		return d, true
	}
	if path, line, col, rest, ok := buildDiagSplitLocation(t); ok {
		return buildDiagItem{sev: "error", path: path, line: line, col: col, msg: rest}, true
	}
	return buildDiagItem{}, false
}

// ---------------------------------------------------------------------------
// go.build — the compiler's diagnostics, or the package list of `go build -v`.

type goBuild struct{}

type goBuildState struct {
	diags    []buildDiagItem
	packages []string
	sawAny   bool
}

func (g *goBuild) ID() string { return "go.build" }

func (g *goBuild) Match(cmd model.Cmd) bool { return goSubcommand(cmd, "build") }

func (g *goBuild) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &goBuildState{}, nil }

func (g *goBuild) Line(state any, l model.Line) error {
	st, ok := state.(*goBuildState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (g *goBuild) Finish(state any, status int) error { return nil }

func (g *goBuild) Event(state any, ev lens.Event) error { return nil }

func (st *goBuildState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	// "# example.com/mod/pkg" heads the error block of one package.
	if strings.HasPrefix(t, "# ") {
		st.sawAny = true
		return
	}
	if d, ok := goDiagParse(t); ok {
		st.diags = append(st.diags, d)
		st.sawAny = true
		return
	}
	if goPackagePath(t) {
		st.packages = append(st.packages, t)
		st.sawAny = true
	}
}

// goPackagePath reports whether a line is a bare import path, as `go build -v`
// prints them.
func goPackagePath(t string) bool {
	if t == "" || strings.ContainsAny(t, " \t:") {
		return false
	}
	dotted := false
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/', r == '_', r == '-', r == '~':
		case r == '.':
			dotted = true
		default:
			return false
		}
	}
	return dotted
}

func (g *goBuild) View(state any) (lens.Node, error) {
	st, ok := state.(*goBuildState)
	if !ok {
		return nil, errBadState
	}
	if len(st.diags) > 0 {
		return buildDiagnosticView(st.diags), nil
	}
	if len(st.packages) > 0 {
		hidden := 0
		shown := st.packages
		if len(shown) > buildMaxItems {
			hidden, shown = len(shown)-buildMaxItems, shown[:buildMaxItems]
		}
		items := make([]lens.Item, 0, len(shown))
		for _, p := range shown {
			items = append(items, lens.Item{
				Label: []lens.Span{{Text: p, Style: "path"}},
				Icon:  "package",
			})
		}
		children := []lens.Node{lens.List{Items: items}}
		if hidden > 0 {
			children = append(children, lens.Overflow{Hidden: hidden})
		}
		children = append(children, lens.Badge{Text: plural(len(st.packages), "package", "packages"), Tone: toneMuted})
		return lens.Cols(children...), nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// go.test — package results, per-test results and the failure output.

type goTest struct{}

type goTestPkg struct {
	path    string
	status  string // ok, FAIL, no test files
	elapsed string
	note    string
}

type goTestFail struct {
	name    string
	elapsed string
	out     []string
}

type goTestState struct {
	pkgs        []goTestPkg
	fails       []goTestFail
	pending     []string
	diags       []buildDiagItem
	passed      int
	failed      int
	skipped     int
	pkgFailed   int
	coverage    float64
	hasCoverage bool
	inBuild     bool // under a "# pkg" header: the lines are compiler errors
	running     bool // between "=== RUN" and the test's result line
	sawAny      bool
}

func (g *goTest) ID() string { return "go.test" }

func (g *goTest) Match(cmd model.Cmd) bool { return goSubcommand(cmd, "test") }

func (g *goTest) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &goTestState{}, nil }

func (g *goTest) Line(state any, l model.Line) error {
	st, ok := state.(*goTestState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (g *goTest) Finish(state any, status int) error { return nil }

func (g *goTest) Event(state any, ev lens.Event) error { return nil }

func (st *goTestState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	indented := text != "" && (text[0] == ' ' || text[0] == '\t')
	if pkg, ok := goTestSummaryLine(t); ok {
		st.pkgs = append(st.pkgs, pkg)
		st.pending, st.inBuild, st.running = nil, false, false
		if i := strings.Index(t, "coverage: "); i >= 0 {
			if f, ok := goPercent(t[i+len("coverage: "):]); ok {
				st.coverage, st.hasCoverage = f, true
			}
		}
		if pkg.status == "FAIL" {
			st.pkgFailed++
		}
		st.sawAny = true
		return
	}
	if strings.HasPrefix(t, "# ") {
		st.inBuild = true
		st.pending = nil
		st.sawAny = true
		return
	}
	if name, status, took, ok := goTestResultLine(t); ok {
		switch status {
		case "PASS":
			st.passed++
			st.pending = nil
		case "FAIL":
			st.failed++
			st.fails = append(st.fails, goTestFail{name: name, elapsed: took, out: st.pending})
			st.pending = nil
		case "SKIP":
			st.skipped++
			st.pending = nil
		}
		st.running = false
		st.sawAny = true
		return
	}
	switch {
	case strings.HasPrefix(t, "=== RUN"):
		st.running = true
		st.sawAny = true
		return
	case strings.HasPrefix(t, "=== PAUSE"), strings.HasPrefix(t, "=== CONT"):
		st.sawAny = true
		return
	case t == "PASS", t == "FAIL":
		st.sawAny = true
		return
	case strings.HasPrefix(t, "panic:"), strings.HasPrefix(t, "exit status "):
		st.pending = append(st.pending, t)
		st.sawAny = true
		return
	}
	// A test's own output is indented. With -v it precedes its "--- FAIL"
	// line, so it waits in pending; without -v it follows the result line and
	// belongs to the failure already recorded.
	if indented {
		switch {
		case st.running:
			if len(st.pending) < 8 {
				st.pending = append(st.pending, t)
			}
		case len(st.fails) > 0:
			last := &st.fails[len(st.fails)-1]
			if len(last.out) < 8 {
				last.out = append(last.out, t)
			}
		default:
			if len(st.pending) < 8 {
				st.pending = append(st.pending, t)
			}
		}
		return
	}
	// A compiler error only appears under the package header go prints first;
	// everything else here is unrelated output.
	if st.inBuild {
		if d, ok := goDiagParse(t); ok {
			st.diags = append(st.diags, d)
			st.sawAny = true
		}
	}
}

// goTestSummaryLine reads a package result: "ok pkg 0.123s [coverage: ...]",
// "FAIL pkg [build failed]" or "? pkg [no test files]".
func goTestSummaryLine(line string) (goTestPkg, bool) {
	f := strings.Fields(line)
	// ConPTY drops the tab between a package path and its result fields, so
	// "ok  probe0.010s" arrives glued: split the elapsed time back off the path.
	if len(f) == 2 && (f[0] == "ok" || f[0] == "FAIL") {
		if path, elapsed, ok := goSplitPathElapsed(f[1]); ok {
			return goTestPkg{path: path, status: f[0], elapsed: elapsed}, true
		}
	}
	if len(f) < 2 {
		return goTestPkg{}, false
	}
	switch f[0] {
	case "ok", "FAIL":
	case "?":
		return goTestPkg{path: f[1], status: "no test files", note: strings.Join(f[2:], " ")}, true
	default:
		return goTestPkg{}, false
	}
	rest := f[2:]
	if len(rest) == 0 {
		return goTestPkg{}, false
	}
	pkg := goTestPkg{path: f[1], status: f[0], elapsed: rest[0], note: strings.Join(rest[1:], " ")}
	if strings.HasPrefix(rest[0], "[") {
		pkg = goTestPkg{path: f[1], status: f[0], note: strings.Join(rest, " ")}
	}
	return pkg, true
}

// goSplitPathElapsed splits the glued "path0.010s" (or "path(cached)") that
// ConPTY produces when it drops the column tab.
func goSplitPathElapsed(tok string) (path, elapsed string, ok bool) {
	if strings.HasSuffix(tok, ")") {
		if i := strings.LastIndexByte(tok, '('); i > 0 && i < len(tok)-2 {
			if goElapsedToken(tok[i+1:len(tok)-1], true) {
				return tok[:i], tok[i:], true
			}
		}
	}
	for i := 1; i < len(tok)-1; i++ {
		if !goElapsedToken(tok[i:], false) {
			continue
		}
		prev := tok[i-1]
		if prev >= '0' && prev <= '9' || prev == '.' {
			continue // "pkg.v2" or a version, not a path/elapsed boundary
		}
		return tok[:i], tok[i:], true
	}
	return "", "", false
}

// goElapsedToken reports whether s is a go test elapsed time ("0.010s").
func goElapsedToken(s string, paren bool) bool {
	if paren {
		if s == "cached" {
			return true
		}
	}
	if !strings.HasSuffix(s, "s") || len(s) < 2 {
		return false
	}
	body := s[:len(s)-1]
	dot := false
	digits := 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot && digits > 0:
			dot = true
		default:
			return false
		}
	}
	return digits > 0
}

// goTestResultLine reads "--- PASS: TestFoo (0.00s)" and its indented subtests.
func goTestResultLine(t string) (name, status, took string, ok bool) {
	if !strings.HasPrefix(t, "--- ") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(t, "--- ")
	i := strings.Index(rest, ": ")
	if i <= 0 {
		return "", "", "", false
	}
	status = rest[:i]
	switch status {
	case "PASS", "FAIL", "SKIP":
	default:
		return "", "", "", false
	}
	rest = rest[i+2:]
	name = rest
	if j := strings.LastIndex(rest, " ("); j > 0 && strings.HasSuffix(rest, ")") {
		name, took = rest[:j], rest[j+2:len(rest)-1]
	}
	if name == "" {
		return "", "", "", false
	}
	return name, status, took, true
}

// goPercent reads "42.0% of statements".
func goPercent(s string) (float64, bool) {
	i := strings.IndexByte(s, '%')
	if i <= 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func (g *goTest) View(state any) (lens.Node, error) {
	st, ok := state.(*goTestState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	if st.passed == 0 && st.failed == 0 && st.skipped == 0 &&
		len(st.pkgs) == 0 && len(st.diags) == 0 && len(st.fails) == 0 {
		return nil, nil
	}
	children := []lens.Node{lens.TestSummary{
		Passed: st.passed, Failed: st.failed, Skipped: st.skipped, Took: goTestTook(st),
	}}
	if len(st.pkgs) > 0 {
		rows := make([][]lens.Cell, 0, len(st.pkgs))
		for _, p := range st.pkgs {
			rows = append(rows, []lens.Cell{
				buildCell(p.path, "path"),
				buildCell(p.status, goPkgTone(p.status)),
				buildCell(p.elapsed, toneMuted),
				buildCell(p.note, toneMuted),
			})
		}
		children = append(children, lens.Table{
			Cols: []lens.Col{
				{ID: "package", Head: "PACKAGE", Grow: 1},
				{ID: "status", Head: "STATUS"},
				{ID: "elapsed", Head: "ELAPSED"},
				{ID: "note", Head: "NOTE", Priority: 1},
			},
			Rows: rows,
		})
	}
	if len(st.diags) > 0 {
		children = append(children, lens.List{Items: buildDiagItems(st.diags)})
	}
	if len(st.fails) > 0 {
		hidden := 0
		shown := st.fails
		if len(shown) > buildMaxItems {
			hidden, shown = len(shown)-buildMaxItems, shown[:buildMaxItems]
		}
		items := make([]lens.Item, 0, len(shown))
		for _, f := range shown {
			label := []lens.Span{{Text: f.name, Style: "strong " + toneError}}
			if f.elapsed != "" {
				label = append(label, lens.Span{Text: " " + f.elapsed, Style: toneMuted})
			}
			detail := make([]lens.Span, 0, len(f.out))
			for _, o := range f.out {
				detail = append(detail, lens.Span{Text: o, Style: toneMuted})
			}
			items = append(items, lens.Item{Label: label, Detail: detail, Tone: toneError, Icon: "failed"})
		}
		children = append(children, lens.List{Items: items})
		if hidden > 0 {
			children = append(children, lens.Overflow{Hidden: hidden})
		}
	}
	var badges []lens.Node
	if st.hasCoverage {
		text := strconv.FormatFloat(st.coverage, 'f', 1, 64) + "% coverage"
		tone := toneSuccess
		if st.coverage < 50 {
			tone = toneWarning
		}
		badges = append(badges, lens.Badge{Text: text, Tone: tone})
	}
	errors, _ := buildDiagCounts(st.diags)
	switch {
	case st.failed > 0:
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.failed) + " failed", Tone: toneError})
	case st.pkgFailed > 0:
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.pkgFailed) + " packages failed", Tone: toneError})
	case errors > 0:
		badges = append(badges, lens.Badge{Text: strconv.Itoa(errors) + " errors", Tone: toneError})
	case len(st.pkgs) > 0 || st.passed > 0:
		badges = append(badges, lens.Badge{Text: "pass", Tone: toneSuccess})
	}
	if len(badges) > 0 {
		children = append(children, lens.Row{Children: badges})
	}
	return lens.Cols(children...), nil
}

func goPkgTone(status string) string {
	switch status {
	case "ok":
		return toneSuccess
	case "FAIL":
		return toneError
	}
	return toneMuted
}

// goTestTook adds up the package elapsed times, as `go test` reports them.
func goTestTook(st *goTestState) string {
	total := 0.0
	seen := false
	for _, p := range st.pkgs {
		if !strings.HasSuffix(p.elapsed, "s") {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSuffix(p.elapsed, "s"), 64)
		if err != nil {
			continue
		}
		total += f
		seen = true
	}
	if !seen {
		return ""
	}
	return strconv.FormatFloat(total, 'f', 3, 64) + "s"
}
