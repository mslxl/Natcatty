package builtin

import (
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// The git family beyond `git status`: diff, log, show, reflog, branch, remote,
// tag, files, shortlog, blame, transfer and stash. Ids and the trigger surface are
// Terminal's (findings/lens-conditions.md §1.1, offsets 149961688-149966664); the
// view shapes are our reading of Terminal's components (table/list/diff/code).
func init() {
	for _, d := range []lens.Definition{
		{ID: "git.diff", New: func() lens.Lens { return &gitxDiff{} }},
		{ID: "git.log", New: func() lens.Lens { return &gitxLog{} }},
		{ID: "git.show", New: func() lens.Lens { return &gitxShow{} }},
		{ID: "git.reflog", New: func() lens.Lens { return &gitxReflog{} }},
		{ID: "git.branch", New: func() lens.Lens { return &gitxBranch{} }},
		{ID: "git.remote", New: func() lens.Lens { return &gitxRemote{} }},
		{ID: "git.tag", New: func() lens.Lens { return &gitxTag{} }},
		{ID: "git.files", New: func() lens.Lens { return &gitxFiles{} }},
		{ID: "git.shortlog", New: func() lens.Lens { return &gitxShortlog{} }},
		{ID: "git.blame", New: func() lens.Lens { return &gitxBlame{} }},
		{ID: "git.transfer", New: func() lens.Lens { return &gitxTransfer{} }},
		{ID: "git.stash", New: func() lens.Lens { return &gitxStash{} }},
	} {
		lens.Register(d)
	}
}

// gitxSub returns the git subcommand after git's global options.
func gitxSub(cmd model.Cmd) (string, []string, bool) {
	if cmd.Program != "git" {
		return "", nil, false
	}
	args := cmd.Args
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" ||
			a == "--namespace" || a == "--exec-path" || a == "--config-env" {
			i++ // these take a value
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, args[i+1:], true
	}
	return "", nil, false
}

// gitxHash reports whether s looks like an abbreviated or full object id.
func gitxHash(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// gitxGraph strips `git log --graph` decorations ("* ", "| ", "|/ ", …).
func gitxGraph(line string) string {
	_, rest := gitxGraphSplit(line)
	return rest
}

// gitxGraphSplit returns the graph glyph prefix ("* ", "| * ", "|\  ") and the
// text after it, so a `--graph` commit keeps its branch glyph in the view.
func gitxGraphSplit(line string) (graph, rest string) {
	for {
		trimmed := strings.TrimLeft(line, " |\\/*")
		if trimmed == line {
			break
		}
		cut := len(line) - len(trimmed)
		graph += line[:cut]
		line = strings.TrimLeft(trimmed, " ")
	}
	return graph, line
}

// gitxCommit is one parsed commit.
type gitxCommit struct {
	graph   string // the `--graph` glyphs before the commit, if any
	hash    string
	subject string
	author  string
	date    string
	stats   []string // `--stat` rows that follow the commit
}

// gitxCommits collects commits from `git log`-shaped output, with or without
// `--oneline`, `--graph` or `--stat`.
type gitxCommits struct {
	commits []gitxCommit
	cur     *gitxCommit
	body    []string
	inBody  bool
	any     bool

	// Graph mode (`--oneline --graph`): one row per output line, so the
	// connector-only lines ("|\", "|/") keep the picture intact.
	rows     []gitxGraphRow
	oneline  bool
	hasGraph bool

	// statTail holds `--stat` rows recovered from a line ConPTY glued to the
	// commit date (`Date: … +0800    subject a.txt | 3 ++-`).
	statTail []string

	// fileMode is set for `--name-only`/`--name-status`, where every line after
	// the message is a changed path.
	fileMode   bool
	fileStatus bool // `--name-status`: the status letter may be glued to the path
	files      []string
}

// gitxGraphRow is one line of a `--graph --oneline` listing: the glyphs and,
// for a commit row, the index of that commit in commits (-1 for connectors).
type gitxGraphRow struct {
	graph  string
	commit int
}

func (c *gitxCommits) add() {
	if c.cur == nil {
		return
	}
	if c.cur.subject == "" && len(c.body) > 0 {
		for _, l := range c.body {
			if t := strings.TrimSpace(l); t != "" {
				c.cur.subject = t
				break
			}
		}
	}
	c.commits = append(c.commits, *c.cur)
	c.cur = nil
	c.body = nil
}

func (c *gitxCommits) line(text string) {
	graph, t := gitxGraphSplit(strings.TrimSpace(text))
	// With --name-only/--name-status every line after the message is a changed
	// path; the first one may be glued to the message (a dropped newline) and
	// stays part of the subject, while the clean ones become a Files section.
	if c.fileMode && gitxPathLike(t) && !gitxStatRow(t) {
		path := t
		if c.fileStatus {
			path = gitxStripStatus(path)
		}
		if c.cur == nil && len(c.commits) > 0 {
			c.files = append(c.files, path)
			return
		}
		if c.cur != nil && len(c.body) > 0 {
			c.files = append(c.files, path)
			return
		}
	}
	graph = strings.TrimRight(strings.TrimLeft(graph, " "), " ")
	t = strings.TrimSpace(t)
	if t == "" {
		if graph != "" {
			// A connector-only row ("|\", "|/"): keep it in graph mode.
			c.hasGraph = true
			c.rows = append(c.rows, gitxGraphRow{graph: graph, commit: -1})
		}
		if c.inBody {
			c.body = append(c.body, "")
		}
		return
	}
	if rest, ok := strings.CutPrefix(t, "commit "); ok {
		c.add()
		hash := strings.TrimSpace(rest)
		if i := strings.IndexByte(hash, ' '); i > 0 {
			hash = hash[:i]
		}
		c.cur = &gitxCommit{hash: hash, graph: strings.TrimSpace(graph)}
		c.inBody = false
		c.any = true
		return
	}
	if c.cur != nil {
		switch {
		case strings.HasPrefix(t, "Author:"):
			c.cur.author = strings.TrimSpace(strings.TrimPrefix(t, "Author:"))
			return
		case strings.HasPrefix(t, "Date:"):
			date, tail := gitxSplitDate(strings.TrimSpace(strings.TrimPrefix(t, "Date:")))
			c.cur.date = date
			if tail != "" {
				// ConPTY dropped the newline after the date, so the commit
				// message and the --stat row arrived on the same line.
				subject, statRow := gitxSplitStatTail(tail)
				c.inBody = true
				if subject != "" {
					c.body = append(c.body, subject)
				}
				if statRow != "" {
					c.statTail = append(c.statTail, statRow)
				}
			} else {
				// The capture screen hands the rows over as they were drawn: the
				// message follows the date on its own lines.
				c.inBody = true
			}
			return
		case strings.HasPrefix(t, "Merge:"):
			return
		}
		if c.inBody {
			// `git show --stat` draws the stat rows after the message.
			if gitxStatRow(t) {
				c.statTail = append(c.statTail, t)
				return
			}
			if strings.Contains(t, "file changed") || strings.Contains(t, "files changed") || strings.Contains(t, "insertion") {
				return
			}
			c.body = append(c.body, strings.TrimRight(t, " "))
			return
		}
	}
	// `--oneline` / `--format=%h %s`: hash then subject. Keep the graph glyph on
	// the commit it precedes so `--graph` output keeps its branch column.

	// `--oneline` / `--format=%h %s`: hash then subject.
	fields := strings.SplitN(t, " ", 2)

	// `git log --oneline --stat` prints the stat rows on the lines after the
	// commit, when no commit is being accumulated: attach them to the last one.
	if c.cur == nil && len(c.commits) > 0 && gitxStatRow(t) {
		last := &c.commits[len(c.commits)-1]
		last.stats = append(last.stats, t)
		return
	}
	if gitxHash(fields[0]) {
		subj := ""
		if len(fields) > 1 {
			subj = strings.TrimSpace(fields[1])
		}
		c.add()
		c.commits = append(c.commits, gitxCommit{
			graph:   graph,
			hash:    fields[0],
			subject: subj,
		})
		c.oneline = true
		if graph != "" {
			c.hasGraph = true
		}
		c.rows = append(c.rows, gitxGraphRow{graph: graph, commit: len(c.commits) - 1})
		c.any = true
		return
	}
	if c.cur != nil {
		c.inBody = true
		c.body = append(c.body, strings.TrimRight(t, " "))
	}
}

// all returns the finished commits plus the one still being parsed. View must
// not consume that commit: the engine rebuilds a running block's view at any
// time, and a View that mutated the state would drop the half-parsed lines.
func (c *gitxCommits) all() []gitxCommit {
	if c.cur == nil {
		return c.commits
	}
	p := *c.cur
	if p.subject == "" && len(c.body) > 0 {
		for _, l := range c.body {
			if t := strings.TrimSpace(l); t != "" {
				p.subject = t
				break
			}
		}
	}
	out := make([]gitxCommit, 0, len(c.commits)+1)
	out = append(out, c.commits...)
	return append(out, p)
}

func (c *gitxCommits) node(limit int) lens.Node {
	commits := c.all()
	if len(commits) == 0 {
		return nil
	}
	if c.oneline && c.hasGraph && len(c.rows) > 0 {
		return c.graphTable(limit)
	}
	items := make([]lens.Item, 0, len(commits))
	for i, cm := range commits {
		if limit > 0 && i >= limit {
			items = append(items, lens.Item{Label: []lens.Span{{Text: "…", Style: "muted"}}})
			break
		}
		label := make([]lens.Span, 0, 5)
		if cm.graph != "" {
			// The `--graph` branch glyph keeps its column in the list.
			label = append(label, lens.Span{Text: cm.graph + " ", Style: "muted mono"})
		}
		label = append(label, lens.Span{Text: cm.hash, Style: "num"})
		if cm.subject != "" {
			label = append(label, gitxSubjectSpans(" "+cm.subject)...)
		}
		it := lens.Item{Label: label}
		if cm.author != "" || cm.date != "" {
			it.Detail = []lens.Span{{Text: strings.TrimSpace(cm.author + " " + cm.date), Style: "muted"}}
		}
		if len(cm.stats) > 0 {
			// `--stat` rows belong to the commit above them.
			it.Detail = append(it.Detail, lens.Span{Text: strings.Join(cm.stats, " · "), Style: "muted"})
		}
		items = append(items, it)
	}
	return lens.List{Items: items}
}

// graphTable renders a `--graph --oneline` listing as a table whose first column
// is the graph: connector-only rows keep their glyphs, so the picture between
// commits stays intact (Terminal keeps a commit graph; `ki-graph`).
func (c *gitxCommits) graphTable(limit int) lens.Node {
	commits := c.all()
	rows := make([][]lens.Cell, 0, len(c.rows)+1)
	for i, r := range c.rows {
		if limit > 0 && i >= limit {
			rows = append(rows, []lens.Cell{
				{Spans: []lens.Span{{Text: "…", Style: "muted"}}},
				{}, {},
			})
			break
		}
		graphCell := lens.Cell{Spans: []lens.Span{{Text: r.graph, Style: "muted mono"}}}
		if r.commit < 0 || r.commit >= len(commits) {
			rows = append(rows, []lens.Cell{graphCell, {}, {}})
			continue
		}
		cm := commits[r.commit]
		subject := make([]lens.Span, 0, 4)
		if cm.subject != "" {
			subject = append(subject, gitxSubjectSpans(" "+cm.subject)...)
		}
		rows = append(rows, []lens.Cell{
			graphCell,
			{Spans: []lens.Span{{Text: cm.hash, Style: "num"}}},
			{Spans: subject},
		})
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "graph", Head: "", Priority: 0},
			{ID: "hash", Head: "COMMIT", Priority: 1},
			{ID: "subject", Head: "SUBJECT", Grow: 1, Truncate: lens.TruncateMiddle},
		},
		Rows: rows,
	}
}

// gitxSubjectSpans styles a commit subject: `--decorate` refs are accent spans
// (HEAD strong, `tag:` warning), the message follows as plain text.
func gitxSubjectSpans(sub string) []lens.Span {
	trimmed := strings.TrimLeft(sub, " ")
	if !strings.HasPrefix(trimmed, "(") {
		return []lens.Span{{Text: sub}}
	}
	end := strings.IndexByte(trimmed, ')')
	if end < 0 {
		return []lens.Span{{Text: sub}}
	}
	out := []lens.Span{{Text: " (", Style: "muted"}}
	for i, ref := range strings.Split(trimmed[1:end], ", ") {
		if i > 0 {
			out = append(out, lens.Span{Text: ", ", Style: "muted"})
		}
		style := "accent"
		switch {
		case strings.HasPrefix(ref, "HEAD -> "), ref == "HEAD":
			style = "accent strong"
		case strings.HasPrefix(ref, "tag: "):
			style = "warning"
		}
		out = append(out, lens.Span{Text: ref, Style: style})
	}
	out = append(out, lens.Span{Text: ")", Style: "muted"})
	if rest := trimmed[end+1:]; rest != "" {
		out = append(out, lens.Span{Text: rest})
	}
	return out
}

// gitxStatRow reports whether a line is one of `git diff --stat`'s rows:
// "path | 3 ++-" or the "N files changed" summary.
func gitxStatRow(t string) bool {
	if strings.Contains(t, " | ") {
		right := strings.TrimSpace(t[strings.Index(t, " | ")+3:])
		f := strings.Fields(right)
		if len(f) == 0 {
			return false
		}
		if _, ok := parseNumber(f[0]); ok {
			return true
		}
		return strings.Contains(right, "Bin ")
	}
	if strings.Contains(t, "changed") && (strings.Contains(t, "insertion") || strings.Contains(t, "deletion")) {
		return true
	}
	return false
}

// gitxSplitDate splits "Wed Oct 21 16:07:04 2026 +0800 rest" into the date (up
// to and including the zone) and whatever followed on the same line.
func gitxSplitDate(s string) (date, tail string) {
	f := strings.Fields(s)
	for i, tok := range f {
		if len(tok) == 5 && (tok[0] == '+' || tok[0] == '-') && allDigits(tok[1:]) {
			date = strings.Join(f[:i+1], " ")
			if i+1 < len(f) {
				tail = strings.Join(f[i+1:], " ")
			}
			return date, tail
		}
	}
	return s, ""
}

// gitxSplitStatTail splits "second a.txt | 3 ++-" into the subject ("second")
// and a stat row ("a.txt | 3 ++-"). Without a stat marker the whole tail is the
// subject.
func gitxSplitStatTail(tail string) (subject, statRow string) {
	i := strings.Index(tail, " | ")
	if i < 0 {
		return tail, ""
	}
	f := strings.Fields(tail[:i])
	if len(f) == 0 {
		return tail, ""
	}
	path := f[len(f)-1]
	return strings.Join(f[:len(f)-1], " "), path + " | " + tail[i+3:]
}

// ---------------------------------------------------------------- git.diff

type gitxDiff struct {
	lines    []string
	path     string
	files    []string // `--name-only` / `--name-status` rows
	nameOnly bool
	nameStat bool
}

func (l *gitxDiff) ID() string { return "git.diff" }

func (l *gitxDiff) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "diff"
}

func (l *gitxDiff) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	d := &gitxDiff{}
	for _, a := range cmd.Args {
		switch {
		case a == "--name-only":
			d.nameOnly = true
		case a == "--name-status":
			d.nameStat = true
		}
	}
	return d, nil
}

func (l *gitxDiff) Line(st any, ln model.Line) error {
	d := st.(*gitxDiff)
	if d.nameOnly || d.nameStat {
		// One path per line (or "<status>\t<path>"); ConPTY may drop the tab.
		if t := strings.TrimSpace(ln.Text); t != "" {
			if strings.HasPrefix(t, "warning:") || strings.HasPrefix(t, "hint:") {
				return nil // git's advice, not a changed path
			}
			if d.nameStat {
				f := strings.Fields(t)
				if len(f) >= 2 && len(f[0]) <= 2 {
					d.files = append(d.files, strings.Join(f[1:], " "))
					return nil
				}
			}
			d.files = append(d.files, t)
		}
		return nil
	}
	d.lines = append(d.lines, ln.Text)
	if d.path == "" {
		if p, ok := strings.CutPrefix(ln.Text, "+++ b/"); ok {
			d.path = strings.TrimSpace(p)
		} else if p, ok := strings.CutPrefix(ln.Text, "+++ "); ok {
			d.path = strings.TrimSpace(p)
			if d.path == "/dev/null" {
				d.path = ""
			}
		}
	}
	return nil
}

// gitxStatView renders `git diff --stat`: one row per file with its change
// count and the +/− graph, under the summary line git prints.
func gitxStatView(lines []string) (lens.Node, bool) {
	cols := []lens.Col{
		{ID: "file", Head: "File", Grow: 4, Truncate: lens.TruncateMiddle},
		{ID: "changes", Head: "Changes", Align: lens.AlignEnd, Priority: 2},
		{ID: "graph", Head: "Graph", Grow: 2},
	}
	var rows [][]lens.Cell
	summary := ""
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "warning:"), strings.HasPrefix(t, "hint:"):
			continue
		case strings.Contains(t, "changed") && (strings.Contains(t, "insertion") || strings.Contains(t, "deletion") || strings.Contains(t, "file changed")):
			summary = t
			continue
		}
		file, rest, ok := strings.Cut(t, " | ")
		if !ok {
			return nil, false // a line that is not stat output
		}
		file = strings.TrimSpace(file)
		rest = strings.TrimSpace(rest)
		if file == "" || rest == "" {
			return nil, false
		}
		changes, graph := rest, ""
		if f := strings.Fields(rest); len(f) > 0 && !strings.HasPrefix(rest, "Bin") {
			changes, graph = f[0], strings.Join(f[1:], "")
		}
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: file, Style: "path"}}},
			{Spans: []lens.Span{{Text: changes, Style: "num"}}},
			{Spans: gitxGraphSpans(graph)},
		})
	}
	if len(rows) == 0 {
		// `git diff --shortstat` (and `--stat` on a binary-only change) prints the
		// summary line alone; the lens still shows it instead of raw text.
		if summary != "" {
			return lens.Spans(lens.Span{Text: summary, Style: "muted"}), true
		}
		return nil, false
	}
	children := []lens.Node{}
	if summary != "" {
		children = append(children, lens.Spans(lens.Span{Text: summary, Style: "muted"}))
	}
	children = append(children, lens.Table{Cols: cols, Rows: rows})
	return lens.Cols(children...), true
}

// gitxGraphSpans colours a stat graph: '+' additions, '-' deletions.
func gitxGraphSpans(graph string) []lens.Span {
	if graph == "" {
		return nil
	}
	var out []lens.Span
	var buf strings.Builder
	cur := "" // "", "success", "error"
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, lens.Span{Text: buf.String(), Style: cur + " mono"})
		buf.Reset()
	}
	for _, r := range graph {
		next := ""
		switch r {
		case '+':
			next = "success"
		case '-':
			next = "error"
		}
		if next != cur {
			flush()
			cur = next
		}
		buf.WriteRune(r)
	}
	flush()
	return out
}

func (l *gitxDiff) Finish(any, int) error       { return nil }
func (l *gitxDiff) Event(any, lens.Event) error { return nil }

func (l *gitxDiff) View(st any) (lens.Node, error) {
	d := st.(*gitxDiff)
	text := strings.Join(d.lines, "\n")
	// `--name-only` / `--name-status` print nothing but a file list: one row per
	// path, no patch to parse (Terminal's git lens shows the changed files).
	if (d.nameOnly || d.nameStat) && len(d.files) > 0 {
		return lens.Column{
			Children: []lens.Node{
				lens.Spans(lens.Span{Text: plural(len(d.files), "file changed", "files changed"), Style: "muted"}),
				lens.List{Items: gitxFileItems(d.files)},
			},
		}, nil
	}
	if !strings.Contains(text, "@@") && !strings.Contains(text, "diff --git") {
		// `git diff --stat` prints no hunks; Terminal's git lens still shows the
		// per-file change counts instead of falling back to raw text.
		if tbl, ok := gitxStatView(d.lines); ok {
			return tbl, nil
		}
		return nil, nil // not a diff
	}
	added, removed := 0, 0
	for _, ln := range d.lines {
		switch {
		case strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++"):
			added++
		case strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---"):
			removed++
		}
	}
	head := []lens.Span{
		{Text: d.path, Style: "path"},
		{Text: " +" + itoaSmall(added), Style: "success"},
		{Text: " −" + itoaSmall(removed), Style: "error"},
	}
	return lens.Cols(
		lens.Spans(head...),
		lens.Diff{Text: text, Path: d.path},
	), nil
}

// ---------------------------------------------------------------- git.log

type gitxLog struct{ gitxCommits }

func (l *gitxLog) ID() string { return "git.log" }

func (l *gitxLog) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "log"
}

func (l *gitxLog) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	g := &gitxLog{}
	g.fileMode = gitxNameMode(cmd)
	g.fileStatus = gitxNameStatus(cmd)
	return g, nil
}

func (l *gitxLog) Line(st any, ln model.Line) error {
	st.(*gitxLog).line(ln.Text)
	return nil
}

func (l *gitxLog) Finish(st any, _ int) error {
	st.(*gitxLog).add() // finalize the last commit once the output ended
	return nil
}

func (l *gitxLog) Event(any, lens.Event) error { return nil }

func (l *gitxLog) View(st any) (lens.Node, error) {
	g := st.(*gitxLog)
	node := g.node(200)
	if len(g.files) > 0 {
		return gitxWithFiles(node, g.files), nil
	}
	return node, nil
}

// ---------------------------------------------------------------- git.show

type gitxShow struct {
	gitxCommits
	diff []string
}

func (l *gitxShow) ID() string { return "git.show" }

func (l *gitxShow) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "show"
}

func (l *gitxShow) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	s := &gitxShow{}
	s.fileMode = gitxNameMode(cmd)
	s.fileStatus = gitxNameStatus(cmd)
	return s, nil
}

func (l *gitxShow) Line(st any, ln model.Line) error {
	s := st.(*gitxShow)
	if strings.HasPrefix(ln.Text, "diff --git") || strings.HasPrefix(ln.Text, "@@") ||
		strings.HasPrefix(ln.Text, "index ") || strings.HasPrefix(ln.Text, "+++") ||
		strings.HasPrefix(ln.Text, "---") || (len(s.diff) > 0 && s.gitxCommits.cur == nil) {
		s.diff = append(s.diff, ln.Text)
		return nil
	}
	s.line(ln.Text)
	return nil
}

func (l *gitxShow) Finish(st any, _ int) error {
	st.(*gitxShow).add()
	return nil
}

func (l *gitxShow) Event(any, lens.Event) error { return nil }

func (l *gitxShow) View(st any) (lens.Node, error) {
	s := st.(*gitxShow)
	summary := s.node(1)
	// A recovered `--stat` row (the line ConPTY glued to the date) renders with
	// the same stat view as `git diff --stat`.
	if len(s.statTail) > 0 {
		if statView, ok := gitxStatView(s.statTail); ok {
			if summary == nil {
				return statView, nil
			}
			return lens.Cols(summary, statView), nil
		}
	}
	if len(s.files) > 0 {
		summary = gitxWithFiles(summary, s.files)
	}
	if len(s.diff) == 0 {
		return summary, nil
	}
	text := strings.Join(s.diff, "\n")
	if summary == nil {
		return lens.Diff{Text: text}, nil
	}
	return lens.Cols(summary, lens.Diff{Text: text}), nil
}

// ---------------------------------------------------------------- git.reflog

type gitxReflog struct{ items []lens.Item }

func (l *gitxReflog) ID() string { return "git.reflog" }

func (l *gitxReflog) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "reflog"
}

func (l *gitxReflog) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxReflog{}, nil }

func (l *gitxReflog) Line(st any, ln model.Line) error {
	r := st.(*gitxReflog)
	t := gitxGraph(strings.TrimSpace(ln.Text))
	fields := strings.SplitN(t, " ", 2)
	if !gitxHash(fields[0]) {
		return nil
	}
	label := []lens.Span{{Text: fields[0], Style: "num"}}
	detail := []lens.Span{}
	if len(fields) > 1 {
		label = append(label, lens.Span{Text: " " + fields[1], Style: "muted"})
	}
	r.items = append(r.items, lens.Item{Label: label, Detail: detail})
	return nil
}

func (l *gitxReflog) Finish(any, int) error       { return nil }
func (l *gitxReflog) Event(any, lens.Event) error { return nil }

func (l *gitxReflog) View(st any) (lens.Node, error) {
	r := st.(*gitxReflog)
	if len(r.items) == 0 {
		return nil, nil
	}
	return lens.List{Items: r.items}, nil
}

// gitxTrackSpans styles a branch's tracking state the way Terminal names it: a
// "gone" upstream is an error, "ahead N" is progress and "behind N" is a warning.
func gitxTrackSpans(track string) []lens.Span {
	if track == "" {
		return nil
	}
	low := strings.ToLower(track)
	if strings.Contains(low, "gone") {
		return []lens.Span{{Text: "gone", Style: "error"}}
	}
	var out []lens.Span
	if n, ok := gitxCount(track, "ahead"); ok {
		out = append(out, lens.Span{Text: "ahead " + n, Style: "success"})
	}
	if n, ok := gitxCount(track, "behind"); ok {
		if len(out) > 0 {
			out = append(out, lens.Span{Text: ", ", Style: "muted"})
		}
		out = append(out, lens.Span{Text: "behind " + n, Style: "warning"})
	}
	if len(out) == 0 {
		return []lens.Span{{Text: track, Style: "num"}}
	}
	return out
}

// gitxCount reads "ahead N" / "behind N" out of a tracking string.
func gitxCount(track, word string) (string, bool) {
	i := strings.Index(strings.ToLower(track), word)
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSpace(track[i+len(word):])
	f := strings.Fields(rest)
	if len(f) == 0 {
		return "", false
	}
	n := strings.Trim(f[0], ",")
	if _, ok := parseNumber(n); !ok {
		return "", false
	}
	return n, true
}

// ---------------------------------------------------------------- git.branch

type gitxBranch struct{ rows [][]lens.Cell }

func (l *gitxBranch) ID() string { return "git.branch" }

func (l *gitxBranch) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "branch"
}

func (l *gitxBranch) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxBranch{}, nil }

func (l *gitxBranch) Line(st any, ln model.Line) error {
	b := st.(*gitxBranch)
	t := strings.TrimSpace(ln.Text)
	if t == "" || strings.HasPrefix(t, "(") || gitxErrorLine(t) {
		return nil
	}
	// A branch row is "* name [upstream]" or "  name": current marker, an
	// upstream bracket, or at most a couple of words. Prose is longer.
	if !strings.HasPrefix(t, "* ") && !strings.Contains(t, "[") && len(strings.Fields(t)) > 3 {
		return nil
	}
	current := ""
	if strings.HasPrefix(t, "* ") {
		current = "current"
		t = strings.TrimSpace(t[2:])
	}
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return nil
	}
	name := fields[0]
	upstream, track := "", ""
	if i := strings.IndexByte(t, '['); i >= 0 {
		if j := strings.IndexByte(t, ']'); j > i {
			up := t[i+1 : j]
			up = strings.SplitN(up, ":", 2)[0]
			upstream = strings.TrimSpace(up)
			if j > i {
				if k := strings.IndexByte(t[i+1:j], ':'); k >= 0 {
					track = strings.TrimSpace(t[i+1+k+1 : j])
				}
			}
		}
	}
	nameSpans := []lens.Span{{Text: name, Style: "strong"}}
	if current != "" {
		nameSpans = append([]lens.Span{{Text: "* ", Style: "success"}}, nameSpans...)
	}
	row := []lens.Cell{
		{Spans: nameSpans},
		{Spans: []lens.Span{{Text: upstream, Style: "muted"}}},
		{Spans: gitxTrackSpans(track)},
	}
	b.rows = append(b.rows, row)
	return nil
}

func (l *gitxBranch) Finish(any, int) error       { return nil }
func (l *gitxBranch) Event(any, lens.Event) error { return nil }

func (l *gitxBranch) View(st any) (lens.Node, error) {
	b := st.(*gitxBranch)
	if len(b.rows) == 0 {
		return nil, nil
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "branch", Head: "BRANCH", Grow: 4},
			{ID: "upstream", Head: "UPSTREAM", Grow: 3},
			{ID: "track", Head: "TRACK", Align: lens.AlignEnd, Grow: 1},
		},
		Rows: b.rows,
	}, nil
}

// ---------------------------------------------------------------- git.remote

type gitxRemote struct {
	rows  [][]lens.Cell
	kinds bool // any row carried a (fetch)/(push) suffix
}

func (l *gitxRemote) ID() string { return "git.remote" }

func (l *gitxRemote) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "remote"
}

func (l *gitxRemote) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxRemote{}, nil }

func (l *gitxRemote) Line(st any, ln model.Line) error {
	r := st.(*gitxRemote)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	if gitxErrorLine(t) {
		return nil // "fatal: not a git repository …" is not a remote
	}
	// `git remote -v` appends " (fetch)" / " (push)". Strip that first: ConPTY
	// may have glued the name and the URL together, and the only space left then
	// belongs to the kind suffix.
	kind := ""
	if strings.HasSuffix(t, "(fetch)") || strings.HasSuffix(t, "(push)") {
		if i := strings.LastIndex(t, " ("); i > 0 {
			kind = strings.TrimSpace(t[i+1:])
			t = strings.TrimSpace(t[:i])
		}
	}
	if gitxURLIndex(t) < 0 && strings.Contains(t, " ") {
		return nil // prose, not a remote line
	}
	name, url := t, ""
	switch {
	case strings.Contains(t, "\t"):
		if i := strings.IndexByte(t, '\t'); i > 0 {
			name, url = t[:i], strings.TrimSpace(t[i:])
		}
	case gitxURLIndex(t) > 0:
		// ConPTY re-serialises git's tab away: "originhttps://…".
		i := gitxURLIndex(t)
		name, url = t[:i], t[i:]
	default:
		if i := strings.IndexByte(t, ' '); i > 0 {
			name, url = t[:i], strings.TrimSpace(t[i:])
		}
	}
	// The capture screen expands git's tab into cells, so a split can leave padding
	// around the name and the URL.
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	if kind != "" {
		r.kinds = true
	}
	r.rows = append(r.rows, []lens.Cell{
		{Spans: []lens.Span{{Text: name, Style: "strong"}}},
		{Spans: []lens.Span{{Text: url, Style: "path"}}},
		{Spans: []lens.Span{{Text: kind, Style: "muted"}}},
	})
	return nil
}

func (l *gitxRemote) Finish(any, int) error       { return nil }
func (l *gitxRemote) Event(any, lens.Event) error { return nil }

func (l *gitxRemote) View(st any) (lens.Node, error) {
	r := st.(*gitxRemote)
	if len(r.rows) == 0 {
		return nil, nil
	}
	cols := []lens.Col{
		{ID: "name", Head: "REMOTE", Grow: 1},
		{ID: "url", Head: "URL", Grow: 6},
	}
	if r.kinds {
		cols = append(cols, lens.Col{ID: "kind", Head: "KIND", Priority: 1})
	}
	rows := r.rows
	if !r.kinds {
		// Drop the always-empty kind cell when no row has one.
		trimmed := make([][]lens.Cell, 0, len(rows))
		for _, row := range rows {
			trimmed = append(trimmed, row[:2])
		}
		rows = trimmed
	}
	return lens.Table{
		Cols: cols,
		Rows: rows,
	}, nil
}

// gitxURLIndex returns the index where a remote URL starts inside s, or -1.
func gitxURLIndex(s string) int {
	best := -1
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://", "git@"} {
		if i := strings.Index(s, p); i > 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// ---------------------------------------------------------------- git.tag

type gitxTag struct{ items []lens.Item }

func (l *gitxTag) ID() string { return "git.tag" }

func (l *gitxTag) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "tag"
}

func (l *gitxTag) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxTag{}, nil }

func (l *gitxTag) Line(st any, ln model.Line) error {
	t := st.(*gitxTag)
	s := strings.TrimSpace(ln.Text)
	if s == "" || !gitxTagLike(s) {
		return nil
	}
	// `git tag -n` pads the tag name and appends the annotation (or the commit
	// subject for a lightweight tag): "v1.0.0          first commit".
	name, note := gitxTagNameNote(s)
	label := []lens.Span{{Text: name, Style: "mark"}}
	if note != "" {
		label = append(label, lens.Span{Text: note, Style: "muted"})
	}
	t.items = append(t.items, lens.Item{Label: label})
	return nil
}

func (l *gitxTag) Finish(any, int) error       { return nil }
func (l *gitxTag) Event(any, lens.Event) error { return nil }

func (l *gitxTag) View(st any) (lens.Node, error) {
	t := st.(*gitxTag)
	if len(t.items) == 0 {
		return nil, nil
	}
	return lens.Column{Children: []lens.Node{
		lens.Badge{Text: itoaSmall(len(t.items)) + " tags", Tone: "info"},
		lens.List{Items: t.items},
	}}, nil
}

// gitxTagNameNote splits a `git tag -n` line into the tag name (which cannot
// contain spaces) and the annotation that follows it.
func gitxTagNameNote(s string) (name, note string) {
	if i := strings.IndexByte(s, ' '); i > 0 {
		name := strings.TrimSpace(s[:i])
		rest := strings.TrimLeft(s[i:], " \t")
		if rest != "" && gitxTagLike(name) {
			return name, "  " + rest
		}
	}
	return s, ""
}

// gitxMode reports whether s is a git file mode ("100644", "040000").
func gitxMode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- git.files

type gitxFiles struct{ rows [][]lens.Cell }

func (l *gitxFiles) ID() string { return "git.files" }

func (l *gitxFiles) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "ls-files"
}

func (l *gitxFiles) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxFiles{}, nil }

func (l *gitxFiles) Line(st any, ln model.Line) error {
	f := st.(*gitxFiles)
	t := strings.TrimRight(ln.Text, " ")
	if strings.TrimSpace(t) == "" || gitxErrorLine(t) {
		return nil
	}
	if !gitxPathLike(strings.TrimSpace(t)) {
		return nil
	}
	// `--stage`: <mode> <object> <stage>\t<path>
	left, path := "", ""
	if l, r, ok := strings.Cut(t, "\t"); ok {
		left, path = l, r
	} else if f := strings.Fields(t); len(f) == 4 && gitxHash(f[1]) {
		left, path = strings.Join(f[:3], " "), f[3]
	} else if f := strings.Fields(t); len(f) >= 3 && gitxMode(f[0]) && gitxHash(f[1]) &&
		len(f[2]) > 1 && f[2][0] >= '0' && f[2][0] <= '3' {
		// ConPTY dropped the tab between the stage digit and the path:
		// "100644 <object> 0a.txt" — the stage is the single digit.
		stage := f[2][:1]
		left = strings.Join(f[:2], " ") + " " + stage
		path = f[2][1:]
		if rest := strings.Join(f[3:], " "); rest != "" {
			path += " " + rest
		}
	}
	if path != "" {
		fields := strings.Fields(left)
		if len(fields) == 3 {
			f.rows = append(f.rows, []lens.Cell{
				{Spans: []lens.Span{{Text: fields[0], Style: "muted"}}},
				{Spans: []lens.Span{{Text: fields[1], Style: "num"}}},
				{Spans: []lens.Span{{Text: fields[2], Style: "num"}}},
				{Spans: []lens.Span{{Text: path, Style: "path"}}},
			})
			return nil
		}
	}
	f.rows = append(f.rows, []lens.Cell{
		{Spans: []lens.Span{{Text: strings.TrimSpace(t), Style: "path"}}},
	})
	return nil
}

func (l *gitxFiles) Finish(any, int) error       { return nil }
func (l *gitxFiles) Event(any, lens.Event) error { return nil }

func (l *gitxFiles) View(st any) (lens.Node, error) {
	f := st.(*gitxFiles)
	if len(f.rows) == 0 {
		return nil, nil
	}
	if len(f.rows[0]) == 1 {
		items := make([]lens.Item, 0, len(f.rows))
		for _, r := range f.rows {
			items = append(items, lens.Item{Label: []lens.Span{{Text: r[0].Spans[0].Text, Style: "path"}}})
		}
		return lens.List{Items: items}, nil
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "mode", Head: "MODE", Priority: 2},
			{ID: "object", Head: "OBJECT", Priority: 1},
			{ID: "stage", Head: "STAGE", Align: lens.AlignEnd, Priority: 3},
			{ID: "path", Head: "PATH", Grow: 6},
		},
		Rows: f.rows,
	}, nil
}

// ---------------------------------------------------------------- git.shortlog

type gitxShortlog struct {
	rows   [][]lens.Cell
	groups []gitxShortGroup
	cur    *gitxShortGroup
}

// gitxShortGroup is one author of `git shortlog`'s grouped output.
type gitxShortGroup struct {
	author, count string
	subjects      []string
}

func (l *gitxShortlog) ID() string { return "git.shortlog" }

func (l *gitxShortlog) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "shortlog"
}

func (l *gitxShortlog) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxShortlog{}, nil }

func (l *gitxShortlog) Line(st any, ln model.Line) error {
	s := st.(*gitxShortlog)
	raw := ln.Text
	t := strings.TrimSpace(raw)
	if t == "" {
		s.cur = nil // a blank line ends a group
		return nil
	}
	// `git shortlog` (without -n) groups the subjects under "Author (N):". Only
	// an open group makes an indented line a subject; the numeric form's
	// "<spaces><count><author>" lines are indented too.
	if s.cur != nil && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
		if len(s.cur.subjects) < 4 {
			s.cur.subjects = append(s.cur.subjects, t)
		}
		return nil
	}
	if i := strings.LastIndex(t, " ("); i > 0 && strings.HasSuffix(t, "):") && !gitxHash(t[:i]) {
		author := strings.TrimSpace(t[:i])
		count := strings.TrimSuffix(t[i+2:], "):")
		if _, ok := parseNumber(strings.TrimSpace(count)); ok && author != "" {
			s.groups = append(s.groups, gitxShortGroup{author: author, count: strings.TrimSpace(count)})
			s.cur = &s.groups[len(s.groups)-1]
			return nil
		}
	}
	// `git shortlog -sn` prints "<spaces><count><tab><author>"; ConPTY
	// re-serialises the tab away, so read the count as the leading digits and
	// take the rest as the author.
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 {
		return nil
	}
	n, ok := parseNumber(t[:i])
	if !ok {
		return nil
	}
	author := strings.TrimSpace(t[i:])
	if author == "" {
		return nil
	}
	s.rows = append(s.rows, []lens.Cell{
		{Spans: []lens.Span{{Text: itoaSmall(n), Style: "num"}}},
		{Spans: []lens.Span{{Text: author, Style: "strong"}}},
	})
	return nil
}
func (l *gitxShortlog) Finish(any, int) error       { return nil }
func (l *gitxShortlog) Event(any, lens.Event) error { return nil }

func (l *gitxShortlog) View(st any) (lens.Node, error) {
	s := st.(*gitxShortlog)
	// Grouped form: one item per author with the commit subjects underneath.
	if len(s.groups) > 0 {
		items := make([]lens.Item, 0, len(s.groups))
		for _, g := range s.groups {
			label := []lens.Span{
				{Text: g.author, Style: "strong"},
				{Text: "  " + g.count, Style: "num"},
			}
			it := lens.Item{Label: label}
			if len(g.subjects) > 0 {
				it.Detail = []lens.Span{{Text: strings.Join(g.subjects, " · "), Style: "muted"}}
			}
			items = append(items, it)
		}
		return lens.List{Items: items}, nil
	}
	if len(s.rows) == 0 {
		return nil, nil
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "count", Head: "COMMITS", Align: lens.AlignEnd, Grow: 1},
			{ID: "author", Head: "AUTHOR", Grow: 5},
		},
		Rows: s.rows,
	}, nil
}

// ---------------------------------------------------------------- git.blame

type gitxBlame struct{ rows [][]lens.Cell }

func (l *gitxBlame) ID() string { return "git.blame" }

func (l *gitxBlame) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && sub == "blame"
}

func (l *gitxBlame) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxBlame{}, nil }

func (l *gitxBlame) Line(st any, ln model.Line) error {
	b := st.(*gitxBlame)
	// A boundary commit is marked with a caret: "^e51322d (T … 1) line".
	t := strings.TrimPrefix(ln.Text, "^")
	hash := ""
	if i := strings.IndexByte(t, ' '); i > 0 {
		if gitxHash(t[:i]) {
			hash = t[:i]
			t = t[i+1:]
		}
	}
	if hash == "" {
		return nil
	}
	// <hash> (<author> <date> <line>) <code>   or   <hash> <path> (<author> <date> <line>) <code>
	author, lineNo, code := "", "", strings.TrimLeft(t, " ")
	if i := strings.IndexByte(t, '('); i >= 0 {
		if j := strings.IndexByte(t[i:], ')'); j > 0 {
			inner := t[i+1 : i+j]
			fields := strings.Fields(inner)
			if len(fields) > 0 {
				author = fields[0]
			}
			for k := len(fields) - 1; k >= 0; k-- {
				if _, ok := parseNumber(fields[k]); ok {
					lineNo = fields[k]
					break
				}
			}
			code = strings.TrimLeft(t[i+j+1:], " ")
		}
	}
	b.rows = append(b.rows, []lens.Cell{
		{Spans: []lens.Span{{Text: hash, Style: "num"}}},
		{Spans: []lens.Span{{Text: author, Style: "muted"}}},
		{Spans: []lens.Span{{Text: lineNo, Style: "num"}}},
		{Spans: []lens.Span{{Text: code, Style: "code"}}},
	})
	return nil
}

func (l *gitxBlame) Finish(any, int) error       { return nil }
func (l *gitxBlame) Event(any, lens.Event) error { return nil }

func (l *gitxBlame) View(st any) (lens.Node, error) {
	b := st.(*gitxBlame)
	if len(b.rows) == 0 {
		return nil, nil
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "commit", Head: "COMMIT", Priority: 1},
			{ID: "author", Head: "AUTHOR", Priority: 2, Grow: 2},
			{ID: "line", Head: "LINE", Align: lens.AlignEnd, Priority: 3},
			{ID: "code", Head: "CODE", Grow: 6, Truncate: lens.TruncateEnd},
		},
		Rows: b.rows,
	}, nil
}

// ---------------------------------------------------------------- git.transfer

type gitxTransfer struct {
	lines   []string
	styled  []string // lines with SGR escapes, for lens.ANSI
	summary string
	failed  bool
}

func (l *gitxTransfer) ID() string { return "git.transfer" }

func (l *gitxTransfer) Match(cmd model.Cmd) bool {
	sub, _, ok := gitxSub(cmd)
	return ok && (sub == "fetch" || sub == "push" || sub == "pull")
}

func (l *gitxTransfer) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxTransfer{}, nil }

func (l *gitxTransfer) Line(st any, ln model.Line) error {
	g := st.(*gitxTransfer)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	g.lines = append(g.lines, ln.Text)
	if ln.Styled != "" {
		g.styled = append(g.styled, ln.Styled)
	}
	switch {
	case strings.HasPrefix(t, "To "), strings.HasPrefix(t, "From "):
		g.summary = t
	case strings.Contains(t, "->"):
		g.summary = t
	case strings.HasPrefix(t, "!"), strings.HasPrefix(t, "error:"), strings.HasPrefix(t, "fatal:"):
		g.failed = true
	}
	return nil
}

func (l *gitxTransfer) Finish(any, int) error       { return nil }
func (l *gitxTransfer) Event(any, lens.Event) error { return nil }

func (l *gitxTransfer) View(st any) (lens.Node, error) {
	g := st.(*gitxTransfer)
	if len(g.lines) == 0 {
		return nil, nil
	}
	// Evidence gate: fetch/push prints object counts, a destination, a ref
	// update or a rejection. Without one of them this is not transfer output.
	transfer := false
	for _, ln := range g.lines {
		t := strings.TrimSpace(ln)
		switch {
		case strings.Contains(t, "->"), strings.HasPrefix(t, "To "),
			strings.HasPrefix(t, "From "), strings.HasPrefix(t, "remote:"),
			strings.Contains(t, "Enumerating objects"), strings.Contains(t, "Counting objects"),
			strings.Contains(t, "Writing objects"), strings.HasPrefix(t, "Total "),
			strings.HasPrefix(t, "* [new"), strings.HasPrefix(t, "! [rejected]"),
			strings.Contains(t, "Compressing objects"):
			transfer = true
		}
	}
	if !transfer {
		return nil, nil // prose or a bare error: keep the block raw
	}
	tone := "success"
	if g.failed {
		tone = "error"
	}
	head := []lens.Span{{Text: "git transfer", Style: "strong"}}
	if g.summary != "" {
		head = append(head, lens.Span{Text: " " + g.summary, Style: "path"})
	}
	return lens.Column{Children: []lens.Node{
		lens.Spans(head...),
		lens.Badge{Text: map[bool]string{true: "failed", false: "done"}[g.failed], Tone: tone},
		lens.Section{Head: []lens.Span{{Text: "Traffic", Style: "muted"}},
			Children: []lens.Node{gitTrafficBody(g)}},
	}}, nil
}

// gitTrafficBody keeps git's transfer colours when it printed any (progress
// lines are often redrawn with CR, which the ANSI mini terminal handles).
func gitTrafficBody(g *gitxTransfer) lens.Node {
	if len(g.styled) > 0 {
		return lens.ANSI{Text: strings.Join(g.styled, "\n")}
	}
	return lens.Code{Text: strings.Join(g.lines, "\n"), Lang: "text", Clamp: 40}
}

// ---------------------------------------------------------------- helpers

// gitxPathLike reports whether s looks like a repository path rather than a
// word of prose.
func gitxPathLike(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	if strings.ContainsAny(s, "/\\") {
		return true
	}
	if i := strings.IndexByte(s, '.'); i > 0 && i < len(s)-1 {
		return true
	}
	return false
}

// gitxErrorLine reports whether the line is a git error, which the lenses show
// as raw output rather than as a table row.
func gitxErrorLine(s string) bool {
	return strings.HasPrefix(s, "fatal:") || strings.HasPrefix(s, "error:") ||
		strings.HasPrefix(s, "warning:") || strings.HasPrefix(s, "hint:")
}

// gitxTagLike reports whether s starts with something that can be a tag name:
// a version ("v1.2.3"), a name without spaces, or a name plus a message.
func gitxTagLike(s string) bool {
	if gitxErrorLine(s) {
		return false
	}
	first := strings.Fields(s)[0]
	if first == "" || len(first) > 64 || strings.HasSuffix(first, ":") {
		return false
	}
	// Real tag names carry a version digit or a separator ("release-1.2",
	// "nightly/2026-01"); prose words do not.
	if first[0] >= '0' && first[0] <= '9' {
		return true
	}
	if first[0] == 'v' || first[0] == 'V' {
		if len(first) > 1 && first[1] >= '0' && first[1] <= '9' {
			return true
		}
	}
	if strings.ContainsAny(first, "-_./@") {
		return true
	}
	for i := 0; i < len(first); i++ {
		if first[i] >= '0' && first[i] <= '9' {
			return true
		}
	}
	return false
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func parseNumber(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// ---------------------------------------------------------------- git.stash

// gitxStash renders `git stash list`. Terminal has this lens too: its git id block
// names `git.stash` (findings/native-widgets.md, offsets 783234-783235) and the
// pager whitelist includes `stash` (findings/lens-conditions.md).
type gitxStash struct {
	entries []gitxStashEntry
	plain   []string
}

type gitxStashEntry struct {
	ref     string // stash@{0}
	branch  string // On main / WIP on main
	message string
}

func (l *gitxStash) ID() string { return "git.stash" }

// Match: `git stash list`, and bare `git stash` (which pushes, so it prints
// little and stays raw through the empty-view path).
func (l *gitxStash) Match(cmd model.Cmd) bool {
	sub, rest, ok := gitxSub(cmd)
	if !ok || sub != "stash" {
		return false
	}
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			return a == "list"
		}
	}
	return true
}

func (l *gitxStash) Open(model.Cmd, *lens.Ctx) (any, error) { return &gitxStash{}, nil }

func (l *gitxStash) Line(st any, ln model.Line) error {
	g := st.(*gitxStash)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	g.plain = append(g.plain, t)
	if e, ok := gitxStashLine(t); ok {
		g.entries = append(g.entries, e)
	}
	return nil
}

func (l *gitxStash) Finish(any, int) error       { return nil }
func (l *gitxStash) Event(any, lens.Event) error { return nil }

// gitxStashLine reads "stash@{0}: On main: wip" and
// "stash@{0}: WIP on main: 1a2b3c4 subject".
func gitxStashLine(t string) (gitxStashEntry, bool) {
	ref, rest, ok := strings.Cut(t, ": ")
	if !ok || !strings.HasPrefix(ref, "stash@{") {
		return gitxStashEntry{}, false
	}
	e := gitxStashEntry{ref: ref}
	switch {
	case strings.HasPrefix(rest, "On "), strings.HasPrefix(rest, "WIP on "):
		branch, msg, cut := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(rest, "WIP on "), "On "), ": ")
		e.branch = branch
		if cut {
			e.message = msg
		}
	default:
		e.message = rest
	}
	return e, true
}

func (l *gitxStash) View(st any) (lens.Node, error) {
	g := st.(*gitxStash)
	if len(g.entries) == 0 {
		return nil, nil // not a stash listing (e.g. the push that prints nothing)
	}
	rows := make([][]lens.Cell, 0, len(g.entries))
	for _, e := range g.entries {
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: e.ref, Style: "num code"}}},
			{Spans: []lens.Span{{Text: e.branch, Style: "muted"}}},
			{Spans: []lens.Span{{Text: e.message}}},
		})
	}
	head := "stashes"
	if len(g.entries) == 1 {
		head = "1 stash"
	} else {
		head = itoaSmall(len(g.entries)) + " stashes"
	}
	return lens.Card{
		Head: []lens.Span{{Text: head, Style: "strong"}},
		Tone: toneAccent,
		Children: []lens.Node{
			lens.Table{
				Cols: []lens.Col{
					{ID: "stash", Head: "Stash"},
					{ID: "branch", Head: "Branch", Priority: 1},
					{ID: "message", Head: "Message", Grow: 1, Truncate: lens.TruncateMiddle},
				},
				Rows: rows,
			},
		},
	}, nil
}

// gitxFileItems turns changed paths into list items (a `git.files`-like list).
func gitxFileItems(paths []string) []lens.Item {
	items := make([]lens.Item, 0, len(paths))
	for _, p := range paths {
		items = append(items, lens.Item{Label: []lens.Span{{Text: p, Style: "path"}}})
	}
	return items
}

// gitxNameMode reports whether the command asked for a name list instead of a
// patch (`--name-only` / `--name-status`).
func gitxNameMode(cmd model.Cmd) bool {
	for _, a := range cmd.Args {
		if a == "--name-only" || a == "--name-status" {
			return true
		}
	}
	return false
}

// gitxNameStatus reports whether the command asked for `--name-status`.
func gitxNameStatus(cmd model.Cmd) bool {
	for _, a := range cmd.Args {
		if a == "--name-status" {
			return true
		}
	}
	return false
}

// gitxStripStatus drops a leading `--name-status` letter when the tab that should
// separate it from the path was lost: "Ab.txt" → "b.txt".
func gitxStripStatus(t string) string {
	if len(t) < 3 {
		return t
	}
	switch t[0] {
	case 'A', 'M', 'D', 'R', 'C', 'T', 'U', 'X':
		rest := t[1:]
		if strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") {
			return strings.TrimSpace(rest)
		}
		if strings.ContainsAny(rest[:1], "abcdefghijklmnopqrstuvwxyz._/") {
			return rest
		}
	}
	return t
}

// gitxWithFiles appends a "Files" section listing the changed paths.
func gitxWithFiles(n lens.Node, files []string) lens.Node {
	sec := lens.Section{
		Head:     []lens.Span{{Text: "Files", Style: "muted"}},
		Children: []lens.Node{lens.List{Items: gitxFileItems(files)}},
	}
	if n == nil {
		return sec
	}
	return lens.Cols(n, sec)
}
