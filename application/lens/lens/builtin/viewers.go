package builtin

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// The rest of Terminal's Viewers family (0.9.14's lens catalog: view.follow "Followed
// logs" for tail -f, view.diff for diff -u, view.json "JSON and YAML" for jq/yq/gron,
// view.sql "Query results" for sqlite3/psql/mysql/duckdb and view.table "Tables" for
// column -t/csvlook). view.file lives in viewfile.go.
func init() {
	lens.Register(lens.Definition{ID: "view.follow", New: func() lens.Lens {
		return &textView{id: "view.follow", match: matchFollow, build: buildFileView}
	}})
	lens.Register(lens.Definition{ID: "view.diff", New: func() lens.Lens {
		return &textView{id: "view.diff", match: matchDiff, build: buildDiff, empty: true}
	}})
	lens.Register(lens.Definition{ID: "view.json", New: func() lens.Lens {
		return &textView{id: "view.json", match: matchJSON, build: buildJSON}
	}})
	lens.Register(lens.Definition{ID: "view.sql", New: func() lens.Lens {
		return &textView{id: "view.sql", match: matchSQL, build: buildSQLOutput, max: 100000}
	}})
	lens.Register(lens.Definition{ID: "view.table", New: func() lens.Lens {
		return &textView{id: "view.table", match: matchTable, build: buildTable, max: 100000}
	}})
}

// textView is a viewer that only needs the command and its output lines: the
// builder turns them into the node tree, and nil leaves the block raw.
type textView struct {
	id     string
	match  func(model.Cmd) bool
	build  func(cmd model.Cmd, lines []string) lens.Node
	cmd    model.Cmd
	lines  []string
	hidden int
	// max bounds the collected lines. Table viewers keep more so the 5000-row
	// clamp can count its overflow; the others stop at maxFileLines.
	max int
	// empty builds a view even when the command printed nothing (a diff with no
	// changes still shows Terminal's "No changes").
	empty bool
}

func (v *textView) ID() string { return v.id }

func (v *textView) Match(cmd model.Cmd) bool { return v.match(cmd) }

func (v *textView) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	return &textView{id: v.id, match: v.match, build: v.build, cmd: cmd, max: v.max, empty: v.empty}, nil
}

func (v *textView) Line(st any, ln model.Line) error {
	s, ok := st.(*textView)
	if !ok {
		return errBadState
	}
	limit := s.max
	if limit <= 0 {
		limit = maxFileLines
	}
	if len(s.lines) < limit {
		s.lines = append(s.lines, ln.Text)
		return nil
	}
	s.hidden++
	return nil
}

func (v *textView) Finish(any, int) error { return nil }

func (v *textView) Event(any, lens.Event) error { return nil }

func (v *textView) View(st any) (lens.Node, error) {
	s, ok := st.(*textView)
	if !ok {
		return nil, errBadState
	}
	if len(s.lines) == 0 && !s.empty {
		return nil, nil
	}
	node := s.build(s.cmd, s.lines)
	if node == nil {
		return nil, nil
	}
	if s.hidden > 0 {
		node = lens.Cols(node, lens.Overflow{Hidden: s.hidden})
	}
	return node, nil
}

// --- matchers ---------------------------------------------------------------

func program(cmd model.Cmd) string {
	p := filepath.Base(strings.ReplaceAll(cmd.Program, "\\", "/"))
	return strings.ToLower(strings.TrimSuffix(p, ".exe"))
}

func matchFollow(cmd model.Cmd) bool {
	return program(cmd) == "tail" && hasFollowFlag(cmd.Args)
}

// hasFollowFlag is Terminal's split between `tail file` (a file snapshot) and
// `tail -f file` (a followed log).
func hasFollowFlag(args []string) bool {
	for _, a := range args {
		if a == "-f" || a == "-F" || a == "--follow" || strings.HasPrefix(a, "--follow=") {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "f") {
			return true
		}
	}
	return false
}

// matchDiff follows the catalog's `diff -u`: only a unified diff is claimed (a plain
// diff prints "2c2" hunks the side-by-side view cannot place).
func matchDiff(cmd model.Cmd) bool {
	if program(cmd) != "diff" {
		return false
	}
	for _, a := range cmd.Args {
		if a == "--unified" || strings.HasPrefix(a, "--unified=") ||
			a == "-u" || (strings.HasPrefix(a, "-U") && len(a) > 2) ||
			(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "u")) {
			return true
		}
	}
	return false
}

func matchJSON(cmd model.Cmd) bool {
	switch program(cmd) {
	case "jq", "yq", "gron":
		return true
	}
	return false
}

func matchSQL(cmd model.Cmd) bool {
	switch program(cmd) {
	case "sqlite3", "psql", "mysql", "duckdb":
		return true
	}
	return false
}

func matchTable(cmd model.Cmd) bool {
	switch program(cmd) {
	case "csvlook":
		return true
	case "column":
		for _, a := range cmd.Args {
			if a == "-t" || a == "--table" {
				return true
			}
		}
	}
	return false
}

// --- builders ---------------------------------------------------------------

func buildFileView(cmd model.Cmd, lines []string) lens.Node {
	if len(lines) == 0 {
		return nil
	}
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return fileNode(lastPathArg(cmd.Args), text)
}

func buildDiff(_ model.Cmd, lines []string) lens.Node {
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		// `diff -u` on identical files prints nothing; Terminal still shows the diff
		// surface with its "No changes" line.
		return lens.Diff{}
	}
	// Only a unified patch is a diff view; `diff` without -u prints "2c2" hunks
	// that the side-by-side view cannot place.
	if !strings.Contains(text, "\n@@") && !strings.HasPrefix(text, "@@") &&
		!(strings.HasPrefix(text, "--- ") && strings.Contains(text, "\n+++ ")) {
		return nil
	}
	add, del := diffCounts(text)
	return lens.Diff{Text: text, Path: diffTargetPath(text), Added: add, Removed: del}
}

// diffCounts counts the added and removed lines of a unified patch, the numbers
// Terminal's diff head shows after the path (`+N`/`−N`, U+2212).
func diffCounts(text string) (added, removed int) {
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "+++ ") || strings.HasPrefix(ln, "--- ") {
			continue
		}
		if strings.HasPrefix(ln, "+") {
			added++
		}
		if strings.HasPrefix(ln, "-") {
			removed++
		}
	}
	return
}

// diffTargetPath is the file a unified patch changes: the `+++ b/…` line, with
// the b/ prefix dropped, so the diff head and grammar name that file rather than
// the .diff file that holds the patch.
func diffTargetPath(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "+++ ") {
			p := strings.TrimSpace(strings.TrimPrefix(ln, "+++ "))
			p = strings.TrimPrefix(p, "b/")
			if p != "" && p != "/dev/null" {
				return p
			}
		}
	}
	return ""
}

func buildJSON(cmd model.Cmd, lines []string) lens.Node {
	text := strings.Join(lines, "\n")
	if n, ok := lens.JSONTree(text); ok {
		return n
	}
	switch program(cmd) {
	case "yq":
		if n, ok := yamlTree(text); ok {
			return n
		}
	case "gron":
		if n, ok := gronTree(text); ok {
			return n
		}
	}
	return nil
}

// parseTable reads the table shapes the viewers' programs print: `column -t`'s
// space-aligned columns, `csvlook`'s pipe rows, psql's `|` columns over a `--+--`
// rule, mysql's `+---+` boxes and sqlite3's bare `a|b` rows.
func buildTable(_ model.Cmd, lines []string) lens.Node {
	t, ok := tableFrom(lines)
	if !ok {
		return nil
	}
	// Terminal clamps a table at 5000 rows and appends the overflow line
	// ("… N more (Raw shows everything)"): FUN_143d3e4e0 passes a table with
	// n < 0x1389 (5001) through and otherwise wraps it with n - 5000.
	if len(t.Rows) > maxTableRows {
		hidden := len(t.Rows) - maxTableRows
		t.Rows = t.Rows[:maxTableRows]
		return lens.Cols(t, lens.Overflow{Hidden: hidden})
	}
	return t
}

// maxTableRows is Terminal's table cap (0x1389 - 1 in FUN_143d3e4e0).
const maxTableRows = 5000

// buildSQLOutput is Terminal's view.sql composition (0x143D393B0): a JSON result is
// handed to parse.json_tree (psql/duckdb --json), anything else is parsed as a
// table.
func buildSQLOutput(cmd model.Cmd, lines []string) lens.Node {
	// Only a JSON document (an object or array, as psql/duckdb --json print) is a
	// tree; a bare number would otherwise turn a one-column pipe table into JSON.
	if t := strings.TrimSpace(strings.Join(lines, "\n")); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if n, ok := lens.JSONTree(t); ok {
			return n
		}
	}
	return buildTable(cmd, lines)
}

var spaceCols = regexp.MustCompile(`\s{2,}`)

func tableFrom(lines []string) (lens.Table, bool) {
	rows := make([][]string, 0, len(lines))
	headerAt := -1
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || isFooter(t) {
			continue
		}
		if isRule(t) {
			// The first rule ends the header row; later rules separate rows
			// (mysql draws one above and below every row).
			if headerAt < 0 && len(rows) > 0 {
				headerAt = len(rows) - 1
			}
			continue
		}
		var cells []string
		switch {
		case strings.Contains(t, "|"):
			raw := strings.Trim(t, "|")
			for _, c := range strings.Split(raw, "|") {
				cells = append(cells, strings.TrimSpace(c))
			}
		default:
			cells = spaceCols.Split(t, -1)
		}
		if len(cells) < 2 {
			return lens.Table{}, false
		}
		rows = append(rows, cells)
	}
	if len(rows) < 2 {
		return lens.Table{}, false
	}
	body := rows
	if headerAt < 0 && looksLikeHeader(rows[0]) {
		headerAt = 0
	}
	var head []string
	if headerAt >= 0 {
		head, body = rows[headerAt], rows[headerAt+1:]
	}
	if len(body) == 0 {
		return lens.Table{}, false
	}
	width := len(head)
	for _, r := range body {
		if len(r) > width {
			width = len(r)
		}
	}
	cols := make([]lens.Col, width)
	for i := range cols {
		name := ""
		if i < len(head) {
			name = head[i]
		}
		cols[i] = lens.Col{ID: name, Head: name}
		if allNumeric(body, i) {
			cols[i].Align = lens.AlignEnd
		}
	}
	t := lens.Table{Cols: cols}
	for _, r := range body {
		cells := make([]lens.Cell, width)
		for i := range cells {
			text := ""
			if i < len(r) {
				text = r[i]
			}
			cells[i] = lens.Cell{Spans: []lens.Span{{Text: text}}}
		}
		t.Rows = append(t.Rows, cells)
	}
	return t, true
}

// isFooter skips the "\(2 rows\)" line psql and mysql append.
func isFooter(t string) bool {
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		return true
	}
	return strings.HasSuffix(t, "rows in set") || strings.HasSuffix(t, "row in set")
}

// looksLikeHeader keeps a headerless listing (sqlite3's bare a|b rows) from being
// read with its first row as the head.
func looksLikeHeader(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" && isNumber(strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

// isRule reports a table rule: psql's "-----+-----", mysql's "+---+" or markdown's
// "| --- |".
func isRule(t string) bool {
	if !strings.ContainsAny(t, "-") {
		return false
	}
	for _, r := range t {
		switch r {
		case '-', '=', '+', '|', ':', ' ':
		default:
			return false
		}
	}
	return true
}

func allNumeric(rows [][]string, col int) bool {
	seen := false
	for _, r := range rows {
		if col >= len(r) || strings.TrimSpace(r[col]) == "" {
			continue
		}
		seen = true
		if !isNumber(strings.TrimSpace(r[col])) {
			return false
		}
	}
	return seen
}

func isNumber(s string) bool {
	s = strings.TrimSuffix(strings.TrimSuffix(s, "%"), "ms")
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return false
	}
	return true
}
