package workspace

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func pane(id string) *Node { return NewPaneNode(model.PaneID(id)) }

func TestNewPaneNodeIsALeaf(t *testing.T) {
	node := pane("p1")
	if !node.IsPane() || node.PaneID != "p1" {
		t.Fatalf("node = %+v, want a pane p1", node)
	}
	if got, want := CollectPaneIDs(node), []model.PaneID{"p1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pane ids = %v, want %v", got, want)
	}
}

func TestSplitAtWrapsTheTargetInTheRequestedOrder(t *testing.T) {
	tests := []struct {
		side      Side
		direction Direction
		first     model.PaneID
		second    model.PaneID
	}{
		{SideRight, Vertical, "p1", "p2"},
		{SideLeft, Vertical, "p2", "p1"},
		{SideBottom, Horizontal, "p1", "p2"},
		{SideTop, Horizontal, "p2", "p1"},
	}
	for _, test := range tests {
		t.Run(string(test.side), func(t *testing.T) {
			root, err := SplitAt(pane("p1"), "p1", "p2", "s1", test.side)
			if err != nil {
				t.Fatalf("SplitAt: %v", err)
			}
			if root.IsPane() || root.Direction != test.direction {
				t.Fatalf("root = %+v, want a %s split", root, test.direction)
			}
			if root.First.PaneID != test.first || root.Second.PaneID != test.second {
				t.Fatalf("children = %s,%s want %s,%s",
					root.First.PaneID, root.Second.PaneID, test.first, test.second)
			}
			if root.Ratio != 50 {
				t.Fatalf("ratio = %v, want 50", root.Ratio)
			}
		})
	}
}

func TestSplitAtRejectsUnknownAndDuplicatePanes(t *testing.T) {
	if _, err := SplitAt(pane("p1"), "missing", "p2", "s1", SideRight); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("unknown target error = %v, want ErrPaneNotFound", err)
	}
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SplitAt(root, "p1", "p2", "s2", SideBottom); !errors.Is(err, ErrDuplicatePane) {
		t.Fatalf("duplicate pane error = %v, want ErrDuplicatePane", err)
	}
}

func TestRemovePaneCollapsesTheSiblingSubtree(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	root, err = SplitAt(root, "p1", "p3", "s2", SideBottom)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := RemovePane(root, "p1")
	if err != nil {
		t.Fatalf("RemovePane: %v", err)
	}
	if removed.IsPane() || removed.Direction != Vertical {
		t.Fatalf("root = %+v, want the surviving vertical split", removed)
	}
	if got, want := CollectPaneIDs(removed), []model.PaneID{"p3", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %v, want %v", got, want)
	}

	last, err := RemovePane(pane("p1"), "p1")
	if err != nil || last != nil {
		t.Fatalf("removing the last pane = %v, %v; want nil, nil", last, err)
	}
	if _, err := RemovePane(pane("p1"), "missing"); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("unknown pane error = %v, want ErrPaneNotFound", err)
	}
}

func TestRemovePaneDoesNotMutateTheInputTree(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RemovePane(root, "p1"); err != nil {
		t.Fatal(err)
	}
	if got, want := CollectPaneIDs(root), []model.PaneID{"p1", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input panes = %v after RemovePane, want %v", got, want)
	}
}

func TestMoveWithinPreservesTheSourcePane(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	root, err = SplitAt(root, "p2", "p3", "s2", SideBottom)
	if err != nil {
		t.Fatal(err)
	}

	moved, err := MoveWithin(root, "p3", "p1", "s3", SideLeft)
	if err != nil {
		t.Fatalf("MoveWithin: %v", err)
	}
	if got, want := CollectPaneIDs(moved), []model.PaneID{"p3", "p1", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %v, want %v", got, want)
	}
	if moved.IsPane() || moved.First.IsPane() || moved.First.First.PaneID != "p3" || moved.Direction != Vertical {
		t.Fatalf("root = %+v, want p3 to the left of p1", moved)
	}
}

func TestMoveWithinRejectsDroppingAPaneOntoItselfOrAnUnknownTarget(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MoveWithin(root, "p1", "p1", "s2", SideLeft); !errors.Is(err, ErrSamePane) {
		t.Fatalf("self drop error = %v, want ErrSamePane", err)
	}
	if _, err := MoveWithin(root, "p1", "missing", "s2", SideLeft); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("unknown target error = %v, want ErrPaneNotFound", err)
	}
	if _, err := MoveWithin(root, "missing", "p1", "s2", SideLeft); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("unknown source error = %v, want ErrPaneNotFound", err)
	}
}

func TestPathAndPaneAtPathRoundTrip(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	root, err = SplitAt(root, "p2", "p3", "s2", SideBottom)
	if err != nil {
		t.Fatal(err)
	}

	path := PathTo(root, "p3")
	if got, want := path, []int{1, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("path = %v, want %v", got, want)
	}
	node, ok := PaneAtPath(root, path)
	if !ok || node.PaneID != "p3" {
		t.Fatalf("PaneAtPath(%v) = %+v, %v; want p3", path, node, ok)
	}
	if _, ok := PaneAtPath(root, nil); ok {
		t.Fatal("a split path was reported as a pane")
	}
}

func TestFocusAfterRemoveKeepsAValidFocusAndFallsBackInTreeOrder(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	root, err = SplitAt(root, "p2", "p3", "s2", SideBottom)
	if err != nil {
		t.Fatal(err)
	}

	if got := FocusAfterRemove(root, "p2", "p1"); got != "p2" {
		t.Fatalf("focus after removing an unfocused pane = %s, want p2", got)
	}
	if got := FocusAfterRemove(root, "p2", "p2"); got != "p1" {
		t.Fatalf("focus after removing the focused pane = %s, want the first remaining pane p1", got)
	}
}

func TestNextFocusUsesGeometryAndWrapsAround(t *testing.T) {
	root, err := SplitAt(pane("left"), "left", "right", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	// left | (right above bottom)
	root, err = SplitAt(root, "right", "bottom", "s2", SideBottom)
	if err != nil {
		t.Fatal(err)
	}

	if got := NextFocus(root, "left", FocusRight); got != "right" {
		t.Errorf("right from left = %q, want right", got)
	}
	if got := NextFocus(root, "right", FocusDown); got != "bottom" {
		t.Errorf("down from right = %q, want bottom", got)
	}
	if got := NextFocus(root, "bottom", FocusUp); got != "right" {
		t.Errorf("up from bottom = %q, want right", got)
	}
	if got := NextFocus(root, "right", FocusLeft); got != "left" {
		t.Errorf("left from right = %q, want left", got)
	}
	// No pane to the left of left: wrap to the rightmost pane.
	if got := NextFocus(root, "left", FocusLeft); got != "right" && got != "bottom" {
		t.Errorf("left wrap = %q, want the rightmost pane", got)
	}
	if got := NextFocus(root, "missing", FocusRight); got != "" {
		t.Errorf("unknown pane focus = %q, want empty", got)
	}
	if got := NextFocus(pane("only"), "only", FocusLeft); got != "" {
		t.Errorf("focus across a single pane = %q, want empty", got)
	}
}

func TestNextFocusPrefersOverlappingPanes(t *testing.T) {
	// A two-by-two grid: top-left, top-right, bottom-left, bottom-right.
	root, err := SplitAt(pane("tl"), "tl", "tr", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	root, err = SplitAt(root, "tl", "bl", "s2", SideBottom)
	if err != nil {
		t.Fatal(err)
	}
	root, err = SplitAt(root, "tr", "br", "s3", SideBottom)
	if err != nil {
		t.Fatal(err)
	}

	if got := NextFocus(root, "tl", FocusRight); got != "tr" {
		t.Errorf("right from top-left = %q, want tr", got)
	}
	if got := NextFocus(root, "tl", FocusDown); got != "bl" {
		t.Errorf("down from top-left = %q, want bl", got)
	}
	if got := NextFocus(root, "br", FocusUp); got != "tr" {
		t.Errorf("up from bottom-right = %q, want tr", got)
	}
}

func TestRemapPanesRebuildsLeafIDs(t *testing.T) {
	root, err := SplitAt(pane("p1"), "p1", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	remapped := RemapPanes(root, map[model.PaneID]model.PaneID{"p1": "n1", "p2": "n2"})
	if got, want := CollectPaneIDs(remapped), []model.PaneID{"n1", "n2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %v, want %v", got, want)
	}
	if got := CollectPaneIDs(root); !reflect.DeepEqual(got, []model.PaneID{"p1", "p2"}) {
		t.Fatalf("input panes changed to %v", got)
	}
}

func TestSplitAtRejectsAnUnknownSide(t *testing.T) {
	if _, err := SplitAt(pane("p1"), "p1", "p2", "s1", Side("center")); !errors.Is(err, ErrInvalidSide) {
		t.Fatalf("unknown side error = %v, want ErrInvalidSide", err)
	}
}

func TestWithSplitRatioCopiesThePath(t *testing.T) {
	nested := NewSplitNode("nested", Horizontal, 50, pane("p2"), pane("p3"))
	root := NewSplitNode("root", Vertical, 50, pane("p1"), nested)

	updated, ok := WithSplitRatio(root, "nested", 70)
	if !ok {
		t.Fatal("WithSplitRatio did not find the split")
	}
	if root.Second.Ratio != 50 {
		t.Fatalf("input ratio changed to %v", root.Second.Ratio)
	}
	if updated.Second.Ratio != 70 {
		t.Fatalf("updated ratio = %v, want 70", updated.Second.Ratio)
	}
	if updated.First != root.First || updated.SplitID != root.SplitID {
		t.Fatal("WithSplitRatio did not preserve unaffected nodes")
	}
	if _, ok := WithSplitRatio(root, "missing", 60); ok {
		t.Fatal("WithSplitRatio found a missing split")
	}
}
