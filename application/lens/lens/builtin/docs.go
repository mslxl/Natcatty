package builtin

// docs family: Terminal's documentation lenses, `docs.systemd`, `docs.tldr`,
// `docs.help`, `docs.man`.
//
// Mined evidence (payload\flow.exe, offsets per findings\lens-conditions\
// raw-lens-region-runs2.txt; literals are glued Rust .rdata runs, so only the
// readable whole pieces are trusted):
//
//	149991864  "command" + "docs.systemd" + the flag table
//	           "-t --type -p --property -H --host -M --machine --state -n --lines
//	            --root -s --signal --kill-whom --job-mode --what"   [观察]
//	149991800  "unit state preset load sub desc strong collapsible parts start
//	            with one"  → the unit/state columns of the systemd view   [观察]
//	149991392  "  ACTIVE listed. mid > len error warning, muted mono accent mono
//	            muted Name Option wrap sh Command active neutral info pending
//	            success num mono : --help -h --list-boots --disk-usage ... -o
//	            --output" → the active/enabled tones and `--help`        [观察]
//	149992288  "docs.tldr" + "docs.help" + "docs.man" + "gap" + "md-"   [观察]
//	           → the three page lenses share a markdown body and a
//	             gap-separated two-column layout
//	149992335  "_****_ name time host msg log unit blocks start at a unit line
//	            since" → journalctl's time/host/unit/msg columns         [观察]
//
// Inferred 【推断】: `systemctl`, `journalctl`, `tldr` and `man` are not
// standalone literals in the lens region (findings §1.2 measures this), so the
// trigger programs are read off the ids `docs.systemd` (plus the systemd flag
// table) and off 149992288; `--help` is the one observed arg literal. The
// parsers below are ours — the constants region holds no parsing code — and are
// tolerant: unrecognized lines are ignored and a view that recognized nothing
// returns nil so the block shows raw output, as Terminal's `view()==nil → raw`.

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:  "docs.systemd",
		New: func() lens.Lens { return &docsSystemd{} },
	})
	lens.Register(lens.Definition{
		ID:  "docs.tldr",
		New: func() lens.Lens { return &docsTLDR{} },
	})
	// docs.help claims a shape, not a family: any command carrying --help. It is
	// the one shape routed ahead of the program families (see lookupHelp), which
	// is what Terminal does with help output. Only the long option is taken
	// [观察 149991392]; `-h` would steal `ls -h`/`df -h`.
	lens.Register(lens.Definition{
		ID:    "docs.help",
		Match: []string{"*--help*"},
		New:   func() lens.Lens { return &docsHelp{} },
	})
	lens.Register(lens.Definition{
		ID:  "docs.man",
		New: func() lens.Lens { return &docsMan{} },
	})
}

// ---------------------------------------------------------------------------
// docs.systemd — systemctl and journalctl.

type docsSystemd struct{}

type docsSystemdState struct {
	journal     bool
	header      []string
	rows        [][]string
	kv          [][2]string
	unit        string
	desc        string
	word        string
	activeState string
	states      map[string]int
	noEntry     bool
	sawAny      bool
}

func (d *docsSystemd) ID() string { return "docs.systemd" }

func (d *docsSystemd) Match(cmd model.Cmd) bool {
	return cmd.Program == "systemctl" || cmd.Program == "journalctl"
}

func (d *docsSystemd) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &docsSystemdState{journal: cmd.Program == "journalctl", states: map[string]int{}}, nil
}

func (d *docsSystemd) Line(state any, l model.Line) error {
	st, ok := state.(*docsSystemdState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (d *docsSystemd) Finish(state any, status int) error { return nil }

func (d *docsSystemd) Event(state any, ev lens.Event) error { return nil }

func (st *docsSystemdState) observe(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	if st.journal {
		st.journalLine(trimmed)
		return
	}
	indented := text != "" && (text[0] == ' ' || text[0] == '\t')
	fields := strings.Fields(trimmed)
	// `systemctl is-active` / `is-enabled` print one state word.
	if len(fields) == 1 && docsStateWords[fields[0]] {
		st.word = fields[0]
		st.sawAny = true
		return
	}
	// `systemctl list-units` opens with "UNIT LOAD ACTIVE SUB DESCRIPTION".
	if docsSystemdHeader(fields) {
		st.header = fields
		st.sawAny = true
		return
	}
	// A list row may carry the same status bullet as `systemctl status`.
	row := fields
	if len(row) > 0 && docsBullet(row[0]) {
		row = row[1:]
	}
	switch {
	case strings.HasPrefix(trimmed, "Legend:"),
		strings.HasPrefix(trimmed, "To show all"),
		strings.HasPrefix(trimmed, "Pass --all"),
		strings.HasPrefix(trimmed, "Warning:"),
		strings.Contains(trimmed, " = "):
		return
	}
	if st.header != nil {
		if cells, ok := docsSystemdRow(st.header, row); ok {
			st.rows = append(st.rows, cells)
			if i := docsHeaderIndex(st.header, "ACTIVE"); i >= 0 && i < len(row) {
				st.states[row[i]]++
			}
			st.sawAny = true
			return
		}
	}
	// `systemctl status`: "● nginx.service - A high performance web server".
	if docsBullet(trimmed) || strings.HasPrefix(trimmed, "*") {
		rest := strings.TrimSpace(strings.TrimLeft(trimmed, docsBullets))
		if i := strings.Index(rest, " - "); i > 0 {
			st.unit, st.desc = rest[:i], rest[i+3:]
		} else if rest != "" {
			st.unit = rest
		}
		st.sawAny = true
		return
	}
	// The status body is an indented "Key: value" block.
	if indented {
		if k, v, ok := docsKeyValue(trimmed); ok {
			st.kv = append(st.kv, [2]string{k, v})
			if k == "Active" {
				if f := strings.Fields(v); len(f) > 0 {
					st.activeState = f[0]
				}
			}
			st.sawAny = true
		}
	}
}

// docsSystemdHeader reports whether a row is the all-caps column header.
func docsSystemdHeader(fields []string) bool {
	if len(fields) < 4 {
		return false
	}
	unit := false
	for _, f := range fields {
		if f != strings.ToUpper(f) {
			return false
		}
		if f == "UNIT" {
			unit = true
		}
	}
	return unit
}

// docsLoadStates guards the row parser: the LOAD column only ever holds these.
var docsLoadStates = map[string]bool{
	"loaded": true, "not-found": true, "bad-setting": true, "error": true,
	"masked": true, "stub": true,
}

// docsSystemdRow splits a list row, keeping a description with spaces whole.
func docsSystemdRow(header, fields []string) ([]string, bool) {
	if len(fields) < 5 {
		return nil, false
	}
	if len(header) > 1 && header[1] == "LOAD" && !docsLoadStates[fields[1]] {
		return nil, false
	}
	if i := docsHeaderIndex(header, "DESCRIPTION"); i > 0 && len(fields) > i {
		cells := append([]string{}, fields[:i]...)
		return append(cells, strings.Join(fields[i:], " ")), true
	}
	return fields, true
}

func docsHeaderIndex(header []string, name string) int {
	for i, h := range header {
		if h == name {
			return i
		}
	}
	return -1
}

// docsKeyValue reads an indented "Key: value" line of `systemctl status`.
func docsKeyValue(t string) (string, string, bool) {
	i := strings.IndexByte(t, ':')
	if i <= 0 || i > 24 {
		return "", "", false
	}
	for _, r := range t[:i] {
		switch {
		case r == ' ', r == '-':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		default:
			return "", "", false
		}
	}
	v := strings.TrimSpace(t[i+1:])
	if v == "" {
		return "", "", false
	}
	return t[:i], v, true
}

// docsJournalTime accepts both journalctl's short ("Aug 01 10:00:00") and ISO
// ("2024-08-01T10:00:00+0000") timestamps.
func docsJournalTime(fields []string) (string, []string, bool) {
	if len(fields) >= 4 && monthAbbrev(fields[0]) && allDigits(fields[1]) && strings.Contains(fields[2], ":") {
		return fields[0] + " " + fields[1] + " " + fields[2], fields[3:], true
	}
	if len(fields) >= 3 && len(fields[0]) >= 19 && fields[0][4] == '-' && strings.Contains(fields[0], "T") {
		return fields[0], fields[1:], true
	}
	return "", nil, false
}

func (st *docsSystemdState) journalLine(t string) {
	if strings.HasPrefix(t, "--") {
		st.noEntry = strings.Contains(t, "No entries")
		st.sawAny = true
		return
	}
	when, rest, ok := docsJournalTime(strings.Fields(t))
	if !ok || len(rest) < 2 {
		return
	}
	host := rest[0]
	body := strings.Join(rest[1:], " ")
	unit, msg := "", body
	if i := strings.Index(body, ": "); i > 0 && i <= 48 {
		unit, msg = body[:i], body[i+2:]
	}
	st.rows = append(st.rows, []string{when, host, unit, msg})
	st.sawAny = true
}

func (d *docsSystemd) View(state any) (lens.Node, error) {
	st, ok := state.(*docsSystemdState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	if st.journal {
		return docsJournalView(st), nil
	}
	if st.word != "" {
		return lens.Cols(docsStateBadge(st.word)), nil
	}
	if len(st.rows) > 0 {
		return docsSystemdListView(st), nil
	}
	if len(st.kv) > 0 {
		return docsSystemdStatusView(st), nil
	}
	return nil, nil
}

func docsJournalView(st *docsSystemdState) lens.Node {
	if len(st.rows) == 0 {
		if st.noEntry {
			return docsStateBadge("no entries")
		}
		return nil
	}
	rows := st.rows
	hidden := 0
	if len(rows) > docsMaxRows {
		hidden, rows = len(rows)-docsMaxRows, rows[:docsMaxRows]
	}
	cells := make([][]lens.Cell, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, []lens.Cell{
			docsCell(r[0], toneMuted),
			docsCell(r[1], "path"),
			docsCell(r[2], "key"),
			docsCell(r[3], ""),
		})
	}
	children := []lens.Node{lens.Table{
		Cols: []lens.Col{
			{ID: "time", Head: "TIME"},
			{ID: "host", Head: "HOST"},
			{ID: "unit", Head: "UNIT", Priority: 1},
			{ID: "message", Head: "MESSAGE", Grow: 1},
		},
		Rows: cells,
	}}
	if hidden > 0 {
		children = append(children, lens.Overflow{Hidden: hidden})
	}
	label := plural(len(st.rows), "entry", "entries")
	if len(st.rows) == 1 {
		label = "1 entry"
	}
	children = append(children, lens.Badge{Text: label, Tone: toneMuted})
	return lens.Cols(children...)
}

// docsMaxRows caps a view; the rest is reported as an Overflow.
const docsMaxRows = 200

func docsSystemdListView(st *docsSystemdState) lens.Node {
	rows := st.rows
	hidden := 0
	if len(rows) > docsMaxRows {
		hidden, rows = len(rows)-docsMaxRows, rows[:docsMaxRows]
	}
	cells := make([][]lens.Cell, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, []lens.Cell{
			docsCell(r[0], "strong"),
			docsCell(r[1], toneMuted),
			docsCell(r[2], docsStateTone(r[2])),
			docsCell(r[3], toneMuted),
			docsCell(r[4], ""),
		})
	}
	children := []lens.Node{lens.Table{
		Cols: []lens.Col{
			{ID: "unit", Head: "UNIT"},
			{ID: "load", Head: "LOAD", Priority: 2},
			{ID: "active", Head: "ACTIVE"},
			{ID: "sub", Head: "SUB", Priority: 1},
			{ID: "description", Head: "DESCRIPTION", Grow: 1},
		},
		Rows: cells,
	}}
	if hidden > 0 {
		children = append(children, lens.Overflow{Hidden: hidden})
	}
	badges := make([]lens.Node, 0, len(st.states))
	for _, word := range []string{"active", "failed", "activating", "inactive"} {
		if n := st.states[word]; n > 0 {
			badges = append(badges, lens.Badge{Text: word + " " + strconv.Itoa(n), Tone: docsStateTone(word)})
		}
	}
	if len(badges) > 0 {
		children = append(children, lens.Row{Children: badges})
	}
	return lens.Cols(children...)
}

func docsSystemdStatusView(st *docsSystemdState) lens.Node {
	state := st.activeState
	if state == "" {
		state = "unknown"
	}
	children := []lens.Node{lens.Row{Children: []lens.Node{docsStateBadge(state)}}}
	if st.unit != "" {
		spans := []lens.Span{{Text: st.unit, Style: "strong"}}
		if st.desc != "" {
			spans = append(spans, lens.Span{Text: " " + st.desc, Style: toneMuted})
		}
		children = append(children, lens.Text{Spans: spans})
	}
	items := make([][2][]lens.Span, 0, len(st.kv))
	for _, pair := range st.kv {
		items = append(items, [2][]lens.Span{
			{{Text: pair[0], Style: "key"}},
			{{Text: pair[1]}},
		})
	}
	children = append(children, lens.KV{Items: items})
	return lens.Cols(children...)
}

// docsBullets are the status glyphs systemctl puts before a unit: the filled
// circle for active, the multiplication sign for failed, the hollow circle for
// inactive [推断: the glyphs themselves are not in the constants region].
const docsBullets = "●*×○•"

func docsBullet(field string) bool {
	if field == "" {
		return false
	}
	return strings.ContainsRune(docsBullets, []rune(field)[0])
}

// docsStateWords are the single-word states `systemctl is-active`/`is-enabled`
// and the ACTIVE/LOAD columns print [观察 149991392: active/neutral/info/
// pending/success, 149991800: unit state preset load sub].
var docsStateWords = map[string]bool{
	"active": true, "inactive": true, "failed": true, "activating": true,
	"deactivating": true, "reloading": true, "unknown": true, "enabled": true,
	"disabled": true, "static": true, "masked": true, "indirect": true,
	"generated": true, "transient": true, "alias": true, "linked": true,
	"enabled-runtime": true, "masked-runtime": true, "not-found": true,
	"loaded": true, "error": true, "bad-setting": true, "maintenance": true,
}

func docsStateTone(s string) string {
	switch s {
	case "active", "enabled", "loaded", "enabled-runtime":
		return toneSuccess
	case "failed", "error", "not-found", "bad-setting":
		return toneError
	case "activating", "deactivating", "reloading", "maintenance", "masked", "masked-runtime":
		return toneWarning
	case "inactive", "dead", "disabled", "static", "unknown":
		return toneMuted
	}
	return toneNeutral
}

func docsStateBadge(s string) lens.Badge {
	return lens.Badge{Text: s, Tone: docsStateTone(s)}
}

func docsCell(text, style string) lens.Cell {
	return lens.Cell{Spans: []lens.Span{{Text: text, Style: style}}}
}

// ---------------------------------------------------------------------------
// docs.tldr — a tldr page is markdown: "> summary", "- bullet:" and a command
// in backticks [观察 149992288 "md-"].

type docsTLDR struct{}

type docsTLDRState struct {
	lines  []string
	sawAny bool
}

func (d *docsTLDR) ID() string { return "docs.tldr" }

func (d *docsTLDR) Match(cmd model.Cmd) bool { return cmd.Program == "tldr" }

func (d *docsTLDR) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &docsTLDRState{}, nil }

func (d *docsTLDR) Line(state any, l model.Line) error {
	st, ok := state.(*docsTLDRState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (d *docsTLDR) Finish(state any, status int) error { return nil }

func (d *docsTLDR) Event(state any, ev lens.Event) error { return nil }

func (st *docsTLDRState) observe(text string) {
	t := strings.TrimRight(text, " ")
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, "# "):
		st.lines = append(st.lines, t, "")
	case strings.HasPrefix(t, "> "):
		st.lines = append(st.lines, t, "")
	case strings.HasPrefix(t, "- "):
		st.lines = append(st.lines, t)
	case len(t) > 2 && strings.HasPrefix(t, "`") && strings.HasSuffix(t, "`"):
		// A page's command is quoted; render it as a fenced code block.
		st.lines = append(st.lines, "```", docsUnbrace(strings.Trim(t, "`")), "```")
	default:
		return
	}
	st.sawAny = true
}

// docsUnbrace turns tldr's placeholders `{{name}}` into plain text.
func docsUnbrace(s string) string {
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "}}")
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+2:i+j] + s[i+j+2:]
	}
}

func (d *docsTLDR) View(state any) (lens.Node, error) {
	st, ok := state.(*docsTLDRState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny || len(st.lines) == 0 {
		return nil, nil
	}
	return lens.Markdown{Text: strings.Join(st.lines, "\n")}, nil
}

// ---------------------------------------------------------------------------
// docs.help — the `--help` page of any program: a usage line, a description
// and one table per option section. The gap-splitting below mirrors the "gap"
// two-column layout mined at 149992288.

type docsHelp struct{}

type docsHelpOption struct {
	flags string
	arg   string
	desc  string
}

type docsHelpState struct {
	usage    string
	intro    []string
	order    []string
	sections map[string][]docsHelpOption
}

func (d *docsHelp) ID() string { return "docs.help" }

func (d *docsHelp) Match(cmd model.Cmd) bool {
	for _, a := range cmd.Args {
		if strings.Contains(a, "--help") {
			return true
		}
	}
	return false
}

func (d *docsHelp) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &docsHelpState{sections: map[string][]docsHelpOption{}}, nil
}

func (d *docsHelp) Line(state any, l model.Line) error {
	st, ok := state.(*docsHelpState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (d *docsHelp) Finish(state any, status int) error { return nil }

func (d *docsHelp) Event(state any, ev lens.Event) error { return nil }

func (st *docsHelpState) observe(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "usage:"):
		st.usage = strings.TrimSpace(trimmed[len("usage:"):])
		return
	case strings.HasPrefix(trimmed, "USAGE"):
		if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "USAGE")); rest != "" {
			st.usage = rest
			return
		}
	}
	// "Options:", "Commands:", "Exit status:" — a short label ending in ':'
	// that is not an option line.
	if !strings.HasPrefix(trimmed, "-") && strings.HasSuffix(trimmed, ":") {
		label := strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
		if label != "" && len(label) <= 32 && len(strings.Fields(label)) <= 3 && !strings.Contains(label, "=") {
			st.section(label)
			return
		}
	}
	// An option line: "-a, --all        do not ignore ...".
	if strings.HasPrefix(trimmed, "-") {
		if flags, desc := docsHelpGap(trimmed); strings.HasPrefix(flags, "-") {
			arg := ""
			switch {
			case strings.IndexAny(flags, "<[") > 0:
				i := strings.IndexAny(flags, "<[")
				flags, arg = strings.TrimSpace(flags[:i]), flags[i:]
			case strings.IndexByte(flags, '=') > 0:
				i := strings.IndexByte(flags, '=')
				flags, arg = strings.TrimSpace(flags[:i]), flags[i:]
			}
			st.add(docsHelpOption{flags: flags, arg: arg, desc: desc})
			return
		}
	}
	// Prose before the first section is the command's description.
	if len(st.order) == 0 {
		st.intro = append(st.intro, trimmed)
	}
}

func (st *docsHelpState) section(name string) {
	if _, ok := st.sections[name]; !ok {
		st.order = append(st.order, name)
	}
}

func (st *docsHelpState) add(opt docsHelpOption) {
	name := "Options"
	if len(st.order) == 0 {
		st.section(name)
	} else {
		name = st.order[len(st.order)-1]
	}
	st.sections[name] = append(st.sections[name], opt)
}

// docsHelpGap splits a help line at its first column gap (two or more spaces),
// the "gap" layout the region shows.
func docsHelpGap(line string) (string, string) {
	for i := 0; i+1 < len(line); i++ {
		if line[i] == ' ' && line[i+1] == ' ' && strings.TrimSpace(line[:i]) != "" {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i:])
		}
	}
	return strings.TrimSpace(line), ""
}

func (d *docsHelp) View(state any) (lens.Node, error) {
	st, ok := state.(*docsHelpState)
	if !ok {
		return nil, errBadState
	}
	if st.usage == "" && len(st.order) == 0 {
		return nil, nil
	}
	var children []lens.Node
	if st.usage != "" {
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: "Usage", Style: "strong"}},
			Children: []lens.Node{lens.Code{Text: st.usage, Lang: "sh"}},
		})
	}
	if len(st.intro) > 0 {
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: "Description", Style: "strong"}},
			Children: []lens.Node{lens.Markdown{Text: strings.Join(st.intro, "\n")}},
		})
	}
	for _, name := range st.order {
		opts := st.sections[name]
		if len(opts) == 0 {
			continue
		}
		rows := make([][]lens.Cell, 0, len(opts))
		for _, o := range opts {
			rows = append(rows, []lens.Cell{
				docsCell(o.flags, "code"),
				docsCell(o.arg, toneMuted),
				docsCell(o.desc, ""),
			})
		}
		children = append(children, lens.Section{
			Head: []lens.Span{{Text: name, Style: "strong"}},
			Children: []lens.Node{lens.Table{
				Cols: []lens.Col{
					{ID: "option", Head: "OPTION"},
					{ID: "arg", Head: "ARG", Priority: 2},
					{ID: "description", Head: "DESCRIPTION", Grow: 1},
				},
				Rows: rows,
			}},
		})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------------------
// docs.man — a man page: a NAME/SYNOPSIS/DESCRIPTION section per heading, the
// synopsis kept as code.

type docsMan struct{}

type docsManSection struct {
	name  string
	lines []string
}

type docsManEntry struct {
	name    string
	section string
	desc    string
}

type docsManState struct {
	title    string
	sections []docsManSection
	whatis   []docsManEntry
	sawAny   bool
}

func (d *docsMan) ID() string { return "docs.man" }

func (d *docsMan) Match(cmd model.Cmd) bool { return cmd.Program == "man" }

func (d *docsMan) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &docsManState{}, nil }

func (d *docsMan) Line(state any, l model.Line) error {
	st, ok := state.(*docsManState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (d *docsMan) Finish(state any, status int) error { return nil }

func (d *docsMan) Event(state any, ev lens.Event) error { return nil }

func (st *docsManState) observe(text string) {
	t := docsManStrip(strings.TrimRight(text, " "))
	if strings.TrimSpace(t) == "" {
		return
	}
	if e, ok := docsManWhatis(t); ok {
		st.whatis = append(st.whatis, e)
		st.sawAny = true
		return
	}
	if name, ok := docsManTitle(t); ok {
		st.title = name
		st.sawAny = true
		return
	}
	if docsManHeading(t) {
		st.sections = append(st.sections, docsManSection{name: strings.TrimSpace(t)})
		st.sawAny = true
		return
	}
	if len(st.sections) == 0 {
		return // a prologue this lens does not model
	}
	last := &st.sections[len(st.sections)-1]
	last.lines = append(last.lines, t)
}

// docsManStrip removes man's overstrike formatting: `l\bl` (bold) and `_\bl`
// (underline) both mean the last character.
func docsManStrip(s string) string {
	if !strings.Contains(s, "\b") {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\b' {
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// docsManWhatis reads an `apropos`/`whatis` row: "ls (1) - list directory ...".
func docsManWhatis(t string) (docsManEntry, bool) {
	if strings.HasPrefix(t, " ") {
		return docsManEntry{}, false
	}
	open := strings.Index(t, " (")
	if open <= 0 {
		return docsManEntry{}, false
	}
	close := strings.IndexByte(t[open:], ')')
	if close < 0 {
		return docsManEntry{}, false
	}
	rest := strings.TrimSpace(t[open+close+1:])
	if !strings.HasPrefix(rest, "- ") {
		return docsManEntry{}, false
	}
	return docsManEntry{
		name:    t[:open],
		section: strings.TrimSpace(t[open+1 : open+close]),
		desc:    strings.TrimSpace(rest[2:]),
	}, true
}

// docsManTitle reads the page's running head: "LS(1)   User Commands   LS(1)".
func docsManTitle(t string) (string, bool) {
	if strings.HasPrefix(t, " ") || strings.HasPrefix(t, "\t") {
		return "", false
	}
	open := strings.IndexByte(t, '(')
	if open <= 0 {
		return "", false
	}
	if strings.ContainsAny(t[:open], " \t") {
		return "", false
	}
	close := strings.IndexByte(t[open:], ')')
	if close <= 1 {
		return "", false
	}
	if !strings.HasPrefix(t[open+close+1:], " ") {
		return "", false
	}
	return t[:open+close+1], true
}

// docsManHeadings are the section names a man page uses; an all-caps line at
// the left margin is accepted too, so an unknown page still parses.
var docsManHeadings = map[string]bool{
	"NAME": true, "SYNOPSIS": true, "DESCRIPTION": true, "OPTIONS": true,
	"EXIT STATUS": true, "RETURN VALUE": true, "EXIT CODE": true,
	"SEE ALSO": true, "EXAMPLES": true, "EXAMPLE": true, "FILES": true,
	"ENVIRONMENT": true, "DIAGNOSTICS": true, "NOTES": true, "BUGS": true,
	"HISTORY": true, "STANDARDS": true, "AUTHOR": true, "AUTHORS": true,
	"COPYRIGHT": true, "REPORTING BUGS": true, "AVAILABILITY": true,
	"COMMANDS": true, "GENERAL OPTIONS": true, "OVERVIEW": true,
	"VERSION": true, "ATTRIBUTES": true, "CONFORMING TO": true, "COLOPHON": true,
}

func docsManHeading(t string) bool {
	if t == "" || t[0] == ' ' || t[0] == '\t' || t[0] == '-' {
		return false
	}
	if docsManHeadings[t] {
		return true
	}
	if len(t) > 32 || len(strings.Fields(t)) > 3 {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z', r == ' ', r == '-', r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// docsManDedent drops the common left margin of a section body and the blank
// lines around it.
func docsManDedent(lines []string) []string {
	margin := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if margin < 0 || n < margin {
			margin = n
		}
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if margin > 0 && len(l) >= margin {
			l = l[margin:]
		}
		out = append(out, strings.TrimRight(l, " "))
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// docsManCodeSections keep their layout; the rest reads as markdown.
var docsManCodeSections = map[string]bool{
	"SYNOPSIS": true, "EXAMPLES": true, "EXAMPLE": true, "COMMANDS": true,
}

func (d *docsMan) View(state any) (lens.Node, error) {
	st, ok := state.(*docsManState)
	if !ok {
		return nil, errBadState
	}
	if len(st.sections) == 0 {
		if len(st.whatis) == 0 {
			return nil, nil
		}
		items := make([]lens.Item, 0, len(st.whatis))
		for _, e := range st.whatis {
			items = append(items, lens.Item{
				Label:  []lens.Span{{Text: e.name, Style: "strong"}},
				Detail: []lens.Span{{Text: "(" + e.section + ")", Style: toneMuted}},
				Value:  []lens.Span{{Text: e.desc}},
			})
		}
		return lens.List{Items: items}, nil
	}
	children := []lens.Node{}
	if st.title != "" {
		children = append(children, lens.Spans(lens.Span{Text: st.title, Style: "strong"}))
	}
	for _, s := range st.sections {
		body := docsManDedent(s.lines)
		if len(body) == 0 {
			continue
		}
		text := strings.Join(body, "\n")
		var child lens.Node = lens.Markdown{Text: text}
		if docsManCodeSections[s.name] {
			child = lens.Code{Text: text, Lang: "sh"}
		}
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: s.name, Style: "strong"}},
			Children: []lens.Node{child},
		})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}
