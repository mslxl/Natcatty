package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens/parse"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// k8s family: k8s.get, k8s.describe, k8s.helm.
//
// Mined from payload\flow.exe (Terminal 0.9.4) per findings/lens-conditions.md §1.1:
//   - offset 149956040 holds `k8s.get`/`k8s.describe` next to kubectl's flag
//     table: "-n --namespace --kube-context --kubeconfig -o -q --short
//     --cluster --user -s --server --request-timeout --token --as --selector
//     --field-selector --sort-by --container", and the output tokens "NAME
//     READY STATUS RESTARTS AGE".                                            【观察】
//   - offset 149954560 holds `k8s.helm`; the "Changes to Outputs / Resources /
//     No changes / Plan / yaml" words sit in the same constants pool but are
//     NOT necessarily this lens's trigger (the findings file says so
//     explicitly) — the helm program name and the Card+Code view are 【推断】.
//   - findings §1.2: the program names `kubectl`/`helm` are not literals in the
//     lens region, so matching them is 推断 (docs/lens-parity.md rule 2).
//   - View components follow findings/lens-widgets.md §1.3: table → Table,
//     key/value summaries → KV, status words → Badge tone.

func init() {
	lens.Register(lens.Definition{ID: "k8s.get", New: func() lens.Lens { return &k8sGet{} }})
	lens.Register(lens.Definition{ID: "k8s.describe", New: func() lens.Lens { return &k8sDescribe{} }})
	lens.Register(lens.Definition{ID: "k8s.helm", New: func() lens.Lens { return &k8sHelm{} }})
}

// k8sFlagValue reports whether a kubectl flag consumes the next token.
func k8sFlagValue(flag string) bool {
	switch flag {
	case "-n", "--namespace", "--kube-context", "--kubeconfig", "-o", "--output",
		"-s", "--server", "--request-timeout", "--token", "--as", "--selector",
		"--field-selector", "--sort-by", "--container", "--user", "--cluster",
		"--context", "--client-key", "--client-certificate", "--password",
		"--username", "--cache-dir", "--profile":
		return true
	}
	return false
}

// k8sVerb returns kubectl's verb ("get", "describe", …).                       【推断】
func k8sVerb(cmd model.Cmd) (string, bool) {
	if cmd.Program != "kubectl" && cmd.Program != "k" {
		return "", false
	}
	for i := 0; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && k8sFlagValue(a) && i+1 < len(cmd.Args) {
				i++
			}
			continue
		}
		return a, true
	}
	return "", true
}

// k8sColumns splits a kubectl row on runs of two or more spaces, keeping a
// two-word header ("NOMINATED NODE", "READINESS GATES") in one column.       【推断】
func k8sColumns(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
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
		out = append(out, strings.TrimSpace(line[start:i]))
	}
	return out
}

func k8sHas(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

// k8sStatusTone maps a STATUS cell value to a badge tone.                   【推断】
func k8sStatusTone(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "crashloopbackoff"), strings.Contains(s, "error"),
		strings.Contains(s, "failed"), strings.Contains(s, "evicted"),
		strings.Contains(s, "unknown"), strings.Contains(s, "notready"):
		return toneError
	case strings.Contains(s, "pending"), strings.Contains(s, "creating"),
		strings.Contains(s, "terminating"), strings.Contains(s, "init"):
		return toneWarning
	case strings.Contains(s, "running"), strings.Contains(s, "completed"),
		strings.Contains(s, "succeeded"), strings.Contains(s, "active"),
		strings.Contains(s, "bound"), strings.Contains(s, "available"):
		return toneSuccess
	}
	return toneMuted
}

func k8sTrim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// -------------------------------------------------------------------- k8s.get

// k8sGet renders `kubectl get` tables and counts the pod states it saw.
type k8sGet struct{}

type k8sGetState struct {
	lines     []string
	json      bool // the command asked for JSON output (-o json/--output json)
	cols      []string
	rows      [][]string
	running   int
	pending   int
	failed    int
	sawHeader bool
	saw       bool
}

func (k *k8sGet) ID() string { return "k8s.get" }

func (k *k8sGet) Match(cmd model.Cmd) bool {
	v, ok := k8sVerb(cmd)
	return ok && v == "get"
}

func (k *k8sGet) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &k8sGetState{json: parse.HasJSONFlag(cmd.Line)}, nil
}

func (k *k8sGet) Line(state any, l model.Line) error {
	st, ok := state.(*k8sGetState)
	if !ok {
		return errBadState
	}
	st.lines = append(st.lines, l.Text)
	st.observe(l.Text)
	return nil
}

func (k *k8sGet) Finish(state any, status int) error { return nil }

func (k *k8sGet) Event(state any, ev lens.Event) error { return nil }

func (st *k8sGetState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "No resources found") {
		st.saw = true
		return
	}
	if !st.sawHeader {
		f := strings.Fields(t)
		if k8sHas(f, "NAME") && (k8sHas(f, "STATUS") || k8sHas(f, "READY") ||
			k8sHas(f, "AGE") || k8sHas(f, "TYPE") || k8sHas(f, "CLUSTER-IP") ||
			k8sHas(f, "DATA")) {
			st.cols = k8sColumns(t)
			st.sawHeader, st.saw = true, true
			return
		}
	}
	if !st.sawHeader {
		return
	}
	row := k8sColumns(t)
	if len(row) < 2 {
		return
	}
	st.rows = append(st.rows, row)
	// Count pod states: k8s.get pods columns are NAME READY STATUS RESTARTS AGE.
	for i, c := range st.cols {
		if c != "STATUS" || i >= len(row) {
			continue
		}
		switch k8sStatusTone(row[i]) {
		case toneSuccess:
			st.running++
		case toneWarning:
			st.pending++
		case toneError:
			st.failed++
		}
	}
}

func (k *k8sGet) View(state any) (lens.Node, error) {
	st, ok := state.(*k8sGetState)
	if !ok {
		return nil, errBadState
	}
	// `kubectl get -o json` prints no header row: Terminal's k8s lens knows the
	// object shape (`items`/`kind`/`namespace`, the literals mined near
	// 149956040), so the JSON becomes the same table instead of a raw tree.
	if st.json {
		if tbl, ok := k8sJSONTable(st.lines); ok {
			return tbl, nil
		}
	}
	if !st.saw || len(st.cols) == 0 {
		return nil, nil
	}
	// Terminal's shared `parse.columns` is header-driven: a row whose middle cell is
	// empty keeps its columns instead of shifting left, which the "split on 2+
	// spaces" heuristic did (flow.exe strings 802659-802660).
	headCols, headRows := st.cols, st.rows
	if t := parse.Columns(st.lines); t != nil && len(t.Rows) > 0 {
		headCols, headRows = t.Heads, t.Rows
	}
	cols := make([]lens.Col, len(headCols))
	for i, c := range headCols {
		col := lens.Col{ID: strings.ToLower(c), Head: c}
		switch c {
		case "NAME":
			col.Grow = 1
		case "RESTARTS", "AGE", "READY":
			col.Priority = 1
		}
		cols[i] = col
	}
	rows := make([][]lens.Cell, 0, len(headRows))
	for _, r := range headRows {
		cells := make([]lens.Cell, len(cols))
		for i, c := range headCols {
			v := ""
			if i < len(r) {
				v = r[i]
			} else if i == len(cols)-1 && len(r) > len(cols) {
				v = strings.Join(r[i:], " ")
			}
			cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: k8sCellStyle(c, v)}}}
		}
		rows = append(rows, cells)
	}
	var children []lens.Node
	if st.running+st.pending+st.failed > 0 {
		var badges []lens.Node
		if st.running > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(st.running) + " running", Tone: toneSuccess})
		}
		if st.pending > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(st.pending) + " pending", Tone: toneWarning})
		}
		if st.failed > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(st.failed) + " failed", Tone: toneError})
		}
		children = append(children, lens.Rows(badges...))
	}
	children = append(children, lens.Table{Cols: cols, Rows: rows})
	return lens.Cols(children...), nil
}

// k8sJSONTable turns `kubectl get -o json` output into NAME/NAMESPACE/KIND/
// STATUS rows. It reports false when the text is not a list of objects.
func k8sJSONTable(lines []string) (lens.Table, bool) {
	v, ok := parse.JSONValue(strings.Join(lines, "\n"))
	if !ok {
		return lens.Table{}, false
	}
	items := k8sJSONChild(v, "items")
	if len(items) == 0 {
		return lens.Table{}, false
	}
	cols := []lens.Col{
		{ID: "name", Head: "NAME", Grow: 4},
		{ID: "namespace", Head: "NAMESPACE", Priority: 2, Grow: 2},
		{ID: "kind", Head: "KIND", Priority: 3},
		{ID: "status", Head: "STATUS", Priority: 1, Grow: 1},
	}
	rows := make([][]lens.Cell, 0, len(items))
	for _, it := range items {
		name := k8sJSONPath(it, "metadata", "name")
		ns := k8sJSONPath(it, "metadata", "namespace")
		kind := k8sJSONPath(it, "kind")
		status := k8sJSONPath(it, "status", "phase")
		if status == "" {
			status = k8sJSONPath(it, "status", "reason")
		}
		if name == "" {
			continue
		}
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: name, Style: k8sCellStyle("NAME", name)}}},
			{Spans: []lens.Span{{Text: ns, Style: "muted"}}},
			{Spans: []lens.Span{{Text: kind, Style: "muted"}}},
			{Spans: []lens.Span{{Text: status, Style: k8sCellStyle("STATUS", status)}}},
		})
	}
	if len(rows) == 0 {
		return lens.Table{}, false
	}
	return lens.Table{Cols: cols, Rows: rows}, true
}

// k8sJSONChild returns the children of the first member named key.
func k8sJSONChild(v parse.Value, key string) []parse.Value {
	for _, c := range v.Children {
		if c.Key == key {
			return c.Children
		}
	}
	return nil
}

// k8sJSONPath walks object members by name and returns the scalar unquoted.
func k8sJSONPath(v parse.Value, keys ...string) string {
	cur := v
	for _, k := range keys {
		var next *parse.Value
		for i := range cur.Children {
			if cur.Children[i].Key == k {
				next = &cur.Children[i]
				break
			}
		}
		if next == nil {
			return ""
		}
		cur = *next
	}
	if !cur.IsScalar {
		return ""
	}
	return strings.Trim(cur.Scalar, `"`)
}

// k8sCellStyle gives a get cell its span style.                              【推断】
func k8sCellStyle(col, value string) string {
	switch col {
	case "STATUS":
		return k8sStatusTone(value)
	case "RESTARTS", "AGE", "READY", "CPU", "MEMORY":
		return "num"
	case "NAME":
		return "strong"
	}
	return ""
}

// --------------------------------------------------------------- k8s.describe

// k8sDescribe turns `kubectl describe` into the key/value header plus the
// Events table.
type k8sDescribe struct{}

type k8sDescribeState struct {
	kv         [][2]string
	eventsCols []string
	events     [][]string
	inEvents   bool
	sawHeader  bool
	saw        bool
}

func (k *k8sDescribe) ID() string { return "k8s.describe" }

func (k *k8sDescribe) Match(cmd model.Cmd) bool {
	v, ok := k8sVerb(cmd)
	return ok && v == "describe"
}

func (k *k8sDescribe) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &k8sDescribeState{}, nil
}

func (k *k8sDescribe) Line(state any, l model.Line) error {
	st, ok := state.(*k8sDescribeState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (k *k8sDescribe) Finish(state any, status int) error { return nil }

func (k *k8sDescribe) Event(state any, ev lens.Event) error { return nil }

func (st *k8sDescribeState) observe(text string) {
	if strings.HasPrefix(text, "Events:") {
		st.inEvents, st.saw = true, true
		return
	}
	if st.inEvents {
		t := strings.TrimSpace(text)
		if t == "" {
			return
		}
		f := strings.Fields(t)
		if !st.sawHeader && k8sHas(f, "Type") && k8sHas(f, "Reason") && k8sHas(f, "Message") {
			st.eventsCols, st.sawHeader = k8sColumns(t), true
			return
		}
		if !st.sawHeader || strings.HasPrefix(t, "----") {
			return
		}
		row := k8sColumns(t)
		if len(row) >= 2 {
			st.events = append(st.events, row)
		}
		return
	}
	if text == "" || strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t") {
		return // continuation of the value above
	}
	i := strings.IndexByte(text, ':')
	if i <= 0 {
		return
	}
	key := text[:i]
	if strings.ContainsAny(key, " \t") || len(key) > 32 {
		return
	}
	st.kv = append(st.kv, [2]string{key, k8sTrim(strings.TrimSpace(text[i+1:]), 200)})
	st.saw = true
}

func (k *k8sDescribe) View(state any) (lens.Node, error) {
	st, ok := state.(*k8sDescribeState)
	if !ok {
		return nil, errBadState
	}
	if !st.saw {
		return nil, nil
	}
	var children []lens.Node
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
	if len(st.eventsCols) > 0 {
		cols := make([]lens.Col, len(st.eventsCols))
		for i, c := range st.eventsCols {
			col := lens.Col{ID: strings.ToLower(c), Head: c}
			if c == "Message" {
				col.Grow = 1
			}
			cols[i] = col
		}
		rows := make([][]lens.Cell, 0, len(st.events))
		for _, r := range st.events {
			cells := make([]lens.Cell, len(cols))
			for i, c := range cols {
				v := ""
				if i < len(r) {
					v = r[i]
				} else if i == len(cols)-1 && len(r) > len(cols) {
					v = strings.Join(r[i:], " ")
				}
				style := ""
				switch c.Head {
				case "Type":
					if strings.EqualFold(v, "Warning") {
						style = toneWarning
					} else {
						style = toneMuted
					}
				case "Age":
					style = "num"
				}
				cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: style}}}
			}
			rows = append(rows, cells)
		}
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: "Events"}},
			Children: []lens.Node{lens.Table{Cols: cols, Rows: rows}},
		})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}

// ------------------------------------------------------------------- k8s.helm

// k8sHelm renders helm output: a rendered manifest as a Card+Code, a `helm
// list` table as a Table, and plan-ish text as Card+Section+Code.
type k8sHelm struct{}

type k8sHelmState struct {
	lines    []string
	yaml     []string
	texts    []string
	cols     []string
	rows     [][]string
	sawYAML  bool
	sawTable bool
	sawText  bool
}

func (k *k8sHelm) ID() string { return "k8s.helm" }

func (k *k8sHelm) Match(cmd model.Cmd) bool { return cmd.Program == "helm" }

func (k *k8sHelm) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &k8sHelmState{}, nil }

func (k *k8sHelm) Line(state any, l model.Line) error {
	st, ok := state.(*k8sHelmState)
	if !ok {
		return errBadState
	}
	st.lines = append(st.lines, l.Text)
	st.observe(l.Text)
	return nil
}

func (k *k8sHelm) Finish(state any, status int) error { return nil }

func (k *k8sHelm) Event(state any, ev lens.Event) error { return nil }

func (st *k8sHelmState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if !st.sawTable {
		f := strings.Fields(t)
		if k8sHas(f, "NAME") && (k8sHas(f, "CHART") || k8sHas(f, "REVISION") || k8sHas(f, "NAMESPACE")) {
			st.cols, st.sawTable = k8sColumns(t), true
			return
		}
	}
	if st.sawTable {
		if row := k8sColumns(t); len(row) >= 2 {
			st.rows = append(st.rows, row)
		}
		return
	}
	switch {
	case strings.HasPrefix(t, "apiVersion:"), strings.HasPrefix(t, "kind:"),
		strings.HasPrefix(t, "# Source:"), t == "---":
		st.sawYAML = true
	}
	if st.sawYAML {
		st.yaml = append(st.yaml, text)
		return
	}
	switch {
	case strings.Contains(t, "No changes"), strings.HasPrefix(t, "Changes to Outputs"),
		strings.HasPrefix(t, "Plan:"), strings.HasPrefix(t, "Resources:"),
		strings.HasPrefix(t, "NAME:"):
		st.sawText = true
	}
	st.texts = append(st.texts, text)
}

func (k *k8sHelm) View(state any) (lens.Node, error) {
	st, ok := state.(*k8sHelmState)
	if !ok {
		return nil, errBadState
	}
	switch {
	case st.sawTable && len(st.cols) > 0:
		// Terminal's shared parse.columns keeps `helm list`'s tab-aligned header
		// intact; the old splitter merged "NAMESPACE / REVISION / UPDATED".
		heads, headRows := st.cols, st.rows
		if t := parse.Columns(st.lines); t != nil && len(t.Rows) > 0 {
			heads, headRows = t.Heads, t.Rows
		}
		cols := make([]lens.Col, len(heads))
		for i, c := range heads {
			col := lens.Col{ID: strings.ToLower(c), Head: c}
			if c == "NAME" || c == "CHART" {
				col.Grow = 1
			}
			cols[i] = col
		}
		rows := make([][]lens.Cell, 0, len(headRows))
		for _, r := range headRows {
			cells := make([]lens.Cell, len(cols))
			for i, c := range cols {
				v := ""
				if i < len(r) {
					v = r[i]
				}
				style := ""
				if c.Head == "STATUS" {
					switch strings.ToLower(v) {
					case "deployed":
						style = toneSuccess
					case "failed", "pending-install", "pending-upgrade":
						style = toneError
					}
				}
				cells[i] = lens.Cell{Spans: []lens.Span{{Text: v, Style: style}}}
			}
			rows = append(rows, cells)
		}
		return lens.Table{Cols: cols, Rows: rows}, nil
	case st.sawYAML && len(st.yaml) > 0:
		return lens.Card{
			Head: []lens.Span{{Text: "helm"}},
			Children: []lens.Node{
				lens.Code{Text: strings.Join(st.yaml, "\n"), Lang: "yaml", Clamp: 400},
			},
		}, nil
	case st.sawText && len(st.texts) > 0:
		return lens.Card{
			Head: []lens.Span{{Text: "helm"}},
			Children: []lens.Node{
				lens.Section{
					Head:     []lens.Span{{Text: "Output"}},
					Children: []lens.Node{lens.Code{Text: strings.Join(st.texts, "\n"), Lang: "text", Clamp: 300}},
				},
			},
		}, nil
	}
	return nil, nil
}
