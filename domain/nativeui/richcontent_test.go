package nativeui

import (
	"strings"
	"testing"
)

func TestValidAssetIDRejectsRemoteURLsAndEscapes(t *testing.T) {
	for _, id := range []string{
		"", "logo.svg", "assets/logo.svg", "img/icons/arrow.png",
		"fonts/Go-Regular.ttf", "a b/c-d_e.png",
	} {
		if !ValidAssetID(id) {
			t.Errorf("ValidAssetID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"https://example.com/logo.png",
		"http://example.com/logo.png",
		"data:image/png;base64,AAAA",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"C:logo.png",
		`assets\logo.png`,
		"/etc/passwd",
		"//example.com/logo.png",
		"../secret.png",
		"assets/../../secret.png",
		"assets//logo.png",
		"assets/./logo.png",
		"assets/logo.png?raw=1",
		"assets/logo.png#frag",
		"assets/logo\n.png",
		strings.Repeat("a", MaxAssetIDBytes+1),
	} {
		if ValidAssetID(id) {
			t.Errorf("ValidAssetID(%q) = true, want false", id)
		}
	}
}

func TestApplyAcceptsRichTextAndVirtualList(t *testing.T) {
	tree := NewTree()
	batch := Batch{
		Root: IDFromNode(1),
		Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
			{Kind: OpCreate, ID: 2, NodeKind: NodeRichText},
			{Kind: OpCreate, ID: 3, NodeKind: NodeTextSpan},
			{Kind: OpCreate, ID: 4, NodeKind: NodeTextSpan},
			{Kind: OpCreate, ID: 5, NodeKind: NodeVirtualList},
			{Kind: OpCreate, ID: 6, NodeKind: NodeVirtualItem},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropFontAsset, Value: StringValue("assets/Go-Regular.ttf")},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropTransitionMs, Value: IntValue(120)},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropTransitionEase, Value: IntValue(int64(EaseInOut))},
			{Kind: OpSetText, Node: IDFromNode(3), Text: "formatted "},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropItalic, Value: BoolValue(true)},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropUnderline, Value: BoolValue(true)},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropStrikethrough, Value: BoolValue(true)},
			{Kind: OpSetText, Node: IDFromNode(4), Text: "docs"},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropLink, Value: StringValue("https://example.com/docs")},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropFontAsset, Value: StringValue("assets/Go-Regular.ttf")},
			{Kind: OpSetChildren, Node: IDFromNode(2), Children: []NodeRef{IDFromNode(3), IDFromNode(4)}},
			{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropLabel, Value: StringValue("Huge list")},
			{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropItemCount, Value: IntValue(1_000_000)},
			{Kind: OpSetChildren, Node: IDFromNode(5), Children: []NodeRef{IDFromNode(6)}},
			{Kind: OpSetProp, Node: IDFromNode(6), Prop: PropItemIndex, Value: IntValue(42)},
			{Kind: OpSetText, Node: IDFromNode(6), Text: "row 42"},
			{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(5)}},
		},
	}
	if _, err := tree.Apply(batch, newTestResolver()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := tree.Nodes[5].IntProp(PropItemCount, -1); got != 1_000_000 {
		t.Fatalf("item count = %d", got)
	}
	if got := tree.Nodes[6].IntProp(PropItemIndex, -1); got != 42 {
		t.Fatalf("item index = %d", got)
	}
	if !tree.Nodes[3].BoolProp(PropItalic, false) {
		t.Fatal("span italic was lost")
	}
	if got := tree.Nodes[4].StringProp(PropLink); got != "https://example.com/docs" {
		t.Fatalf("span link = %q", got)
	}
}

func TestApplyRejectsInvalidRichContent(t *testing.T) {
	tree := NewTree()
	base := []Op{
		{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
		{Kind: OpCreate, ID: 2, NodeKind: NodeImage},
		{Kind: OpCreate, ID: 3, NodeKind: NodeRichText},
		{Kind: OpCreate, ID: 4, NodeKind: NodeVirtualList},
		{Kind: OpCreate, ID: 5, NodeKind: NodeTextSpan},
		{Kind: OpCreate, ID: 6, NodeKind: NodeGraphNode},
		{Kind: OpSetProp, Node: IDFromNode(1), Prop: PropLayout, Value: IntValue(int64(LayoutColumn))},
		{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(3), IDFromNode(4), IDFromNode(6)}},
		{Kind: OpSetChildren, Node: IDFromNode(3), Children: []NodeRef{IDFromNode(5)}},
		{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropLabel, Value: StringValue("Results")},
		{Kind: OpSetProp, Node: IDFromNode(6), Prop: PropLabel, Value: StringValue("Node A")},
	}
	if _, err := tree.Apply(Batch{Root: IDFromNode(1), Ops: base}, newTestResolver()); err != nil {
		t.Fatalf("base apply: %v", err)
	}
	before := tree.Clone()

	for _, tc := range []struct {
		name string
		op   Op
	}{
		{"remote image", Op{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("https://example.com/logo.png")}},
		{"escaping image", Op{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("../../secret.png")}},
		{"absolute image", Op{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("/tmp/logo.png")}},
		{"remote font", Op{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropFontAsset, Value: StringValue("https://example.com/f.ttf")}},
		{"font on image", Op{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropFontAsset, Value: StringValue("f.ttf")}},
		{"negative item count", Op{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropItemCount, Value: IntValue(-1)}},
		{"item count on span", Op{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropItemCount, Value: IntValue(1)}},
		{"unknown easing", Op{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropTransitionEase, Value: IntValue(99)}},
		{"negative duration", Op{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropTransitionMs, Value: IntValue(-1)}},
		{"duration above limit", Op{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropTransitionMs, Value: IntValue(MaxTransitionMs + 1)}},
		{"transition on graph node", Op{Kind: OpSetProp, Node: IDFromNode(6), Prop: PropTransitionMs, Value: IntValue(120)}},
		{"focusable graph node", Op{Kind: OpSetProp, Node: IDFromNode(6), Prop: PropFocusable, Value: BoolValue(true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tree.Apply(Batch{Ops: []Op{tc.op}}, newTestResolver()); err == nil {
				t.Fatal("invalid rich-content op was accepted")
			}
			if tree.Revision != before.Revision || !reflectTreesEqual(tree, before) {
				t.Fatal("a rejected batch changed the tree")
			}
		})
	}
}

// reflectTreesEqual compares the observable tree state without importing
// reflect just for one assertion.
func reflectTreesEqual(a, b *Tree) bool {
	if a.Root != b.Root || a.Revision != b.Revision || len(a.Nodes) != len(b.Nodes) {
		return false
	}
	for id, node := range a.Nodes {
		other, ok := b.Nodes[id]
		if !ok || node.Kind != other.Kind || node.Text != other.Text || node.Parent != other.Parent {
			return false
		}
		if len(node.Children) != len(other.Children) || len(node.Props) != len(other.Props) {
			return false
		}
		for i, child := range node.Children {
			if child != other.Children[i] {
				return false
			}
		}
		for prop, value := range node.Props {
			if other.Props[prop] != value {
				return false
			}
		}
	}
	return true
}

func TestGraphNodesAreNotFocusTargets(t *testing.T) {
	tree := NewTree()
	batch := Batch{
		Root: IDFromNode(1),
		Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
			{Kind: OpCreate, ID: 2, NodeKind: NodeGraph},
			{Kind: OpCreate, ID: 3, NodeKind: NodeGraphNode},
			{Kind: OpCreate, ID: 4, NodeKind: NodeButton},
			{Kind: OpSetProp, Node: IDFromNode(1), Prop: PropLayout, Value: IntValue(int64(LayoutColumn))},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropLabel, Value: StringValue("Diagram")},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropLabel, Value: StringValue("Node A")},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropLabel, Value: StringValue("Continue")},
			{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(4)}},
			{Kind: OpSetChildren, Node: IDFromNode(2), Children: []NodeRef{IDFromNode(3)}},
		},
	}
	if _, err := tree.Apply(batch, newTestResolver()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	order := FocusOrder(tree)
	for _, id := range order {
		if id == 3 {
			t.Fatalf("graph node 3 is a phantom focus target: %v", order)
		}
	}
	foundButton := false
	for _, id := range order {
		if id == 4 {
			foundButton = true
		}
	}
	if !foundButton {
		t.Fatalf("focus order %v never reaches the button", order)
	}
	if Focusable(tree, 3) {
		t.Fatal("a graph node reports focusable")
	}
}
