// sys.go implements Terminal's `sys` lens family: ps, df, env, ss, uptime, system
// info, shell history, cal and brew. Each lens is registered from init below
// and its View returns nil when the output is not what the command prints,
// which leaves the block showing raw output.
//
// # Mined literals (from payload\flow.exe, constant region)
//
// These are byte-for-byte readable windows with their file offsets (the same
// dump as findings\lens-conditions\raw-lens-region-runs.txt; the ids are also
// in findings\lens-conditions\raw-lens-ids-with-offset.txt):
//
//	149957712: errorwarning: successpendingpid=neutralinfoaccentCapacitynameNamecurrentInstalledlatestLatestversionsVersionsMounted onMounted
//	149958136: metercpu_meterLocal Address:PortLocalPeer Address:PortPeerProcesstcpudpgaplgwrap
//	149958233: usedbuff/cacheMemory
//	149958258: " used "   149958269: " buff/cache "   149958286: " total "
//	149958428: thresholdsmd--json
//	149958464: sys.brew-jsys.cal-l
//	149958504: sys.history-s--shortsys.info-a--allvm.loadavgsys.load--pretty--since--summary
//	149958616: sys.sockets-r-I-m-g-M--route--statistics--interfaces--groups--masquerade-t-Fsys.env-0--null
//	149958744: --outputsys.df--list-name--list-fullsys.ps
//	149958791: BKiBMiBGiBTiBPiB
//	149958924: 1 min5 min15 mind0d1d2d3d4d5d6
//	149959072: pidcommand linkload average, TimeUpUsers -://ProtoLocal AddressForeign AddressForeignPID/Program nameInternet connectionsMultipathprotolocalremoteRemotestateprocesspage size of ... in use ... free of ...
//
// From those literals this file takes as observed: the nine ids; the flags
// (-j for brew, -l for cal, -s/--short for history, -a/--all for info,
// --pretty/--since/--summary for load, -0/--null for env, --output for df);
// the ps column vocabulary (PID/Program/State/CPU/MEMORY) and the cpu_meter
// meter; the ss column vocabulary (Local Address:Port/Peer Address:Port/
// Process/tcp/udp); df's Mounted on, Used, buff/cache, total and the KiB..PiB
// size units; uptime's "1 min/5 min/15 min" and "load average"; history's
// number column; Cal's "-l".
//
// # Inferences (marked 【推断】 where they are used)
//
// The program names of the trigger surface are not in those windows (only the
// ids and flags are), so "the program is ps/df/env/ss/uptime/uname/history/
// cal/brew" is 【推断】 from the literal column vocabulary and the ids. The
// output grammars (GNU/BSD ps fields, df columns, ss rows, uptime phrasing,
// brew's --json shapes) are the tools' documented formats【推断】; Terminal's own
// parsers are code that was not read.
//
// brew is the one lens whose mined trigger is a flag (`-j`, 149958464) rather
// than a column vocabulary, so besides the --json shape it also reads the
// human `brew outdated` line "name (installed) < latest" (formulae) and
// "name (installed) != latest" (casks), with the `==> Outdated Formulae` /
// `==> Outdated Casks` headers skipped. That human form is not in the mined
// literals: it is 【推断】 from Homebrew's documented output, accepted only for
// the strict shape (name, parenthesised version, comparison, version) so
// unrelated text still falls back to raw.
//
// # Registration note
//
// These lenses register with no globs and match in code, unlike the earlier
// families. lens.Lookup falls back to matching pattern+"*", so a glob "ss"
// would also claim "ssh" and a glob "ps" would claim "psql"; matching in code
// keeps the trigger surface exact. See glob in internal/lens/lens.go.
package builtin

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{ID: "sys.ps", New: func() lens.Lens { return &sysPS{} }})
	lens.Register(lens.Definition{ID: "sys.df", New: func() lens.Lens { return &sysDF{} }})
	lens.Register(lens.Definition{ID: "sys.env", New: func() lens.Lens { return &sysEnv{} }})
	lens.Register(lens.Definition{ID: "sys.sockets", New: func() lens.Lens { return &sysSockets{} }})
	lens.Register(lens.Definition{ID: "sys.load", New: func() lens.Lens { return &sysLoad{} }})
	lens.Register(lens.Definition{ID: "sys.info", New: func() lens.Lens { return &sysInfo{} }})
	lens.Register(lens.Definition{ID: "sys.history", New: func() lens.Lens { return &sysHistory{} }})
	lens.Register(lens.Definition{ID: "sys.cal", New: func() lens.Lens { return &sysCal{} }})
	lens.Register(lens.Definition{ID: "sys.brew", New: func() lens.Lens { return &sysBrew{} }})
}

// sysMaxRows bounds every table's rows; the rest become an Overflow node.
const sysMaxRows = 200

// sysCap keeps the first max rows and reports how many were hidden.
func sysCap[T any](rows []T, max int) ([]T, int) {
	if len(rows) <= max {
		return rows, 0
	}
	return rows[:max], len(rows) - max
}

// sysFloat reads a number, ignoring a trailing percent sign.
func sysFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0
	}
	return f
}

// sysNum formats a number without trailing zeros.
func sysNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// sysMeter is a thin bar cell; warn/bad of 0 leave the thresholds unset.
func sysMeter(value, warn, bad float64) *lens.MeterCell {
	m := &lens.MeterCell{Value: value}
	if warn > 0 || bad > 0 {
		m.Thresholds = &lens.Thresholds{Warn: warn, Bad: bad}
	}
	return m
}

func sysSpan(style, text string) lens.Span  { return lens.Span{Text: text, Style: style} }
func sysCells(spans ...lens.Span) lens.Cell { return lens.Cell{Spans: spans} }

// sysLastInt is the last integer in s, or 0 (builtin.go has firstInt; this
// family keeps its own helpers prefixed so families never collide).
func sysLastInt(s string) int {
	nums := parseFloats(s)
	if len(nums) == 0 {
		return 0
	}
	return int(nums[len(nums)-1])
}

// sysTable wraps a table with an Overflow node when rows were hidden.
func sysTable(cols []lens.Col, rows [][]lens.Cell, hidden int) lens.Node {
	if hidden <= 0 {
		return lens.Table{Cols: cols, Rows: rows}
	}
	return lens.Cols(lens.Table{Cols: cols, Rows: rows}, lens.Overflow{Hidden: hidden})
}

// ---------------------------------------------------------------------------
// sys.ps
// ---------------------------------------------------------------------------

// sysPS parses `ps aux`, `ps -ef` and `ps axo ...` by reading the header the
// command prints, so any column selection works. CPU and memory get both a
// number and a meter (the cpu_meter the constant region names).
type sysPS struct{}

type sysPSRow struct {
	pid, command, state string
	cpu, mem            float64
}

type sysPSState struct {
	cols []string
	rows []sysPSRow
}

func (s *sysPS) ID() string                                    { return "sys.ps" }
func (s *sysPS) Match(cmd model.Cmd) bool                      { return cmd.Program == "ps" } // 【推断】program name
func (s *sysPS) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysPSState{}, nil }
func (s *sysPS) Finish(state any, status int) error            { return nil }
func (s *sysPS) Event(state any, ev lens.Event) error          { return nil }

func (s *sysPS) Line(state any, l model.Line) error {
	st, ok := state.(*sysPSState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysPSState) observe(text string) {
	if text == "" {
		return
	}
	if st.cols == nil {
		if f := strings.Fields(text); sysPSHeader(f) {
			st.cols = make([]string, len(f))
			for i, c := range f {
				st.cols[i] = strings.ToUpper(c)
			}
		}
		return
	}
	f := strings.Fields(text)
	if len(f) < 2 {
		return
	}
	row := sysPSRow{}
	for i, name := range st.cols {
		if i >= len(f) {
			break
		}
		v := f[i]
		if i == len(st.cols)-1 {
			v = strings.Join(f[i:], " ") // the command takes the rest
		}
		switch name {
		case "PID":
			if !allDigits(v) {
				return
			}
			row.pid = v
		case "COMMAND", "CMD", "COMM", "ARGS":
			row.command = v
		case "STAT", "STATE":
			row.state = v
		case "%CPU", "CPU", "C":
			row.cpu = sysFloat(v)
		case "%MEM", "MEM", "%MEMORY", "MEMORY":
			row.mem = sysFloat(v)
		}
	}
	if row.pid == "" || row.command == "" {
		return
	}
	st.rows = append(st.rows, row)
}

// sysPSHeader reports whether a line is a ps column header. ps accepts its own
// column selection (`-o pid,comm`, the `-o/--output` flags mined with
// `CPU`/`MEMORY` near 783151), so any header with a PID column counts: unknown
// columns are simply ignored when the row is read.
func sysPSHeader(f []string) bool {
	hasPID := false
	for _, c := range f {
		if strings.EqualFold(c, "PID") {
			hasPID = true
			break
		}
	}
	return hasPID && len(f) >= 2
}

func (s *sysPS) View(state any) (lens.Node, error) {
	st, ok := state.(*sysPSState)
	if !ok {
		return nil, errBadState
	}
	if len(st.rows) == 0 {
		return nil, nil
	}
	rows, hidden := sysCap(st.rows, sysMaxRows)
	cols := []lens.Col{
		{ID: "pid", Head: "PID", Align: lens.AlignEnd, Priority: 1},
		{ID: "command", Head: "Program", Grow: 5, Truncate: lens.TruncateMiddle},
		{ID: "state", Head: "State", Priority: 3},
		{ID: "cpu", Head: "CPU %", Align: lens.AlignEnd, Priority: 2},
		{ID: "mem", Head: "MEM %", Align: lens.AlignEnd, Priority: 2},
		{ID: "cpu_meter", Head: "CPU", Priority: 6},
		{ID: "mem_meter", Head: "MEM", Priority: 6},
	}
	out := make([][]lens.Cell, 0, len(rows))
	for _, r := range rows {
		out = append(out, []lens.Cell{
			sysCells(sysSpan("num", r.pid)),
			sysCells(sysSpan("", r.command)),
			sysCells(sysSpan("", r.state)),
			sysCells(sysSpan("num", sysNum(r.cpu))),
			sysCells(sysSpan("num", sysNum(r.mem))),
			{Meter: sysMeter(r.cpu/100, 0, 0)},
			{Meter: sysMeter(r.mem/100, 0.5, 0.9)},
		})
	}
	return sysTable(cols, out, hidden), nil
}

// ---------------------------------------------------------------------------
// sys.df
// ---------------------------------------------------------------------------

// sysDF parses df, df -h, df -i and df --output. The used percentage is a
// meter with thresholds (the `thresholds` literal, offset 149958428).
type sysDF struct{}

type sysDFRow struct {
	fs, size, used, avail, mount string
	pct                          float64
}

type sysDFState struct {
	rows []sysDFRow
}

func (s *sysDF) ID() string                                    { return "sys.df" }
func (s *sysDF) Match(cmd model.Cmd) bool                      { return cmd.Program == "df" } // 【推断】program name
func (s *sysDF) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysDFState{}, nil }
func (s *sysDF) Finish(state any, status int) error            { return nil }
func (s *sysDF) Event(state any, ev lens.Event) error          { return nil }

func (s *sysDF) Line(state any, l model.Line) error {
	st, ok := state.(*sysDFState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysDFState) observe(text string) {
	if text == "" {
		return
	}
	f := strings.Fields(text)
	if len(f) < 3 { // filesystem, use%, mount
		return
	}
	if sysDFHeader(text, f) {
		return
	}
	pct := -1
	for i, v := range f {
		if strings.HasSuffix(v, "%") && allDigits(strings.TrimSuffix(v, "%")) {
			pct = i
			break
		}
	}
	// df accepts its own field list (`--output=source,pcent,target`, mined at
	// 783182), so the three size columns before the percentage are optional:
	// read them only when they are there.
	if pct < 1 || pct+1 > len(f)-1 {
		return
	}
	row := sysDFRow{
		fs:    f[0],
		mount: strings.Join(f[pct+1:], " "),
		pct:   sysFloat(f[pct]),
	}
	if pct >= 3 {
		row.size, row.used, row.avail = f[pct-3], f[pct-2], f[pct-1]
	}
	if row.mount == "" {
		return
	}
	st.rows = append(st.rows, row)
}

// sysDFHeader reports whether a line is df's header, from the literals
// "Mounted on" and "Capacity".
func sysDFHeader(text string, f []string) bool {
	if strings.Contains(text, "Mounted") || strings.Contains(text, "Capacity") {
		return true
	}
	switch f[0] {
	case "Filesystem", "Source", "File system", "Type":
		return strings.Contains(text, "%")
	}
	return false
}

func (s *sysDF) View(state any) (lens.Node, error) {
	st, ok := state.(*sysDFState)
	if !ok {
		return nil, errBadState
	}
	if len(st.rows) == 0 {
		return nil, nil
	}
	rows, hidden := sysCap(st.rows, sysMaxRows)
	cols := []lens.Col{
		{ID: "filesystem", Head: "Filesystem", Grow: 2, Truncate: lens.TruncateMiddle},
		{ID: "size", Head: "Size", Align: lens.AlignEnd, Priority: 2},
		{ID: "used", Head: "Used", Align: lens.AlignEnd, Priority: 1},
		{ID: "avail", Head: "Available", Align: lens.AlignEnd, Priority: 2},
		{ID: "use_pct", Head: "Use %", Align: lens.AlignEnd, Priority: 1},
		{ID: "used_meter", Head: "Used", Priority: 5},
		{ID: "mount", Head: "Mounted on", Grow: 3, Truncate: lens.TruncateMiddle},
	}
	out := make([][]lens.Cell, 0, len(rows))
	for _, r := range rows {
		out = append(out, []lens.Cell{
			sysCells(sysSpan("", r.fs)),
			sysCells(sysSpan("num", r.size)),
			sysCells(sysSpan("num", r.used)),
			sysCells(sysSpan("num", r.avail)),
			sysCells(sysSpan("num", sysNum(r.pct)+"%")),
			{Meter: sysMeter(r.pct/100, 0.8, 0.95)},
			sysCells(sysSpan("path", r.mount)),
		})
	}
	return sysTable(cols, out, hidden), nil
}

// ---------------------------------------------------------------------------
// sys.env
// ---------------------------------------------------------------------------

// sysEnv shows KEY=VALUE output as key/value pairs; `env -0 --null` separates
// entries with NUL, which the tap keeps inside one line.
type sysEnv struct{}

type sysEnvState struct {
	items [][2]string
}

func (s *sysEnv) ID() string { return "sys.env" }

// Match: lens.Normalize treats "env" as a wrapper and looks through it, so a
// bare `env`, `env -0` or `env -i` normalizes to an empty Program. Those, and
// only those, are this lens's: `env VAR=x cmd` normalizes to cmd, which some
// other lens owns. printenv is not a wrapper.
func (s *sysEnv) Match(cmd model.Cmd) bool { // 【推断】program names
	switch cmd.Program {
	case "env", "printenv":
		return true
	}
	if cmd.Program != "" {
		return false
	}
	switch sysCommandWord(cmd.Typed, cmd.Line) {
	case "env", "printenv":
		return true
	}
	return false
}

// sysCommandWord is the first word of a command line that is not a VAR=value
// assignment.
func sysCommandWord(lines ...string) string {
	for _, line := range lines {
		for _, f := range strings.Fields(line) {
			if strings.ContainsRune(f, '=') {
				continue
			}
			return f
		}
	}
	return ""
}

func (s *sysEnv) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysEnvState{}, nil }
func (s *sysEnv) Finish(state any, status int) error            { return nil }
func (s *sysEnv) Event(state any, ev lens.Event) error          { return nil }

func (s *sysEnv) Line(state any, l model.Line) error {
	st, ok := state.(*sysEnvState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysEnvState) observe(text string) {
	for _, part := range strings.Split(text, "\x00") {
		if part == "" {
			continue
		}
		i := strings.IndexByte(part, '=')
		if i <= 0 || !sysEnvKey(part[:i]) {
			continue
		}
		st.items = append(st.items, [2]string{part[:i], part[i+1:]})
	}
}

// sysEnvKey reports whether s is a shell variable name.
func sysEnvKey(s string) bool {
	for i, r := range s {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return s != ""
}

func (s *sysEnv) View(state any) (lens.Node, error) {
	st, ok := state.(*sysEnvState)
	if !ok {
		return nil, errBadState
	}
	if len(st.items) == 0 {
		return nil, nil
	}
	items, hidden := sysCap(st.items, sysMaxRows)
	kv := lens.KV{Items: make([][2][]lens.Span, 0, len(items))}
	for _, it := range items {
		kv.Items = append(kv.Items, [2][]lens.Span{
			{sysSpan("key", it[0])},
			{sysSpan("", it[1])},
		})
	}
	if hidden > 0 {
		return lens.Cols(kv, lens.Overflow{Hidden: hidden}), nil
	}
	return kv, nil
}

// ---------------------------------------------------------------------------
// sys.sockets
// ---------------------------------------------------------------------------

// sysSockets parses `ss` connection tables (the literal names Local
// Address:Port, Peer Address:Port, Process, tcp, udp) and the `ss -s`
// statistics summary.
type sysSockets struct{}

type sysSockRow struct {
	netid, state, recv, send, local, peer, process string
}

type sysSocketsState struct {
	header bool
	cols   []string // the header's column names, when ss printed one
	conns  []sysSockRow
	stats  [][2]string
	lines  []string // header + rows, for forms the socket layout does not cover
}

func (s *sysSockets) ID() string                                    { return "sys.sockets" }
func (s *sysSockets) Match(cmd model.Cmd) bool                      { return cmd.Program == "ss" } // 【推断】program name
func (s *sysSockets) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysSocketsState{}, nil }
func (s *sysSockets) Finish(state any, status int) error            { return nil }
func (s *sysSockets) Event(state any, ev lens.Event) error          { return nil }

func (s *sysSockets) Line(state any, l model.Line) error {
	st, ok := state.(*sysSocketsState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysSocketsState) observe(text string) {
	if text == "" {
		return
	}
	f := strings.Fields(text)
	if len(f) == 0 {
		return
	}
	// Any ss report starts with a header row; keep it (and the rows) so the
	// generic forms (`--route`, `--statistics`, `--interfaces`) can be parsed
	// with the shared columns parser, and so the socket form knows its layout.
	if strings.Contains(text, "Recv-Q") && strings.Contains(text, "Send-Q") {
		st.header = true
		st.cols = f
		st.lines = append(st.lines, text)
		return
	}
	if !st.header && len(st.cols) == 0 {
		if h := sysSockHeader(f); h != nil {
			st.header = true
			st.cols = h
			st.lines = append(st.lines, text)
			return
		}
	}
	if st.header {
		st.lines = append(st.lines, text)
	}
	// Socket listings: `ss -ta` starts at State, `ss -a` at Netid.
	if len(st.cols) > 0 && len(f) >= 4 {
		off := 0
		if strings.EqualFold(st.cols[0], "Netid") {
			off = 1
		}
		if len(f) > off+3 && allDigits(f[off+1]) && allDigits(f[off+2]) {
			row := sysSockRow{netid: "", state: f[off], recv: f[off+1], send: f[off+2], local: f[off+3]}
			if off == 1 {
				row.netid = f[0]
			}
			if len(f) > off+4 {
				row.peer = f[off+4]
			}
			if len(f) > off+5 {
				row.process = strings.Join(f[off+5:], " ")
			}
			st.conns = append(st.conns, row)
			return
		}
	}
	if sysSockNetid(f[0]) {
		if len(f) < 5 || !allDigits(f[2]) || !allDigits(f[3]) {
			return
		}
		row := sysSockRow{netid: f[0], state: f[1], recv: f[2], send: f[3], local: f[4]}
		if len(f) > 5 {
			row.peer = f[5]
		}
		if len(f) > 6 {
			row.process = strings.Join(f[6:], " ")
		}
		st.conns = append(st.conns, row)
		return
	}
	// `ss -s`: "Total: 123", "TCP:   45 (estab 10, ...)".
	if key, value, ok := strings.Cut(text, ":"); ok && !strings.Contains(key, " ") && value != "" {
		st.stats = append(st.stats, [2]string{strings.TrimSpace(key), strings.TrimSpace(value)})
	}
}

// sysFieldsTable renders a report whose rows have exactly as many whitespace
// separated fields as its header: the header names the columns.
func sysFieldsTable(lines []string) (lens.Table, bool) {
	if len(lines) < 2 {
		return lens.Table{}, false
	}
	heads := strings.Fields(lines[0])
	if len(heads) < 2 {
		return lens.Table{}, false
	}
	cols := make([]lens.Col, len(heads))
	for i, h := range heads {
		align := lens.AlignStart
		if i > 0 {
			align = lens.AlignEnd
		}
		cols[i] = lens.Col{ID: strings.ToLower(h), Head: h, Align: align, Grow: 1}
	}
	rows := make([][]lens.Cell, 0, len(lines)-1)
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) != len(heads) {
			return lens.Table{}, false
		}
		cells := make([]lens.Cell, len(heads))
		for i, v := range f {
			style := "num"
			if i == 0 {
				style = ""
			}
			cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: style}}}
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return lens.Table{}, false
	}
	return lens.Table{Cols: cols, Rows: rows}, true
}

// sysSockHeader reports whether a first line is an ss header of a form without
// the socket counters, and returns its column names.
func sysSockHeader(f []string) []string {
	if len(f) < 2 {
		return nil
	}
	for _, name := range []string{"Destination", "Interface", "State", "Family", "Type", "Protocol"} {
		if strings.EqualFold(f[0], name) {
			return f
		}
	}
	return nil
}

// sysSockNetid reports whether a first field is an ss netid.
func sysSockNetid(s string) bool {
	switch s {
	case "tcp", "udp", "tcp6", "udp6", "raw", "raw6", "netlink", "unix", "vsock", "icmp", "icmp6", "sctp":
		return true
	}
	return false
}

func (s *sysSockets) View(state any) (lens.Node, error) {
	st, ok := state.(*sysSocketsState)
	if !ok {
		return nil, errBadState
	}
	if len(st.conns) > 0 {
		rows, hidden := sysCap(st.conns, sysMaxRows)
		cols := []lens.Col{
			{ID: "netid", Head: "Netid", Priority: 1},
			{ID: "state", Head: "State", Priority: 2},
			{ID: "recv", Head: "Recv-Q", Align: lens.AlignEnd, Priority: 4},
			{ID: "send", Head: "Send-Q", Align: lens.AlignEnd, Priority: 4},
			{ID: "local", Head: "Local Address:Port", Grow: 3, Truncate: lens.TruncateMiddle},
			{ID: "peer", Head: "Peer Address:Port", Grow: 3, Truncate: lens.TruncateMiddle},
			{ID: "process", Head: "Process", Grow: 2, Truncate: lens.TruncateMiddle},
		}
		out := make([][]lens.Cell, 0, len(rows))
		for _, r := range rows {
			out = append(out, []lens.Cell{
				sysCells(sysSpan("", r.netid)),
				sysCells(sysSpan("", r.state)),
				sysCells(sysSpan("num", r.recv)),
				sysCells(sysSpan("num", r.send)),
				sysCells(sysSpan("num", r.local)),
				sysCells(sysSpan("num", r.peer)),
				sysCells(sysSpan("muted", r.process)),
			})
		}
		return sysTable(cols, out, hidden), nil
	}
	if len(st.lines) > 1 && len(st.conns) == 0 {
		// `ss --route` / `--interfaces`: every row has exactly as many fields as
		// the header names, so split on whitespace (the shared columns parser
		// would merge single-space header words).
		if tbl, ok := sysFieldsTable(st.lines); ok {
			return tbl, nil
		}
		// Otherwise fall back to Terminal's shared parse.columns.
		if t := parse.Columns(st.lines); t != nil && len(t.Rows) > 0 {
			cols := make([]lens.Col, len(t.Heads))
			for i, h := range t.Heads {
				align := lens.AlignStart
				if i > 0 {
					align = lens.AlignEnd
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
					style := "num"
					if i == 0 {
						style = ""
					}
					cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: style}}}
				}
				rows = append(rows, cells)
			}
			return lens.Table{Cols: cols, Rows: rows}, nil
		}
	}
	if len(st.stats) > 0 {
		kv := lens.KV{Items: make([][2][]lens.Span, 0, len(st.stats))}
		for _, it := range st.stats {
			kv.Items = append(kv.Items, [2][]lens.Span{{sysSpan("key", it[0])}, {sysSpan("", it[1])}})
		}
		return kv, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// sys.load
// ---------------------------------------------------------------------------

// sysLoad parses uptime: the up time and user count as pairs, the three load
// averages as bars labelled with the literal "1 min/5 min/15 min", and the
// --pretty ("up …") and --since (a bare timestamp) forms.
type sysLoad struct{}

type sysLoadState struct {
	up, users, pretty, since string
	loads                    []float64
	any                      bool
}

func (s *sysLoad) ID() string { return "sys.load" }

// Match: `uptime`, and the macOS form Terminal's id block pairs with this lens
// (`vm.loadavg`, mined at 783180): `sysctl -n vm.loadavg` prints "{ a b c }".
func (s *sysLoad) Match(cmd model.Cmd) bool {
	if cmd.Program == "uptime" {
		return true
	}
	if cmd.Program == "sysctl" {
		for _, a := range cmd.Args {
			if a == "vm.loadavg" {
				return true
			}
		}
	}
	return false
}
func (s *sysLoad) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysLoadState{}, nil }
func (s *sysLoad) Finish(state any, status int) error            { return nil }
func (s *sysLoad) Event(state any, ev lens.Event) error          { return nil }

func (s *sysLoad) Line(state any, l model.Line) error {
	st, ok := state.(*sysLoadState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysLoadState) observe(text string) {
	if text == "" {
		return
	}
	// `sysctl -n vm.loadavg`: "{ 1.23 2.34 3.45 }".
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		if loads := parseFloats(t); len(loads) > 0 {
			st.loads = loads
			st.any = true
			return
		}
	}
	if i := strings.Index(text, "load average"); i >= 0 {
		head, tail := text[:i], text[i:]
		if j := strings.Index(head, "up "); j >= 0 {
			up := head[j+3:]
			if k := strings.Index(up, " user"); k >= 0 {
				up = up[:k]
			}
			up = strings.TrimSpace(up)
			// Drop the trailing user count: "12 days, 3:14, 4".
			if k := strings.LastIndex(up, ","); k >= 0 && allDigits(strings.TrimSpace(up[k+1:])) {
				up = up[:k]
			}
			st.up = strings.Join(strings.Fields(strings.Trim(up, " ,")), " ")
		}
		if j := strings.Index(text, " user"); j > 0 {
			st.users = strconv.Itoa(sysLastInt(text[:j]))
		}
		st.loads = parseFloats(tail)
		st.any = true
		return
	}
	// `uptime -p`: "up 3 days, 4 hours, 20 minutes".
	if strings.HasPrefix(text, "up ") {
		st.pretty = text
		st.any = true
		return
	}
	// `uptime -s`: a bare timestamp.
	if sysTimeStamp(text) {
		st.since = text
		st.any = true
	}
}

// sysTimeStamp reports whether text is "2006-01-02 15:04:05".
func sysTimeStamp(text string) bool {
	if len(text) != 19 || text[4] != '-' || text[7] != '-' || text[10] != ' ' || text[13] != ':' || text[16] != ':' {
		return false
	}
	for i, r := range text {
		if i == 4 || i == 7 || i == 10 || i == 13 || i == 16 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *sysLoad) View(state any) (lens.Node, error) {
	st, ok := state.(*sysLoadState)
	if !ok {
		return nil, errBadState
	}
	if !st.any {
		return nil, nil
	}
	var kv lens.KV
	add := func(key, value string) {
		if value != "" {
			kv.Items = append(kv.Items, [2][]lens.Span{{sysSpan("key", key)}, {sysSpan("", value)}})
		}
	}
	add("Up", st.up)
	if st.users != "" {
		add("Users", st.users)
	}
	add("Since", st.since)
	add("Pretty", st.pretty)
	children := []lens.Node{}
	if len(kv.Items) > 0 {
		children = append(children, kv)
	}
	if len(st.loads) > 0 {
		labels := []string{"1 min", "5 min", "15 min"}
		series := make([]lens.Series, 0, len(st.loads))
		for i, v := range st.loads {
			label := ""
			if i < len(labels) {
				label = labels[i]
			}
			series = append(series, lens.Series{Label: label, Value: v})
		}
		children = append(children, lens.Bars{Series: series})
		texts := make([]string, 0, len(st.loads))
		for _, v := range st.loads {
			texts = append(texts, sysNum(v))
		}
		children = append(children, lens.Text{Spans: []lens.Span{
			sysSpan("muted", "load average "),
			sysSpan("num", strings.Join(texts, ", ")),
		}})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------------------
// sys.info
// ---------------------------------------------------------------------------

// sysInfo shows system information: uname -a (the literal -a/--all) as a
// parsed line, and the key: value output of hostnamectl and sw_vers as pairs.
type sysInfo struct{}

type sysInfoState struct {
	items [][2]string
}

func (s *sysInfo) ID() string { return "sys.info" }

// Match: uname's -a/--all is the literal at 149958504.
func (s *sysInfo) Match(cmd model.Cmd) bool { // 【推断】program names
	switch cmd.Program {
	case "uname", "sw_vers", "hostnamectl":
		return true
	}
	return false
}

func (s *sysInfo) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysInfoState{}, nil }
func (s *sysInfo) Finish(state any, status int) error            { return nil }
func (s *sysInfo) Event(state any, ev lens.Event) error          { return nil }

func (s *sysInfo) Line(state any, l model.Line) error {
	st, ok := state.(*sysInfoState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysInfoState) observe(text string) {
	if text == "" {
		return
	}
	if key, value, ok := strings.Cut(text, ":"); ok {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if sysInfoKey(key) && value != "" {
			st.items = append(st.items, [2]string{key, value})
			return
		}
	}
	f := strings.Fields(text)
	if len(f) < 3 || !sysInfoSysname(f[0]) {
		return
	}
	st.items = append(st.items, [2]string{"System", f[0]})
	if len(f) > 1 {
		st.items = append(st.items, [2]string{"Hostname", f[1]})
	}
	if len(f) > 2 {
		st.items = append(st.items, [2]string{"Kernel", f[2]})
	}
	for _, v := range f {
		if sysInfoMachine(v) {
			st.items = append(st.items, [2]string{"Machine", v})
			break
		}
	}
	// The trailing words name the OS: "GNU/Linux", "Darwin Kernel Version ...".
	for i, v := range f {
		if strings.Contains(v, "GNU/Linux") || v == "Darwin" || strings.Contains(v, "BSD") {
			st.items = append(st.items, [2]string{"OS", strings.Join(f[i:], " ")})
			break
		}
	}
}

// sysInfoKey accepts the keys hostnamectl and sw_vers print.
func sysInfoKey(s string) bool {
	switch s {
	case "ProductName", "ProductVersion", "BuildVersion",
		"Static hostname", "Transient hostname", "Icon name", "Chassis",
		"Machine ID", "Boot ID", "Operating System", "CPE OS Name",
		"Kernel", "Architecture", "Hardware Vendor", "Hardware Model",
		"Firmware Version", "Virtualization":
		return true
	}
	return false
}

func sysInfoSysname(s string) bool {
	switch s {
	case "Linux", "Darwin", "FreeBSD", "OpenBSD", "NetBSD", "SunOS", "Windows_NT":
		return true
	}
	return false
}

func sysInfoMachine(s string) bool {
	switch s {
	case "x86_64", "amd64", "aarch64", "arm64", "i686", "i386", "riscv64", "ppc64le", "s390x":
		return true
	}
	return false
}

func (s *sysInfo) View(state any) (lens.Node, error) {
	st, ok := state.(*sysInfoState)
	if !ok {
		return nil, errBadState
	}
	if len(st.items) == 0 {
		return nil, nil
	}
	kv := lens.KV{Items: make([][2][]lens.Span, 0, len(st.items))}
	for _, it := range st.items {
		kv.Items = append(kv.Items, [2][]lens.Span{{sysSpan("key", it[0])}, {sysSpan("", it[1])}})
	}
	return kv, nil
}

// ---------------------------------------------------------------------------
// sys.history
// ---------------------------------------------------------------------------

// sysHistory parses a shell history listing: the number is the literal's
// number column, and `history -s/--short` drops it (then lines are commands).
type sysHistory struct{}

type sysHistItem struct {
	num int
	cmd string
}

type sysHistoryState struct {
	items []sysHistItem
	short bool // `history -s/--short`: the listing has no numbers
}

func (s *sysHistory) ID() string               { return "sys.history" }
func (s *sysHistory) Match(cmd model.Cmd) bool { return cmd.Program == "history" } // 【推断】program name

// Open reads the mined -s/--short flag, which prints commands without numbers.
func (s *sysHistory) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	st := &sysHistoryState{}
	for _, a := range cmd.Args {
		if a == "-s" || a == "--short" {
			st.short = true
		}
	}
	return st, nil
}

func (s *sysHistory) Finish(state any, status int) error   { return nil }
func (s *sysHistory) Event(state any, ev lens.Event) error { return nil }

func (s *sysHistory) Line(state any, l model.Line) error {
	st, ok := state.(*sysHistoryState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (st *sysHistoryState) observe(text string) {
	if text == "" {
		return
	}
	if st.short {
		st.items = append(st.items, sysHistItem{num: len(st.items) + 1, cmd: text})
		return
	}
	t := strings.TrimLeft(text, " \t*")
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 {
		return
	}
	num, err := strconv.Atoi(t[:i])
	if err != nil || num <= 0 {
		return
	}
	cmd := strings.TrimSpace(t[i:])
	if cmd == "" {
		return
	}
	st.items = append(st.items, sysHistItem{num: num, cmd: cmd})
}

func (s *sysHistory) View(state any) (lens.Node, error) {
	st, ok := state.(*sysHistoryState)
	if !ok {
		return nil, errBadState
	}
	if len(st.items) == 0 {
		return nil, nil
	}
	rows, hidden := sysCap(st.items, sysMaxRows)
	cols := []lens.Col{
		{ID: "num", Head: "#", Align: lens.AlignEnd, Priority: 1},
		{ID: "command", Head: "Command", Grow: 6, Truncate: lens.TruncateMiddle},
	}
	out := make([][]lens.Cell, 0, len(rows))
	for _, it := range rows {
		out = append(out, []lens.Cell{
			sysCells(sysSpan("num", strconv.Itoa(it.num))),
			sysCells(sysSpan("", it.cmd)),
		})
	}
	return sysTable(cols, out, hidden), nil
}

// ---------------------------------------------------------------------------
// sys.cal
// ---------------------------------------------------------------------------

// sysCal shows a calendar as code, so the columns keep their alignment (the
// literal "-l" is cal's); the month header and the weekday row identify it.
type sysCal struct{}

type sysCalState struct {
	lines []string
	ok    bool
}

func (s *sysCal) ID() string                                    { return "sys.cal" }
func (s *sysCal) Match(cmd model.Cmd) bool                      { return cmd.Program == "cal" || cmd.Program == "ncal" } // 【推断】program names
func (s *sysCal) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysCalState{}, nil }
func (s *sysCal) Finish(state any, status int) error            { return nil }
func (s *sysCal) Event(state any, ev lens.Event) error          { return nil }

func (s *sysCal) Line(state any, l model.Line) error {
	st, ok := state.(*sysCalState)
	if !ok {
		return errBadState
	}
	if !st.ok && sysCalHeader(l.Text) {
		st.ok = true
	}
	st.lines = append(st.lines, l.Text)
	return nil
}

// sysCalHeader recognizes the month line ("October 2026") and the weekday
// line ("Su Mo Tu We Th Fr Sa", "Mo Tu We Th Fr").
func sysCalHeader(text string) bool {
	f := strings.Fields(text)
	if len(f) >= 2 && monthAbbrev(f[0]) && allDigits(f[1]) {
		return true
	}
	if len(f) >= 1 && sysFullMonth(f[0]) && len(f) > 1 && allDigits(f[1]) {
		return true
	}
	for _, pair := range []string{"Su Mo", "Mo Tu", "Mo Tu We", "Tu We", "We Th"} {
		if strings.Contains(text, pair) {
			return true
		}
	}
	return strings.Contains(text, "Mo Tu We Th Fr")
}

var sysMonthNames = [...]string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

func sysFullMonth(s string) bool {
	for _, m := range sysMonthNames {
		if s == m {
			return true
		}
	}
	return false
}

func (s *sysCal) View(state any) (lens.Node, error) {
	st, ok := state.(*sysCalState)
	if !ok {
		return nil, errBadState
	}
	if !st.ok || len(st.lines) == 0 {
		return nil, nil
	}
	return lens.Code{Text: strings.Join(st.lines, "\n")}, nil
}

// ---------------------------------------------------------------------------
// sys.brew
// ---------------------------------------------------------------------------

// sysBrew parses `brew <subcommand> --json` (the literal -j/--json) into a
// table of name/installed/latest, and a plain `brew list` into a list of
// names.
type sysBrew struct{}

type sysBrewRow struct {
	name, installed, latest string
	outdated                bool
}

type sysBrewState struct {
	lines []string
	names []string
}

func (s *sysBrew) ID() string                                    { return "sys.brew" }
func (s *sysBrew) Match(cmd model.Cmd) bool                      { return cmd.Program == "brew" } // 【推断】program name
func (s *sysBrew) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &sysBrewState{}, nil }
func (s *sysBrew) Finish(state any, status int) error            { return nil }
func (s *sysBrew) Event(state any, ev lens.Event) error          { return nil }

func (s *sysBrew) Line(state any, l model.Line) error {
	st, ok := state.(*sysBrewState)
	if !ok {
		return errBadState
	}
	st.lines = append(st.lines, l.Text)
	if f := strings.Fields(l.Text); len(f) == 1 && sysBrewName(f[0]) {
		st.names = append(st.names, f[0])
	}
	return nil
}

// sysBrewName reports whether a bare token could be a formula name.
func sysBrewName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '@' || r == '+' || r == '-' || r == '.' || r == '_' || r == '/':
		default:
			return false
		}
	}
	return true
}

func (s *sysBrew) View(state any) (lens.Node, error) {
	st, ok := state.(*sysBrewState)
	if !ok {
		return nil, errBadState
	}
	rows, ok := sysBrewJSON(strings.Join(st.lines, "\n"))
	if !ok || len(rows) == 0 {
		rows = sysBrewOutdated(st.lines) // the human `brew outdated` form
	}
	if len(rows) > 0 {
		return sysBrewTable(rows), nil
	}
	if len(st.names) > 0 {
		shown, hidden := sysCap(st.names, sysMaxRows)
		items := make([]lens.Item, 0, len(shown))
		for _, n := range shown {
			items = append(items, lens.Item{Label: []lens.Span{sysSpan("mono", n)}})
		}
		if hidden > 0 {
			return lens.Cols(lens.List{Items: items}, lens.Overflow{Hidden: hidden}), nil
		}
		return lens.List{Items: items}, nil
	}
	return nil, nil
}

// sysBrewTable is the name/installed/latest/status table both brew forms share.
func sysBrewTable(rows []sysBrewRow) lens.Node {
	shown, hidden := sysCap(rows, sysMaxRows)
	cols := []lens.Col{
		{ID: "name", Head: "Name", Grow: 4, Truncate: lens.TruncateMiddle},
		{ID: "installed", Head: "Installed", Priority: 2},
		{ID: "latest", Head: "Latest", Priority: 1},
		{ID: "status", Head: "Status", Priority: 3},
	}
	out := make([][]lens.Cell, 0, len(shown))
	for _, r := range shown {
		status, tone := "ok", toneSuccess
		if r.outdated {
			status, tone = "outdated", toneWarning
		}
		out = append(out, []lens.Cell{
			sysCells(sysSpan("", r.name)),
			sysCells(sysSpan("num", r.installed)),
			sysCells(sysSpan("num", r.latest)),
			sysCells(sysSpan("strong "+tone, status)),
		})
	}
	return sysTable(cols, out, hidden)
}

// sysBrewOutdated reads the human `brew outdated` listing: "name (installed) <
// latest" for formulae and "name (installed) != latest" for casks, skipping
// the "==> Outdated Formulae"/"==> Outdated Casks" headers. Only the strict
// shape is accepted, and both versions must start like versions, so prose is
// left to the raw fallback.【推断】: this form is Homebrew's documented output,
// not a mined literal (the mined trigger is -j at 149958464).
func sysBrewOutdated(lines []string) []sysBrewRow {
	var rows []sysBrewRow
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "==>") {
			continue
		}
		i := strings.Index(line, " (")
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if !sysBrewName(name) {
			continue
		}
		rest := line[i+2:]
		j := strings.IndexByte(rest, ')')
		if j <= 0 {
			continue
		}
		installed := strings.TrimSpace(rest[:j])
		tail := strings.TrimSpace(rest[j+1:])
		latest := ""
		for _, op := range []string{"!=", "<", ">", "="} {
			if strings.HasPrefix(tail, op) {
				latest = strings.TrimSpace(tail[len(op):])
				break
			}
		}
		if k := strings.Index(latest, " ("); k > 0 {
			latest = strings.TrimSpace(latest[:k])
		}
		if !sysVersionish(installed) || !sysVersionish(latest) {
			continue
		}
		rows = append(rows, sysBrewRow{name: name, installed: installed, latest: latest, outdated: true})
	}
	return rows
}

// sysVersionish reports whether s starts like a version, so that a sentence
// with parentheses and a "<" does not claim the block.
func sysVersionish(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= '0' && c <= '9' || c == 'v' || c == 'V'
}

// sysBrewJSON reads brew's --json output: an array of formulae, or an object
// with a formulae/casks array (brew info --json).
func sysBrewJSON(text string) ([]sysBrewRow, bool) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '[' && text[0] != '{' {
		return nil, false
	}
	var root any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return nil, false
	}
	var list []any
	switch v := root.(type) {
	case []any:
		list = v
	case map[string]any:
		for _, key := range []string{"formulae", "casks"} {
			if arr, ok := v[key].([]any); ok {
				list = append(list, arr...)
			}
		}
		if len(list) == 0 {
			list = []any{v}
		}
	default:
		return nil, false
	}
	rows := make([]sysBrewRow, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			rows = append(rows, sysBrewRow{name: s})
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row := sysBrewRow{name: sysBrewStr(m["full_name"])}
		if row.name == "" {
			row.name = sysBrewStr(m["name"])
		}
		if row.name == "" {
			continue
		}
		if arr, ok := m["installed"].([]any); ok && len(arr) > 0 {
			if im, ok := arr[0].(map[string]any); ok {
				row.installed = sysBrewStr(im["version"])
			}
		}
		if row.installed == "" {
			if arr, ok := m["installed_versions"].([]any); ok && len(arr) > 0 {
				row.installed = sysBrewStr(arr[0])
			}
		}
		row.latest = sysBrewStr(m["current_version"])
		if row.latest == "" {
			if vm, ok := m["versions"].(map[string]any); ok {
				row.latest = sysBrewStr(vm["stable"])
			}
		}
		row.outdated, _ = m["outdated"].(bool)
		rows = append(rows, row)
	}
	return rows, true
}

func sysBrewStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}
