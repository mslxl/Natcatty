// Package workspace is the pure Workspace model: ordered Tabs, each holding a
// recursive binary split tree whose leaves are Panes bound to Sessions. It
// owns the layout rules (split, close, focus, move, sanitize) and nothing
// about transports, windows, or storage.
package workspace

import (
	"errors"
	"math"

	"github.com/mslxl/natcatty/domain/model"
)

// Direction is the orientation of a split.
type Direction string

const (
	// Horizontal splits top and bottom.
	Horizontal Direction = "horizontal"
	// Vertical splits left and right.
	Vertical Direction = "vertical"
)

// Valid reports whether d is a split direction.
func (d Direction) Valid() bool { return d == Horizontal || d == Vertical }

// Side is the slot a pane is inserted into, relative to a target pane.
type Side string

const (
	SideLeft   Side = "left"
	SideRight  Side = "right"
	SideTop    Side = "top"
	SideBottom Side = "bottom"
)

// Direction reports the split orientation a side implies.
func (s Side) Direction() Direction {
	switch s {
	case SideLeft, SideRight:
		return Vertical
	default:
		return Horizontal
	}
}

// PutsFirst reports whether a pane inserted on this side becomes the first
// child of the new split.
func (s Side) PutsFirst() bool { return s == SideLeft || s == SideTop }

// Valid reports whether s is an insertion side.
func (s Side) Valid() bool {
	switch s {
	case SideLeft, SideRight, SideTop, SideBottom:
		return true
	default:
		return false
	}
}

// NodeType distinguishes a Pane leaf from a split.
type NodeType string

const (
	// PaneNode is a leaf bound to one Pane.
	PaneNode NodeType = "pane"
	// SplitNode has exactly two children.
	SplitNode NodeType = "split"
)

// DefaultRatio is the percentage of room the first child of a new split takes.
const DefaultRatio float32 = 50

// Node is one node of a Tab's recursive split tree. A Pane leaf carries
// PaneID; a split carries SplitID, Direction, Ratio, and exactly two
// children. Nodes are treated as immutable: every operation returns a new
// tree.
type Node struct {
	Type      NodeType      `json:"type"`
	PaneID    model.PaneID  `json:"paneId,omitempty"`
	SplitID   model.SplitID `json:"id,omitempty"`
	Direction Direction     `json:"direction,omitempty"`
	// Ratio is the percentage of the split's room the first child takes,
	// between 1 and 99.
	Ratio  float32 `json:"ratio,omitempty"`
	First  *Node   `json:"first,omitempty"`
	Second *Node   `json:"second,omitempty"`
}

// NewPaneNode returns a leaf bound to pane.
func NewPaneNode(pane model.PaneID) *Node {
	return &Node{Type: PaneNode, PaneID: pane}
}

// NewSplitNode returns a split with two children.
func NewSplitNode(id model.SplitID, direction Direction, ratio float32, first, second *Node) *Node {
	if ratio <= 0 || ratio >= 100 {
		ratio = DefaultRatio
	}
	return &Node{
		Type:      SplitNode,
		SplitID:   id,
		Direction: direction,
		Ratio:     ratio,
		First:     first,
		Second:    second,
	}
}

// IsPane reports whether n is a leaf.
func (n *Node) IsPane() bool { return n != nil && n.Type == PaneNode }

// Clone deep-copies a tree.
func Clone(n *Node) *Node {
	if n == nil {
		return nil
	}
	clone := *n
	clone.First = Clone(n.First)
	clone.Second = Clone(n.Second)
	return &clone
}

// Layout errors.
var (
	// ErrPaneNotFound reports an operation whose pane is not in the tree.
	ErrPaneNotFound = errors.New("workspace: pane not found")
	// ErrDuplicatePane reports adding a pane that already exists.
	ErrDuplicatePane = errors.New("workspace: pane already exists")
	// ErrSamePane reports moving a pane onto itself.
	ErrSamePane = errors.New("workspace: a pane cannot be moved onto itself")
	// ErrInvalidSide reports an unknown insertion side.
	ErrInvalidSide = errors.New("workspace: invalid insertion side")
)

// WithSplitRatio returns a copy of root in which splitID takes ratio. It
// reports false when no split has that id. The copy shares unaffected
// subtrees, so no node of the input is changed.
func WithSplitRatio(root *Node, splitID model.SplitID, ratio float32) (*Node, bool) {
	if root == nil || root.IsPane() {
		return nil, false
	}
	if ratio <= 0 || ratio >= 100 {
		ratio = DefaultRatio
	}
	if root.SplitID == splitID {
		clone := *root
		clone.Ratio = ratio
		return &clone, true
	}
	if first, ok := WithSplitRatio(root.First, splitID, ratio); ok {
		clone := *root
		clone.First = first
		return &clone, true
	}
	if second, ok := WithSplitRatio(root.Second, splitID, ratio); ok {
		clone := *root
		clone.Second = second
		return &clone, true
	}
	return nil, false
}

// CollectPaneIDs returns every pane in tree order.
func CollectPaneIDs(n *Node) []model.PaneID {
	if n == nil {
		return nil
	}
	if n.IsPane() {
		return []model.PaneID{n.PaneID}
	}
	ids := CollectPaneIDs(n.First)
	return append(ids, CollectPaneIDs(n.Second)...)
}

// ContainsPane reports whether pane is in the tree.
func ContainsPane(n *Node, pane model.PaneID) bool {
	if n == nil {
		return false
	}
	if n.IsPane() {
		return n.PaneID == pane
	}
	return ContainsPane(n.First, pane) || ContainsPane(n.Second, pane)
}

// SplitAt inserts newPane beside target by wrapping the target leaf in a new
// split. newPane is placed on side. The target and every other pane keep
// their order.
func SplitAt(root *Node, target, newPane model.PaneID, splitID model.SplitID, side Side) (*Node, error) {
	if !side.Valid() {
		return nil, ErrInvalidSide
	}
	if ContainsPane(root, newPane) {
		return nil, ErrDuplicatePane
	}
	if !ContainsPane(root, target) {
		return nil, ErrPaneNotFound
	}
	return splitAt(root, target, newPane, splitID, side), nil
}

func splitAt(n *Node, target, newPane model.PaneID, splitID model.SplitID, side Side) *Node {
	if n.IsPane() {
		if n.PaneID != target {
			return Clone(n)
		}
		leaf := NewPaneNode(newPane)
		existing := Clone(n)
		first, second := existing, leaf
		if side.PutsFirst() {
			first, second = leaf, existing
		}
		return NewSplitNode(splitID, side.Direction(), DefaultRatio, first, second)
	}
	clone := *n
	clone.First = splitAt(n.First, target, newPane, splitID, side)
	clone.Second = splitAt(n.Second, target, newPane, splitID, side)
	return &clone
}

// RemovePane removes a leaf. Removing the only pane returns nil, which asks
// the caller to close the Tab. The sibling subtree collapses into the
// removed pane's parent.
func RemovePane(root *Node, target model.PaneID) (*Node, error) {
	pruned, found := removePane(root, target)
	if !found {
		return nil, ErrPaneNotFound
	}
	return pruned, nil
}

func removePane(n *Node, target model.PaneID) (*Node, bool) {
	if n == nil {
		return nil, false
	}
	if n.IsPane() {
		if n.PaneID == target {
			return nil, true
		}
		return Clone(n), false
	}
	first, foundFirst := removePane(n.First, target)
	second, foundSecond := removePane(n.Second, target)
	if !foundFirst && !foundSecond {
		return Clone(n), false
	}
	if first == nil {
		return second, true
	}
	if second == nil {
		return first, true
	}
	clone := *n
	clone.First, clone.Second = first, second
	return &clone, true
}

// MoveWithin moves source beside target inside one Tab. Dropping a pane onto
// itself is rejected; the source subtree is a leaf, so it cannot contain the
// target.
func MoveWithin(root *Node, source, target model.PaneID, splitID model.SplitID, side Side) (*Node, error) {
	if !side.Valid() {
		return nil, ErrInvalidSide
	}
	if source == target {
		return nil, ErrSamePane
	}
	if !ContainsPane(root, source) || !ContainsPane(root, target) {
		return nil, ErrPaneNotFound
	}
	pruned, err := RemovePane(root, source)
	if err != nil {
		return nil, err
	}
	moved, err := SplitAt(pruned, target, source, splitID, side)
	if err != nil {
		return nil, err
	}
	return moved, nil
}

// PathTo returns the child indexes from root to target, or nil when target is
// absent. A root pane has an empty path.
func PathTo(root *Node, target model.PaneID) []int {
	return pathTo(root, target, nil)
}

func pathTo(n *Node, target model.PaneID, path []int) []int {
	if n == nil {
		return nil
	}
	if n.IsPane() {
		if n.PaneID == target {
			return append([]int{}, path...)
		}
		return nil
	}
	if found := pathTo(n.First, target, appendPath(path, 0)); found != nil {
		return found
	}
	return pathTo(n.Second, target, appendPath(path, 1))
}

func appendPath(path []int, index int) []int {
	next := make([]int, len(path), len(path)+1)
	copy(next, path)
	return append(next, index)
}

// PaneAtPath resolves a focus path to a leaf.
func PaneAtPath(root *Node, path []int) (*Node, bool) {
	n := root
	for _, index := range path {
		if n == nil || n.IsPane() || (index != 0 && index != 1) {
			return nil, false
		}
		if index == 0 {
			n = n.First
		} else {
			n = n.Second
		}
	}
	if n == nil || !n.IsPane() {
		return nil, false
	}
	return n, true
}

// FocusAfterRemove chooses the next focus. The old focus survives unless it
// was removed; otherwise the first pane in tree order takes focus.
func FocusAfterRemove(root *Node, focused, removed model.PaneID) model.PaneID {
	if focused != removed && ContainsPane(root, focused) {
		return focused
	}
	ids := CollectPaneIDs(root)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// RemapPanes rebuilds the leaves through mapping. Unmapped panes keep their
// id.
func RemapPanes(n *Node, mapping map[model.PaneID]model.PaneID) *Node {
	if n == nil {
		return nil
	}
	if n.IsPane() {
		id := n.PaneID
		if mapped, ok := mapping[id]; ok {
			id = mapped
		}
		return NewPaneNode(id)
	}
	return NewSplitNode(n.SplitID, n.Direction, n.Ratio,
		RemapPanes(n.First, mapping), RemapPanes(n.Second, mapping))
}

// FocusDirection is a pane-navigation direction.
type FocusDirection string

const (
	FocusLeft  FocusDirection = "left"
	FocusRight FocusDirection = "right"
	FocusUp    FocusDirection = "up"
	FocusDown  FocusDirection = "down"
)

type paneBounds struct {
	id                  model.PaneID
	x, y, width, height float64
}

// collectBounds maps each pane to its normalized rectangle. Splits lay their
// children out by Ratio.
func collectBounds(root *Node) []paneBounds {
	return boundsOf(root, 0, 0, 1, 1)
}

func boundsOf(n *Node, x, y, width, height float64) []paneBounds {
	if n == nil {
		return nil
	}
	if n.IsPane() {
		return []paneBounds{{id: n.PaneID, x: x, y: y, width: width, height: height}}
	}
	ratio := float64(n.Ratio)
	if ratio <= 0 || ratio >= 100 {
		ratio = float64(DefaultRatio)
	}
	share := ratio / 100
	if n.Direction == Horizontal {
		return append(
			boundsOf(n.First, x, y, width, height*share),
			boundsOf(n.Second, x, y+height*share, width, height*(1-share))...,
		)
	}
	return append(
		boundsOf(n.First, x, y, width*share, height),
		boundsOf(n.Second, x+width*share, y, width*(1-share), height)...,
	)
}

const boundsEpsilon = 0.001

// NextFocus returns the pane to focus when moving in direction. It prefers
// panes that overlap the current pane on the perpendicular axis. Moving past
// an edge wraps around to the far side, matching the original. It returns ""
// when there is no other pane or the current pane is unknown.
func NextFocus(root *Node, focused model.PaneID, direction FocusDirection) model.PaneID {
	positions := collectBounds(root)
	current, ok := findBounds(positions, focused)
	if !ok {
		return ""
	}
	others := make([]paneBounds, 0, len(positions)-1)
	for _, position := range positions {
		if position.id != focused {
			others = append(others, position)
		}
	}
	if len(others) == 0 {
		return ""
	}

	var candidates []paneBounds
	switch direction {
	case FocusLeft:
		candidates = filter(others, func(p paneBounds) bool {
			return p.x+p.width <= current.x+boundsEpsilon
		})
		if len(candidates) == 0 {
			edge := maxX(others)
			candidates = filter(others, func(p paneBounds) bool { return p.x >= edge-boundsEpsilon })
		}
	case FocusRight:
		candidates = filter(others, func(p paneBounds) bool {
			return p.x >= current.x+current.width-boundsEpsilon
		})
		if len(candidates) == 0 {
			edge := minX(others)
			candidates = filter(others, func(p paneBounds) bool { return p.x <= edge+boundsEpsilon })
		}
	case FocusUp:
		candidates = filter(others, func(p paneBounds) bool {
			return p.y+p.height <= current.y+boundsEpsilon
		})
		if len(candidates) == 0 {
			edge := maxY(others)
			candidates = filter(others, func(p paneBounds) bool { return p.y >= edge-boundsEpsilon })
		}
	case FocusDown:
		candidates = filter(others, func(p paneBounds) bool {
			return p.y >= current.y+current.height-boundsEpsilon
		})
		if len(candidates) == 0 {
			edge := minY(others)
			candidates = filter(others, func(p paneBounds) bool { return p.y <= edge+boundsEpsilon })
		}
	default:
		return ""
	}

	best := ""
	bestScore := math.Inf(1)
	currentCenterX := current.x + current.width/2
	currentCenterY := current.y + current.height/2
	for _, candidate := range candidates {
		centerX := candidate.x + candidate.width/2
		centerY := candidate.y + candidate.height/2
		var score float64
		if direction == FocusLeft || direction == FocusRight {
			overlap := overlapLength(current.y, current.y+current.height, candidate.y, candidate.y+candidate.height)
			penalty := 0.0
			if overlap <= 0 {
				penalty = math.Abs(centerY-currentCenterY) * 2
			}
			score = math.Abs(centerX-currentCenterX) + penalty
		} else {
			overlap := overlapLength(current.x, current.x+current.width, candidate.x, candidate.x+candidate.width)
			penalty := 0.0
			if overlap <= 0 {
				penalty = math.Abs(centerX-currentCenterX) * 2
			}
			score = math.Abs(centerY-currentCenterY) + penalty
		}
		if score < bestScore {
			bestScore = score
			best = string(candidate.id)
		}
	}
	return model.PaneID(best)
}

func findBounds(positions []paneBounds, id model.PaneID) (paneBounds, bool) {
	for _, position := range positions {
		if position.id == id {
			return position, true
		}
	}
	return paneBounds{}, false
}

func filter(positions []paneBounds, keep func(paneBounds) bool) []paneBounds {
	var out []paneBounds
	for _, position := range positions {
		if keep(position) {
			out = append(out, position)
		}
	}
	return out
}

func overlapLength(aStart, aEnd, bStart, bEnd float64) float64 {
	start := math.Max(aStart, bStart)
	end := math.Min(aEnd, bEnd)
	return end - start
}

func minX(positions []paneBounds) float64 {
	value := math.Inf(1)
	for _, position := range positions {
		value = math.Min(value, position.x)
	}
	return value
}

func maxX(positions []paneBounds) float64 {
	value := math.Inf(-1)
	for _, position := range positions {
		value = math.Max(value, position.x)
	}
	return value
}

func minY(positions []paneBounds) float64 {
	value := math.Inf(1)
	for _, position := range positions {
		value = math.Min(value, position.y)
	}
	return value
}

func maxY(positions []paneBounds) float64 {
	value := math.Inf(-1)
	for _, position := range positions {
		value = math.Max(value, position.y)
	}
	return value
}
