// Package builtin holds Terminal's built-in command lenses: git status, ls -l,
// ping and pytest. Each registers itself with the lens registry from its own
// file's init and turns a command's output into a view tree of native
// components (cards, sections, tables, trees, bars, badges, test summaries).
//
// The parsers are deliberately tolerant: a line they do not recognize is
// ignored, and a lens whose output looks nothing like its command returns a
// nil view from View, which leaves the block showing raw output.
//
// This package is pure data: it imports the lens node model and never the
// native UI, so the same trees render natively or in a web view.
package builtin

import (
	"errors"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
)

// errBadState is a programming error: the engine passed another lens's state.
var errBadState = errors.New("builtin: unexpected lens state")

// Tones a view uses through Span.Style and Badge.Tone.
const (
	toneNeutral = "neutral"
	toneSuccess = "success"
	toneWarning = "warning"
	toneError   = "error"
	toneAccent  = "accent"
	toneMuted   = "muted"
)

// toneForCode maps a git status code (A/M/D/R/C/T/U/?) to a tone.
func toneForCode(code string) string {
	switch code {
	case "A", "C":
		return toneSuccess
	case "M", "T":
		return toneWarning
	case "D", "U":
		return toneError
	case "R":
		return toneAccent
	case "?":
		return toneMuted
	}
	return toneNeutral
}

// styleForCode is the span style a status letter shows in.
func styleForCode(code string) string { return "strong " + toneForCode(code) }

// treeEntry is one path in a path tree, with the status code it carries.
type treeEntry struct {
	path string
	code string
}

// buildTree groups entries by directory, as Terminal's path tree does: one node
// per path segment, a leaf per file.
func buildTree(entries []treeEntry) lens.Tree {
	root := &dirNode{children: map[string]*dirNode{}}
	for _, e := range entries {
		parts := strings.Split(strings.Trim(strings.TrimSpace(e.path), "/"), "/")
		cur := root
		for i, p := range parts {
			if p == "" || p == "." {
				continue
			}
			c := cur.child(p)
			if i == len(parts)-1 {
				c.code = e.code
			}
			cur = c
		}
	}
	return lens.Tree{Nodes: root.nodes()}
}

type dirNode struct {
	name     string
	code     string
	children map[string]*dirNode
	order    []string
}

func (d *dirNode) child(name string) *dirNode {
	if c, ok := d.children[name]; ok {
		return c
	}
	c := &dirNode{name: name, children: map[string]*dirNode{}}
	d.children[name] = c
	d.order = append(d.order, name)
	return c
}

func (d *dirNode) nodes() []lens.TreeNode {
	out := make([]lens.TreeNode, 0, len(d.order))
	for _, name := range d.order {
		c := d.children[name]
		label := []lens.Span{{Text: name}}
		if c.code != "" {
			label = append(label, lens.Span{Text: " " + c.code, Style: styleForCode(c.code)})
		}
		n := lens.TreeNode{Label: label, Open: true, Children: c.nodes()}
		if len(c.children) > 0 {
			n.Icon = "folder"
		}
		out = append(out, n)
	}
	return out
}

// leafCount counts the files under a tree; tests and views use it.
func leafCount(nodes []lens.TreeNode) int {
	n := 0
	for _, node := range nodes {
		if len(node.Children) == 0 {
			n++
			continue
		}
		n += leafCount(node.Children)
	}
	return n
}

// findSection returns the first section of children whose head starts with
// head, for views and tests that pick a section out of a card.
func findSection(children []lens.Node, head string) (lens.Section, bool) {
	for _, n := range children {
		s, ok := n.(lens.Section)
		if !ok || len(s.Head) == 0 {
			continue
		}
		if strings.HasPrefix(s.Head[0].Text, head) {
			return s, true
		}
	}
	return lens.Section{}, false
}

var monthNames = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// monthAbbrev reports whether s is an ls month name (Jan..Dec).
func monthAbbrev(s string) bool {
	for _, m := range monthNames {
		if s == m {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseSizeBytes parses an ls size: "1536", "2.5M", "1.0K", "12B", "1.0KiB".
func parseSizeBytes(s string) (int64, bool) {
	t := strings.TrimSpace(s)
	t = strings.TrimSuffix(t, "B")
	t = strings.TrimSuffix(t, "i")
	if t == "" {
		return 0, false
	}
	mult := int64(1)
	switch t[len(t)-1] {
	case 'k', 'K':
		mult, t = 1<<10, t[:len(t)-1]
	case 'm', 'M':
		mult, t = 1<<20, t[:len(t)-1]
	case 'g', 'G':
		mult, t = 1<<30, t[:len(t)-1]
	case 't', 'T':
		mult, t = 1<<40, t[:len(t)-1]
	case 'p', 'P':
		mult, t = 1<<50, t[:len(t)-1]
	case 'e', 'E':
		mult, t = 1<<60, t[:len(t)-1]
	}
	if t == "" {
		return 0, false
	}
	if v, err := strconv.ParseInt(t, 10, 64); err == nil {
		return v * mult, true
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil && f >= 0 {
		return int64(f * float64(mult)), true
	}
	return 0, false
}

// humanBytes renders a byte count the way ls -h does.
func humanBytes(n int64) string {
	f := float64(n)
	units := [...]string{"B", "K", "M", "G", "T", "P"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + units[0]
	}
	if f < 10 {
		return strconv.FormatFloat(f, 'f', 1, 64) + units[i]
	}
	return strconv.FormatFloat(f, 'f', 0, 64) + units[i]
}

// leadingInt reads the digits at the start of s.
func leadingInt(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return -1
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return -1
	}
	return n
}

// leadingFloat reads the number at the start of s.
func leadingFloat(s string) (float64, bool) {
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(s[:i], "."), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// firstInt is the first integer anywhere in s, or 0.
func firstInt(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if n := leadingInt(s[i:]); n >= 0 {
				return n
			}
			return 0
		}
	}
	return 0
}

// parseFloats reads every number in s, in order.
func parseFloats(s string) []float64 {
	var out []float64
	for i := 0; i < len(s); {
		if s[i] >= '0' && s[i] <= '9' || s[i] == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			f, ok := leadingFloat(s[i:])
			if !ok {
				i++
				continue
			}
			out = append(out, f)
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			i = j
			continue
		}
		i++
	}
	return out
}
