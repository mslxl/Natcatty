package builtin

import (
	"fmt"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// Docker family: docker.build, docker.logs, docker.table.
//
// Mined from payload\flow.exe (Terminal 0.9.4) per findings/lens-conditions.md §1.1:
//   - offsets 149955232–149955274 hold the docker family ids (glued in .rdata)
//     next to the literals "docker", "container", "build", "pull", "buildx",
//     "builder", "compose", "--progress", "--quiet", "--no-stream", "-H/--host",
//     "-c/--context", "-l/--log-level", "--tlscacert/--tlscert/--tlskey", and
//     compose's "-f/--file", "-p/--project-name", "--env-file",
//     "--project-directory", "--ansi", "--parallel".                       【观察】
//   - The split of those words into the three ids (build|logs|table) is   【推断】:
//     Terminal's predicates are code, not data (docs/lens-parity.md).
//   - View components follow findings/lens-widgets.md §1.3: progress → Progress,
//     raw log → Section+Code, table → Table, status words → Badge tone. A table
//     cell holds spans, not a Badge node, so a status cell carries the badge tone
//     as its span style ("success"/"warning"/"error"/"muted").             【推断】

func init() {
	lens.Register(lens.Definition{ID: "docker.build", New: func() lens.Lens { return &dockerBuild{} }})
	lens.Register(lens.Definition{ID: "docker.logs", New: func() lens.Lens { return &dockerLogs{} }})
	lens.Register(lens.Definition{ID: "docker.table", New: func() lens.Lens { return &dockerTable{} }})
}

// dockerGroups are docker's command groups: the group word is followed by the
// real verb ("docker container ls", "docker buildx build", "docker compose ps").
var dockerGroups = map[string]bool{
	"compose": true, "buildx": true, "builder": true, "container": true,
	"image": true, "system": true, "manifest": true,
}

// dockerFlagValue reports whether a docker flag consumes the next token.
func dockerFlagValue(flag string) bool {
	switch flag {
	case "-H", "--host", "-c", "--context", "-l", "--log-level", "--config",
		"--tlscacert", "--tlscert", "--tlskey", "-f", "--file", "-p",
		"--project-name", "--env-file", "--project-directory", "--profile",
		"--format", "-o", "--output", "-t", "--tag", "--namespace":
		return true
	}
	return false
}

// dockerVerbs returns the non-flag words of a docker command, up to two: the
// group and the verb ("compose build", "container ls", "ps").
func dockerVerbs(cmd model.Cmd) ([]string, bool) {
	switch cmd.Program {
	case "docker", "docker-compose":
	default:
		return nil, false
	}
	var verbs []string
	for i := 0; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && dockerFlagValue(a) && i+1 < len(cmd.Args) {
				i++
			}
			continue
		}
		verbs = append(verbs, a)
		if len(verbs) == 2 || !dockerGroups[a] {
			break
		}
	}
	return verbs, true
}

// dockerColStarts splits a docker table line on runs of two or more spaces and
// returns the byte offset each cell starts at, so that a cell with single
// spaces inside ("/bin/sh -c …", "Up 2 hours") stays one cell, and a row is
// sliced on the header's own column offsets.                                 【推断】
func dockerColStarts(line string) []int {
	var starts []int
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		starts = append(starts, i)
		for i < len(line) {
			if line[i] != ' ' {
				i++
				continue
			}
			j := i
			for j < len(line) && line[j] == ' ' {
				j++
			}
			if j-i >= 2 {
				break
			}
			i = j
		}
	}
	return starts
}

// dockerSliceRow cuts a row at the header's column offsets; a column the row
// does not reach (docker drops trailing empty cells) stays empty.
func dockerSliceRow(line string, starts []int) []string {
	out := make([]string, len(starts))
	for i, start := range starts {
		if start >= len(line) {
			continue
		}
		end := len(line)
		if i+1 < len(starts) && starts[i+1] <= len(line) {
			end = starts[i+1]
		}
		out[i] = strings.TrimSpace(line[start:end])
	}
	return out
}

// dockerStatusTone maps a docker STATUS cell to a badge tone.
func dockerStatusTone(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.HasPrefix(s, "up"):
		return toneSuccess
	case strings.HasPrefix(s, "exited"), strings.HasPrefix(s, "dead"),
		strings.HasPrefix(s, "removing"):
		return toneError
	case strings.HasPrefix(s, "restarting"), strings.HasPrefix(s, "created"),
		strings.HasPrefix(s, "paused"):
		return toneWarning
	}
	return toneMuted
}

// ---------------------------------------------------------------- docker.build

// dockerBuild turns `docker build`/`pull` output into BuildKit step progress,
// the log and a success/failure badge.
type dockerBuild struct{}

type dockerStep struct {
	name   string
	status string // DONE, CACHED, ERROR, or "" while running
	took   string
}

type dockerBuildState struct {
	steps  []dockerStep
	index  map[string]int
	logs   []string
	saw    bool
	failed bool
	fail   string
	image  string
}

func (d *dockerBuild) ID() string { return "docker.build" }

func (d *dockerBuild) Match(cmd model.Cmd) bool {
	v, ok := dockerVerbs(cmd)
	if !ok || len(v) == 0 {
		return false
	}
	switch v[len(v)-1] {
	case "build", "pull":
		return true
	}
	return false
}

func (d *dockerBuild) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &dockerBuildState{index: map[string]int{}}, nil
}

func (d *dockerBuild) Line(state any, l model.Line) error {
	st, ok := state.(*dockerBuildState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (d *dockerBuild) Finish(state any, status int) error {
	if st, ok := state.(*dockerBuildState); ok && status != 0 {
		st.failed = true
	}
	return nil
}

func (d *dockerBuild) Event(state any, ev lens.Event) error { return nil }

func (st *dockerBuildState) step(id, name string) *dockerStep {
	if i, ok := st.index[id]; ok {
		if name != "" && st.steps[i].name == "" {
			st.steps[i].name = name
		}
		return &st.steps[i]
	}
	st.index[id] = len(st.steps)
	st.steps = append(st.steps, dockerStep{name: name})
	return &st.steps[len(st.steps)-1]
}

// observe reads one BuildKit plain-progress line ("#4 [1/3] RUN …",
// "#4 DONE 0.5s", "#4 CACHED", "#4 ERROR: …") or a classic build line.  【推断】
func (st *dockerBuildState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "#") {
		id := ""
		rest := t
		for i := 1; i < len(t); i++ {
			if t[i] < '0' || t[i] > '9' {
				break
			}
			id += string(t[i])
		}
		if id != "" {
			st.saw = true
			rest = strings.TrimSpace(t[1+len(id):])
			step := st.step(id, "")
			switch {
			case rest == "CACHED":
				step.status = "CACHED"
				return
			case strings.HasPrefix(rest, "DONE"):
				step.status = "DONE"
				fields := strings.Fields(rest)
				if len(fields) > 1 {
					step.took = fields[1]
				}
				return
			case strings.HasPrefix(rest, "ERROR"):
				step.status = "ERROR"
				st.failed = true
				st.fail = strings.TrimSpace(strings.TrimPrefix(rest, "ERROR:"))
				return
			case strings.HasPrefix(rest, "["):
				step.name = rest
				return
			default:
				// "#4 exporting to image" names a step without brackets; a
				// context line always starts with its duration ("0.123 hi").
				if rest == "" {
					return
				}
				if step.name == "" && !(rest[0] >= '0' && rest[0] <= '9') {
					step.name = rest
					return
				}
				st.logs = append(st.logs, rest)
				return
			}
		}
	}
	switch {
	case strings.HasPrefix(t, "Step ") && strings.Contains(t, " : "):
		st.saw = true
		name, _, _ := strings.Cut(t, " : ")
		st.steps = append(st.steps, dockerStep{name: name, status: "DONE"})
	case strings.HasPrefix(t, "Successfully built "):
		st.saw, st.image = true, strings.TrimPrefix(t, "Successfully built ")
	case strings.HasPrefix(t, "Successfully tagged "):
		st.saw, st.image = true, strings.TrimPrefix(t, "Successfully tagged ")
	case strings.HasPrefix(t, "Pulling "), strings.HasPrefix(t, "Digest: "),
		strings.HasPrefix(t, "Status: "), strings.HasPrefix(t, "Building "):
		st.saw = true
	case strings.HasPrefix(t, "ERROR:"), strings.HasPrefix(t, "error:"),
		strings.HasPrefix(t, "Error response from daemon"),
		strings.HasPrefix(t, "failed to solve"):
		st.saw, st.failed, st.fail = true, true, t
	default:
		st.logs = append(st.logs, t)
	}
}

func (d *dockerBuild) View(state any) (lens.Node, error) {
	st, ok := state.(*dockerBuildState)
	if !ok {
		return nil, errBadState
	}
	if !st.saw {
		return nil, nil
	}
	done, total := 0, len(st.steps)
	for _, s := range st.steps {
		if s.status == "DONE" || s.status == "CACHED" {
			done++
		}
	}
	var children []lens.Node
	if total > 0 {
		children = append(children, lens.Progress{
			Value: float64(done) / float64(total),
			Known: true,
			Label: []lens.Span{{Text: fmt.Sprintf("%d/%d steps", done, total)}},
		})
	} else {
		children = append(children, lens.Progress{Known: false, Label: []lens.Span{{Text: "building"}}})
	}
	if len(st.logs) > 0 {
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: "Build log"}},
			Children: []lens.Node{lens.Code{Text: strings.Join(st.logs, "\n"), Lang: "text", Clamp: 200}},
		})
	}
	badge := lens.Badge{Text: "succeeded", Tone: toneSuccess}
	if st.failed {
		badge = lens.Badge{Text: "failed", Tone: toneError}
	}
	children = append(children, badge)
	return lens.Cols(children...), nil
}

// ----------------------------------------------------------------- docker.logs

// dockerLogs shows `docker logs` output as code, with an overflow marker.
type dockerLogs struct{}

const dockerLogsClamp = 500

// dockerLogSplit separates a leading timestamp ("2026-01-02T10:00:00.123Z",
// "2026-01-02 10:00:00", "10:00:00") from the message.
func dockerLogSplit(line string) (stamp, rest string) {
	i := 0
	for i < len(line) && line[i] != ' ' {
		i++
	}
	head := line[:i]
	if !strings.Contains(head, ":") {
		return "", line
	}
	digits := 0
	for k := 0; k < len(head); k++ {
		if head[k] >= '0' && head[k] <= '9' {
			digits++
		}
	}
	if digits < 4 {
		return "", line
	}
	return head, strings.TrimSpace(line[i:])
}

// dockerLogsState keeps what `docker logs` printed plus the evidence that the
// output really is a log stream: Terminal's contract is view()==nil → raw, so a
// block of arbitrary text must not become a native Code view.              【推断】
type dockerLogsState struct {
	lines   []string
	styled  []string // lines with SGR escapes, for lens.ANSI
	hidden  int
	stamped int
	level   bool
	prefix  bool
	json    bool
	help    bool
	saw     bool
}

func (d *dockerLogs) ID() string { return "docker.logs" }

func (d *dockerLogs) Match(cmd model.Cmd) bool {
	v, ok := dockerVerbs(cmd)
	if !ok || len(v) == 0 {
		return false
	}
	return v[len(v)-1] == "logs"
}

func (d *dockerLogs) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &dockerLogsState{}, nil }

func (d *dockerLogs) Line(state any, l model.Line) error {
	st, ok := state.(*dockerLogsState)
	if !ok {
		return errBadState
	}
	t := l.Text
	if strings.TrimSpace(t) == "" {
		return nil
	}
	if l.Styled != "" {
		st.styled = append(st.styled, l.Styled)
	}
	if strings.HasPrefix(t, "Usage:") || strings.HasPrefix(t, "Options:") ||
		strings.HasPrefix(t, "Flags:") || strings.HasPrefix(t, "docker logs") {
		st.help = true
		return nil
	}
	st.saw = true
	if dockerLogStamp(t) {
		st.stamped++
	}
	if dockerLogLevel(t) {
		st.level = true
	}
	if strings.HasPrefix(t, "docker") {
		st.prefix = true
	}
	if strings.HasPrefix(strings.TrimSpace(t), "{") && strings.HasSuffix(strings.TrimSpace(t), "}") {
		st.json = true
	}
	if len(st.lines) < dockerLogsClamp {
		st.lines = append(st.lines, t)
	} else {
		st.hidden++
	}
	return nil
}

func (d *dockerLogs) Finish(state any, status int) error { return nil }

func (d *dockerLogs) Event(state any, ev lens.Event) error { return nil }

// dockerLogStamp reports whether a line starts with docker's `-t` timestamp
// (RFC3339 with nanoseconds, or the "2006-01-02 15:04:05" form).          【推断】
func dockerLogStamp(line string) bool {
	if len(line) < 19 {
		return false
	}
	for _, i := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if line[i] < '0' || line[i] > '9' {
			return false
		}
	}
	if line[4] != '-' || line[7] != '-' {
		return false
	}
	return line[10] == 'T' || line[10] == ' '
}

// dockerLogLevel reports whether a line carries a log level: logfmt
// ("level=info") or a bare INFO/WARN/ERROR/DEBUG/TRACE word.                【推断】
func dockerLogLevel(line string) bool {
	if strings.Contains(line, "level=") {
		return true
	}
	for _, lvl := range []string{" INFO ", " WARN", " ERROR ", " DEBUG", " TRACE"} {
		if strings.Contains(line, lvl) {
			return true
		}
	}
	return false
}

func (d *dockerLogs) View(state any) (lens.Node, error) {
	st, ok := state.(*dockerLogsState)
	if !ok {
		return nil, errBadState
	}
	// Evidence gate: without a log shape (timestamp, level, docker prefix or a
	// JSON line) the output is not this lens's, so the block stays raw.
	if !st.saw || len(st.lines) == 0 || (st.help && st.stamped == 0) {
		return nil, nil
	}
	if st.stamped == 0 && !st.level && !st.prefix && !st.json {
		return nil, nil
	}
	// A timestamped log reads better as rows: the stamp is numeric, the message
	// is text (Terminal renders log lines as rows, not one code blob).           【推断】
	var children []lens.Node
	if st.stamped > 0 {
		items := make([]lens.Item, 0, len(st.lines))
		for i, ln := range st.lines {
			if i >= dockerLogsClamp {
				break
			}
			stamp, rest := dockerLogSplit(ln)
			if stamp == "" {
				items = append(items, lens.Item{Label: []lens.Span{{Text: ln}}})
				continue
			}
			items = append(items, lens.Item{
				Label:  []lens.Span{{Text: stamp, Style: "num"}},
				Detail: []lens.Span{{Text: rest}},
			})
		}
		children = append(children, lens.List{Items: items})
	} else if len(st.styled) > 0 {
		// docker writes level colours; Terminal's `ansi(text)` shows them.
		children = append(children, lens.ANSI{Text: strings.Join(st.styled, "\n")})
	} else {
		children = append(children, lens.Code{
			Text:  strings.Join(st.lines, "\n"),
			Lang:  "text",
			Start: 1,
			Clamp: dockerLogsClamp,
		})
	}
	if st.hidden > 0 {
		children = append(children, lens.Overflow{Hidden: st.hidden})
	}
	return lens.Cols(children...), nil
}

// ---------------------------------------------------------------- docker.table

// dockerTable renders `docker ps` / `docker images` / `docker compose ps` as a
// table: the id is mono, STATUS carries the badge tone, numeric cells use num.
type dockerTable struct{}

type dockerTableState struct {
	cols   []string
	starts []int
	rows   [][]string
	saw    bool
}

func (d *dockerTable) ID() string { return "docker.table" }

func (d *dockerTable) Match(cmd model.Cmd) bool {
	v, ok := dockerVerbs(cmd)
	if !ok || len(v) == 0 {
		return false
	}
	last := v[len(v)-1]
	if last == "ps" || last == "images" {
		return true
	}
	if len(v) >= 2 && (v[0] == "container" || v[0] == "image") && last == "ls" {
		return true
	}
	return false
}

func (d *dockerTable) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &dockerTableState{}, nil }

func (d *dockerTable) Line(state any, l model.Line) error {
	st, ok := state.(*dockerTableState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (d *dockerTable) Finish(state any, status int) error { return nil }

func (d *dockerTable) Event(state any, ev lens.Event) error { return nil }

func (st *dockerTableState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if !st.saw {
		if cols := dockerTableHeader(t); cols != nil {
			st.cols, st.starts = cols, dockerColStarts(t)
			st.saw = true
			return
		}
	}
	if !st.saw {
		return
	}
	row := dockerSliceRow(t, st.starts)
	if len(row) < 2 {
		return
	}
	st.rows = append(st.rows, row)
}

// dockerTableHeader recognizes the header of docker's two main tables.  【推断】
func dockerTableHeader(line string) []string {
	f := strings.Fields(line)
	if len(f) == 0 {
		return nil
	}
	switch {
	case len(f) >= 2 && f[0] == "CONTAINER" && f[1] == "ID":
		return []string{"CONTAINER ID", "IMAGE", "COMMAND", "CREATED", "STATUS", "PORTS", "NAMES"}
	case f[0] == "REPOSITORY" && len(f) >= 2 && f[1] == "TAG":
		return []string{"REPOSITORY", "TAG", "IMAGE ID", "CREATED", "SIZE"}
	case f[0] == "NAME" && dockerHasField(f, "IMAGE") && dockerHasField(f, "STATUS"):
		return []string{"NAME", "IMAGE", "COMMAND", "SERVICE", "CREATED", "STATUS", "PORTS"}
	}
	return nil
}

func dockerHasField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func (d *dockerTable) View(state any) (lens.Node, error) {
	st, ok := state.(*dockerTableState)
	if !ok {
		return nil, errBadState
	}
	if !st.saw || len(st.cols) == 0 {
		return nil, nil
	}
	cols := make([]lens.Col, len(st.cols))
	for i, c := range st.cols {
		col := lens.Col{ID: strings.ToLower(strings.ReplaceAll(c, " ", "_")), Head: c}
		switch c {
		case "NAMES", "NAME", "REPOSITORY":
			col.Grow = 1
		case "CONTAINER ID", "IMAGE ID":
			col.Priority = 1
		}
		cols[i] = col
	}
	rows := make([][]lens.Cell, 0, len(st.rows))
	for _, r := range st.rows {
		cells := make([]lens.Cell, len(cols))
		for i, c := range st.cols {
			v := ""
			if i < len(r) {
				v = r[i]
			} else if i == len(cols)-1 && len(r) > len(cols) {
				v = strings.Join(r[i:], " ")
			}
			cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: dockerCellStyle(c, v)}}}
		}
		rows = append(rows, cells)
	}
	return lens.Table{Cols: cols, Rows: rows}, nil
}

// dockerCellStyle gives a cell its span style: ids are mono, STATUS uses the
// badge tone, sizes/durations are num.                                   【推断】
func dockerCellStyle(col, value string) string {
	switch col {
	case "CONTAINER ID", "IMAGE ID":
		return "mono"
	case "STATUS":
		return dockerStatusTone(value)
	case "CREATED", "SIZE", "PORTS", "TAG":
		return "num"
	}
	return ""
}
