package builtin

import (
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

// The rest of the files family: which, stat, wc, du, find and tree. Ids and
// trigger surface are Terminal's (findings/lens-conditions.md §1.1, offsets
// 149947168-149947664); the views are Terminal's components (table/KV/list/tree).
func init() {
	for _, d := range []lens.Definition{
		{ID: "files.which", New: func() lens.Lens { return &filesWhich{} }},
		{ID: "files.stat", New: func() lens.Lens { return &filesStat{} }},
		{ID: "files.wc", New: func() lens.Lens { return &filesWC{} }},
		{ID: "files.du", New: func() lens.Lens { return &filesDU{} }},
		{ID: "files.find", New: func() lens.Lens { return &filesFind{} }},
		{ID: "files.tree", New: func() lens.Lens { return &filesTree{} }},
	} {
		lens.Register(d)
	}
}

// ---------------------------------------------------------------- files.which

type filesWhich struct {
	cmd   string
	paths []string
}

func (l *filesWhich) ID() string { return "files.which" }

func (l *filesWhich) Match(cmd model.Cmd) bool {
	switch cmd.Program {
	case "which", "where":
		return true
	}
	return false
}

func (l *filesWhich) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	name := ""
	for _, a := range cmd.Args {
		if !strings.HasPrefix(a, "-") {
			name = a
		}
	}
	return &filesWhich{cmd: name}, nil
}

func (l *filesWhich) Line(st any, ln model.Line) error {
	w := st.(*filesWhich)
	t := strings.TrimSpace(ln.Text)
	if t == "" || strings.HasPrefix(t, "which: no ") || strings.HasPrefix(t, "no ") {
		return nil
	}
	if !strings.ContainsAny(t, `/\`) && !strings.Contains(t, ":") {
		return nil // prose, not a program path
	}
	w.paths = append(w.paths, t)
	return nil
}

func (l *filesWhich) Finish(any, int) error       { return nil }
func (l *filesWhich) Event(any, lens.Event) error { return nil }

func (l *filesWhich) View(st any) (lens.Node, error) {
	w := st.(*filesWhich)
	if len(w.paths) == 0 {
		return nil, nil
	}
	rows := make([][]lens.Cell, 0, len(w.paths))
	for _, p := range w.paths {
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: w.cmd, Style: "strong"}}},
			{Spans: []lens.Span{{Text: p, Style: "path"}}},
		})
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "command", Head: "COMMAND", Grow: 1},
			{ID: "path", Head: "PATH", Grow: 6},
		},
		Rows: rows,
	}, nil
}

// ---------------------------------------------------------------- files.stat

type filesStat struct {
	file string
	kv   lens.KV
}

func (l *filesStat) ID() string { return "files.stat" }

func (l *filesStat) Match(cmd model.Cmd) bool { return cmd.Program == "stat" }

func (l *filesStat) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	name := ""
	for _, a := range cmd.Args {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "%") {
			name = a
		}
	}
	return &filesStat{file: name}, nil
}

func (l *filesStat) Line(st any, ln model.Line) error {
	s := st.(*filesStat)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	// GNU stat: "  Size: 1234        Blocks: 8    IO Block: 4096  regular file"
	// and "Access: (0644/-rw-r--r--)  Uid: (1000/me)   Gid: (1000/me)"
	key, rest, ok := strings.Cut(t, ":")
	if !ok {
		return nil
	}
	key = strings.TrimSpace(key)
	for _, pair := range strings.Split(strings.TrimSpace(rest), "  ") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v := key, pair
		if k2, v2, ok := strings.Cut(pair, ": "); ok && k2 != "" && !strings.Contains(k2, " ") {
			k, v = strings.TrimSpace(k2), strings.TrimSpace(v2)
		} else if k2, v2, ok := strings.Cut(pair, ":"); ok && k2 != "" && !strings.Contains(k2, " ") {
			k, v = strings.TrimSpace(k2), strings.TrimSpace(v2)
		}
		s.kv.Items = append(s.kv.Items, [2][]lens.Span{
			{{Text: k, Style: "muted"}},
			{{Text: v, Style: "num"}},
		})
	}
	return nil
}

func (l *filesStat) Finish(any, int) error       { return nil }
func (l *filesStat) Event(any, lens.Event) error { return nil }

func (l *filesStat) View(st any) (lens.Node, error) {
	s := st.(*filesStat)
	if len(s.kv.Items) == 0 {
		return nil, nil
	}
	head := s.file
	if head == "" {
		head = "stat"
	}
	return lens.Column{Gap: 6, Children: []lens.Node{
		lens.Spans(lens.Span{Text: head, Style: "path strong"}),
		s.kv,
	}}, nil
}

// wcHeader reports whether a line is `wc --output`'s header row: two or more
// words, none of them a number.
func wcHeader(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	for _, f := range fields {
		if _, ok := parseNumber(f); ok {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- files.wc

type filesWC struct {
	rows  [][]lens.Cell
	heads []string // `wc --output=...` prints its own header row
}

func (l *filesWC) ID() string { return "files.wc" }

func (l *filesWC) Match(cmd model.Cmd) bool { return cmd.Program == "wc" }

func (l *filesWC) Open(model.Cmd, *lens.Ctx) (any, error) { return &filesWC{}, nil }

func (l *filesWC) Line(st any, ln model.Line) error {
	w := st.(*filesWC)
	fields := strings.Fields(ln.Text)
	if len(fields) == 0 {
		return nil
	}
	// `wc --output=lines,words,…` (the `output` token beside files.wc in Terminal's
	// id block) prints a header row naming the requested columns.
	if w.heads == nil && wcHeader(fields) {
		w.heads = make([]string, len(fields))
		for i, f := range fields {
			w.heads[i] = strings.ToUpper(f)
		}
		return nil
	}
	numCap := 3
	if len(w.heads) > 0 {
		numCap = len(w.heads) - 1 // the last column is the file name
	}
	nums := make([]string, 0, numCap)
	name := ""
	for _, f := range fields {
		if _, ok := parseNumber(f); ok && len(nums) < numCap {
			nums = append(nums, f)
			continue
		}
		name = strings.Join([]string{name, f}, " ")
	}
	if len(nums) == 0 {
		return nil
	}
	name = strings.TrimSpace(name)
	row := make([]lens.Cell, 0, 4)
	for _, n := range nums {
		row = append(row, lens.Cell{Spans: []lens.Span{{Text: n, Style: "num"}}})
	}
	for len(row) < numCap {
		row = append(row, lens.Cell{})
	}
	row = append(row, lens.Cell{Spans: []lens.Span{{Text: name, Style: "path"}}})
	w.rows = append(w.rows, row)
	return nil
}

func (l *filesWC) Finish(any, int) error       { return nil }
func (l *filesWC) Event(any, lens.Event) error { return nil }

func (l *filesWC) View(st any) (lens.Node, error) {
	w := st.(*filesWC)
	if len(w.rows) == 0 {
		return nil, nil
	}
	if len(w.heads) > 0 {
		cols := make([]lens.Col, 0, len(w.heads))
		for i, h := range w.heads {
			col := lens.Col{ID: strings.ToLower(strings.ReplaceAll(h, " ", "_")), Head: h, Align: lens.AlignEnd, Grow: 1}
			if i == len(w.heads)-1 {
				col.Head = "FILE"
				col.Align = lens.AlignStart
				col.Grow = 4
			}
			cols = append(cols, col)
		}
		return lens.Table{Cols: cols, Rows: w.rows}, nil
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "lines", Head: "LINES", Align: lens.AlignEnd, Grow: 1},
			{ID: "words", Head: "WORDS", Align: lens.AlignEnd, Grow: 1},
			{ID: "bytes", Head: "BYTES", Align: lens.AlignEnd, Grow: 1},
			{ID: "file", Head: "FILE", Grow: 4},
		},
		Rows: w.rows,
	}, nil
}

// ---------------------------------------------------------------- files.du

type filesDU struct {
	rows  [][]lens.Cell
	sizes []int64
}

func (l *filesDU) ID() string { return "files.du" }

func (l *filesDU) Match(cmd model.Cmd) bool { return cmd.Program == "du" }

func (l *filesDU) Open(model.Cmd, *lens.Ctx) (any, error) { return &filesDU{}, nil }

func (l *filesDU) Line(st any, ln model.Line) error {
	d := st.(*filesDU)
	fields := strings.Fields(ln.Text)
	if len(fields) < 2 {
		return nil
	}
	if strings.HasPrefix(fields[0], "total") {
		return nil
	}
	n, ok := parse.Size(fields[0])
	if !ok {
		return nil
	}
	rest := fields[1:]
	// `du --time` (the `time` token beside files.du in Terminal's id block) prints a
	// timestamp between the size and the path: drop it when both halves are there.
	if len(rest) >= 3 && lsDateToken(rest[0]) && lsTimeToken(rest[1]) {
		rest = rest[2:]
	}
	path := strings.Join(rest, " ")
	if path == "" {
		return nil
	}
	d.sizes = append(d.sizes, n)
	d.rows = append(d.rows, []lens.Cell{
		{Spans: []lens.Span{{Text: fields[0], Style: "num"}}},
		{Spans: []lens.Span{{Text: path, Style: "path"}}},
		{Meter: &lens.MeterCell{Value: 0}},
	})
	return nil
}

func (l *filesDU) Finish(any, int) error       { return nil }
func (l *filesDU) Event(any, lens.Event) error { return nil }

func (l *filesDU) View(st any) (lens.Node, error) {
	d := st.(*filesDU)
	if len(d.rows) == 0 {
		return nil, nil
	}
	var max int64 = 1
	for _, n := range d.sizes {
		if n > max {
			max = n
		}
	}
	for i := range d.rows {
		d.rows[i][2].Meter.Value = float64(d.sizes[i]) / float64(max)
	}
	return lens.Table{
		Cols: []lens.Col{
			{ID: "size", Head: "SIZE", Align: lens.AlignEnd, Grow: 1},
			{ID: "path", Head: "PATH", Grow: 6},
			{ID: "usage", Head: "USAGE", Grow: 2},
		},
		Rows: d.rows,
	}, nil
}

// ---------------------------------------------------------------- files.find

type filesFind struct {
	items []lens.Item
	rows  [][]lens.Cell
}

func (l *filesFind) ID() string { return "files.find" }

func (l *filesFind) Match(cmd model.Cmd) bool {
	return cmd.Program == "find" || cmd.Program == "fd"
}

func (l *filesFind) Open(model.Cmd, *lens.Ctx) (any, error) { return &filesFind{}, nil }

func (l *filesFind) Line(st any, ln model.Line) error {
	f := st.(*filesFind)
	t := strings.TrimSpace(ln.Text)
	if t == "" || gitxErrorLine(t) || strings.HasPrefix(t, "find: ") {
		return nil
	}
	// find -ls: inode blocks ... size date path — take the trailing path.
	if fields := strings.Fields(t); len(fields) > 8 && !strings.Contains(fields[0], "/") {
		if p := fields[len(fields)-1]; strings.ContainsAny(p, "/.") {
			f.rows = append(f.rows, []lens.Cell{
				{Spans: []lens.Span{{Text: fields[len(fields)-2], Style: "num"}}},
				{Spans: []lens.Span{{Text: strings.Join(fields[len(fields)-2:], " "), Style: "path"}}},
			})
			return nil
		}
	}
	if !strings.ContainsAny(t, `/\`) && !strings.HasPrefix(t, ".") && !strings.Contains(t, ".") {
		return nil // prose, not a path
	}
	f.items = append(f.items, lens.Item{Label: []lens.Span{{Text: t, Style: "path"}}})
	return nil
}

func (l *filesFind) Finish(any, int) error       { return nil }
func (l *filesFind) Event(any, lens.Event) error { return nil }

func (l *filesFind) View(st any) (lens.Node, error) {
	f := st.(*filesFind)
	if len(f.rows) > 0 {
		return lens.Table{
			Cols: []lens.Col{
				{ID: "size", Head: "SIZE", Align: lens.AlignEnd, Grow: 1},
				{ID: "path", Head: "PATH", Grow: 6},
			},
			Rows: f.rows,
		}, nil
	}
	if len(f.items) == 0 {
		return nil, nil
	}
	return lens.Column{Gap: 6, Children: []lens.Node{
		lens.Badge{Text: itoaSmall(len(f.items)) + " paths", Tone: "info"},
		lens.List{Items: f.items},
	}}, nil
}

// ---------------------------------------------------------------- files.tree

type filesTree struct {
	roots  []*filesTreeNode
	glyphs int
}

type filesTreeNode struct {
	label    string
	children []*filesTreeNode
	depth    int
}

func (l *filesTree) ID() string { return "files.tree" }

func (l *filesTree) Match(cmd model.Cmd) bool { return cmd.Program == "tree" }

func (l *filesTree) Open(model.Cmd, *lens.Ctx) (any, error) { return &filesTree{}, nil }

func (l *filesTree) Line(st any, ln model.Line) error {
	tr := st.(*filesTree)
	raw := strings.TrimRight(ln.Text, " ")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	depth, label := filesTreeSplit(raw)
	if label == "" || strings.HasPrefix(label, "directories,") || strings.HasPrefix(label, "files,") {
		return nil
	}
	if strings.ContainsAny(raw, "├└│`|+") {
		tr.glyphs++
	}
	node := &filesTreeNode{label: label, depth: depth}
	tr.insert(node)
	return nil
}

// filesTreeSplit returns the depth and the entry name of one `tree` line, which
// looks like "│   ├── src" or "├── a.go".
func filesTreeSplit(line string) (int, string) {
	rest := line
	depth := 0
	for {
		trimmed := strings.TrimLeft(rest, " ")
		if trimmed == rest {
			break
		}
		rest = trimmed
	}
	for {
		switch {
		case strings.HasPrefix(rest, "│   "):
			rest = rest[len("│   "):]
			depth++
		case strings.HasPrefix(rest, "|   "):
			rest = rest[len("|   "):]
			depth++
		case strings.HasPrefix(rest, "    "):
			rest = rest[len("    "):]
			depth++
		case strings.HasPrefix(rest, "├── "):
			rest = rest[len("├── "):]
			return depth, strings.TrimSpace(rest)
		case strings.HasPrefix(rest, "└── "):
			rest = rest[len("└── "):]
			return depth, strings.TrimSpace(rest)
		case strings.HasPrefix(rest, "|-- "):
			rest = rest[len("|-- "):]
			return depth, strings.TrimSpace(rest)
		case strings.HasPrefix(rest, "`-- "):
			rest = rest[len("`-- "):]
			return depth, strings.TrimSpace(rest)
		default:
			return depth, strings.TrimSpace(rest)
		}
	}
}

func (tr *filesTree) insert(node *filesTreeNode) {
	if node.depth == 0 {
		tr.roots = append(tr.roots, node)
		return
	}
	var parent *filesTreeNode
	for i := len(tr.roots) - 1; i >= 0; i-- {
		if p := filesTreeLastAt(tr.roots[i], node.depth-1); p != nil {
			parent = p
			break
		}
	}
	if parent == nil {
		tr.roots = append(tr.roots, node)
		return
	}
	parent.children = append(parent.children, node)
}

// filesTreeLastAt finds the last node at the given depth in the subtree.
func filesTreeLastAt(n *filesTreeNode, depth int) *filesTreeNode {
	if n.depth == depth {
		return n
	}
	for i := len(n.children) - 1; i >= 0; i-- {
		if p := filesTreeLastAt(n.children[i], depth); p != nil {
			return p
		}
	}
	return nil
}

func (l *filesTree) Finish(any, int) error       { return nil }
func (l *filesTree) Event(any, lens.Event) error { return nil }

func (l *filesTree) View(st any) (lens.Node, error) {
	tr := st.(*filesTree)
	if len(tr.roots) == 0 || tr.glyphs == 0 {
		return nil, nil // no tree drawing: prose, not `tree` output
	}
	return lens.Tree{Nodes: filesTreeNodes(tr.roots)}, nil
}

func filesTreeNodes(nodes []*filesTreeNode) []lens.TreeNode {
	out := make([]lens.TreeNode, 0, len(nodes))
	for _, n := range nodes {
		item := lens.TreeNode{
			Label: []lens.Span{{Text: n.label, Style: "path"}},
			Open:  true,
		}
		if len(n.children) > 0 {
			item.Icon = "dir"
			item.Children = filesTreeNodes(n.children)
		}
		out = append(out, item)
	}
	return out
}
