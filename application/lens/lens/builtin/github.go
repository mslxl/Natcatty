package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

// gh family: gh.view, gh.list.
//
// Mined from payload\flow.exe (Terminal 0.9.4) per findings/lens-conditions.md §1.1:
// offset 149997776 holds `gh.view`/`gh.list` next to the literals "gh", "view"
// and "list".                                                             【观察】
// The subcommand split (pr/issue/repo + view|list) and the view shapes are
// 【推断】 per docs/lens-parity.md (Terminal's predicates are code, not data).
// View components follow findings/lens-widgets.md §1.3: title card → Card,
// status words → Badge (OPEN=success, MERGED=accent, CLOSED=error, DRAFT=
// muted), metadata → KV, body → Markdown, table → Table with a num column.

func init() {
	lens.Register(lens.Definition{ID: "gh.view", New: func() lens.Lens { return &ghView{} }})
	lens.Register(lens.Definition{ID: "gh.list", New: func() lens.Lens { return &ghList{} }})
}

// ghFlagValue reports whether a gh flag consumes the next token.
func ghFlagValue(flag string) bool {
	switch flag {
	case "-R", "--repo", "--json", "-q", "--jq", "-t", "--template",
		"--hostname", "-L", "--limit", "--search", "--state", "--author",
		"--label", "--assignee", "--base", "--head", "--app", "--milestone",
		"--config", "--log":
		return true
	}
	return false
}

// ghTokens returns gh's first two non-flag words, lowercased ("pr view").    【推断】
func ghTokens(cmd model.Cmd) ([]string, bool) {
	if cmd.Program != "gh" {
		return nil, false
	}
	var out []string
	for i := 0; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && ghFlagValue(a) && i+1 < len(cmd.Args) {
				i++
			}
			continue
		}
		out = append(out, strings.ToLower(a))
		if len(out) == 2 {
			break
		}
	}
	return out, true
}

// ghStateTone maps gh's state word to a badge tone.                        【观察】
func ghStateTone(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "OPEN":
		return toneSuccess
	case "MERGED":
		return toneAccent
	case "CLOSED":
		return toneError
	case "DRAFT":
		return toneMuted
	}
	return toneNeutral
}

// ghMetaKeys are the metadata keys `gh … view` prints before the body.      【推断】
var ghMetaKeys = map[string]bool{
	"title": true, "state": true, "author": true, "labels": true,
	"assignees": true, "reviewers": true, "projects": true, "milestone": true,
	"number": true, "url": true, "additions": true, "deletions": true,
	"changed files": true, "created": true, "updated": true, "base": true,
	"head": true, "branch": true, "name": true, "description": true,
	"visibility": true, "license": true, "forks": true, "stars": true,
	"watchers": true, "default branch": true, "topics": true,
}

// ------------------------------------------------------------------- gh.view

// ghView renders `gh pr|issue|repo view`: a title card, the state badge, the
// metadata and the markdown body.
type ghView struct{}

// ghViewState keeps what `gh … view` printed plus the evidence that the output
// really is a PR/issue/repo view: a metadata key, a state word, a `--` body
// separator or an "#N" issue number. Without one of them the block stays raw
// (Terminal's view()==nil → raw contract).                                     【推断】
type ghViewState struct {
	title  string
	state  string
	kv     [][2]string
	body   []string
	inBody bool
	meta   bool
	sep    bool
	hash   bool
	saw    bool
}

func (g *ghView) ID() string { return "gh.view" }

func (g *ghView) Match(cmd model.Cmd) bool {
	t, ok := ghTokens(cmd)
	if !ok || len(t) < 2 || t[1] != "view" {
		return false
	}
	switch t[0] {
	case "pr", "issue", "repo":
		return true
	}
	return false
}

func (g *ghView) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &ghViewState{}, nil }

func (g *ghView) Line(state any, l model.Line) error {
	st, ok := state.(*ghViewState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (g *ghView) Finish(state any, status int) error { return nil }

func (g *ghView) Event(state any, ev lens.Event) error { return nil }

func (st *ghViewState) observe(text string) {
	t := strings.TrimRight(text, " \t")
	if strings.TrimSpace(t) == "" {
		return
	}
	if strings.TrimSpace(t) == "--" {
		st.inBody, st.saw, st.sep = true, true, true
		return
	}
	if st.inBody {
		st.body = append(st.body, strings.TrimSpace(t))
		return
	}
	if i := strings.IndexByte(t, ':'); i > 0 && !strings.ContainsAny(t[:i], " \t") {
		key := strings.ToLower(strings.TrimSpace(t[:i]))
		if ghMetaKeys[key] {
			val := strings.TrimSpace(t[i+1:])
			switch key {
			case "title":
				st.title = val
			case "state":
				st.state = strings.ToUpper(val)
			default:
				st.kv = append(st.kv, [2]string{key, val})
			}
			st.meta, st.saw = true, true
			return
		}
	}
	// Human format: "Add lens parity #42" then "Open • alice wants to merge …".
	if st.title == "" {
		st.title, st.saw = strings.TrimSpace(t), true
		if ghHasIssueNumber(t) {
			st.hash = true
		}
		return
	}
	if st.state == "" {
		low := strings.ToLower(t)
		for _, s := range []string{"open", "draft", "merged", "closed"} {
			if strings.Contains(low, s) {
				st.state, st.saw = strings.ToUpper(s), true
				return
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "view this") {
		if u := ghLastURL(t); u != "" {
			st.kv = append(st.kv, [2]string{"url", u})
		}
		return
	}
	st.body = append(st.body, strings.TrimSpace(t))
	st.saw = true
}

func ghLastURL(t string) string {
	for _, f := range strings.Fields(t) {
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			return strings.TrimRight(f, ".,)")
		}
	}
	return ""
}

// ghHasIssueNumber reports whether s carries an "#N" issue/PR number.
func ghHasIssueNumber(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '#' && s[i+1] >= '0' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

func (g *ghView) View(state any) (lens.Node, error) {
	st, ok := state.(*ghViewState)
	if !ok {
		return nil, errBadState
	}
	// Evidence gate: arbitrary text is not a PR/issue view. Only a metadata
	// key, a state word, a `--` separator or an "#N" title may claim the block;
	// otherwise the view is nil and the block stays raw.                  【推断】
	if !st.saw || !(st.meta || st.sep || st.hash || st.state != "") {
		return nil, nil
	}
	head := st.title
	if head == "" {
		head = "gh"
	}
	var children []lens.Node
	if st.state != "" {
		children = append(children, lens.Badge{Text: st.state, Tone: ghStateTone(st.state)})
	}
	if len(st.kv) > 0 {
		items := make([][2][]lens.Span, 0, len(st.kv))
		for _, p := range st.kv {
			items = append(items, [2][]lens.Span{
				{{Text: p[0], Style: "key"}},
				{{Text: p[1]}},
			})
		}
		children = append(children, lens.KV{Items: items})
	}
	if len(st.body) > 0 {
		children = append(children, lens.Markdown{Text: strings.Join(st.body, "\n")})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Card{
		Head:     []lens.Span{{Text: head, Style: "strong"}},
		Children: children,
	}, nil
}

// ------------------------------------------------------------------- gh.list

// ghList renders `gh pr|issue list` as a count badge plus a table.
type ghList struct{}

type ghListState struct {
	cols   []string
	rows   [][]string
	lines  []string // every listing line, so the shared column reader can align them
	shown  int
	total  int
	header bool
	saw    bool
}

func (g *ghList) ID() string { return "gh.list" }

func (g *ghList) Match(cmd model.Cmd) bool {
	t, ok := ghTokens(cmd)
	if !ok || len(t) < 2 || t[1] != "list" {
		return false
	}
	switch t[0] {
	case "pr", "issue", "repo", "release", "gist":
		return true
	}
	return false
}

func (g *ghList) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &ghListState{}, nil }

func (g *ghList) Line(state any, l model.Line) error {
	st, ok := state.(*ghListState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (g *ghList) Finish(state any, status int) error { return nil }

func (g *ghList) Event(state any, ev lens.Event) error { return nil }

func (st *ghListState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "Showing ") {
		if nums := parseFloats(t); len(nums) >= 2 {
			st.shown, st.total = int(nums[0]), int(nums[1])
		} else if len(nums) == 1 {
			st.shown, st.total = int(nums[0]), int(nums[0])
		}
		st.saw = true
		return
	}
	// gh's listings are aligned tables whose TITLE can contain spaces; keeping the
	// lines lets parse.Columns read the header and the rows the way they are
	// printed instead of splitting each row on whitespace.
	st.lines = append(st.lines, t)
	if !st.header {
		f := strings.Fields(t)
		if ghHasField(f, "TITLE") && (ghHasField(f, "BRANCH") ||
			ghHasField(f, "UPDATED") || ghHasField(f, "#")) {
			st.cols, st.header, st.saw = f, true, true
			return
		}
	}
	if strings.Contains(t, "\t") {
		row := strings.Split(t, "\t")
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}
		if len(row) >= 2 {
			st.rows = append(st.rows, row)
			st.saw = true
		}
		return
	}
	f := strings.Fields(t)
	if len(f) >= 3 {
		num := strings.TrimPrefix(f[0], "#")
		if allDigits(num) {
			st.rows = append(st.rows, append([]string{num}, f[1:]...))
			st.saw = true
		}
	}
}

func ghHasField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func (g *ghList) View(state any) (lens.Node, error) {
	st, ok := state.(*ghListState)
	if !ok {
		return nil, errBadState
	}
	if !st.saw {
		return nil, nil
	}
	heads := st.cols
	rawRows := st.rows
	// Only a listing that printed a header can be read as a table; without one the
	// rows were tab- or space-separated data and Columns would mistake the first
	// row for the header.
	if st.header {
		if tbl := parse.Columns(st.lines); tbl != nil && len(tbl.Rows) > 0 && len(tbl.Heads) >= 2 {
			heads, rawRows = tbl.Heads, tbl.Rows
		}
	}
	if len(heads) == 0 {
		heads = []string{"NUM", "TITLE", "BRANCH", "UPDATED"}
	} else if heads[0] == "#" {
		heads = append([]string{"NUM"}, heads[1:]...)
	}
	cols := make([]lens.Col, len(heads))
	for i, c := range heads {
		col := lens.Col{ID: strings.ToLower(c), Head: c}
		switch c {
		case "NUM", "#":
			col.Align = lens.AlignEnd
		case "TITLE":
			col.Grow = 1
		case "BRANCH", "UPDATED":
			col.Priority = 1
		}
		cols[i] = col
	}
	rows := make([][]lens.Cell, 0, len(rawRows))
	for _, r := range rawRows {
		cells := make([]lens.Cell, len(cols))
		for i := range cols {
			v := ""
			if i < len(r) {
				v = r[i]
			} else if i == len(cols)-1 && len(r) > len(cols) {
				v = strings.Join(r[i:], " ")
			}
			style := ""
			switch heads[i] {
			case "NUM", "#":
				style = "num"
			case "TITLE":
				style = "strong"
			case "UPDATED":
				style = toneMuted
			}
			cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: style}}}
		}
		rows = append(rows, cells)
	}
	badge := lens.Badge{Text: strconv.Itoa(st.shown) + " open", Tone: toneSuccess}
	if st.shown == 0 {
		badge = lens.Badge{Text: "0 open", Tone: toneMuted}
	}
	return lens.Cols(badge, lens.Table{Cols: cols, Rows: rows}), nil
}
