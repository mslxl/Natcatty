package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID: "files.ls",
		// No globs: `ls*` also claimed `lsof`. The aliases `ll`/`la`/`lsa`
		// normalize to `ls -l`/`ls -la`, and the lens's own Match checks the
		// exact programs ls/eza/exa/lsd.
		New: func() lens.Lens { return &filesLS{} },
	})
}

// filesLS turns `ls -l` (and eza/exa/lsd long listings) into a table, and a
// plain `ls` into a list of names. Unknown lines are ignored; a long listing
// that parses nothing falls back to raw.
type filesLS struct{}

type lsRow struct {
	mode, links, owner, group, size string
	sizeBytes                       int64
	date                            string
	name, target                    string
	dir                             bool
}

type lsState struct {
	expectLong bool
	rows       []lsRow
	maxSize    int64
	names      []string
	// unparsed is output the plain fallback does not understand (a program
	// error, a recursive listing's heading). The block then stays raw instead
	// of showing a list guessed from it.
	unparsed bool

	// PowerShell's `ls` is Get-ChildItem: a "Directory:" heading, then a
	// Mode/LastWriteTime/Length/Name header over two rules, then the rows. It is a
	// different table from ls -l, so it is read on its own.
	psDir  string
	psMode bool
	psRows []psRow
}

type psRow struct {
	mode, date, size, name string
	dir                    bool
}

func (f *filesLS) ID() string { return "files.ls" }

func (f *filesLS) Match(cmd model.Cmd) bool {
	switch cmd.Program {
	case "ls", "eza", "exa", "lsd":
		return true
	}
	return false
}

func (f *filesLS) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	long := false
	for _, a := range cmd.Args {
		switch {
		case a == "--long":
			long = true
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
			if strings.ContainsAny(a[1:], "lgo") {
				long = true
			}
		}
	}
	return &lsState{expectLong: long}, nil
}

func (f *filesLS) Line(state any, l model.Line) error {
	st, ok := state.(*lsState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (f *filesLS) Finish(state any, status int) error { return nil }

func (f *filesLS) Event(state any, ev lens.Event) error { return nil }

func (st *lsState) observe(text string) {
	if text == "" || strings.HasPrefix(text, "total ") {
		return
	}
	trimmed := strings.TrimSpace(text)
	// The capture screen hands each row over as a line, so the heading and the table
	// header arrive separately even though the formatter draws them with cursor
	// positioning (see the tap's CSI handling).
	if strings.HasPrefix(trimmed, "Directory:") {
		st.psDir = strings.TrimSpace(strings.TrimPrefix(trimmed, "Directory:"))
		return
	}
	if !st.psMode {
		if strings.HasPrefix(trimmed, "Mode") && strings.Contains(trimmed, "LastWriteTime") {
			st.psMode = true
			return
		}
	} else {
		if strings.HasPrefix(trimmed, "----") || strings.HasPrefix(trimmed, "Name") {
			return
		}
		if row, ok := parsePSRow(trimmed); ok {
			st.psRows = append(st.psRows, row)
			return
		}
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	if row, ok := parseLSRow(fields); ok {
		if row.sizeBytes > st.maxSize {
			st.maxSize = row.sizeBytes
		}
		st.rows = append(st.rows, row)
		return
	}
	// A plain ls prints names; keep them for the list fallback. A long
	// listing's continuation lines (wrapped names) are not worth guessing.
	// The plain fallback understands name listings only: a program error or a
	// recursive listing's directory heading is not a name, and a widget guessed
	// from it would be wrong, so leave the block raw.
	if !st.expectLong {
		if listingError(trimmed) || strings.HasSuffix(trimmed, ":") {
			st.unparsed = true
			return
		}
		st.names = append(st.names, splitListingLine(trimmed)...)
	}
}

// parsePSRow parses one Get-ChildItem row:
//
//	-a----        10/28/2026   9:04 AM             60 .bash_history
//	d-----         7/22/2026   8:11 PM                .git
func parsePSRow(t string) (psRow, bool) {
	f := strings.Fields(t)
	if len(f) < 4 || len(f[0]) < 5 || !psModeToken(f[0]) {
		return psRow{}, false
	}
	i := 1
	if !psDateToken(f[i]) {
		return psRow{}, false
	}
	date := f[i]
	i++
	if i >= len(f) || !psTimeToken(f[i]) {
		return psRow{}, false
	}
	date += " " + f[i]
	i++
	if i < len(f) && (strings.EqualFold(f[i], "AM") || strings.EqualFold(f[i], "PM")) {
		date += " " + f[i]
		i++
	}
	size := ""
	if i < len(f) {
		if digits := strings.ReplaceAll(f[i], ",", ""); digits != "" && allDigits(digits) {
			size = f[i]
			i++
		}
	}
	name := strings.Join(f[i:], " ")
	if name == "" {
		return psRow{}, false
	}
	return psRow{mode: f[0], date: date, size: size, name: name, dir: f[0][0] == 'd'}, true
}

// psModeToken reports whether s is a Get-ChildItem mode such as -a----, d----- or
// la--- (six cells of d/l/- and attributes).
func psModeToken(s string) bool {
	if len(s) < 5 {
		return false
	}
	switch s[0] {
	case 'd', 'l', '-', 'a':
	default:
		return false
	}
	for _, c := range s[1:] {
		if !(c == '-' || c == 'a' || c == 'l' || c == 'r' || c == 'h' || c == 's') {
			return false
		}
	}
	return true
}

// psTimeToken is Get-ChildItem's clock: "9:04" (one-digit hour), "10:01" or
// "9:04:23", where Unix ls always pads to two digits.
func psTimeToken(s string) bool {
	if len(s) == 4 && s[1] == ':' {
		return allDigits(s[:1]) && allDigits(s[2:])
	}
	return lsTimeToken(s)
}

// psDateToken reports whether s is a Get-ChildItem date ("10/28/2026" in the
// system's short format, or an ISO date).
func psDateToken(s string) bool {
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		return allDigits(s[:4]) && allDigits(s[5:7]) && allDigits(s[8:10])
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '-' || r == '.' })
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || !allDigits(p) {
			return false
		}
	}
	return true
}

// parseLSRow parses one long-format line:
//
//	-rw-r--r--  1 alice staff 1.5K Jan  2 15:04 main.go
//	drwxr-xr-x  5 alice staff  160 Jan  2 15:04 src
func parseLSRow(fields []string) (lsRow, bool) {
	if len(fields) < 6 {
		return lsRow{}, false
	}
	fields = mergeDeviceSizes(fields)
	if !looksLikeMode(fields[0]) {
		return lsRow{}, false
	}
	i := 1
	if i >= len(fields) || !allDigits(fields[i]) {
		return lsRow{}, false
	}
	links := fields[i]
	i++
	month := -1
	for j := i; j < len(fields); j++ {
		if lsDateToken(fields[j]) {
			month = j
			break
		}
	}
	if month < 1 {
		return lsRow{}, false
	}
	owner, group := "", ""
	switch n := month - i; {
	case n == 1:
		owner = fields[i]
	case n == 2:
		owner, group = fields[i], fields[i+1]
	case n > 2:
		owner, group = fields[i], fields[month-1]
	default:
		return lsRow{}, false
	}
	sizeText := fields[month-1]
	sizeBytes, ok := parseSizeBytes(sizeText)
	if !ok {
		return lsRow{}, false
	}
	// ls prints the date in two or three columns depending on the style:
	// "Jan  2 15:04" (3), "2026-01-02 15:04" (2, --time-style=long-iso) and
	// "2026-01-02 15:04:05.123456789 +0800" (3, --full-time).
	// "Jan  2 15:04" (month style) is always three columns; an ISO date takes
	// two ("2026-01-02 15:04") or three with the zone ("… +0800", --full-time).
	dateWidth := 3
	if !monthAbbrev(fields[month]) {
		switch {
		case month+1 >= len(fields) || !lsTimeToken(fields[month+1]):
			dateWidth = 1
		case month+2 < len(fields) && lsTZToken(fields[month+2]):
			dateWidth = 3
		default:
			dateWidth = 2
		}
	}
	if month+dateWidth >= len(fields) {
		return lsRow{}, false
	}
	date := strings.Join(fields[month:month+dateWidth], " ")
	nameFields := make([]string, 0, len(fields)-month-dateWidth)
	for _, field := range fields[month+dateWidth:] {
		if !isIconToken(field) {
			nameFields = append(nameFields, field)
		}
	}
	name := strings.Join(nameFields, " ")
	if name == "" {
		return lsRow{}, false
	}
	row := lsRow{
		mode: fields[0], links: links, owner: owner, group: group,
		size: sizeText, sizeBytes: sizeBytes, date: date, name: name,
		dir: fields[0][0] == 'd',
	}
	if i := strings.Index(name, " -> "); i >= 0 {
		row.name, row.target = name[:i], name[i+4:]
	}
	return row, true
}

// lsDateToken reports whether s starts ls's date column: a month name ("Jan")
// or an ISO date ("2026-01-02", from --time-style / --full-time).
func lsDateToken(s string) bool {
	if monthAbbrev(s) {
		return true
	}
	return len(s) == 10 && s[4] == '-' && s[7] == '-' &&
		allDigits(s[:4]) && allDigits(s[5:7]) && allDigits(s[8:10])
}

// lsTimeToken reports whether s is a clock time ("15:04", "15:04:05.123").
func lsTimeToken(s string) bool {
	if len(s) < 5 || s[2] != ':' || !allDigits(s[:2]) || !allDigits(s[3:5]) {
		return false
	}
	for i := 5; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9') && c != ':' && c != '.' && c != ',' {
			return false
		}
	}
	return true
}

// lsTZToken reports whether s is the zone column ls --full-time prints.
func lsTZToken(s string) bool {
	if s == "Z" || s == "UTC" || s == "GMT" {
		return true
	}
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return false
	}
	return allDigits(s[1:])
}

// mergeDeviceSizes joins a device size split by Fields: "1," "3" -> "1, 3".
func mergeDeviceSizes(fields []string) []string {
	for i := 0; i < len(fields)-1; i++ {
		if strings.HasSuffix(fields[i], ",") {
			out := make([]string, 0, len(fields)-1)
			out = append(out, fields[:i]...)
			out = append(out, fields[i]+" "+fields[i+1])
			out = append(out, fields[i+2:]...)
			return out
		}
	}
	return fields
}

// isIconToken reports a whitespace-separated field that is a file-type icon
// rather than a name. eza and lsd print a Nerd Font glyph from a private use
// area before each entry (" Applications"), so a plain listing's fields are
// icon, name, icon, name; parsing the icons as names listed each one on a row
// of its own and gave every icon row the same list key.
func isIconToken(field string) bool {
	if field == "" {
		return false
	}
	for _, r := range field {
		if !privateUseRune(r) {
			return false
		}
	}
	return true
}

// privateUseRune reports the Unicode private use areas, where Nerd Font icons
// (and other icon fonts) live.
func privateUseRune(r rune) bool {
	return (r >= 0xE000 && r <= 0xF8FF) ||
		(r >= 0xF0000 && r <= 0xFFFFD) ||
		(r >= 0x100000 && r <= 0x10FFFD)
}

// splitListingLine splits one plain listing line into its entries. ls -C and
// eza's grid separate columns with two or more spaces (or a tab), while a single
// space belongs to the name, so "My Documents" stays one entry. Splitting on
// every space cut such names in two.
func splitListingLine(line string) []string {
	var out []string
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) {
			if line[i] == '\t' {
				break
			}
			if line[i] == ' ' {
				j := i
				for j < len(line) && line[j] == ' ' {
					j++
				}
				if j-i >= 2 {
					break
				}
				i = j
				continue
			}
			i++
		}
		if entry := stripListingIcon(strings.TrimSpace(line[start:i])); entry != "" {
			out = append(out, entry)
		}
		i++ // the tab or the first space that ended the entry
	}
	return out
}

// stripListingIcon removes the icon glyph eza and lsd print in front of an
// entry (" Applications", and in a grid " 'My Documents'" with no space after
// the glyph); the glyph is not part of the name.
func stripListingIcon(entry string) string {
	trimmed := strings.TrimLeftFunc(entry, privateUseRune)
	if trimmed == entry {
		return entry
	}
	return strings.TrimSpace(trimmed)
}

// listingError reports a listing program's own error line ("ls: cannot access
// …"), which must not become a name in the plain fallback.
func listingError(line string) bool {
	for _, program := range []string{"ls", "eza", "exa", "lsd"} {
		if strings.HasPrefix(line, program+":") {
			return true
		}
	}
	return false
}

func looksLikeMode(s string) bool {
	if len(s) < 10 {
		return false
	}
	switch s[0] {
	case '-', 'd', 'l', 'b', 'c', 'p', 's':
	default:
		return false
	}
	n := 0
	for _, r := range s[1:] {
		switch r {
		case 'r', 'w', 'x', '-', 's', 'S', 't', 'T':
			n++
		}
	}
	return n >= 6
}

func lsModeStyle(row lsRow) string {
	switch {
	case row.dir:
		return "strong accent"
	case row.mode[0] == 'l':
		return toneAccent
	case strings.Contains(row.mode, "x"):
		return toneSuccess
	}
	return toneMuted
}

func (f *filesLS) View(state any) (lens.Node, error) {
	st, ok := state.(*lsState)
	if !ok {
		return nil, errBadState
	}
	if len(st.psRows) > 0 {
		return lsPSTable(st), nil
	}
	if len(st.rows) > 0 {
		return lsTable(st), nil
	}
	if !st.expectLong && !st.unparsed && len(st.names) > 0 {
		return lsList(st.names), nil
	}
	return nil, nil
}

// lsPSTable lays a Get-ChildItem listing out: the directory it lists, then Mode,
// LastWriteTime, Length and Name, the sizes humanised and directories marked.
func lsPSTable(st *lsState) lens.Node {
	cols := []lens.Col{
		{ID: "mode", Head: "Mode", Priority: 4, Truncate: lens.TruncateEnd},
		{ID: "date", Head: "LastWriteTime", Priority: 2, Truncate: lens.TruncateEnd},
		{ID: "size", Head: "Length", Align: lens.AlignEnd, Priority: 1, Truncate: lens.TruncateEnd},
		{ID: "name", Head: "Name", Grow: 1, Truncate: lens.TruncateMiddle},
	}
	rows := make([][]lens.Cell, 0, len(st.psRows))
	for _, r := range st.psRows {
		size := r.size
		if digits := strings.ReplaceAll(r.size, ",", ""); digits != "" && allDigits(digits) {
			if n, err := strconv.ParseInt(digits, 10, 64); err == nil {
				size = humanBytes(n)
			}
		}
		name, style := r.name, "path"
		if r.dir {
			name, style = r.name+"/", "path strong"
		}
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: r.mode, Style: toneMuted}}},
			{Spans: []lens.Span{{Text: r.date, Style: toneMuted}}},
			{Spans: []lens.Span{{Text: size, Style: "num"}}},
			{Spans: []lens.Span{{Text: name, Style: style}}},
		})
	}
	children := []lens.Node{}
	if st.psDir != "" {
		children = append(children, lens.Spans(lens.Span{Text: st.psDir, Style: toneMuted}))
	}
	children = append(children, lens.Table{Cols: cols, Rows: rows})
	return lens.Cols(children...)
}

// lsTable lays the listing out with the size right aligned, a meter for how
// much of the largest entry each one is, and the name growing.
func lsTable(st *lsState) lens.Node {
	cols := []lens.Col{
		{ID: "mode", Head: "Mode", Align: lens.AlignStart, Priority: 4, Truncate: lens.TruncateEnd},
		{ID: "links", Head: "Links", Align: lens.AlignEnd, Priority: 5, Truncate: lens.TruncateEnd},
		{ID: "owner", Head: "Owner", Align: lens.AlignStart, Priority: 2, Grow: 1, Truncate: lens.TruncateEnd},
		{ID: "group", Head: "Group", Align: lens.AlignStart, Priority: 3, Truncate: lens.TruncateEnd},
		{ID: "size", Head: "Size", Align: lens.AlignEnd, Priority: 1, Truncate: lens.TruncateEnd},
		{ID: "usage", Head: "Usage", Align: lens.AlignStart, Priority: 6, Grow: 1},
		{ID: "name", Head: "Name", Align: lens.AlignStart, Grow: 6, Truncate: lens.TruncateMiddle},
	}
	rows := make([][]lens.Cell, 0, len(st.rows))
	for _, r := range st.rows {
		nameSpans := []lens.Span{{Text: r.name, Style: lsNameStyle(r)}}
		if r.target != "" {
			nameSpans = append(nameSpans, lens.Span{Text: " → " + r.target, Style: toneMuted})
		}
		ratio := 0.0
		if st.maxSize > 0 {
			ratio = float64(r.sizeBytes) / float64(st.maxSize)
		}
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: r.mode, Style: lsModeStyle(r)}}},
			{Spans: []lens.Span{{Text: r.links, Style: "num"}}},
			{Spans: []lens.Span{{Text: r.owner}}},
			{Spans: []lens.Span{{Text: r.group}}},
			{Spans: []lens.Span{{Text: r.size, Style: "num"}}},
			{Meter: &lens.MeterCell{Value: ratio}},
			{Spans: nameSpans},
		})
	}
	return lens.Table{Cols: cols, Rows: rows}
}

func lsNameStyle(r lsRow) string {
	if r.dir {
		return "strong accent"
	}
	return ""
}

func lsList(names []string) lens.Node {
	items := make([]lens.Item, 0, len(names))
	for _, n := range names {
		style := ""
		if strings.HasSuffix(n, "/") {
			style = "strong accent"
		}
		items = append(items, lens.Item{Label: []lens.Span{{Text: n, Style: style}}})
	}
	// A directory can be long; the filter box narrows it as it is typed, the way
	// Terminal's file listing lenses offer a search input.
	return lens.List{Items: items, Filter: len(items) > 8}
}
