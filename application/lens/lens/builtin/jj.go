package builtin

// jj.diff / jj.status / jj.log — Jujutsu's diff, status and log.
//
// Mined from payload\flow.exe (lens-conditions.md §1.1; re-enumerated from the
// constants region):
//
//	149997296  "--git"
//	149997320  "--stat-s--summary--t"                    jj diff's flags 【观察】
//	149997464  "jj.diffjj.status-T--template-p--patch"   the two ids, glued
//	           with -T --template -p --patch                        【观察】
//	149997680  "jj.log--json--jq-q-t--web-w--repo--hostname"  the log id 【观察】
//	149997798  "(no description set)root()graphchangedescauthortime
//	           bookmarkscommitAT, mark # "   the log's words: graph, change,
//	           desc, author, time, bookmarks, commit; the empty description;
//	           the root commit; the "mark" style                       【观察】
//	149997968  "MmodifiedAaddedDdeletedRrenamedCcopiedWorking copy changes"
//	           status' letters with their words and its section head 【观察】
//	149998031  "Conflicts"                                            【观察】
//
// The three ids are registered with an exact Match (no globs): the registry's
// globs append "*", which would let "jj status" claim "jj diff". The shapes —
// Diff nodes per file for jj diff, a Table of code/path plus a Badge for jj
// status, a List for jj log — follow the task's mapping and are 【推断】 where
// Terminal's own parser is code we cannot read. `jj log --json` and any output
// without graph nodes fall back to raw.

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	// Empty Match: Lookup calls each lens's Match(cmd), so the three
	// subcommands are told apart exactly (see the package comment).
	lens.Register(lens.Definition{ID: "jj.diff", New: func() lens.Lens { return &jjDiff{} }})
	lens.Register(lens.Definition{ID: "jj.log", New: func() lens.Lens { return &jjLog{} }})
	lens.Register(lens.Definition{ID: "jj.status", New: func() lens.Lens { return &jjStatus{} }})
}

// jjSubcommand is the first operand of a jj command line, skipping the global
// flags and the values they take.
func jjSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "":
			continue
		case a == "-R" || a == "--repository" || a == "--at-operation" || a == "--color" ||
			a == "--config" || a == "--config-toml" || a == "-T" || a == "--template" ||
			a == "--tool" || a == "--revision" || a == "-r":
			i++
			continue
		case strings.HasPrefix(a, "-"):
			continue
		}
		return a
	}
	return ""
}

// jjChange is one "M path" line of jj status, jj diff --summary or
// --name-only.
type jjChange struct{ code, path string }

// jjChangeFromLine reads "M path" with one of the status letters the lens
// region carries (M A D R C; C is a conflict).
func jjChangeFromLine(s string) (jjChange, bool) {
	if len(s) < 3 || s[1] != ' ' || !strings.ContainsRune("MADRC", rune(s[0])) {
		return jjChange{}, false
	}
	path := strings.TrimSpace(s[2:])
	if path == "" {
		return jjChange{}, false
	}
	return jjChange{code: string(s[0]), path: path}, true
}

// jjToneForCode maps a status letter to a tone: added/copied succeed,
// renamed is an accent, deleted and conflicted are errors.
func jjToneForCode(code string) string {
	switch code {
	case "A", "C":
		return toneSuccess
	case "R":
		return toneAccent
	case "D":
		return toneError
	}
	return toneWarning // M, and a conflict shown as C by some versions
}

// ---------------------------------------------------------------------------
// jj.status

type jjStatus struct{}

type jjStatusState struct {
	changes []jjChange
	working string
	parent  string
	clean   bool
	any     bool
}

func (l *jjStatus) ID() string { return "jj.status" }

func (l *jjStatus) Match(cmd model.Cmd) bool {
	if cmd.Program != "jj" {
		return false
	}
	switch jjSubcommand(cmd.Args) {
	case "", "status", "st":
		return true
	}
	return false
}

func (l *jjStatus) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &jjStatusState{}, nil
}

func (l *jjStatus) Line(state any, line model.Line) error {
	st, ok := state.(*jjStatusState)
	if !ok {
		return errBadState
	}
	st.observe(line.Text)
	return nil
}

func (st *jjStatusState) observe(s string) {
	t := strings.TrimSpace(s)
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, "Working copy changes:"):
		st.any = true
		return
	case strings.HasPrefix(t, "Conflicts:") || t == "Conflicts":
		st.any = true
		return
	case strings.HasPrefix(t, "Working copy"):
		st.working = strings.TrimLeft(strings.TrimPrefix(t, "Working copy"), " :")
		st.any = true
		return
	case strings.HasPrefix(t, "Parent commit"):
		st.parent = strings.TrimLeft(strings.TrimPrefix(t, "Parent commit"), " :")
		st.any = true
		return
	case strings.Contains(t, "no changes"):
		st.clean, st.any = true, true
		return
	}
	if c, ok := jjChangeFromLine(t); ok {
		st.changes = append(st.changes, c)
		st.any = true
	}
}

func (l *jjStatus) Finish(state any, status int) error { return nil }

func (l *jjStatus) Event(state any, ev lens.Event) error { return nil }

func (l *jjStatus) View(state any) (lens.Node, error) {
	st, ok := state.(*jjStatusState)
	if !ok {
		return nil, errBadState
	}
	if !st.any {
		return nil, nil
	}
	head := []lens.Span{{Text: "jj status", Style: "code"}}
	if len(st.changes) == 0 {
		tone := toneMuted
		if st.clean {
			tone = toneSuccess
		}
		return lens.Card{Head: head, Children: []lens.Node{lens.Badge{Text: "no changes", Tone: tone}}}, nil
	}
	rows := make([][]lens.Cell, 0, len(st.changes))
	for _, c := range st.changes {
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: c.code, Style: "strong " + jjToneForCode(c.code)}}},
			{Spans: []lens.Span{{Text: c.path, Style: "path"}}},
		})
	}
	children := []lens.Node{
		lens.Text{Spans: []lens.Span{{Text: fmt.Sprintf("%d changes", len(st.changes)), Style: "num"}}},
		lens.Table{
			Cols: []lens.Col{
				{ID: "code", Head: "Status"},
				{ID: "path", Head: "Path", Grow: 1},
			},
			Rows: rows,
		},
	}
	if st.working != "" {
		children = append(children, lens.KV{Items: [][2][]lens.Span{
			{
				[]lens.Span{{Text: "working copy"}},
				[]lens.Span{{Text: st.working, Style: toneMuted}},
			},
		}})
	}
	return lens.Card{Head: head, Children: children}, nil
}

// ---------------------------------------------------------------------------
// jj.diff

type jjDiff struct{}

type jjDiffFile struct {
	path    string
	text    string
	added   int
	removed int
}

type jjStat struct {
	path        string
	added       int
	removed     int
	description string
}

type jjDiffState struct {
	files   []jjDiffFile
	stats   []jjStat
	summary []jjChange
	any     bool
}

func (l *jjDiff) ID() string { return "jj.diff" }

func (l *jjDiff) Match(cmd model.Cmd) bool {
	return cmd.Program == "jj" && jjSubcommand(cmd.Args) == "diff"
}

func (l *jjDiff) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &jjDiffState{}, nil
}

func (l *jjDiff) Line(state any, line model.Line) error {
	st, ok := state.(*jjDiffState)
	if !ok {
		return errBadState
	}
	st.observe(strings.TrimRight(line.Text, "\r"))
	return nil
}

func (st *jjDiffState) observe(s string) {
	if s == "" {
		return
	}
	// "+++ b/x" is a header only when nothing was started yet; inside a
	// git-style diff it is the file's content header.
	if !strings.HasPrefix(s, "+++ ") || len(st.files) == 0 {
		if path, ok := jjDiffHeader(s); ok {
			st.files = append(st.files, jjDiffFile{path: path})
			st.any = true
			return
		}
	}
	if stat, ok := jjStatLine(s); ok {
		st.stats = append(st.stats, stat)
		st.any = true
		return
	}
	if c, ok := jjChangeFromLine(s); ok {
		st.summary = append(st.summary, c)
		st.any = true
		return
	}
	if len(st.files) == 0 {
		return
	}
	// A hunk or a diff line belongs to the file the last header started.
	if strings.HasPrefix(s, "@@") || strings.HasPrefix(s, "---") || strings.HasPrefix(s, "+++") {
		st.appendDiff(s)
		return
	}
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") || strings.HasPrefix(s, " ") {
		st.countDiffLine(s)
		st.appendDiff(s)
		return
	}
	// jj's own numbered format: "   1    2: +added" / ":  context".
	if i := strings.Index(s, ": "); i > 0 {
		body := s[i+2:]
		st.countDiffBody(body)
		st.appendDiff(body)
	}
}

func (st *jjDiffState) appendDiff(line string) {
	f := &st.files[len(st.files)-1]
	if f.text != "" {
		f.text += "\n"
	}
	f.text += line
}

func (st *jjDiffState) countDiffLine(s string) {
	f := &st.files[len(st.files)-1]
	switch {
	case strings.HasPrefix(s, "+") && !strings.HasPrefix(s, "+++"):
		f.added++
	case strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "---"):
		f.removed++
	}
}

func (st *jjDiffState) countDiffBody(body string) {
	f := &st.files[len(st.files)-1]
	switch {
	case strings.HasPrefix(body, "+"):
		f.added++
	case strings.HasPrefix(body, "-"):
		f.removed++
	}
}

// jjDiffHeader reads the headers jj and `jj diff --git` write: "Modified
// regular file X:", "Added regular file X:", …, "diff --git a/X b/X",
// "+++ b/X".
func jjDiffHeader(s string) (string, bool) {
	for _, word := range []string{"Modified", "Added", "Removed", "Renamed", "Copied", "Changed"} {
		prefix := word + " regular file "
		if strings.HasPrefix(s, prefix) {
			path := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(s, prefix)), ":")
			if path != "" {
				return strings.TrimSuffix(path, ":"), true
			}
		}
	}
	if strings.HasPrefix(s, "diff --git ") {
		fields := strings.Fields(s)
		if len(fields) >= 4 {
			return jjTrimAB(fields[3]), true // b/path
		}
	}
	if strings.HasPrefix(s, "+++ ") {
		return jjTrimAB(strings.TrimSpace(strings.TrimPrefix(s, "+++ "))), true
	}
	return "", false
}

func jjTrimAB(p string) string {
	p = strings.TrimSpace(strings.TrimSuffix(p, "\t"))
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// jjStatLine reads `jj diff --stat`'s "path | 5 +++--" and the "3 files
// changed, 4 insertions(+), 1 deletion(-)" summary.
func jjStatLine(s string) (jjStat, bool) {
	if i := strings.IndexByte(s, '|'); i > 0 {
		path := strings.TrimSpace(s[:i])
		bar := s[i+1:]
		if path == "" || strings.HasPrefix(path, "-") {
			return jjStat{}, false
		}
		st := jjStat{path: path}
		st.added = strings.Count(bar, "+")
		st.removed = strings.Count(bar, "-")
		if n, ok := jjLeadingNumber(bar); ok {
			st.description = fmt.Sprintf("%d", n)
		}
		return st, true
	}
	if strings.Contains(s, "changed") && strings.Contains(s, "insertion") {
		return jjStat{description: strings.TrimSpace(s)}, true
	}
	return jjStat{}, false
}

func jjLeadingNumber(s string) (int, bool) {
	t := strings.TrimSpace(s)
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	return leadingInt(t[:i]), true
}

func (l *jjDiff) Finish(state any, status int) error { return nil }

func (l *jjDiff) Event(state any, ev lens.Event) error { return nil }

func (l *jjDiff) View(state any) (lens.Node, error) {
	st, ok := state.(*jjDiffState)
	if !ok {
		return nil, errBadState
	}
	if !st.any {
		return nil, nil
	}
	head := []lens.Span{{Text: "jj diff", Style: "code"}}
	switch {
	case len(st.files) > 0:
		added, removed := 0, 0
		children := make([]lens.Node, 0, len(st.files)+1)
		for _, f := range st.files {
			added += f.added
			removed += f.removed
			children = append(children, lens.Diff{Text: f.text, Path: f.path, Split: false})
		}
		children = append([]lens.Node{lens.Text{Spans: []lens.Span{
			{Text: fmt.Sprintf("%d files", len(st.files)), Style: "num"},
			{Text: fmt.Sprintf(" · +%d −%d", added, removed), Style: toneMuted},
		}}}, children...)
		return lens.Card{Head: head, Children: children}, nil
	case len(st.stats) > 0:
		rows := make([][]lens.Cell, 0, len(st.stats))
		for _, s := range st.stats {
			if s.path == "" {
				continue
			}
			rows = append(rows, []lens.Cell{
				{Spans: []lens.Span{{Text: s.path, Style: "path"}}},
				{Spans: []lens.Span{{Text: fmt.Sprintf("+%d", s.added), Style: toneSuccess}}},
				{Spans: []lens.Span{{Text: fmt.Sprintf("−%d", s.removed), Style: toneError}}},
			})
		}
		return lens.Card{Head: head, Children: []lens.Node{lens.Table{
			Cols: []lens.Col{
				{ID: "path", Head: "Path", Grow: 1},
				{ID: "added", Head: "+", Align: lens.AlignEnd},
				{ID: "removed", Head: "−", Align: lens.AlignEnd},
			},
			Rows: rows,
		}}}, nil
	default:
		rows := make([][]lens.Cell, 0, len(st.summary))
		for _, c := range st.summary {
			rows = append(rows, []lens.Cell{
				{Spans: []lens.Span{{Text: c.code, Style: "strong " + jjToneForCode(c.code)}}},
				{Spans: []lens.Span{{Text: c.path, Style: "path"}}},
			})
		}
		return lens.Card{Head: head, Children: []lens.Node{lens.Table{
			Cols: []lens.Col{
				{ID: "code", Head: "Status"},
				{ID: "path", Head: "Path", Grow: 1},
			},
			Rows: rows,
		}}}, nil
	}
}

// ---------------------------------------------------------------------------
// jj.log

type jjLog struct{}

type jjLogEntry struct {
	graph       string
	changeID    string
	description string
	author      string
	when        string
	bookmarks   string
	commit      string
}

type jjLogState struct {
	entries []jjLogEntry
	any     bool
}

func (l *jjLog) ID() string { return "jj.log" }

func (l *jjLog) Match(cmd model.Cmd) bool {
	return cmd.Program == "jj" && jjSubcommand(cmd.Args) == "log"
}

func (l *jjLog) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &jjLogState{}, nil
}

func (l *jjLog) Line(state any, line model.Line) error {
	st, ok := state.(*jjLogState)
	if !ok {
		return errBadState
	}
	st.observe(strings.TrimRight(line.Text, "\r"))
	return nil
}

func (st *jjLogState) observe(s string) {
	if strings.TrimSpace(s) == "" || strings.Trim(strings.TrimSpace(s), "~") == "" {
		return
	}
	graph, rest := jjStripGraph(s)
	if jjGraphHasNode(graph) {
		if e, ok := jjLogEntryOf(graph, rest); ok {
			st.entries = append(st.entries, e)
			st.any = true
			return
		}
	}
	// A continuation line is the description of the entry above it.
	if len(st.entries) > 0 && strings.TrimSpace(rest) != "" {
		last := &st.entries[len(st.entries)-1]
		if last.description == "" {
			last.description = strings.TrimSpace(rest)
		} else {
			last.description += " " + strings.TrimSpace(rest)
		}
	}
}

func jjLogEntryOf(graph, rest string) (jjLogEntry, bool) {
	fields := strings.Fields(rest)
	if len(fields) == 0 || !jjIsChangeID(fields[0]) {
		return jjLogEntry{}, false
	}
	e := jjLogEntry{graph: strings.TrimRight(graph, " "), changeID: fields[0]}
	author, when, bookmarks, commit := jjParseLogRest(strings.Join(fields[1:], " "))
	e.author, e.when, e.bookmarks, e.commit = author, when, bookmarks, commit
	return e, true
}

// jjParseLogRest splits the fields after the change id: the author (something
// with "@", or root() with a commit id), the timestamp, the commit id and the
// bookmarks.
func jjParseLogRest(rest string) (author, when, bookmarks, commit string) {
	fields := strings.Fields(rest)
	i := 0
	if i < len(fields) && strings.Contains(fields[i], "@") {
		author = fields[i]
		i++
	}
	if i < len(fields) && jjIsDate(fields[i]) {
		when = fields[i]
		i++
		if i < len(fields) && strings.Contains(fields[i], ":") && !strings.Contains(fields[i], "-") {
			when += " " + fields[i]
			i++
		}
	}
	var rest2 []string
	for ; i < len(fields); i++ {
		if jjIsHex(fields[i]) && len(fields[i]) >= 12 {
			commit = fields[i]
			continue
		}
		rest2 = append(rest2, fields[i])
	}
	if author == "" && commit != "" && len(rest2) > 0 {
		author, rest2 = rest2[0], rest2[1:] // the root commit: "root() <40 hex>"
	}
	return author, when, strings.Join(rest2, " "), commit
}

// jjGraphRunes are the characters jj draws a log's graph with.
const jjGraphRunes = "@○◆●×~│├┼┬┴╭╮╯╰┤┠┨"

// jjStripGraph splits a log line's leading graph drawing ("@  ", "│  ",
// "├─╮ ") from what follows. The graph's characters are multi-byte, so the
// line is decoded rune by rune.
func jjStripGraph(s string) (graph, rest string) {
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != ' ' && !strings.ContainsRune(jjGraphRunes, r) {
			break
		}
		i += size
	}
	return s[:i], s[i:]
}

func jjGraphHasNode(graph string) bool {
	return strings.ContainsAny(graph, "@○◆●×")
}

// jjIsChangeID reports whether s looks like a jj change id (lowercase
// letters; the root's zzzzzzzz is one too).
func jjIsChangeID(s string) bool {
	if len(s) < 4 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// jjIsDate reports whether s starts with a YYYY-MM-DD date.
func jjIsDate(s string) bool {
	if len(s) < 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for _, i := range []int{0, 1, 2, 3, 5, 6, 8, 9} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func jjIsHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func (l *jjLog) Finish(state any, status int) error { return nil }

func (l *jjLog) Event(state any, ev lens.Event) error { return nil }

func (l *jjLog) View(state any) (lens.Node, error) {
	st, ok := state.(*jjLogState)
	if !ok {
		return nil, errBadState
	}
	if !st.any {
		return nil, nil
	}
	items := make([]lens.Item, 0, len(st.entries))
	for _, e := range st.entries {
		label := []lens.Span{{Text: e.graph + " ", Style: toneMuted}, {Text: e.changeID, Style: "num"}}
		detail := e.description
		detailStyle := ""
		if detail == "" || detail == "(no description set)" {
			detailStyle = toneMuted
		}
		var value []lens.Span
		if e.author != "" {
			value = append(value, lens.Span{Text: e.author, Style: toneMuted})
		}
		if e.when != "" {
			value = append(value, lens.Span{Text: e.when, Style: toneMuted})
		}
		if e.bookmarks != "" {
			value = append(value, lens.Span{Text: e.bookmarks, Style: "mark"})
		}
		items = append(items, lens.Item{
			Label:  label,
			Detail: []lens.Span{{Text: detail, Style: detailStyle}},
			Value:  value,
		})
	}
	return lens.Card{
		Head: []lens.Span{{Text: "jj log", Style: "code"}},
		Children: []lens.Node{
			lens.Text{Spans: []lens.Span{{Text: fmt.Sprintf("%d commits", len(st.entries)), Style: "num"}}},
			lens.List{Items: items},
		},
	}, nil
}
