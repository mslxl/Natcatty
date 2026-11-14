package builtin

// search.grep — grep and ripgrep output as a table of matches.
//
// Mined from payload\flow.exe (lens-conditions.md §1.1, evidence read from the
// constants region and re-enumerated here):
//
//	149981272  "search.grep-C--git-dir--work-tree--namespace"   the id, glued
//	           with -C and the git passthrough flags                【观察】
//	149981144  "--nonumbers--no-numbers"
//	149981200  "-n--line-number"
//	149981248  "--column@"
//	149981405  "No matchesfilematchmatches"    the empty state and the words
//	           the summary counts (file/match/matches)                【观察】
//	149981442  "pathcounts"                    column words             【观察】
//	149981464  "-t-A-B-m-M-j-d-D-E--glob--iglob--type--type-not--type-add
//	           --after-context--before-context--context--max-count
//	           --max-columns--max-depth--threads--replace--color--c…"
//	           ripgrep's flag table                                     【观察】
//	B:149978488 "cannot compile the search pattern" (S:783301): the lens
//	           exists, so a bad pattern is a recognized failure        【观察】
//
// The view is a Card whose head is the pattern and the counts, with a Table of
// path/line/column/match (path grows, columns appear when the output has
// them); a run with no matches shows a muted "No matches" badge, and a
// `grep -c` style run shows path/count rows. Terminal's parser is code we cannot
// read, so the table shape is 【推断】 from those column words.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:  "search.grep",
		New: func() lens.Lens { return &searchGrep{} },
	})
}

type searchGrep struct{}

// grepHit is one output line: a match (context false) or a -A/-B/-C context
// line (context true), whose separator in the output is '-' rather than ':'.
type grepHit struct {
	path, text string
	line, col  int
	context    bool
}

type grepState struct {
	pattern string
	hits    []grepHit
	matched int            // hits that are matches, not -A/-B/-C context lines
	counts  map[string]int // grep -c: path -> count
	stats   map[string]int // ripgrep's "N matches" summary lines
	files   map[string]bool
	notes   []string // the tools' own diagnostics (rg: path: message)
	file    string   // the file the current hits belong to
	heading string   // a file line waiting for a hit to prove it
	// fileFromHeading tells that file came from a --heading line rather than
	// from the command's own file operand.
	fileFromHeading bool
	sawPos          bool // a path:line:… line was seen, so bare path:text is trusted
	any             bool
}

func (g *searchGrep) ID() string { return "search.grep" }

func (g *searchGrep) Match(cmd model.Cmd) bool {
	switch cmd.Program {
	case "grep", "egrep", "fgrep", "rg", "ripgrep":
		return true
	}
	return false
}

func (g *searchGrep) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &grepState{
		pattern: grepPattern(cmd.Args),
		file:    grepDefaultFile(cmd.Args), // single-file runs print no name
		counts:  map[string]int{},
		stats:   map[string]int{},
		files:   map[string]bool{},
	}, nil
}

func (g *searchGrep) Line(state any, l model.Line) error {
	st, ok := state.(*grepState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (g *searchGrep) Finish(state any, status int) error { return nil }

func (g *searchGrep) Event(state any, ev lens.Event) error { return nil }

// grepPattern is the first operand that is not a flag; a flag that takes a
// value (-C 2, --glob '*.go', -e PATTERN) skips its argument too.
func grepPattern(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "":
			continue
		case a == "-e" || a == "--regexp" || a == "-f" || a == "--file":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(a, "-"):
			if grepFlagTakesValue(a) {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// grepFlagTakesValue reports whether a grep/ripgrep flag consumes the next
// operand (the value-taking flags of the tables in the lens region).
func grepFlagTakesValue(a string) bool {
	if strings.HasPrefix(a, "--") {
		if strings.Contains(a, "=") {
			return false // --glob=*.go carries its own value
		}
		switch a {
		case "--after-context", "--before-context", "--context", "--max-count",
			"--max-columns", "--max-depth", "--threads", "--replace", "--color",
			"--glob", "--iglob", "--type", "--type-not", "--type-add",
			"--encoding", "--pre", "--sort", "--engine", "--context-separator":
			return true
		}
		return false
	}
	if len(a) < 2 {
		return false
	}
	switch a[len(a)-1] {
	case 'A', 'B', 'C', 'm', 'M', 't', 'j', 'd', 'D', 'E', 'e', 'f', 'g', 'r', 'P':
		return true
	}
	return false
}

func (st *grepState) observe(text string) {
	s := strings.TrimRight(text, "\r")
	if s == "" || s == "--" {
		return
	}
	// The tools' own diagnostics ("rg: b.txt: The system cannot find the
	// file specified. (os error 2)") are not matches.
	if note, ok := grepNoteLine(s); ok {
		st.notes = append(st.notes, note)
		st.heading = ""
		st.any = true
		return
	}
	if s == "No matches" || strings.HasPrefix(s, "No matches") {
		st.heading = ""
		st.any = true
		return
	}
	if n, word, ok := grepStatLine(s); ok {
		st.stats[word] = n
		st.heading = ""
		st.any = true
		return
	}
	if strings.HasPrefix(s, "Binary file ") && strings.HasSuffix(s, " matches") {
		path := strings.TrimSuffix(strings.TrimPrefix(s, "Binary file "), " matches")
		st.addHit(grepHit{path: path, text: "binary file matches"})
		return
	}
	// ripgrep -c under --heading: the path line, then the count alone.
	if st.heading != "" && allDigits(strings.TrimSpace(s)) {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		st.counts[st.heading] = n
		st.files[st.heading] = true
		st.file, st.fileFromHeading, st.heading = st.heading, true, ""
		st.any = true
		return
	}
	// grep -c: "path:12" with nothing after the count.
	if p, rest, ok := grepSplitPath(s); ok && allDigits(strings.TrimSpace(rest)) {
		n, _ := strconv.Atoi(strings.TrimSpace(rest))
		st.counts[p] = n
		st.files[p] = true
		st.heading = ""
		st.any = true
		return
	}
	// ripgrep on a TTY prints a file name on its own line (--heading), and
	// with a single file it prints none: the hits are "N:text" or "N:C:text".
	if line, col, body, ok := grepPositionOnly(s); ok {
		hit := grepHit{path: st.file, line: line, col: col, text: body}
		if st.heading != "" {
			st.file, st.fileFromHeading, st.heading = st.heading, true, ""
			hit.path = st.file
		}
		st.sawPos = true
		st.addHit(hit)
		return
	}
	hit, ok := grepParseHit(s)
	if !ok {
		// A bare path line is the heading of the hits below it; it only
		// becomes the current file once a hit proves it, so that unrelated
		// output still falls back to raw.
		if grepHeadingCandidate(s, st) {
			st.heading = s
			return
		}
		st.heading = ""
		return
	}
	if hit.line > 0 {
		st.sawPos = true
	}
	if !st.sawPos && hit.line == 0 && !grepLooksLikePath(hit.path) {
		return // `error: something` must not read as a match
	}
	st.heading = ""
	st.addHit(hit)
}

// addHit records one match or context line.
func (st *grepState) addHit(h grepHit) {
	st.hits = append(st.hits, h)
	if !h.context {
		if h.path != "" {
			st.files[h.path] = true
		}
		st.matched++
	}
	st.any = true
}

// grepNoteLine recognizes the tools' own diagnostics, which are not matches.
func grepNoteLine(s string) (string, bool) {
	for _, prefix := range []string{"rg: ", "grep: ", "egrep: ", "fgrep: ", "ripgrep: "} {
		if strings.HasPrefix(s, prefix) {
			return s, true
		}
	}
	return "", false
}

// grepHeadingCandidate reports whether a line is ripgrep's --heading file
// line: no separator, and a path (or a bare name once this run has shown one).
func grepHeadingCandidate(s string, st *grepState) bool {
	if s == "" || strings.ContainsRune(s, ':') || strings.HasPrefix(s, "-") {
		return false
	}
	if grepLooksLikePath(s) {
		return true
	}
	return st.fileFromHeading || len(st.hits) > 0 || st.heading != ""
}

// grepPositionOnly reads the "N:text" and "N:C:text" forms ripgrep prints
// when the file name is on its own line (or absent).
func grepPositionOnly(s string) (line, col int, text string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != ':' {
		return 0, 0, "", false
	}
	line, _ = strconv.Atoi(s[:i])
	rest := s[i+1:]
	hasCol := false
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j > 0 && j < len(rest) && rest[j] == ':' {
		col, _ = strconv.Atoi(rest[:j])
		rest = rest[j+1:]
		hasCol = true
	}
	// "12:30:00 a log line" must not read as line 12, column 30, text "00…".
	if rest == "" || (hasCol && rest[0] >= '0' && rest[0] <= '9') {
		return 0, 0, "", false
	}
	return line, col, rest, true
}

// grepDefaultFile is the command's single file operand, if it has one: with a
// TTY ripgrep prints no file name for a single file, so the hits would have
// no path of their own.
func grepDefaultFile(args []string) string {
	operands, patternFromFlag := grepOperands(args)
	if patternFromFlag {
		if len(operands) == 1 {
			return operands[0]
		}
		return ""
	}
	if len(operands) == 2 {
		return operands[1] // pattern + one file
	}
	return ""
}

// grepOperands collects the non-flag operands and reports whether the pattern
// came from -e/--regexp/-f/--file instead of the first operand.
func grepOperands(args []string) (operands []string, patternFromFlag bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "":
			continue
		case a == "-e" || a == "--regexp" || a == "-f" || a == "--file":
			patternFromFlag = true
			i++
			continue
		case strings.HasPrefix(a, "-"):
			if grepFlagTakesValue(a) {
				i++
			}
			continue
		}
		operands = append(operands, a)
	}
	return operands, patternFromFlag
}

// grepStatLine reads ripgrep's summary lines, e.g. "12 matches",
// "3 files contained matches", "10 matched lines", "20 files searched".
func grepStatLine(s string) (int, string, bool) {
	for _, word := range []string{"matches", "matched lines", "files contained matches", "files searched", "bytes printed"} {
		if !strings.HasSuffix(s, word) {
			continue
		}
		head := strings.TrimSpace(strings.TrimSuffix(s, word))
		if allDigits(head) {
			n, _ := strconv.Atoi(head)
			return n, word, true
		}
	}
	return 0, "", false
}

// grepSplitPath splits "path:rest" at the first colon that is not a Windows
// drive's.
func grepSplitPath(s string) (path, rest string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return "", "", false
	}
	if i == 1 && len(s) > 2 && (s[2] == '\\' || s[2] == '/') {
		i = strings.IndexByte(s[2:], ':')
		if i < 0 {
			return "", "", false
		}
		i += 2
	}
	return s[:i], s[i+1:], true
}

// grepLooksLikePath keeps a line whose first colon merely separates a word (as
// "error:" does) from reading as a match.
func grepLooksLikePath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "-") &&
		(strings.ContainsAny(p, `/\`) || strings.Contains(p, "."))
}

// grepParseHit reads `path:line:col:text`, `path:line:text`, `path-line-text`
// (context) and `path:text`. It scans for a separator followed by digits and
// another separator, so a path may itself contain '-' or ':' (a drive).
func grepParseHit(s string) (grepHit, bool) {
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c != ':' && c != '-' {
			continue
		}
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i+1 || j >= len(s) || (s[j] != ':' && s[j] != '-') {
			continue
		}
		line, _ := strconv.Atoi(s[i+1 : j])
		k, col := j+1, 0
		if cj := k; cj < len(s) {
			for cj < len(s) && s[cj] >= '0' && s[cj] <= '9' {
				cj++
			}
			if cj > k && cj < len(s) && (s[cj] == ':' || s[cj] == '-') {
				col, _ = strconv.Atoi(s[k:cj])
				k = cj + 1
			}
		}
		return grepHit{path: s[:i], line: line, col: col, text: s[k:], context: c == '-'}, true
	}
	if p, rest, ok := grepSplitPath(s); ok && rest != "" {
		return grepHit{path: p, text: rest}, true
	}
	return grepHit{}, false
}

func (g *searchGrep) View(state any) (lens.Node, error) {
	st, ok := state.(*grepState)
	if !ok {
		return nil, errBadState
	}
	if !st.any {
		return nil, nil
	}
	head := st.headSpans()
	var body lens.Node
	switch {
	case len(st.hits) == 0 && len(st.counts) == 0:
		children := st.noteNodes()
		children = append(children, lens.Badge{Text: "No matches", Tone: toneMuted})
		return lens.Card{Head: head, Children: children}, nil
	case len(st.counts) > 0:
		body = grepCountTable(st) // grep -c: path/count rows
	default:
		body = grepMatchTable(st)
	}
	children := []lens.Node{body}
	children = append(children, st.noteNodes()...)
	return lens.Card{Head: head, Children: children}, nil
}

// noteNodes are the tools' diagnostics, as muted lines under the matches.
func (st *grepState) noteNodes() []lens.Node {
	out := make([]lens.Node, 0, len(st.notes))
	for _, n := range st.notes {
		out = append(out, lens.Text{Spans: []lens.Span{{Text: n, Style: toneMuted + " code"}}})
	}
	return out
}

// headSpans is the card's head: the pattern, then how much was found.
func (st *grepState) headSpans() []lens.Span {
	var spans []lens.Span
	if st.pattern != "" {
		spans = append(spans, lens.Span{Text: st.pattern, Style: "code"})
	}
	matches, files := st.matched, len(st.files)
	if n, ok := st.stats["matches"]; ok {
		matches = n
	}
	if n, ok := st.stats["files contained matches"]; ok {
		files = n
	}
	summary := ""
	switch {
	case len(st.counts) > 0:
		total := 0
		for _, n := range st.counts {
			total += n
		}
		summary = grepCounts(total, len(st.counts)) // the counts mode owns the head
	case matches == 0 && files == 0:
		summary = "No matches"
	default:
		summary = grepCounts(matches, files)
	}
	if len(spans) > 0 {
		spans = append(spans, lens.Span{Text: " · ", Style: toneMuted})
	}
	spans = append(spans, lens.Span{Text: summary, Style: "num"})
	return spans
}

// grepCounts is "N match(es) in M file(s)", the words the lens region carries
// ("filematchmatches").
func grepCounts(matches, files int) string {
	return fmt.Sprintf("%d %s in %d %s", matches, grepPlural(matches, "match", "matches"),
		files, grepPlural(files, "file", "files"))
}

func grepPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// grepMatchTable is one row per match (and context line), with the columns the
// output carries: path grows; line, column and match follow.
func grepMatchTable(st *grepState) lens.Table {
	cols := []lens.Col{
		{ID: "path", Head: "Path", Grow: 1},
		{ID: "line", Head: "Line", Align: lens.AlignEnd},
	}
	hasCol := false
	for _, h := range st.hits {
		if h.col > 0 {
			hasCol = true
			break
		}
	}
	if hasCol {
		cols = append(cols, lens.Col{ID: "col", Head: "Col", Align: lens.AlignEnd})
	}
	cols = append(cols, lens.Col{ID: "match", Head: "Match", Grow: 2})

	rows := make([][]lens.Cell, 0, len(st.hits))
	for _, h := range st.hits {
		cells := []lens.Cell{
			{Spans: []lens.Span{{Text: h.path, Style: "path"}}},
			{Spans: []lens.Span{{Text: grepLineLabel(h), Style: "num"}}},
		}
		if hasCol {
			col := ""
			if h.col > 0 {
				col = strconv.Itoa(h.col)
			}
			cells = append(cells, lens.Cell{Spans: []lens.Span{{Text: col, Style: "num"}}})
		}
		style := "code"
		if h.context {
			style = toneMuted + " code"
		}
		// The matched run is highlighted with the `mark` style, as Terminal marks a
		// grep hit instead of relying on the program's own colours.
		cells = append(cells, lens.Cell{Spans: grepMarkSpans(h.text, st.pattern, style)})
		rows = append(rows, cells)
	}
	return lens.Table{Cols: cols, Rows: rows}
}

// grepMarkSpans splits text on the pattern and marks each occurrence. The match
// is case-insensitive so `grep -i` output highlights too; a pattern that does
// not occur literally (a regex) simply leaves the text unmarked.
func grepMarkSpans(text, pattern, base string) []lens.Span {
	if pattern == "" || text == "" {
		return []lens.Span{{Text: text, Style: base}}
	}
	lowText, lowPat := strings.ToLower(text), strings.ToLower(pattern)
	var out []lens.Span
	rest, restLow := text, lowText
	for {
		i := strings.Index(restLow, lowPat)
		if i < 0 {
			break
		}
		if i > 0 {
			out = append(out, lens.Span{Text: rest[:i], Style: base})
		}
		out = append(out, lens.Span{Text: rest[i : i+len(pattern)], Style: "mark"})
		rest = rest[i+len(pattern):]
		restLow = restLow[i+len(lowPat):]
	}
	if rest != "" || len(out) == 0 {
		out = append(out, lens.Span{Text: rest, Style: base})
	}
	return out
}

func grepLineLabel(h grepHit) string {
	if h.line == 0 {
		return ""
	}
	return strconv.Itoa(h.line)
}

// grepCountTable is `grep -c` output: path and how many matches it holds.
func grepCountTable(st *grepState) lens.Table {
	rows := make([][]lens.Cell, 0, len(st.counts))
	for _, p := range grepSortedKeys(st.counts) {
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: p, Style: "path"}}},
			{Spans: []lens.Span{{Text: strconv.Itoa(st.counts[p]), Style: "num"}}},
		})
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "path", Head: "Path", Grow: 1},
			{ID: "count", Head: "Matches", Align: lens.AlignEnd},
		},
		Rows: rows,
	}
}

// grepSortedKeys keeps the table deterministic (map order is not).
func grepSortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
