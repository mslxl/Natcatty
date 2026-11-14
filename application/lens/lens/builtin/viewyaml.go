package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
)

// yamlTree and gronTree complete view.json: Terminal's "JSON and YAML" viewer takes
// jq's JSON, yq's YAML and gron's assignments. JSON goes through the shared
// json_tree; these two turn the other shapes into the same collapsible tree
// (labels in `key`/`num` like json_tree's).
func yamlTree(text string) (lens.Node, bool) {
	v, ok := parseYAML(text)
	if !ok {
		return nil, false
	}
	return lens.Tree{Nodes: valueNodes(v)}, true
}

func gronTree(text string) (lens.Node, bool) {
	root := newYMap()
	any := false
	for _, ln := range strings.Split(text, "\n") {
		s := strings.TrimSpace(ln)
		if !strings.HasPrefix(s, "json") {
			continue
		}
		s = strings.TrimSuffix(s, ";")
		i := strings.Index(s, " = ")
		if i < 0 {
			continue
		}
		path, raw := s[len("json"):i], s[i+3:]
		segs := gronPath(path)
		if len(segs) == 0 {
			continue
		}
		setPath(root, segs, gronValue(raw))
		any = true
	}
	if !any {
		return nil, false
	}
	return lens.Tree{Nodes: valueNodes(root)}, true
}

// ymap keeps the document's key order: a tree that reorders keys would not match
// the file the reader typed.
type ymap struct {
	keys []string
	vals map[string]any
}

func newYMap() *ymap { return &ymap{vals: map[string]any{}} }

func (m *ymap) set(k string, v any) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

// --- YAML -------------------------------------------------------------------

type yline struct {
	indent int
	text   string
}

func parseYAML(text string) (any, bool) {
	var lines []yline
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(t) == "" {
			continue
		}
		body := strings.TrimLeft(t, " ")
		if strings.HasPrefix(body, "#") || body == "---" || body == "..." {
			continue
		}
		lines = append(lines, yline{indent: len(t) - len(body), text: body})
	}
	if len(lines) == 0 {
		return nil, false
	}
	v, next := parseYAMLBlock(lines, 0, lines[0].indent)
	if next == 0 {
		return nil, false
	}
	return v, true
}

func parseYAMLBlock(lines []yline, i, indent int) (any, int) {
	if i >= len(lines) || lines[i].indent < indent {
		return nil, i
	}
	if strings.HasPrefix(lines[i].text, "- ") || lines[i].text == "-" {
		return parseYAMLSeq(lines, i, indent)
	}
	return parseYAMLMap(lines, i, indent)
}

func parseYAMLSeq(lines []yline, i, indent int) (any, int) {
	out := []any{}
	for i < len(lines) && lines[i].indent == indent &&
		(strings.HasPrefix(lines[i].text, "- ") || lines[i].text == "-") {
		rest := strings.TrimSpace(strings.TrimPrefix(lines[i].text, "-"))
		i++
		if rest == "" {
			if i < len(lines) && lines[i].indent > indent {
				var v any
				v, i = parseYAMLBlock(lines, i, lines[i].indent)
				out = append(out, v)
			} else {
				out = append(out, nil)
			}
			continue
		}
		if k, v, ok := splitYAMLKey(rest); ok {
			m := newYMap()
			if v == "" {
				if i < len(lines) && lines[i].indent > indent {
					var child any
					child, i = parseYAMLBlock(lines, i, lines[i].indent)
					m.set(k, child)
				} else {
					m.set(k, nil)
				}
			} else {
				m.set(k, yamlScalar(v))
			}
			if i < len(lines) && lines[i].indent > indent {
				var more any
				more, i = parseYAMLMap(lines, i, lines[i].indent)
				if mm, ok := more.(*ymap); ok {
					for _, kk := range mm.keys {
						m.set(kk, mm.vals[kk])
					}
				}
			}
			out = append(out, m)
			continue
		}
		out = append(out, yamlScalar(rest))
	}
	return out, i
}

func parseYAMLMap(lines []yline, i, indent int) (any, int) {
	m := newYMap()
	for i < len(lines) && lines[i].indent == indent {
		k, v, ok := splitYAMLKey(lines[i].text)
		if !ok {
			return m, i
		}
		i++
		if v == "" {
			if i < len(lines) && lines[i].indent > indent {
				var child any
				child, i = parseYAMLBlock(lines, i, lines[i].indent)
				m.set(k, child)
			} else {
				m.set(k, nil)
			}
			continue
		}
		m.set(k, yamlScalar(v))
	}
	return m, i
}

// splitYAMLKey reads one "key: value" line; a bare "key:" keeps an empty value.
func splitYAMLKey(s string) (key, val string, ok bool) {
	if i := strings.Index(s, ": "); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:]), true
	}
	if strings.HasSuffix(s, ":") {
		return strings.TrimSpace(strings.TrimSuffix(s, ":")), "", true
	}
	return "", "", false
}

func yamlScalar(s string) any {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		if s[0] == '"' {
			if u, err := strconv.Unquote(s); err == nil {
				return ystr(u)
			}
		}
		return ystr(s[1 : len(s)-1])
	}
	switch s {
	case "~", "null", "Null", "NULL":
		return nil
	}
	return s
}

// ystr marks a quoted scalar so the tree can style it as a string.
type ystr string

// --- gron -------------------------------------------------------------------

func gronPath(p string) []string {
	var out []string
	for len(p) > 0 {
		switch {
		case p[0] == '.':
			p = p[1:]
			j := strings.IndexAny(p, ".[")
			if j < 0 {
				j = len(p)
			}
			out = append(out, p[:j])
			p = p[j:]
		case p[0] == '[':
			j := strings.IndexByte(p, ']')
			if j < 0 {
				return out
			}
			out = append(out, p[1:j])
			p = p[j+1:]
		default:
			return out
		}
	}
	return out
}

func gronValue(raw string) any {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ";"))
	switch raw {
	case "{}":
		return newYMap()
	case "[]":
		return []any{}
	}
	if strings.HasPrefix(raw, `"`) {
		if u, err := strconv.Unquote(raw); err == nil {
			return ystr(u)
		}
	}
	return raw
}

func setPath(root *ymap, segs []string, v any) {
	cur := root
	for i, seg := range segs {
		last := i == len(segs)-1
		if last {
			cur.set(seg, v)
			return
		}
		next, ok := cur.vals[seg]
		if !ok || next == nil {
			next = newYMap()
			cur.set(seg, next)
		}
		nm, ok := next.(*ymap)
		if !ok {
			nm = newYMap()
			cur.set(seg, nm)
		}
		cur = nm
	}
}

// --- tree -------------------------------------------------------------------

// valueNodes renders an any-tree (ymap, []any, scalar) as lens tree nodes.
func valueNodes(v any) []lens.TreeNode {
	switch t := v.(type) {
	case *ymap:
		if n, ok := numericOnly(t.keys); ok {
			// gron's arrays become a map with "0","1",... keys: label them as a
			// list, in index order.
			out := make([]lens.TreeNode, 0, n)
			for i := 0; i < n; i++ {
				out = append(out, valueNode("["+strconv.Itoa(i)+"]", t.vals[strconv.Itoa(i)]))
			}
			return out
		}
		out := make([]lens.TreeNode, 0, len(t.keys))
		for _, k := range t.keys {
			out = append(out, valueNode(k, t.vals[k]))
		}
		return out
	case []any:
		out := make([]lens.TreeNode, 0, len(t))
		for i, e := range t {
			out = append(out, valueNode("["+strconv.Itoa(i)+"]", e))
		}
		return out
	default:
		return []lens.TreeNode{{Label: scalarLabel(v)}}
	}
}

func valueNode(key string, v any) lens.TreeNode {
	switch t := v.(type) {
	case *ymap:
		return lens.TreeNode{Label: []lens.Span{{Text: key, Style: "key"}}, Children: valueNodes(t), Open: true}
	case []any:
		return lens.TreeNode{Label: []lens.Span{{Text: key, Style: "key"}}, Children: valueNodes(t), Open: true}
	}
	label := []lens.Span{{Text: key, Style: "key"}, {Text: ": "}}
	return lens.TreeNode{Label: append(label, scalarLabel(v)...)}
}

func scalarLabel(v any) []lens.Span {
	switch t := v.(type) {
	case nil:
		return []lens.Span{{Text: "null", Style: "muted"}}
	case ystr:
		return []lens.Span{{Text: string(t)}}
	case string:
		if isNumber(t) {
			return []lens.Span{{Text: t, Style: "num"}}
		}
		if t == "true" || t == "false" {
			return []lens.Span{{Text: t, Style: "muted"}}
		}
		return []lens.Span{{Text: t}}
	}
	return []lens.Span{{Text: "…", Style: "muted"}}
}

// numericOnly reports a map whose keys are exactly 0..n-1.
func numericOnly(keys []string) (int, bool) {
	n := len(keys)
	if n == 0 {
		return 0, false
	}
	seen := make([]bool, n)
	for _, k := range keys {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= n || seen[i] {
			return 0, false
		}
		seen[i] = true
	}
	return n, true
}
