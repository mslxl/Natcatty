package builtin

// build family: Terminal's compiler/build lenses, `build.diag`, `build.make`,
// `build.swift`.
//
// Mined evidence (payload\flow.exe, offsets per findings\lens-conditions\
// raw-lens-region-runs2.txt; the region is glued Rust .rdata, so only the
// readable whole pieces are trusted):
//
//	149989072  "build.diag" + "build.swift" + "build.make" + "--message-format"
//	           → the three ids and SwiftPM's structured-output flag    [观察]
//	149989216  ": In function '" / "For more information about this error" /
//	           "Some errors have detailed explanations: " / "Leaving directory'"
//	           / "Doc-tests" / " generated " → the diagnostic vocabulary [观察]
//	149989383  ": fatal error: " / ": error: " / ": warning: " / ": note: " /
//	           ": remark: " / " [output" / "Output: " / "Entering directory" /
//	           "Nothing to be done" → the severity markers this lens splits on
//	           and the two make markers                                [观察]
//	149987998  " ... ok) make: ***  recorded an issue Swift Testing at
//	           passed after  failed after  skipped after " → make's "*** " error
//	           marker and Swift Testing's per-test result words   [观察]
//	149987520  "ninja: build stopped: no work to do."              [观察]
//	149988267  "ar as bash bison bun cargo ... cango cmake codesign cp flex go
//	           install java cld ld ld.lld libtool lipo ln mkdir mvn asm ninja
//	           node npm npx protoc python python3 ranlib rm rustc sh strip tar
//	           touch xcrun yacc zip" → the build tool-name table    [观察, glued]
//
// Inferred 【推断】: the table is de-glued by hand, and the lens claims only the
// compiler-like tools in it. `go.*` and `rust.*` own `go`/`cargo`, and
// `make`/`swift` own their subcommands, so build.diag never answers for them
// (a build.diag that swallowed `go build` would hide go.*'s diagnostics). The
// parsers are ours and tolerant: an unrecognized line is ignored and a view
// that recognized nothing returns nil, which leaves the block raw.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:    "build.diag",
		Match: nil, // the tool table decides; see buildDiagPrograms
		New:   func() lens.Lens { return &buildDiag{} },
	})
	lens.Register(lens.Definition{
		ID:    "build.make",
		Match: nil, // exactly `make`/`gmake`
		New:   func() lens.Lens { return &buildMake{} },
	})
	lens.Register(lens.Definition{
		ID:    "build.swift",
		Match: nil, // exactly `swift`; `swiftc` keeps raw output
		New:   func() lens.Lens { return &buildSwift{} },
	})
}

// ---------------------------------------------------------------------------
// build.diag — compiler output: "file:line:col: error: message".

type buildDiag struct{}

// buildDiagItem is one parsed diagnostic; swift reuses it too.
type buildDiagItem struct {
	sev       string // error, warning, note, help
	code      string // rustc's E0308
	path      string
	line, col int
	msg       string
}

type buildDiagState struct {
	items []buildDiagItem
}

// buildDiagPrograms is the compiler-like subset of the mined tool table
// [149988267]. Families that own a program elsewhere are deliberately absent:
// go (go.*), cargo (rust.*), npm/npx/node/bun (js.*), python/python3 (py.*),
// make and swift (this family's own two lenses).
var buildDiagPrograms = map[string]bool{
	"cc": true, "gcc": true, "g++": true, "c++": true, "clang": true, "clang++": true,
	"rustc": true, "ld": true, "ld.lld": true, "libtool": true, "lipo": true,
	"as": true, "ar": true, "nm": true, "ranlib": true, "strip": true,
	"cmake": true, "ninja": true, "mvn": true, "gradle": true, "msbuild": true,
	"protoc": true, "codesign": true, "xcrun": true, "yacc": true, "bison": true,
	"flex": true, "java": true, "javac": true,
}

func (b *buildDiag) ID() string { return "build.diag" }

func (b *buildDiag) Match(cmd model.Cmd) bool { return buildDiagProgram(cmd.Program) }

// buildDiagProgram accepts `gcc` and its versioned spellings `gcc-13`.
func buildDiagProgram(program string) bool {
	if buildDiagPrograms[program] {
		return true
	}
	if i := strings.LastIndexByte(program, '-'); i > 0 && allDigits(program[i+1:]) {
		return buildDiagPrograms[program[:i]]
	}
	return false
}

func (b *buildDiag) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &buildDiagState{}, nil }

func (b *buildDiag) Line(state any, l model.Line) error {
	st, ok := state.(*buildDiagState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (b *buildDiag) Finish(state any, status int) error { return nil }

func (b *buildDiag) Event(state any, ev lens.Event) error { return nil }

func (st *buildDiagState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	// rustc puts the location on the line after the diagnostic: "--> src/a.rs:5:9".
	if strings.HasPrefix(t, "-->") {
		if path, line, col, _, ok := buildDiagSplitLocation(strings.TrimSpace(t[3:])); ok && len(st.items) > 0 {
			last := &st.items[len(st.items)-1]
			if last.path == "" {
				last.path, last.line, last.col = path, line, col
			}
		}
		return
	}
	if it, ok := buildDiagParse(t); ok {
		st.items = append(st.items, it)
	}
}

// buildDiagParse reads one diagnostic line in any of the shapes the toolchain
// vocabulary covers: gcc/clang "file:line:col: error: ...", rustc
// "error[E0308]: ...", "fatal error: ...", make/ninja wrapper prefixes and the
// "In function '...'" context lines.
func buildDiagParse(t string) (buildDiagItem, bool) {
	// gcc prefixes its context line with the file: "main.c: In function 'main':".
	if i := strings.Index(t, "In function '"); i > 0 && strings.HasSuffix(t, "':") &&
		!strings.Contains(t, ": error:") && !strings.Contains(t, ": warning:") {
		return buildDiagItem{sev: "note", msg: t}, true
	}
	switch {
	case strings.HasPrefix(t, "In function '"), strings.HasPrefix(t, "In member function '"),
		strings.HasPrefix(t, "In file included from"), strings.HasPrefix(t, "In instantiation of"),
		strings.HasPrefix(t, "At global scope"), strings.HasPrefix(t, "Some errors have detailed explanations:"),
		strings.HasPrefix(t, "For more information about this error"):
		return buildDiagItem{sev: "note", msg: strings.TrimSuffix(t, ":")}, true
	case strings.HasPrefix(t, "ninja: build stopped"), strings.HasPrefix(t, "ninja: error"):
		return buildDiagItem{sev: "error", msg: t}, true
	case strings.Contains(t, "could not compile"), strings.HasPrefix(t, "error: aborting due to"):
		return buildDiagItem{sev: "error", msg: t}, true
	}
	if path, line, col, rest, ok := buildDiagSplitLocation(t); ok {
		if d, ok := buildDiagSeverity(rest); ok {
			d.path, d.line, d.col = path, line, col
			return d, true
		}
	}
	if d, ok := buildDiagSeverity(t); ok {
		return d, true
	}
	// A tool prefix in front of the severity: "ninja: error: ...".
	if i := buildDiagSeverityIndex(t); i > 0 {
		if d, ok := buildDiagSeverity(t[i+1:]); ok {
			return d, true
		}
	}
	return buildDiagItem{}, false
}

// buildDiagSplitLocation reads "path:line:col: rest" and "path:line: rest".
func buildDiagSplitLocation(t string) (path string, line, col int, rest string, ok bool) {
	start := 0
	if len(t) > 2 && t[1] == ':' && (t[2] == '\\' || t[2] == '/') {
		start = 2 // C:\src\a.c
	}
	i := strings.IndexByte(t[start:], ':')
	if i < 0 {
		return "", 0, 0, "", false
	}
	i += start
	path = t[:i]
	if path == "" || strings.ContainsAny(path, " \t") {
		return "", 0, 0, "", false
	}
	p := t[i+1:]
	line = leadingInt(p)
	if line <= 0 {
		return "", 0, 0, "", false
	}
	p = p[len(strconv.Itoa(line)):]
	if !strings.HasPrefix(p, ":") {
		return "", 0, 0, "", false
	}
	p = p[1:]
	if n := leadingInt(p); n > 0 {
		col = n
		p = p[len(strconv.Itoa(n)):]
		if p == "" {
			// rustc's "--> src/main.rs:5:9" puts the location last.
			return path, line, col, "", true
		}
		if !strings.HasPrefix(p, ":") {
			return "", 0, 0, "", false
		}
		p = p[1:]
	}
	rest = strings.TrimSpace(p)
	if rest == "" {
		return "", 0, 0, "", false
	}
	return path, line, col, rest, true
}

// buildDiagSeverity reads a "severity: message" head, including rustc's
// "error[E0308]: ..." code form.
func buildDiagSeverity(s string) (buildDiagItem, bool) {
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "error[") {
		if j := strings.IndexByte(s, ']'); j > 6 {
			msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[j+1:]), ":"))
			if msg == "" {
				return buildDiagItem{}, false
			}
			return buildDiagItem{sev: "error", code: s[6:j], msg: msg}, true
		}
	}
	for _, m := range []struct{ prefix, sev string }{
		{"fatal error:", "error"},
		{"error:", "error"},
		{"warning:", "warning"},
		{"note:", "note"},
		{"remark:", "note"},
		{"help:", "help"},
	} {
		if strings.HasPrefix(low, m.prefix) {
			msg := strings.TrimSpace(s[len(m.prefix):])
			if msg == "" {
				return buildDiagItem{}, false
			}
			return buildDiagItem{sev: m.sev, msg: msg}, true
		}
	}
	return buildDiagItem{}, false
}

// buildDiagSeverityIndex finds a ": severity:" marker in the middle of a line.
func buildDiagSeverityIndex(t string) int {
	low := strings.ToLower(t)
	best := -1
	for _, marker := range []string{": fatal error:", ": error:", ": warning:", ": note:", ": remark:"} {
		if i := strings.Index(low, marker); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// buildDiagItems turns parsed diagnostics into list rows: the severity and the
// path:line:col on the label, the message under it.
func buildDiagItems(items []buildDiagItem) []lens.Item {
	out := make([]lens.Item, 0, len(items))
	for _, d := range items {
		label := []lens.Span{{Text: d.sev, Style: "strong " + buildSevTone(d.sev)}}
		if d.code != "" {
			label = append(label, lens.Span{Text: " [" + d.code + "]", Style: toneMuted})
		}
		if d.path != "" {
			loc := d.path
			if d.line > 0 {
				loc += ":" + strconv.Itoa(d.line)
				if d.col > 0 {
					loc += ":" + strconv.Itoa(d.col)
				}
			}
			label = append(label, lens.Span{Text: " " + loc, Style: "path"})
		}
		out = append(out, lens.Item{
			Label:  label,
			Detail: []lens.Span{{Text: d.msg}},
			Tone:   buildSevTone(d.sev),
			Icon:   d.sev,
		})
	}
	return out
}

func buildSevTone(sev string) string {
	switch sev {
	case "error":
		return toneError
	case "warning":
		return toneWarning
	case "help":
		return toneAccent
	}
	return toneMuted
}

// buildCell is one styled table cell.
func buildCell(text, style string) lens.Cell {
	return lens.Cell{Spans: []lens.Span{{Text: text, Style: style}}}
}

// buildSwiftStatusTone tints a test result word.
func buildSwiftStatusTone(status string) string {
	switch status {
	case "failed":
		return toneError
	case "passed":
		return toneSuccess
	case "skipped":
		return toneMuted
	}
	return toneNeutral
}

// buildDiagCounts counts the errors and warnings of a diagnostic list.
func buildDiagCounts(items []buildDiagItem) (errors, warnings int) {
	for _, d := range items {
		switch d.sev {
		case "error":
			errors++
		case "warning":
			warnings++
		}
	}
	return errors, warnings
}

// buildMaxItems caps a diagnostic list; the rest is an Overflow.
const buildMaxItems = 50

func buildDiagnosticView(items []buildDiagItem) lens.Node {
	hidden := 0
	shown := items
	if len(shown) > buildMaxItems {
		hidden, shown = len(shown)-buildMaxItems, shown[:buildMaxItems]
	}
	// One diagnostic node per item, the unit Terminal's `flow.ui.diagnostic` draws
	// (severity icon + code badge, message, path:line:col, snippet, notes).
	nodes := make([]lens.Node, 0, len(shown))
	for _, it := range shown {
		nodes = append(nodes, lens.Diagnostic{
			Severity: it.sev,
			Code:     it.code,
			Message:  it.msg,
			Path:     it.path,
			Line:     it.line,
			Col:      it.col,
		})
	}
	children := []lens.Node{lens.Column{Children: nodes}}
	if hidden > 0 {
		children = append(children, lens.Overflow{Hidden: hidden})
	}
	errors, warnings := buildDiagCounts(items)
	var badges []lens.Node
	if errors > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(errors) + " errors", Tone: toneError})
	}
	if warnings > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(warnings) + " warnings", Tone: toneWarning})
	}
	if len(badges) > 0 {
		children = append(children, lens.Row{Children: badges})
	}
	return lens.Cols(children...)
}

func (b *buildDiag) View(state any) (lens.Node, error) {
	st, ok := state.(*buildDiagState)
	if !ok {
		return nil, errBadState
	}
	if len(st.items) == 0 {
		return nil, nil
	}
	return buildDiagnosticView(st.items), nil
}

// ---------------------------------------------------------------------------
// buildMakeOutput keeps make's own colours when it printed any: Terminal's
// `ansi(text)` mini terminal renders the captured escape stream, while plain
// output stays a code block.
func buildMakeOutput(st *buildMakeState) lens.Node {
	if len(st.styled) > 0 {
		return lens.ANSI{Text: strings.Join(st.styled, "\n")}
	}
	return lens.Code{Text: strings.Join(st.lines, "\n"), Lang: "make"}
}

// build.make — `make` keeps its output and reports the failing target.

type buildMake struct{}

type buildMakeState struct {
	lines  []string
	styled []string // lines that carried SGR escapes, for lens.ANSI
	target string
	failed bool
	marker bool
	status int
	ended  bool
}

func (m *buildMake) ID() string { return "build.make" }

func (m *buildMake) Match(cmd model.Cmd) bool {
	return cmd.Program == "make" || cmd.Program == "gmake"
}

func (m *buildMake) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &buildMakeState{}, nil }

func (m *buildMake) Line(state any, l model.Line) error {
	st, ok := state.(*buildMakeState)
	if !ok {
		return errBadState
	}
	if l.Styled != "" {
		st.styled = append(st.styled, l.Styled)
	}
	st.observe(l.Text)
	return nil
}

func (m *buildMake) Finish(state any, status int) error {
	if st, ok := state.(*buildMakeState); ok {
		st.status, st.ended = status, true
	}
	return nil
}

func (m *buildMake) Event(state any, ev lens.Event) error { return nil }

func (st *buildMakeState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	st.lines = append(st.lines, text)
	switch {
	case strings.HasPrefix(t, "make") || strings.HasPrefix(t, "gmake"):
		st.marker = true
		if i := strings.Index(t, "***"); i >= 0 {
			st.failed = true
			if st.target == "" {
				st.target = buildMakeTarget(t[i+3:])
			}
		}
	case strings.Contains(t, "Entering directory"), strings.Contains(t, "Leaving directory"),
		strings.Contains(t, "Nothing to be done"):
		st.marker = true
	case strings.HasPrefix(t, "[") && strings.Contains(t, "%]"): // CMake's "[ 50%] Building ..."
		st.marker = true
	case buildMakeRecipe(t):
		st.marker = true
	}
}

// buildMakeTarget pulls "all" out of "*** [Makefile:3: all] Error 1"; a message
// without a target in brackets ("No targets specified") yields no target.
func buildMakeTarget(s string) string {
	i := strings.IndexByte(s, '[')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], ']')
	if j <= 0 {
		return ""
	}
	body := s[i+1 : i+j]
	if k := strings.LastIndex(body, ": "); k >= 0 {
		return strings.TrimSpace(body[k+2:])
	}
	return strings.TrimSpace(body)
}

// buildMakeTools are the first words of a recipe line, from the mined tool
// table [149988267].
var buildMakeTools = map[string]bool{
	"cc": true, "gcc": true, "g++": true, "c++": true, "clang": true, "clang++": true,
	"rustc": true, "ld": true, "ar": true, "ranlib": true, "as": true, "strip": true,
	"go": true, "cargo": true, "cmake": true, "ninja": true, "install": true,
	"cp": true, "rm": true, "mkdir": true, "ln": true, "tar": true, "zip": true,
	"protoc": true, "flex": true, "bison": true, "yacc": true,
	"python": true, "python3": true, "node": true, "npm": true, "npx": true,
	"sh": true, "bash": true, "java": true, "javac": true,
}

func buildMakeRecipe(t string) bool {
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return false
	}
	return buildMakeTools[fields[0]]
}

func (m *buildMake) View(state any) (lens.Node, error) {
	st, ok := state.(*buildMakeState)
	if !ok {
		return nil, errBadState
	}
	if !st.marker || len(st.lines) == 0 {
		return nil, nil
	}
	tone, badge := toneSuccess, "ok"
	switch {
	case st.ended && st.status != 0:
		tone, badge = toneError, "exit "+strconv.Itoa(st.status)
	case st.failed:
		tone, badge = toneError, "failed"
	}
	children := []lens.Node{}
	if st.target != "" {
		children = append(children, lens.Text{Spans: []lens.Span{{Text: "target " + st.target, Style: toneError}}})
	}
	children = append(children, lens.Section{
		Head:     []lens.Span{{Text: "make", Style: "strong"}},
		Children: []lens.Node{buildMakeOutput(st)},
	})
	children = append(children, lens.Badge{Text: badge, Tone: tone})
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------------------
// build.swift — SwiftPM output, text or `--message-format json`.

type buildSwift struct{}

type buildSwiftCase struct {
	name   string
	status string
	took   string
}

type buildSwiftState struct {
	diagnostics []buildDiagItem
	steps       []string
	cases       []buildSwiftCase
	passed      int
	failed      int
	skipped     int
	issues      int
	sawAny      bool
}

// buildSwiftMsg is the `--message-format json` envelope [观察 149989072 for the
// flag; the field names are 推断 from SwiftPM's documented schema].
type buildSwiftMsg struct {
	Kind        string             `json:"kind"`
	Message     string             `json:"message"`
	Diagnostic  *buildSwiftReport  `json:"diagnostic"`
	Diagnostics []buildSwiftReport `json:"diagnostics"`
}

type buildSwiftReport struct {
	Behavior string `json:"behavior"`
	Level    string `json:"level"`
	Message  string `json:"message"`
	Location struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	} `json:"location"`
}

func (s *buildSwift) ID() string { return "build.swift" }

func (s *buildSwift) Match(cmd model.Cmd) bool { return cmd.Program == "swift" }

func (s *buildSwift) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &buildSwiftState{}, nil }

func (s *buildSwift) Line(state any, l model.Line) error {
	st, ok := state.(*buildSwiftState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (s *buildSwift) Finish(state any, status int) error { return nil }

func (s *buildSwift) Event(state any, ev lens.Event) error { return nil }

func (st *buildSwiftState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "{") {
		st.jsonLine(t)
		return
	}
	switch {
	case strings.HasPrefix(t, "Compiling "), strings.HasPrefix(t, "Linking "),
		strings.HasPrefix(t, "Fetching "), strings.HasPrefix(t, "Computing "),
		strings.HasPrefix(t, "Building "), strings.HasPrefix(t, "Build complete"),
		strings.HasPrefix(t, "Build succeeded"), strings.Contains(t, "Compiling "):
		st.steps = append(st.steps, t)
		st.sawAny = true
		return
	}
	if name, status, took, ok := buildSwiftCaseLine(t); ok {
		st.cases = append(st.cases, buildSwiftCase{name: name, status: status, took: took})
		st.count(status)
		st.sawAny = true
		return
	}
	if name, status, ok := buildSwiftTestingLine(t); ok {
		st.cases = append(st.cases, buildSwiftCase{name: name, status: status})
		st.count(status)
		st.sawAny = true
		return
	}
	if strings.Contains(t, "recorded an issue") {
		st.issues++
		st.sawAny = true
		return
	}
	if strings.HasPrefix(t, "Executed ") {
		if n := firstInt(strings.TrimPrefix(t, "Executed ")); n > 0 && st.passed+st.failed+st.skipped == 0 {
			st.passed = n
		}
		if i := strings.Index(t, "with "); i >= 0 {
			if n := firstInt(t[i+5:]); n > 0 {
				st.failed = n
			}
		}
		st.sawAny = true
		return
	}
	if d, ok := buildDiagParse(t); ok {
		st.diagnostics = append(st.diagnostics, d)
		st.sawAny = true
	}
}

func (st *buildSwiftState) count(status string) {
	switch status {
	case "passed":
		st.passed++
	case "failed":
		st.failed++
	case "skipped":
		st.skipped++
	}
}

func (st *buildSwiftState) jsonLine(t string) {
	var msg buildSwiftMsg
	if err := json.Unmarshal([]byte(t), &msg); err != nil {
		return
	}
	reports := msg.Diagnostics
	if msg.Diagnostic != nil {
		reports = append(reports, *msg.Diagnostic)
	}
	for _, r := range reports {
		sev := r.Level
		if sev == "" {
			sev = r.Behavior
		}
		switch sev {
		case "error", "warning", "note":
		case "remark":
			sev = "note"
		default:
			continue
		}
		if r.Message == "" {
			continue
		}
		st.diagnostics = append(st.diagnostics, buildDiagItem{
			sev: sev, path: r.Location.File, line: r.Location.Line, col: r.Location.Column, msg: r.Message,
		})
		st.sawAny = true
	}
	if len(reports) == 0 && msg.Kind == "diagnostic" && msg.Message != "" {
		st.diagnostics = append(st.diagnostics, buildDiagItem{sev: "error", msg: msg.Message})
		st.sawAny = true
	}
}

// buildSwiftCaseLine reads XCTest's "Test Case '-[Suite test]' passed (0.001 seconds)."
func buildSwiftCaseLine(t string) (name, status, took string, ok bool) {
	if !strings.HasPrefix(t, "Test Case '") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(t, "Test Case '")
	i := strings.IndexByte(rest, '\'')
	if i < 0 {
		return "", "", "", false
	}
	name, rest = rest[:i], strings.TrimSpace(rest[i+1:])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", "", "", false
	}
	switch fields[0] {
	case "passed", "failed", "skipped":
		status = fields[0]
	default:
		return "", "", "", false
	}
	if j := strings.IndexByte(rest, '('); j >= 0 {
		if k := strings.IndexByte(rest[j:], ')'); k > 0 {
			took = strings.TrimSpace(rest[j+1 : j+k])
		}
	}
	name = strings.Trim(name, "[]-")
	if strings.Contains(name, " ") {
		name = strings.ReplaceAll(name, " ", ".")
	}
	return name, status, took, true
}

// buildSwiftTestingLine reads swift-testing's
// "✔ Test foo() passed after 0.001 seconds."
func buildSwiftTestingLine(t string) (name, status string, ok bool) {
	for _, m := range []struct{ word, status string }{
		{" passed after ", "passed"},
		{" failed after ", "failed"},
		{" skipped after ", "skipped"},
	} {
		i := strings.Index(t, m.word)
		if i <= 0 {
			continue
		}
		name = strings.TrimSpace(strings.TrimLeft(t[:i], "✔✘✗◇•*"))
		name = strings.TrimSpace(strings.TrimPrefix(name, "Test"))
		if name == "" {
			return "", "", false
		}
		return name, m.status, true
	}
	return "", "", false
}

func (s *buildSwift) View(state any) (lens.Node, error) {
	st, ok := state.(*buildSwiftState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	var children []lens.Node
	if len(st.steps) > 0 {
		items := make([]lens.Item, 0, len(st.steps))
		for _, step := range st.steps {
			items = append(items, lens.Item{
				Label: []lens.Span{{Text: step, Style: toneMuted}},
				Tone:  toneNeutral,
			})
		}
		children = append(children, lens.List{Items: items})
	}
	if len(st.cases) > 0 {
		rows := make([][]lens.Cell, 0, len(st.cases))
		for _, c := range st.cases {
			rows = append(rows, []lens.Cell{
				buildCell(c.name, "path"),
				buildCell(c.status, buildSwiftStatusTone(c.status)),
				buildCell(c.took, toneMuted),
			})
		}
		children = append(children, lens.Table{
			Cols: []lens.Col{
				{ID: "test", Head: "TEST", Grow: 1},
				{ID: "status", Head: "STATUS"},
				{ID: "time", Head: "TIME", Priority: 1},
			},
			Rows: rows,
		})
	}
	var badges []lens.Node
	if st.failed > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.failed) + " failed", Tone: toneError})
	}
	if st.passed > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.passed) + " passed", Tone: toneSuccess})
	}
	if st.skipped > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.skipped) + " skipped", Tone: toneMuted})
	}
	errors, warnings := buildDiagCounts(st.diagnostics)
	if errors > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(errors) + " errors", Tone: toneError})
	}
	if warnings > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(warnings) + " warnings", Tone: toneWarning})
	}
	if st.issues > 0 {
		badges = append(badges, lens.Badge{Text: strconv.Itoa(st.issues) + " issues", Tone: toneWarning})
	}
	if len(st.diagnostics) > 0 {
		hidden := 0
		shown := st.diagnostics
		if len(shown) > buildMaxItems {
			hidden, shown = len(shown)-buildMaxItems, shown[:buildMaxItems]
		}
		children = append(children, lens.List{Items: buildDiagItems(shown)})
		if hidden > 0 {
			children = append(children, lens.Overflow{Hidden: hidden})
		}
	}
	if len(badges) > 0 {
		children = append(children, lens.Row{Children: badges})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}
