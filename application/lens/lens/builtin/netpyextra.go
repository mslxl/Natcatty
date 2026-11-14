package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

// The rest of the net family (dns, http, trace) and of the py family (pkg,
// lint). Ids and the trigger surface are Terminal's (findings/lens-conditions.md
// §1.1, offsets 149975056 and 149986504). py.lint's trigger surface is the
// weakest evidence we have (only a flag table in the same constant cluster), so
// its program list is marked [推断] in the code below.
func init() {
	for _, d := range []lens.Definition{
		{ID: "net.dns", New: func() lens.Lens { return &netDNS{} }},
		{ID: "net.http", New: func() lens.Lens { return &netHTTP{} }},
		{ID: "net.trace", New: func() lens.Lens { return &netTrace{} }},
		{ID: "py.pkg", New: func() lens.Lens { return &pyPkg{} }},
		{ID: "py.lint", New: func() lens.Lens { return &pyLint{} }},
	} {
		lens.Register(d)
	}
}

// ---------------------------------------------------------------- net.dns

type netDNS struct {
	status, server, qtime string
	name, addr            string // nslookup: the queried name and the resolver address
	answers               [][2]string
	notes                 []string
	kv                    lens.KV
	section               string
	rows                  [][]lens.Cell
	sections              []lens.Node
}

func (l *netDNS) ID() string { return "net.dns" }

func (l *netDNS) Match(cmd model.Cmd) bool {
	return cmd.Program == "dig" || cmd.Program == "host" || cmd.Program == "nslookup"
}

func (l *netDNS) Open(model.Cmd, *lens.Ctx) (any, error) { return &netDNS{}, nil }

func (l *netDNS) Line(st any, ln model.Line) error {
	d := st.(*netDNS)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	// nslookup prints its own shape ("Server:", "Name:", "Address:"), which
	// ConPTY can glue into one line ("… 192.168.100.1Name:  example.com").
	if netDNSIsNslookup(t) {
		for _, part := range netDNSSplitGlued(t) {
			d.nslLine(part)
		}
		return nil
	}
	switch {
	case strings.Contains(t, "->>HEADER<<-"):
		if _, rest, ok := strings.Cut(t, "status:"); ok {
			d.status = strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
		}
		return nil
	case strings.HasPrefix(t, ";; SERVER:"):
		d.server = strings.TrimSpace(strings.TrimPrefix(t, ";; SERVER:"))
		return nil
	case strings.HasPrefix(t, ";; Query time:"):
		d.qtime = strings.TrimSpace(strings.TrimPrefix(t, ";; Query time:"))
		return nil
	case strings.HasSuffix(t, "SECTION:"):
		if len(d.rows) > 0 {
			d.flush()
		}
		d.section = strings.TrimSuffix(strings.TrimPrefix(t, ";; "), " SECTION:")
		return nil
	case strings.HasPrefix(t, ";"):
		return nil
	}
	// Resource record: <name> <ttl> <class> <type> <data…>, and only inside a
	// section we recognised — otherwise prose would look like a record.
	fields := strings.Fields(t)
	if len(fields) >= 4 && d.section != "" {
		d.rows = append(d.rows, []lens.Cell{
			{Spans: []lens.Span{{Text: fields[0], Style: "path"}}},
			{Spans: []lens.Span{{Text: fields[1], Style: "num"}}},
			{Spans: []lens.Span{{Text: fields[2], Style: "muted"}}},
			{Spans: []lens.Span{{Text: fields[3], Style: "strong"}}},
			{Spans: []lens.Span{{Text: strings.Join(fields[4:], " "), Style: "code"}}},
		})
	}
	return nil
}

// sectionNode builds the section for rows without touching the state: the engine
// rebuilds a running block's view at any time, so View must not consume rows.
func (d *netDNS) sectionNode(rows [][]lens.Cell) lens.Section {
	head := d.section
	if head == "" {
		head = "ANSWER"
	}
	return lens.Section{
		Head: []lens.Span{{Text: head, Style: "muted"}},
		Children: []lens.Node{lens.Table{
			Cols: []lens.Col{
				{ID: "name", Head: "NAME", Grow: 4},
				{ID: "ttl", Head: "TTL", Align: lens.AlignEnd, Grow: 1},
				{ID: "class", Head: "CLASS", Grow: 1},
				{ID: "type", Head: "TYPE", Grow: 1},
				{ID: "data", Head: "DATA", Grow: 4},
			},
			Rows: rows,
		}},
	}
}

func (d *netDNS) flush() {
	if len(d.rows) == 0 {
		return
	}
	d.sections = append(d.sections, d.sectionNode(d.rows))
	d.rows = nil
}

func (l *netDNS) Finish(st any, _ int) error {
	st.(*netDNS).flush() // finalize the last section once the output ended
	return nil
}

func (l *netDNS) Event(any, lens.Event) error { return nil }

func (l *netDNS) View(st any) (lens.Node, error) {
	d := st.(*netDNS)
	if len(d.answers) > 0 {
		kv := lens.KV{}
		add := func(k, v string) {
			if v == "" {
				return
			}
			kv.Items = append(kv.Items, [2][]lens.Span{
				{{Text: k, Style: "muted"}},
				{{Text: v, Style: "num"}},
			})
		}
		add("server", d.server)
		add("address", d.addr)
		for _, n := range d.notes {
			add("note", n)
		}
		rows := make([][]lens.Cell, 0, len(d.answers))
		for _, a := range d.answers {
			rows = append(rows, []lens.Cell{
				{Spans: []lens.Span{{Text: a[0], Style: "strong"}}},
				{Spans: []lens.Span{{Text: a[1], Style: "num"}}},
			})
		}
		children := []lens.Node{}
		if len(kv.Items) > 0 {
			children = append(children, kv)
		}
		children = append(children, lens.Section{
			Head: []lens.Span{{Text: "ANSWER", Style: "muted"}},
			Children: []lens.Node{lens.Table{
				Cols: []lens.Col{
					{ID: "name", Head: "NAME", Grow: 2},
					{ID: "address", Head: "ADDRESS", Grow: 2},
				},
				Rows: rows,
			}},
		})
		return lens.Column{Gap: 6, Children: children}, nil
	}
	sections := d.sections
	if len(d.rows) > 0 {
		sections = append(append([]lens.Node{}, d.sections...), d.sectionNode(d.rows))
	}
	if d.status == "" && len(sections) == 0 {
		return nil, nil
	}
	children := []lens.Node{}
	kv := lens.KV{}
	for _, p := range [][2]string{{"status", d.status}, {"server", d.server}, {"query time", d.qtime}} {
		if p[1] == "" {
			continue
		}
		kv.Items = append(kv.Items, [2][]lens.Span{
			{{Text: p[0], Style: "muted"}},
			{{Text: p[1], Style: "num"}},
		})
	}
	if len(kv.Items) > 0 {
		children = append(children, kv)
	}
	children = append(children, sections...)
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Column{Gap: 6, Children: children}, nil
}

// netDNSIsNslookup reports whether a line belongs to nslookup's output.
func netDNSIsNslookup(t string) bool {
	for _, p := range []string{"Server:", "Address:", "Addresses:", "Name:", "Non-authoritative answer:", "Aliases:"} {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// netDNSSplitGlued splits a line at the nslookup labels that ConPTY glued
// together ("Address:  192.168.100.1Name:    example.com").
func netDNSSplitGlued(t string) []string {
	parts := []string{t}
	for _, m := range []string{"Name:", "Address:"} {
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
	return out
}

// nslLine files one nslookup line.
func (d *netDNS) nslLine(p string) {
	switch {
	case strings.HasPrefix(p, "Server:"):
		d.server = strings.TrimSpace(strings.TrimPrefix(p, "Server:"))
	case strings.HasPrefix(p, "Addresses:"):
		// nslookup prints every address of a name on one "Addresses:" line.
		if d.name == "" {
			return
		}
		for _, a := range strings.Fields(strings.TrimPrefix(p, "Addresses:")) {
			d.answers = append(d.answers, [2]string{d.name, a})
		}
	case strings.HasPrefix(p, "Address:"):
		addr := strings.TrimSpace(strings.TrimPrefix(p, "Address:"))
		if addr == "" {
			return
		}
		if d.name == "" {
			d.addr = addr // the resolver's address
			return
		}
		d.answers = append(d.answers, [2]string{d.name, addr})
	case strings.HasPrefix(p, "Name:"):
		d.name = strings.TrimSpace(strings.TrimPrefix(p, "Name:"))
	case strings.HasPrefix(p, "Non-authoritative answer:"):
		d.notes = append(d.notes, "non-authoritative answer")
	case strings.HasPrefix(p, "Aliases:"):
		if rest := strings.TrimSpace(strings.TrimPrefix(p, "Aliases:")); rest != "" {
			d.notes = append(d.notes, "aliases: "+rest)
		}
	}
}

// ---------------------------------------------------------------- net.http

type netHTTP struct {
	lines   []string
	code    string
	size    string
	timeTot string
	saved   string
}

func (l *netHTTP) ID() string { return "net.http" }

func (l *netHTTP) Match(cmd model.Cmd) bool { return cmd.Program == "curl" || cmd.Program == "wget" }

func (l *netHTTP) Open(model.Cmd, *lens.Ctx) (any, error) { return &netHTTP{}, nil }

func (l *netHTTP) Line(st any, ln model.Line) error {
	h := st.(*netHTTP)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	h.lines = append(h.lines, ln.Text)
	switch {
	case strings.HasPrefix(t, "HTTP/"), strings.HasPrefix(t, "< HTTP/"):
		fields := strings.Fields(t)
		for _, f := range fields {
			if len(f) == 3 && f[0] == '2' || len(f) == 3 && f[0] == '3' || len(f) == 3 && f[0] == '4' || len(f) == 3 && f[0] == '5' {
				h.code = f
			}
		}
	case strings.HasPrefix(t, "saved ["):
		h.saved = t
	case strings.HasPrefix(t, "Length:"):
		h.size = strings.TrimSpace(strings.TrimPrefix(t, "Length:"))
	case strings.Contains(t, "100%") && h.saved == "":
		if i := strings.Index(t, "100%"); i >= 0 {
			h.saved = strings.TrimSpace(t[i:])
		}
	}
	return nil
}

func (l *netHTTP) Finish(any, int) error       { return nil }
func (l *netHTTP) Event(any, lens.Event) error { return nil }

func (l *netHTTP) View(st any) (lens.Node, error) {
	h := st.(*netHTTP)
	if len(h.lines) == 0 {
		return nil, nil
	}
	// Only claim output with transfer evidence; a bare help dump stays raw.
	ev := h.code != "" || h.saved != "" || h.size != ""
	if !ev {
		hasProgress := false
		for _, ln := range h.lines {
			if strings.Contains(ln, "%") && (strings.Contains(ln, "Total") || strings.Contains(ln, "Dload") || strings.Contains(ln, "%[") || strings.Contains(ln, "100%")) {
				hasProgress = true
				break
			}
		}
		if !hasProgress {
			return nil, nil
		}
	}
	kv := lens.KV{}
	add := func(k, v string) {
		if v == "" {
			return
		}
		kv.Items = append(kv.Items, [2][]lens.Span{
			{{Text: k, Style: "muted"}},
			{{Text: v, Style: "num"}},
		})
	}
	add("status", h.code)
	add("size", h.size)
	add("result", h.saved)
	if h.code != "" {
		tone := "success"
		if strings.HasPrefix(h.code, "4") || strings.HasPrefix(h.code, "5") {
			tone = "error"
		}
		kv.Items = append(kv.Items, [2][]lens.Span{
			{{Text: "http", Style: "muted"}},
			{{Text: h.code, Style: tone}},
		})
	}
	children := []lens.Node{}
	if len(kv.Items) > 0 {
		children = append(children, kv)
	}
	children = append(children, lens.Section{
		Head:     []lens.Span{{Text: "Transfer", Style: "muted"}},
		Children: []lens.Node{lens.Code{Text: strings.Join(h.lines, "\n"), Lang: "text", Clamp: 12}},
	})
	return lens.Column{Gap: 6, Children: children}, nil
}

// ---------------------------------------------------------------- net.trace

type netTrace struct {
	lines []string
	rows  [][]lens.Cell
	hops  []lens.Series
}

func (l *netTrace) ID() string { return "net.trace" }

func (l *netTrace) Match(cmd model.Cmd) bool {
	return cmd.Program == "mtr" || cmd.Program == "traceroute" || cmd.Program == "tracert"
}

func (l *netTrace) Open(model.Cmd, *lens.Ctx) (any, error) { return &netTrace{}, nil }

func (l *netTrace) Line(st any, ln model.Line) error {
	tr := st.(*netTrace)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	tr.lines = append(tr.lines, ln.Text)
	if strings.HasPrefix(t, "HOST:") || stringsHasSlot(t) {
		return nil
	}
	fields := strings.Fields(t)
	if len(fields) < 6 {
		return nil
	}
	// "1.|-- host  0.0%  10  1.2  1.3  1.1  1.5  0.1"
	hop := fields[0]
	n := 0
	for n < len(hop) && hop[n] >= '0' && hop[n] <= '9' {
		n++
	}
	if n == 0 {
		return nil
	}
	hop = hop[:n]
	host := fields[1]
	rest := fields[2:]
	cell := func(s string, style string) lens.Cell {
		return lens.Cell{Spans: []lens.Span{{Text: s, Style: style}}}
	}
	row := []lens.Cell{cell(hop, "num"), cell(host, "path")}
	for i, f := range rest {
		style := "num"
		if i == 0 {
			style = "muted"
		}
		row = append(row, cell(f, style))
	}
	tr.rows = append(tr.rows, row)
	if len(rest) >= 4 {
		if avg, ok := parseFloat(rest[3]); ok {
			tr.hops = append(tr.hops, lens.Series{Label: host, Value: avg})
		}
	}
	return nil
}

// stringsHasSlot reports whether the line is the mtr header row.
func stringsHasSlot(s string) bool { return strings.Contains(s, "Loss%") && strings.Contains(s, "Avg") }

func (l *netTrace) Finish(any, int) error       { return nil }
func (l *netTrace) Event(any, lens.Event) error { return nil }

func (l *netTrace) View(st any) (lens.Node, error) {
	tr := st.(*netTrace)
	if len(tr.rows) == 0 {
		return nil, nil
	}
	// Terminal's shared parse.columns handles mtr's aligned report (the hop column
	// carries the "1.|--" decoration, which the header-driven split absorbs).
	tbl := lens.Table{Cols: netTraceCols(), Rows: tr.rows}
	if t := parse.Columns(tr.lines); t != nil && len(t.Rows) > 0 {
		cols := make([]lens.Col, len(t.Heads))
		for i, h := range t.Heads {
			align := lens.AlignEnd
			if i == 0 || i == 1 {
				align = lens.AlignStart
			}
			cols[i] = lens.Col{ID: strings.ToLower(h), Head: h, Align: align, Grow: 1}
		}
		rows := make([][]lens.Cell, 0, len(t.Rows))
		for _, r := range t.Rows {
			cells := make([]lens.Cell, len(cols))
			for i := range cols {
				v := ""
				if i < len(r) {
					v = r[i]
				}
				cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: "num"}}}
			}
			rows = append(rows, cells)
		}
		tbl = lens.Table{Cols: cols, Rows: rows}
	}
	nodes := []lens.Node{tbl}
	if len(tr.hops) > 0 {
		nodes = append(nodes, lens.Section{
			Head:     []lens.Span{{Text: "avg RTT per hop", Style: "muted"}},
			Children: []lens.Node{lens.Bars{Series: tr.hops}},
		})
	}
	return lens.Column{Gap: 6, Children: nodes}, nil
}

// netTraceCols is the fallback column set when the report does not parse.
func netTraceCols() []lens.Col {
	cols := []lens.Col{
		{ID: "hop", Head: "HOP", Align: lens.AlignStart, Grow: 1},
		{ID: "host", Head: "HOST", Align: lens.AlignStart, Grow: 4},
	}
	for _, h := range []string{"LOSS%", "SNT", "LAST", "AVG", "BEST", "WRST", "STDEV"} {
		cols = append(cols, lens.Col{ID: strings.ToLower(h), Head: h, Align: lens.AlignEnd, Grow: 1})
	}
	return cols
}

// ---------------------------------------------------------------- py.pkg

type pyPkg struct {
	rows    [][]lens.Cell
	aktions []lens.Item
	styled  []string // output lines with SGR escapes
	listing []string // `pip list` rows, parsed with Terminal's shared parse.columns
	done    string
}

func (l *pyPkg) ID() string { return "py.pkg" }

func (l *pyPkg) Match(cmd model.Cmd) bool {
	switch cmd.Program {
	case "pip", "pip3", "uv", "pipx":
		return true
	case "python", "python3", "py":
		// `python -m pip ...` is the same pip family; Terminal matches by program
		// family, not by one executable name (findings, id naming rule).
		return pyRunsModule(cmd.Args, "pip")
	}
	return false
}

// pyRunsModule reports whether the interpreter runs module as `-m module`.
func pyRunsModule(args []string, module string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-m" && strings.EqualFold(args[i+1], module) {
			return true
		}
	}
	return false
}

func (l *pyPkg) Open(model.Cmd, *lens.Ctx) (any, error) { return &pyPkg{}, nil }

func (l *pyPkg) Line(st any, ln model.Line) error {
	p := st.(*pyPkg)
	if ln.Styled != "" {
		p.styled = append(p.styled, ln.Styled)
	}
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	row := func(pkg, ver, status, tone string) []lens.Cell {
		return []lens.Cell{
			{Spans: []lens.Span{{Text: pkg, Style: "strong"}}},
			{Spans: []lens.Span{{Text: ver, Style: "num"}}},
			{Spans: []lens.Span{{Text: status, Style: tone}}},
		}
	}
	switch {
	case strings.HasPrefix(t, "Collecting "):
		name := strings.TrimSpace(strings.TrimPrefix(t, "Collecting "))
		if i := strings.IndexByte(name, ' '); i > 0 {
			name = name[:i]
		}
		pkg, ver := splitPkgVersion(name)
		p.rows = append(p.rows, row(pkg, ver, "collecting", "muted"))
	case strings.HasPrefix(t, "Successfully installed "):
		p.done = strings.TrimSpace(strings.TrimPrefix(t, "Successfully installed "))
	case strings.HasPrefix(t, "Requirement already satisfied:"):
		name := strings.TrimSpace(strings.TrimPrefix(t, "Requirement already satisfied:"))
		if i := strings.IndexByte(name, ' '); i > 0 {
			name = name[:i]
		}
		pkg, ver := splitPkgVersion(name)
		p.rows = append(p.rows, row(pkg, ver, "satisfied", "success"))
	case strings.HasPrefix(t, "Downloading "), strings.HasPrefix(t, "Using cached "):
		p.aktions = append(p.aktions, lens.Item{Label: []lens.Span{{Text: t, Style: "muted"}}})
	case pyListRow(t):
		// `pip list` / `pip freeze --all` print a package table; Terminal's shared
		// parse.columns turns it into the same table shape as the other lenses.
		p.listing = append(p.listing, ln.Text)
	}
	return nil
}

// pyListRow reports whether a line belongs to a package listing table: a header
// ("Package Version"), a separator, or a "name version" pair.
func pyListRow(t string) bool {
	if strings.Trim(t, "- ") == "" {
		return false // the dashed separator is dropped, not parsed
	}
	f := strings.Fields(t)
	if len(f) < 2 {
		return false
	}
	head := strings.ToLower(f[0])
	if head == "package" || head == "name" {
		return true
	}
	return f[1][0] >= '0' && f[1][0] <= '9'
}

// splitPkgVersion splits "requests-2.31.0" or "requests==2.31.0".
func splitPkgVersion(s string) (string, string) {
	if i := strings.Index(s, "=="); i > 0 {
		return s[:i], strings.TrimSpace(s[i+2:])
	}
	if i := strings.LastIndex(s, "-"); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func (l *pyPkg) Finish(any, int) error       { return nil }
func (l *pyPkg) Event(any, lens.Event) error { return nil }

func (l *pyPkg) View(st any) (lens.Node, error) {
	p := st.(*pyPkg)
	if len(p.rows) == 0 && p.done == "" && len(p.listing) == 0 {
		return nil, nil
	}
	nodes := []lens.Node{}
	// `pip list` prints a package table with no install events: build it with
	// Terminal's shared parse.columns (the same parser its built-in lenses use).
	if tbl, ok := pyPkgListing(p.listing); ok {
		nodes = append(nodes, tbl)
	}
	if p.done != "" {
		nodes = append(nodes, lens.Badge{Text: "installed " + p.done, Tone: "success"})
	}
	if len(p.rows) > 0 {
		nodes = append(nodes, lens.Table{
			Cols: []lens.Col{
				{ID: "package", Head: "PACKAGE", Grow: 4},
				{ID: "version", Head: "VERSION", Grow: 2},
				{ID: "status", Head: "STATUS", Grow: 2},
			},
			Rows: p.rows,
		})
	}
	if len(p.aktions) > 0 {
		nodes = append(nodes, lens.Section{
			Head:     []lens.Span{{Text: "Downloads", Style: "muted"}},
			Children: []lens.Node{lens.List{Items: p.aktions}},
		})
	}
	// pip prints progress bars redrawn with CR and colour: the raw stream goes
	// through Terminal's `ansi(text)` mini terminal.
	if len(p.styled) > 0 {
		nodes = append(nodes, lens.Section{
			Head:     []lens.Span{{Text: "Output", Style: "muted"}},
			Children: []lens.Node{lens.ANSI{Text: strings.Join(p.styled, "\n")}},
		})
	}
	return lens.Column{Gap: 6, Children: nodes}, nil
}

// pyPkgListing turns the collected listing lines into a package table.
func pyPkgListing(lines []string) (lens.Table, bool) {
	if len(lines) < 2 {
		return lens.Table{}, false
	}
	t := parse.Columns(lines)
	if t == nil || len(t.Rows) == 0 || len(t.Heads) < 2 {
		// The shared parser rejects two-column text whose header uses a single
		// space (it cannot tell it from prose), but pip pads the version column
		// with two spaces: split on that gutter here.
		var ok bool
		if t, ok = pySplitListing(lines); !ok {
			return lens.Table{}, false
		}
	}
	cols := make([]lens.Col, len(t.Heads))
	for i, h := range t.Heads {
		col := lens.Col{ID: strings.ToLower(h), Head: h, Grow: 1}
		if i > 0 {
			col.Align = lens.AlignEnd
		}
		switch strings.ToLower(h) {
		case "version", "location", "editable project location":
			col.Grow = 2
		}
		cols[i] = col
	}
	rows := make([][]lens.Cell, 0, len(t.Rows))
	for _, r := range t.Rows {
		cells := make([]lens.Cell, len(cols))
		for i := range cols {
			v := ""
			if i < len(r) {
				v = r[i]
			}
			style := "num"
			if i == 0 {
				style = "strong"
			}
			cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: style}}}
		}
		rows = append(rows, cells)
	}
	return lens.Table{Cols: cols, Rows: rows}, true
}

// pySplitListing splits a listing on a 2+ space gutter, falling back to fields
// when a line (the header, for instance) uses a single space.
func pySplitListing(lines []string) (*parse.Table, bool) {
	split := func(l string, want int) []string {
		for i := 0; i+1 < len(l); i++ {
			if l[i] == ' ' && l[i+1] == ' ' {
				name := strings.TrimSpace(l[:i])
				rest := strings.TrimSpace(l[i:])
				if name != "" && rest != "" {
					return []string{name, rest}
				}
				break
			}
		}
		return strings.Fields(l)
	}
	head := split(lines[0], 2)
	if len(head) < 2 {
		return nil, false
	}
	t := &parse.Table{Heads: head}
	for _, l := range lines[1:] {
		row := split(l, len(head))
		for len(row) < len(head) {
			row = append(row, "")
		}
		t.Rows = append(t.Rows, row[:len(head)])
	}
	if len(t.Rows) == 0 {
		return nil, false
	}
	return t, true
}

// ---------------------------------------------------------------- py.lint

type pyLint struct {
	diags  []lens.Diagnostic
	styled []string // output lines with SGR escapes
	errors int
	warns  int
}

func (l *pyLint) ID() string { return "py.lint" }

// Match: the trigger surface for py.lint is the weakest evidence in the family —
// only the flag cluster at 149986504 is observed, the program names are 推断.
func (l *pyLint) Match(cmd model.Cmd) bool {
	switch cmd.Program {
	case "flake8", "ruff", "pylint", "mypy", "pyflakes", "black":
		return true
	}
	return false
}

func (l *pyLint) Open(model.Cmd, *lens.Ctx) (any, error) { return &pyLint{}, nil }

func (l *pyLint) Line(st any, ln model.Line) error {
	p := st.(*pyLint)
	t := strings.TrimSpace(ln.Text)
	if t == "" {
		return nil
	}
	if ln.Styled != "" {
		p.styled = append(p.styled, ln.Styled)
	}
	path, line, col, ok := parse.Location(t)
	if !ok {
		return nil
	}
	rest := strings.TrimLeft(strings.TrimPrefix(t, path), ": ")
	rest = strings.TrimLeft(rest, "0123456789: ")
	sev := lens.SeverityWarning
	if strings.Contains(rest, "error") || strings.HasPrefix(rest, "E") || strings.HasPrefix(rest, "F") {
		sev = lens.SeverityError
		p.errors++
	} else {
		p.warns++
	}
	// A linter line is a diagnostic: Terminal renders it with `flow.ui.diagnostic`.
	p.diags = append(p.diags, lens.Diagnostic{
		Severity: sev,
		Message:  rest,
		Path:     path,
		Line:     line,
		Col:      col,
	})
	return nil
}

func (l *pyLint) Finish(any, int) error       { return nil }
func (l *pyLint) Event(any, lens.Event) error { return nil }

func (l *pyLint) View(st any) (lens.Node, error) {
	p := st.(*pyLint)
	if len(p.diags) == 0 {
		return nil, nil
	}
	tone := "success"
	switch {
	case p.errors > 0:
		tone = "error"
	case p.warns > 0:
		tone = "warning"
	}
	nodes := []lens.Node{
		lens.Row{Children: []lens.Node{
			lens.Badge{Text: itoaSmall(p.errors) + " errors", Tone: tone},
			lens.Badge{Text: itoaSmall(p.warns) + " warnings", Tone: "warning"},
		}},
		lens.Column{Children: pyLintNodes(p.diags)},
	}
	// A linter that colourises its output keeps it through lens.ANSI.
	if len(p.styled) > 0 {
		nodes = append(nodes, lens.Section{
			Head:     []lens.Span{{Text: "Output", Style: "muted"}},
			Children: []lens.Node{lens.ANSI{Text: strings.Join(p.styled, "\n")}},
		})
	}
	return lens.Column{Gap: 6, Children: nodes}, nil
}

// pyLintNodes wraps each diagnostic as a node.
func pyLintNodes(diags []lens.Diagnostic) []lens.Node {
	out := make([]lens.Node, 0, len(diags))
	for _, d := range diags {
		out = append(out, d)
	}
	return out
}

// parseFloat parses a plain decimal number ("1.23").
func parseFloat(s string) (float64, bool) {
	dot := false
	digits := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}
