package lens

import (
	"github.com/mslxl/natcatty/application/lens/lens/parse"
)

// JSONTree renders a JSON document (or a stream of documents) as a collapsible
// tree, as Terminal's shared `parse.json_tree` does, and reports false when the
// text is not JSON (Terminal: "nil when not JSON").
func JSONTree(text string) (Node, bool) {
	v, ok := parse.JSONValue(text)
	if !ok {
		return nil, false
	}
	nodes, _ := jsonNodes(v.Children, 0)
	return Tree{Nodes: nodes}, true
}

// jsonNodes converts decoded JSON values to tree nodes, stopping at a budget so
// a huge document cannot lock up the view.
func jsonNodes(vs []parse.Value, budget int) ([]TreeNode, int) {
	const maxNodes = 2000
	out := make([]TreeNode, 0, len(vs))
	for _, v := range vs {
		if budget >= maxNodes {
			out = append(out, TreeNode{Label: []Span{{Text: "…", Style: "muted"}}})
			break
		}
		budget++
		label := jsonLabel(v)
		node := TreeNode{Label: label}
		if len(v.Children) > 0 {
			children, n := jsonNodes(v.Children, budget)
			node.Children = children
			budget = n
			node.Open = true
		}
		out = append(out, node)
	}
	return out, budget
}

func jsonLabel(v parse.Value) []Span {
	if v.IsScalar {
		if v.Key == "" {
			return []Span{{Text: v.Scalar, Style: "num"}}
		}
		return []Span{{Text: v.Key, Style: "key"}, {Text: ": "}, {Text: v.Scalar, Style: "num"}}
	}
	if v.Key == "" {
		return []Span{{Text: "document", Style: "muted"}}
	}
	return []Span{{Text: v.Key, Style: "key"}}
}
