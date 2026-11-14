package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

// rust family: rust.test, rust.cargo.
//
// Mined from payload\flow.exe (Terminal 0.9.4) per findings/lens-conditions.md §1.1
// (offset 149989136, S:783350-783363): the literal cluster holds
// "Compiling", "Finished", "Building", "Failed", "Compiled", "Doc-tests",
// "future-incompatibilities", "error:", "warning:" plus cargo test's
// "running N tests", "test foo ... ok|FAILED", "test result: ok. 3 passed;
// 0 failed; 0 ignored".                                                     【观察】
// The program name `cargo` is in the build-tool name table (S:783366) rather
// than in the lens region, so matching it is 【推断】 per docs/lens-parity.md
// rule 2.
// View components follow findings/lens-widgets.md §1.3: test summary →
// TestSummary, diagnostics → List of styled Text, compiling → Progress,
// status words → Badge, failure details → Code.

func init() {
	lens.Register(lens.Definition{ID: "rust.test", New: func() lens.Lens { return &rustTest{} }})
	lens.Register(lens.Definition{ID: "rust.cargo", New: func() lens.Lens { return &rustCargo{} }})
}

// cargoFlagValue reports whether a cargo flag consumes the next token.
func cargoFlagValue(flag string) bool {
	switch flag {
	case "-p", "--package", "-F", "--features", "--target", "--profile",
		"-j", "--jobs", "--manifest-path", "--config", "-Z", "--example",
		"--bin", "--message-format", "-C", "--color", "--target-dir":
		return true
	}
	return false
}

// cargoVerb returns cargo's subcommand, skipping the toolchain selector
// ("+nightly") and flags.                                                    【推断】
func cargoVerb(cmd model.Cmd) (string, bool) {
	if cmd.Program != "cargo" {
		return "", false
	}
	for i := 0; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "+") {
			continue
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && cargoFlagValue(a) && i+1 < len(cmd.Args) {
				i++
			}
			continue
		}
		return a, true
	}
	return "", true
}

// rustLocation reads a compiler location line " --> src/main.rs:3:5".
func rustLocation(t string) (file string, line, col int, ok bool) {
	i := strings.Index(t, "--> ")
	if i < 0 {
		return "", 0, 0, false
	}
	// The location is the same `path:line[:col]` shape every other lens reads, so
	// one parser handles it (including a Windows drive colon).
	file, line, col, ok = parse.Location(strings.TrimSpace(t[i+4:]))
	if !ok || line <= 0 {
		return "", 0, 0, false
	}
	return file, line, col, true
}

// rustDiagnostic reads a rustc diagnostic head ("error[E0308]: …",
// "warning: …", "note: …", "help: …").                                       【观察】
func rustDiagnostic(t string) (rustDiag, bool) {
	var sev string
	switch {
	case strings.HasPrefix(t, "error["), strings.HasPrefix(t, "error:"):
		sev = "error"
	case strings.HasPrefix(t, "warning["), strings.HasPrefix(t, "warning:"):
		sev = "warning"
	case strings.HasPrefix(t, "note:"):
		sev = "note"
	case strings.HasPrefix(t, "help:"):
		sev = "help"
	default:
		return rustDiag{}, false
	}
	d := rustDiag{severity: sev}
	rest := t
	if i := strings.IndexByte(rest, '['); i >= 0 {
		if j := strings.IndexByte(rest[i:], ']'); j > 0 {
			d.code = rest[i+1 : i+j]
			rest = rest[i+j+1:]
		}
	} else if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[i+1:]
	}
	d.message = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":"))
	return d, true
}

// -------------------------------------------------------------------- rust.test

// rustTest turns `cargo test` into a TestSummary plus the failing tests and
// their captured output.
type rustTest struct{}

type rustTestResult struct {
	name   string
	status string
}

type rustTestState struct {
	running    int
	passed     int
	failed     int
	ignored    int
	results    []rustTestResult
	details    []string
	finished   string
	summary    bool
	inFailures bool
	saw        bool
}

func (r *rustTest) ID() string { return "rust.test" }

func (r *rustTest) Match(cmd model.Cmd) bool {
	v, ok := cargoVerb(cmd)
	return ok && v == "test"
}

func (r *rustTest) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &rustTestState{}, nil }

func (r *rustTest) Line(state any, l model.Line) error {
	st, ok := state.(*rustTestState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (r *rustTest) Finish(state any, status int) error { return nil }

func (r *rustTest) Event(state any, ev lens.Event) error { return nil }

func (st *rustTestState) result(name, status string) {
	for i := range st.results {
		if st.results[i].name == name {
			if st.results[i].status == "" {
				st.results[i].status = status
			}
			return
		}
	}
	st.results = append(st.results, rustTestResult{name: name, status: status})
}

// observe splits the line at the markers cargo prints first: ConPTY drops some
// newlines on Windows, so lines arrive glued ("… FAILEDfailures:---- tests::x
// stdout ----…").
func (st *rustTestState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	for _, part := range rustSplitMarkers(t) {
		st.observeOne(part)
	}
}

// rustSplitMarkers splits a glued line at the boundaries of cargo test's
// messages, keeping the marker with the part it starts.
func rustSplitMarkers(t string) []string {
	markers := []string{"test result:", "failures:", "---- ", "error: test failed", "running "}
	parts := []string{t}
	for _, m := range markers {
		var next []string
		for _, part := range parts {
			for {
				i := strings.Index(part[1:], m)
				if i <= 0 {
					break
				}
				i++
				next = append(next, strings.TrimSpace(part[:i]))
				part = strings.TrimSpace(part[i:])
			}
			next = append(next, part)
		}
		parts = next
	}
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{t}
	}
	return out
}

func (st *rustTestState) observeOne(t string) {
	switch {
	case strings.HasPrefix(t, "running ") && strings.Contains(t, " test"):
		st.running, st.saw = firstInt(strings.TrimPrefix(t, "running ")), true
		return
	case strings.HasPrefix(t, "test result:"):
		if p, f, i, took, ok := rustParseResult(t); ok {
			st.passed, st.failed, st.ignored, st.finished = p, f, i, took
			st.summary, st.saw = true, true
		}
		return
	case strings.HasPrefix(t, "test "):
		rest := strings.TrimPrefix(t, "test ")
		name, status, ok := strings.Cut(rest, " ... ")
		if !ok {
			return
		}
		name, status = strings.TrimSpace(name), strings.TrimSpace(status)
		if i := strings.IndexAny(status, " \t"); i > 0 {
			status = status[:i]
		}
		st.saw = true
		// The status may carry glued text ("FAILEDfailures:…"), so match its
		// start rather than requiring the whole token.
		switch {
		case strings.HasPrefix(status, "ok"):
			st.passed++
		case strings.HasPrefix(status, "FAILED"):
			st.failed++
			st.result(name, "FAILED")
		case strings.HasPrefix(status, "ignored"):
			st.ignored++
		}
		return
	case strings.HasPrefix(t, "Doc-tests"):
		st.saw = true
		return
	case strings.HasPrefix(t, "failures:"):
		st.inFailures, st.saw = true, true
		return
	case strings.HasPrefix(t, "---- ") && strings.HasSuffix(t, " stdout ----"):
		st.inFailures, st.saw = true, true
		st.details = append(st.details, t)
		return
	}
	if st.inFailures {
		switch {
		case strings.HasPrefix(t, "test result:"), strings.HasPrefix(t, "failures:"):
			return
		case !strings.Contains(t, " "):
			// A bare name under "failures:" is one failing test.
			st.result(strings.TrimSpace(t), "FAILED")
			return
		default:
			st.details = append(st.details, t)
			return
		}
	}
}

// rustParseResult reads "test result: ok. 3 passed; 0 failed; 0 ignored; …
// finished in 0.01s".                                                        【观察】
func rustParseResult(t string) (passed, failed, ignored int, took string, ok bool) {
	body := strings.TrimPrefix(t, "test result:")
	for _, part := range strings.Split(body, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		switch {
		case strings.Contains(part, "passed"):
			passed, ok = firstInt(part), true
		case strings.Contains(part, "failed"):
			failed, ok = firstInt(part), true
		case strings.Contains(part, "ignored"):
			ignored, ok = firstInt(part), true
		case strings.Contains(part, "finished in"):
			if i := strings.Index(part, "finished in"); i >= 0 {
				took = strings.TrimSpace(part[i+len("finished in"):])
			}
		}
	}
	return passed, failed, ignored, took, ok
}

func (r *rustTest) View(state any) (lens.Node, error) {
	st, ok := state.(*rustTestState)
	if !ok {
		return nil, errBadState
	}
	if !st.saw {
		return nil, nil
	}
	passed, failed, ignored := st.passed, st.failed, st.ignored
	if !st.summary && passed == 0 && failed == 0 && ignored == 0 {
		passed, failed, ignored = 0, 0, 0
		for _, res := range st.results {
			switch res.status {
			case "ok":
				passed++
			case "FAILED":
				failed++
			case "ignored":
				ignored++
			}
		}
	}
	children := []lens.Node{lens.TestSummary{
		Passed: passed, Failed: failed, Skipped: ignored, Took: st.finished,
	}}
	var items []lens.Item
	for _, res := range st.results {
		if res.status != "FAILED" {
			continue
		}
		items = append(items, lens.Item{
			Label: []lens.Span{{Text: res.name, Style: "strong " + toneError}},
			Tone:  toneError,
		})
	}
	if len(items) > 0 {
		children = append(children, lens.List{Items: items})
	}
	if len(st.details) > 0 {
		children = append(children, lens.Code{
			Text: strings.Join(st.details, "\n"), Lang: "text", Clamp: 200,
		})
	}
	return lens.Cols(children...), nil
}

// ------------------------------------------------------------------- rust.cargo

// rustCargo turns a cargo build/check run into a card: the crate being built,
// its diagnostics and the compiling progress.
type rustCargo struct{}

type rustDiag struct {
	severity string
	code     string
	message  string
	file     string
	line     int
	col      int
}

type rustCargoState struct {
	crates    []string
	diags     []rustDiag
	tree      []string // `cargo tree` lines, rendered as a Tree
	verb      string
	done      int
	expected  int
	finished  string
	compiling bool
	failed    bool
	saw       bool
}

func (r *rustCargo) ID() string { return "rust.cargo" }

func (r *rustCargo) Match(cmd model.Cmd) bool {
	v, ok := cargoVerb(cmd)
	return ok && v != "" && v != "test" && v != "tree"
}

func (r *rustCargo) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	v, _ := cargoVerb(cmd)
	return &rustCargoState{verb: v}, nil
}

func (r *rustCargo) Line(state any, l model.Line) error {
	st, ok := state.(*rustCargoState)
	if !ok {
		return errBadState
	}
	if st.verb == "tree" {
		// `cargo tree` prints a box-drawing dependency tree; Terminal's `tree`
		// component is the view for it.
		if strings.TrimSpace(l.Text) != "" {
			st.tree = append(st.tree, l.Text)
		}
		return nil
	}
	st.observe(l.Text)
	return nil
}

func (r *rustCargo) Finish(state any, status int) error { return nil }

func (r *rustCargo) Event(state any, ev lens.Event) error { return nil }

func (st *rustCargoState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, "Compiling "), strings.HasPrefix(t, "Checking "),
		strings.HasPrefix(t, "Documenting "), strings.HasPrefix(t, "Fresh "):
		fields := strings.Fields(t)
		if len(fields) >= 2 {
			name := fields[1]
			dup := false
			for _, c := range st.crates {
				if c == name {
					dup = true
					break
				}
			}
			if !dup {
				st.crates = append(st.crates, name)
			}
		}
		st.compiling, st.saw = true, true
		return
	case strings.HasPrefix(t, "Compiled "):
		st.done++
		st.saw = true
		return
	case strings.HasPrefix(t, "Building ["):
		if nums := parseFloats(t); len(nums) >= 2 {
			st.done, st.expected = int(nums[0]), int(nums[1])
		}
		st.compiling, st.saw = true, true
		return
	case strings.HasPrefix(t, "Finished "):
		st.compiling, st.saw = false, true
		if i := strings.LastIndex(t, " in "); i >= 0 {
			st.finished = strings.TrimSpace(t[i+4:])
		}
		return
	case strings.HasPrefix(t, "Failed to compile"), strings.HasPrefix(t, "error: could not compile"):
		st.failed, st.saw = true, true
		return // the closing "could not compile" line is not a diagnostic of its own
	}
	if d, ok := rustDiagnostic(t); ok {
		st.diags = append(st.diags, d)
		st.saw = true
		if d.severity == "error" {
			st.failed = true
		}
		return
	}
	if file, line, col, ok := rustLocation(t); ok && len(st.diags) > 0 {
		last := &st.diags[len(st.diags)-1]
		if last.file == "" {
			last.file, last.line, last.col = file, line, col
		}
	}
}

func (r *rustCargo) View(state any) (lens.Node, error) {
	st, ok := state.(*rustCargoState)
	if !ok {
		return nil, errBadState
	}
	if st.verb == "tree" && len(st.tree) > 0 {
		if node, ok := rustCargoTree(st.tree); ok {
			return node, nil
		}
	}
	if !st.saw {
		return nil, nil
	}
	head := "cargo"
	lastCrate := ""
	if len(st.crates) > 0 {
		lastCrate = st.crates[len(st.crates)-1]
		head = lastCrate
		if n := len(st.crates) - 1; n > 0 {
			head += " +" + strconv.Itoa(n) + " more"
		}
	}
	badge := lens.Badge{Text: "compiling", Tone: toneAccent}
	switch {
	case st.failed:
		badge = lens.Badge{Text: "failed", Tone: toneError}
	case st.finished != "":
		badge = lens.Badge{Text: "finished", Tone: toneSuccess}
	}
	children := []lens.Node{badge}
	if st.expected > 0 {
		children = append(children, lens.Progress{
			Value: float64(st.done) / float64(st.expected),
			Known: true,
			Label: []lens.Span{{Text: strconv.Itoa(st.done) + "/" + strconv.Itoa(st.expected) + " crates"}},
		})
	} else if lastCrate != "" && st.compiling {
		children = append(children, lens.Progress{
			Known: false,
			Label: []lens.Span{{Text: "compiling " + lastCrate}},
		})
	}
	if len(st.diags) > 0 {
		// One diagnostic node per rustc diagnostic, Terminal's `flow.ui.diagnostic`.
		nodes := make([]lens.Node, 0, len(st.diags))
		for _, d := range st.diags {
			nodes = append(nodes, lens.Diagnostic{
				Severity: d.severity,
				Code:     d.code,
				Message:  d.message,
				Path:     d.file,
				Line:     d.line,
				Col:      d.col,
			})
		}
		children = append(children, lens.Column{Children: nodes})
	}
	return lens.Card{
		Head:     []lens.Span{{Text: head, Style: "strong"}},
		Tone:     toneAccent,
		Children: children,
	}, nil
}

// rustCargoTree turns `cargo tree` output into Terminal's tree component. Depth is
// the number of four-cell indent prefixes before the crate ("|   " or four
// spaces, with the branch marker "\- " / "|- " itself not counted), so nested
// dependencies nest in the view too.
func rustCargoTree(lines []string) (lens.Node, bool) {
	const (
		vert   = "\u2502   "           // bar + three spaces
		blank  = "    "                // four spaces
		branch = "\u251c\u2500\u2500 " // tee
		last   = "\u2514\u2500\u2500 " // elbow
	)
	type entry struct {
		depth int
		label []lens.Span
	}
	entries := make([]entry, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		depth := 0
		// The markers are three-byte UTF-8 characters, so cut by byte length of
		// the matched prefix (a fixed slice of four would split a character).
		for {
			cut, isIndent := 0, false
			switch {
			case strings.HasPrefix(line, vert):
				cut, isIndent = len(vert), true
			case strings.HasPrefix(line, blank):
				cut, isIndent = len(blank), true
			case strings.HasPrefix(line, branch):
				cut = len(branch)
			case strings.HasPrefix(line, last):
				cut = len(last)
			}
			if cut == 0 {
				break
			}
			// Each indent block and the branch marker itself is one nesting level.
			_ = isIndent
			depth++
			line = line[cut:]
		}
		label := strings.TrimSpace(line)
		if label == "" {
			continue
		}
		entries = append(entries, entry{depth: depth, label: rustCrateSpans(label)})
	}
	if len(entries) == 0 {
		return nil, false
	}
	// Build the tree from the depth stack.
	var nodes []lens.TreeNode
	type frame struct {
		node  *lens.TreeNode
		depth int
	}
	stack := []frame{}
	for _, e := range entries {
		tn := lens.TreeNode{Label: e.label, Open: true}
		for len(stack) > 0 && stack[len(stack)-1].depth >= e.depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			nodes = append(nodes, tn)
			stack = append(stack, frame{node: &nodes[len(nodes)-1], depth: e.depth})
			continue
		}
		parent := stack[len(stack)-1].node
		parent.Children = append(parent.Children, tn)
		n := len(parent.Children) - 1
		stack = append(stack, frame{node: &parent.Children[n], depth: e.depth})
	}
	return lens.Tree{Nodes: nodes}, true
}

// rustCrateSpans styles a tree row: the crate name, then its version muted.
func rustCrateSpans(label string) []lens.Span {
	if v := strings.LastIndex(label, " v"); v > 0 {
		return []lens.Span{
			{Text: label[:v], Style: "strong"},
			{Text: label[v:], Style: "muted"},
		}
	}
	return []lens.Span{{Text: label, Style: "strong"}}
}
