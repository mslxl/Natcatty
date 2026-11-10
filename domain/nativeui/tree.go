package nativeui

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Contract limits. A patch batch that exceeds any of them is rejected as a
// whole, exactly like an op that fails validation.
const (
	// MaxBatchBytes bounds one apply request body.
	MaxBatchBytes = 1 << 20
	// MaxBatchOps bounds the operations in one apply request.
	MaxBatchOps = 4096
	// MaxNodes bounds the live nodes of one retained tree.
	MaxNodes = 8192
	// MaxTextBytes bounds one node's text or one property string.
	MaxTextBytes = 64 << 10
	// MaxTreeDepth bounds the nesting depth of one retained tree.
	MaxTreeDepth = 64
)

// OpKind is one patch operation in a batch.
type OpKind uint8

const (
	OpCreate      OpKind = 0
	OpDestroy     OpKind = 1
	OpInsert      OpKind = 2
	OpMove        OpKind = 3
	OpSetProp     OpKind = 4
	OpSetText     OpKind = 5
	OpSetChildren OpKind = 6
)

// Op is one patch operation. Only the fields the Kind uses are meaningful.
type Op struct {
	Kind     OpKind
	Node     NodeRef
	Parent   NodeRef
	Index    uint32
	Child    NodeRef
	Children []NodeRef
	ID       NodeID
	NodeKind NodeKind
	Prop     PropID
	Value    Value
	Text     string
}

// Batch is one atomic retained-tree patch. Root selects the root node after
// every op has applied; zero keeps the current root.
type Batch struct {
	Root NodeRef
	Ops  []Op
}

// BatchError is a typed rejection. It never partially applies.
type BatchError struct {
	Op      int
	Problem string
	Cause   error
}

func (e *BatchError) Error() string {
	if e.Op < 0 {
		return "native ui: batch: " + e.Problem
	}
	return fmt.Sprintf("native ui: batch op %d: %s", e.Op, e.Problem)
}

// Unwrap preserves typed causes such as StaleHandleError.
func (e *BatchError) Unwrap() error { return e.Cause }

// ErrEmptyTree reports an operation that needs an existing tree.
var ErrEmptyTree = errors.New("native ui: the retained tree is empty")

// ErrDuplicateNode reports a create with an already-live guest id.
var ErrDuplicateNode = errors.New("native ui: node id is already live")

// ErrUnknownNode reports an operation on a node the tree does not hold.
var ErrUnknownNode = errors.New("native ui: node does not exist")

// StaleHandleError reports a node handle that was released, revoked, or never
// issued by this session's table.
type StaleHandleError struct {
	Handle uint64
}

func (e *StaleHandleError) Error() string {
	return fmt.Sprintf("native ui: node handle %#x is stale or unknown", e.Handle)
}

// Tree is one retained node tree. Nodes are keyed by their guest-allocated
// identity. Root is zero for an empty tree.
type Tree struct {
	Root     NodeID
	Nodes    map[NodeID]*Node
	Revision uint64
}

// NewTree returns an empty tree.
func NewTree() *Tree { return &Tree{Nodes: make(map[NodeID]*Node)} }

// Clone returns a deep copy that shares no mutable state with t.
func (t *Tree) Clone() *Tree {
	out := &Tree{Root: t.Root, Revision: t.Revision, Nodes: make(map[NodeID]*Node, len(t.Nodes))}
	for id, node := range t.Nodes {
		out.Nodes[id] = cloneNode(node)
	}
	return out
}

func cloneNode(n *Node) *Node {
	if n == nil {
		return nil
	}
	clone := &Node{
		ID:       n.ID,
		Kind:     n.Kind,
		Text:     n.Text,
		Parent:   n.Parent,
		Children: append([]NodeID(nil), n.Children...),
		Props:    make(map[PropID]Value, len(n.Props)),
	}
	for id, value := range n.Props {
		clone.Props[id] = value
	}
	return clone
}

// Node returns one live node.
func (t *Tree) Node(id NodeID) (*Node, bool) {
	if t == nil {
		return nil, false
	}
	node, ok := t.Nodes[id]
	return node, ok
}

// Len returns the number of live nodes.
func (t *Tree) Len() int {
	if t == nil {
		return 0
	}
	return len(t.Nodes)
}

// Resolver is the generation-tagged handle surface an Apply uses. The
// application context owns the table; the domain package never issues a
// handle itself.
type Resolver interface {
	// NodeForHandle returns the guest id behind a live handle.
	NodeForHandle(handle uint64) (NodeID, bool)
	// IssueHandle creates a handle for a newly created node.
	IssueHandle(id NodeID) (uint64, error)
	// ReleaseHandle invalidates one handle.
	ReleaseHandle(handle uint64) error
	// ReleaseNode invalidates the handle of a destroyed node. Apply calls it
	// only after a committed batch, so a rejected batch never invalidates a
	// live node. It must be idempotent and safe for a node with no handle.
	ReleaseNode(id NodeID)
}

// Created is one node created by a committed batch and its new handle.
type Created struct {
	ID     NodeID
	Handle uint64
}

// ApplyResult describes one committed batch. Destroyed lists every guest id
// removed by the batch, including descendants removed with their parent, so
// the caller can release their node handles.
type ApplyResult struct {
	Revision  uint64
	Created   []Created
	Destroyed []NodeID
}

// Apply validates and applies batch atomically. On any failure the receiver
// is unchanged and no handle is issued; a successful batch commits every
// operation and advances Revision once.
func (t *Tree) Apply(batch Batch, resolver Resolver) (ApplyResult, error) {
	next, result, err := t.Applied(batch, resolver)
	if err != nil {
		return ApplyResult{}, err
	}
	t.Root = next.Root
	t.Nodes = next.Nodes
	t.Revision = next.Revision
	return result, nil
}

// Applied validates and applies batch atomically and returns the resulting
// tree without changing the receiver. It lets a host publish each committed
// revision as an immutable snapshot: the receiver, and every tree previously
// returned by Applied, stays readable and unchanged. Handle issuance and
// release have the same side effects as Apply.
func (t *Tree) Applied(batch Batch, resolver Resolver) (*Tree, ApplyResult, error) {
	if len(batch.Ops) > MaxBatchOps {
		return nil, ApplyResult{}, &BatchError{Op: -1, Problem: fmt.Sprintf("%d operations exceed the limit of %d", len(batch.Ops), MaxBatchOps)}
	}
	working := t.Clone()

	// Ref resolution is against the working tree, so an op may reference a
	// node created earlier in the same batch by its guest id.
	resolve := func(ref NodeRef) (NodeID, error) {
		if handle, ok := ref.Handle(); ok {
			if resolver == nil {
				return 0, &BatchError{Op: -1, Problem: "node handle used without a handle table"}
			}
			id, ok := resolver.NodeForHandle(handle)
			if !ok {
				return 0, &StaleHandleError{Handle: handle}
			}
			if _, live := working.Nodes[id]; !live {
				return 0, &StaleHandleError{Handle: handle}
			}
			return id, nil
		}
		id, _ := ref.NodeID()
		if _, ok := working.Nodes[id]; !ok {
			return 0, fmt.Errorf("%w: %d", ErrUnknownNode, id)
		}
		return id, nil
	}

	createdOrder := make([]NodeID, 0, len(batch.Ops))
	destroyed := make([]NodeID, 0, len(batch.Ops))
	destroyedSet := make(map[NodeID]bool)
	for index, op := range batch.Ops {
		if err := applyOp(working, index, op, resolver, resolve, destroyedSet, &destroyed); err != nil {
			var batchErr *BatchError
			if errors.As(err, &batchErr) {
				return nil, ApplyResult{}, err
			}
			return nil, ApplyResult{}, &BatchError{Op: index, Problem: err.Error(), Cause: err}
		}
		if op.Kind == OpCreate {
			createdOrder = append(createdOrder, op.ID)
		}
	}

	rootRef := batch.Root
	if rootRef == 0 {
		// An empty batch on an empty tree has nothing to describe; a batch
		// that destroys the last node legitimately leaves an empty tree.
		if working.Root == 0 && len(batch.Ops) == 0 {
			return nil, ApplyResult{}, ErrEmptyTree
		}
	} else {
		root, err := resolve(rootRef)
		if err != nil {
			return nil, ApplyResult{}, &BatchError{Op: -1, Problem: "root: " + err.Error(), Cause: err}
		}
		working.Root = root
	}
	if err := validateTree(working); err != nil {
		return nil, ApplyResult{}, err
	}

	// Handles are issued only after the whole batch validated. If issuing
	// fails the tree stays unchanged; any handle already issued for this
	// attempt is released again.
	created := make([]Created, 0, len(createdOrder))
	for _, id := range createdOrder {
		if resolver == nil {
			return nil, ApplyResult{}, &BatchError{Op: -1, Problem: "create requires a handle table"}
		}
		handle, err := resolver.IssueHandle(id)
		if err != nil {
			for _, done := range created {
				_ = resolver.ReleaseHandle(done.Handle)
			}
			return nil, ApplyResult{}, &BatchError{Op: -1, Problem: "issue node handle: " + err.Error()}
		}
		created = append(created, Created{ID: id, Handle: handle})
	}

	working.Revision++
	if resolver != nil {
		for _, id := range destroyed {
			resolver.ReleaseNode(id)
		}
	}
	return working, ApplyResult{Revision: working.Revision, Created: created, Destroyed: destroyed}, nil
}

func applyOp(tree *Tree, index int, op Op, resolver Resolver, resolve func(NodeRef) (NodeID, error),
	destroyedSet map[NodeID]bool, destroyed *[]NodeID) error {
	fail := func(format string, args ...any) error {
		return &BatchError{Op: index, Problem: fmt.Sprintf(format, args...)}
	}
	switch op.Kind {
	case OpCreate:
		if !op.NodeKind.Valid() {
			return fail("unknown node kind %d", op.NodeKind)
		}
		if op.ID == 0 {
			return fail("node id 0 is reserved")
		}
		if _, exists := tree.Nodes[op.ID]; exists {
			return fail("node id %d is already live", op.ID)
		}
		if destroyedSet[op.ID] {
			return fail("node id %d was destroyed earlier in this batch", op.ID)
		}
		if len(tree.Nodes) >= MaxNodes {
			return fail("live nodes exceed the limit of %d", MaxNodes)
		}
		tree.Nodes[op.ID] = &Node{ID: op.ID, Kind: op.NodeKind, Props: make(map[PropID]Value)}
		return nil
	case OpDestroy:
		id, err := resolve(op.Node)
		if err != nil {
			return err
		}
		node := tree.Nodes[id]
		if node == nil {
			return fail("node %d does not exist", id)
		}
		// Destroying a parent destroys its subtree; destroying a child
		// detaches it. Reachability validation after the batch rejects a
		// leftover orphan. The removed ids are reported after commit so the
		// caller releases their handles only for a successful batch.
		if id == tree.Root {
			tree.Root = 0
		}
		for _, removed := range removeSubtree(tree, id) {
			destroyedSet[removed] = true
			*destroyed = append(*destroyed, removed)
		}
		return nil
	case OpInsert:
		parent, err := resolve(op.Parent)
		if err != nil {
			return err
		}
		child, err := resolve(op.Child)
		if err != nil {
			return err
		}
		if child == tree.Root {
			return fail("the root cannot be inserted as a child")
		}
		if tree.Nodes[child].Parent != 0 {
			return fail("node %d already has a parent; move it instead", child)
		}
		parentNode := tree.Nodes[parent]
		if int(op.Index) > len(parentNode.Children) {
			return fail("insert index %d is outside 0..%d", op.Index, len(parentNode.Children))
		}
		if parent == child || isAncestor(tree, child, parent) {
			return fail("inserting node %d under its own descendant", child)
		}
		parentNode.Children = insertAt(parentNode.Children, int(op.Index), child)
		tree.Nodes[child].Parent = parent
		return nil
	case OpMove:
		node, err := resolve(op.Node)
		if err != nil {
			return err
		}
		parent, err := resolve(op.Parent)
		if err != nil {
			return err
		}
		if node == tree.Root {
			return fail("the root cannot be moved")
		}
		if node == parent || isAncestor(tree, node, parent) {
			return fail("moving node %d under its own descendant", node)
		}
		value := tree.Nodes[node]
		if value.Parent == 0 {
			return fail("node %d has no parent to move from; insert it instead", node)
		}
		oldParent := tree.Nodes[value.Parent]
		oldParent.Children = removeValue(oldParent.Children, node)
		parentNode := tree.Nodes[parent]
		position := int(op.Index)
		if position > len(parentNode.Children) {
			position = len(parentNode.Children)
		}
		parentNode.Children = insertAt(parentNode.Children, position, node)
		value.Parent = parent
		return nil
	case OpSetProp:
		id, err := resolve(op.Node)
		if err != nil {
			return err
		}
		node := tree.Nodes[id]
		spec, ok := propSpecs[op.Prop]
		if !ok {
			return fail("unknown property %d", op.Prop)
		}
		if !kindIn(spec.kinds, node.Kind) {
			return fail("property %d is not accepted by %s", op.Prop, node.Kind)
		}
		if op.Value.Kind != spec.kind {
			return fail("property %d has type %d, want %d", op.Prop, op.Value.Kind, spec.kind)
		}
		if op.Value.Kind == PropString && len(op.Value.Text) > MaxTextBytes {
			return fail("property %d text exceeds %d bytes", op.Prop, MaxTextBytes)
		}
		if op.Value.Kind == PropString && (op.Prop == PropSrc || op.Prop == PropFontAsset) &&
			!ValidAssetID(op.Value.Text) {
			return fail("property %d is not a packaged asset id: %q", op.Prop, op.Value.Text)
		}
		if op.Value.Kind == PropInt && op.Prop == PropRole && !Role(op.Value.Int).Valid() {
			return fail("unknown accessibility role %d", op.Value.Int)
		}
		if op.Value.Kind == PropInt && (op.Prop == PropBackgroundToken || op.Prop == PropTextToken || op.Prop == PropBorderToken) &&
			!Token(op.Value.Int).Valid() {
			return fail("unknown theme token %d", op.Value.Int)
		}
		if op.Value.Kind == PropInt && op.Prop == PropLayout && !Layout(op.Value.Int).Valid() {
			return fail("unknown layout %d", op.Value.Int)
		}
		if op.Value.Kind == PropInt {
			switch op.Prop {
			case PropShape:
				if op.Value.Int < 0 || op.Value.Int > int64(CanvasImage) {
					return fail("unknown canvas shape %d", op.Value.Int)
				}
			case PropPlanStatus:
				if op.Value.Int < 0 || op.Value.Int > int64(PlanSkipped) {
					return fail("unknown plan status %d", op.Value.Int)
				}
			case PropRowCount, PropRowIndex, PropItemCount, PropItemIndex:
				if op.Value.Int < 0 {
					return fail("property %d cannot be negative", op.Prop)
				}
			case PropSortColumn:
				if op.Value.Int < -1 {
					return fail("sort column %d is below -1", op.Value.Int)
				}
			case PropTransitionMs:
				if op.Value.Int < 0 || op.Value.Int > MaxTransitionMs {
					return fail("transition duration %d is outside 0..%d ms", op.Value.Int, MaxTransitionMs)
				}
			case PropTransitionEase:
				if !Ease(op.Value.Int).Valid() {
					return fail("unknown transition easing %d", op.Value.Int)
				}
			}
		}
		if op.Value.Kind == PropFloat {
			if dataSurfaceKind(node.Kind) &&
				(math.IsNaN(op.Value.Float) || math.IsInf(op.Value.Float, 0)) {
				return fail("property %d is not a finite number", op.Prop)
			}
			switch op.Prop {
			case PropLineWidth, PropCost:
				if op.Value.Float < 0 {
					return fail("property %d cannot be negative", op.Prop)
				}
			case PropWidth, PropHeight:
				if dataSurfaceKind(node.Kind) && op.Value.Float < 0 {
					return fail("property %d cannot be negative for %s", op.Prop, node.Kind)
				}
			}
		}
		if op.Value.Kind == PropRef {
			target, err := resolve(op.Value.Ref)
			if err != nil {
				return err
			}
			op.Value.Ref = IDFromNode(target)
		}
		node.Props[op.Prop] = op.Value
		return nil
	case OpSetText:
		id, err := resolve(op.Node)
		if err != nil {
			return err
		}
		if len(op.Text) > MaxTextBytes {
			return fail("text exceeds %d bytes", MaxTextBytes)
		}
		tree.Nodes[id].Text = op.Text
		return nil
	case OpSetChildren:
		parent, err := resolve(op.Node)
		if err != nil {
			return err
		}
		node := tree.Nodes[parent]
		children := make([]NodeID, 0, len(op.Children))
		seen := make(map[NodeID]bool, len(op.Children))
		for _, ref := range op.Children {
			child, err := resolve(ref)
			if err != nil {
				return err
			}
			if child == parent {
				return fail("node %d cannot be its own child", parent)
			}
			if seen[child] {
				return fail("node %d appears twice in the children list", child)
			}
			if child == tree.Root {
				return fail("the root cannot be a child")
			}
			if isAncestor(tree, child, parent) {
				return fail("node %d would become a child of its own descendant", child)
			}
			if tree.Nodes[child].Parent != 0 && tree.Nodes[child].Parent != parent {
				return fail("node %d already has a parent", child)
			}
			seen[child] = true
			children = append(children, child)
		}
		for _, existing := range node.Children {
			if !seen[existing] {
				tree.Nodes[existing].Parent = 0
			}
		}
		for _, child := range children {
			tree.Nodes[child].Parent = parent
		}
		node.Children = children
		return nil
	default:
		return fail("unknown operation %d", op.Kind)
	}
}

func removeSubtree(tree *Tree, id NodeID) []NodeID {
	node := tree.Nodes[id]
	if node == nil {
		return nil
	}
	if node.Parent != 0 {
		if parent := tree.Nodes[node.Parent]; parent != nil {
			parent.Children = removeValue(parent.Children, id)
		}
	}
	var removed []NodeID
	var walk func(NodeID)
	walk = func(current NodeID) {
		n := tree.Nodes[current]
		if n == nil {
			return
		}
		for _, child := range append([]NodeID(nil), n.Children...) {
			walk(child)
		}
		delete(tree.Nodes, current)
		removed = append(removed, current)
	}
	walk(id)
	return removed
}

// validWebviewSrc reports whether src names a packaged HTML entry under ui/.
// The asset transport repeats the check against the package, but rejecting a
// source that cannot be an entry keeps an invalid tree from opening a page.
func validWebviewSrc(src string) bool {
	return strings.HasPrefix(src, "ui/") &&
		strings.HasSuffix(strings.ToLower(src), ".html") &&
		ValidAssetID(src)
}

func validateTree(tree *Tree) error {
	if tree.Root == 0 {
		if len(tree.Nodes) != 0 {
			return &BatchError{Op: -1, Problem: "a non-empty tree needs a root"}
		}
		return nil
	}
	root, ok := tree.Nodes[tree.Root]
	if !ok {
		return &BatchError{Op: -1, Problem: fmt.Sprintf("root node %d does not exist", tree.Root)}
	}
	if root.Parent != 0 {
		return &BatchError{Op: -1, Problem: "the root node has a parent"}
	}
	seen := make(map[NodeID]bool, len(tree.Nodes))
	depth := make(map[NodeID]int, len(tree.Nodes))
	var walk func(id NodeID, level int) error
	walk = func(id NodeID, level int) error {
		if level > MaxTreeDepth {
			return &BatchError{Op: -1, Problem: fmt.Sprintf("tree depth exceeds %d", MaxTreeDepth)}
		}
		if seen[id] {
			return &BatchError{Op: -1, Problem: fmt.Sprintf("node %d is reachable twice or forms a cycle", id)}
		}
		seen[id] = true
		depth[id] = level
		node := tree.Nodes[id]
		if node == nil {
			return &BatchError{Op: -1, Problem: fmt.Sprintf("child node %d does not exist", id)}
		}
		if node.Kind == NodeWebview {
			if len(node.Children) != 0 {
				return &BatchError{Op: -1, Problem: fmt.Sprintf("webview node %d cannot have children", id)}
			}
			if !validWebviewSrc(node.StringProp(PropSrc)) {
				return &BatchError{Op: -1, Problem: fmt.Sprintf("webview node %d requires a packaged html src under ui/", id)}
			}
		}
		for _, child := range node.Children {
			if tree.Nodes[child].Parent != id {
				return &BatchError{Op: -1, Problem: fmt.Sprintf("node %d names parent %d, want %d", child, tree.Nodes[child].Parent, id)}
			}
			if err := walk(child, level+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(tree.Root, 0); err != nil {
		return err
	}
	for id := range tree.Nodes {
		if !seen[id] {
			return &BatchError{Op: -1, Problem: fmt.Sprintf("node %d is not reachable from the root", id)}
		}
	}
	for _, node := range tree.Nodes {
		if err := validateAccessibility(node, tree); err != nil {
			return err
		}
	}
	return nil
}

// validateAccessibility enforces the host accessibility rules: an
// interactive widget must carry a label or a live label reference.
func validateAccessibility(node *Node, tree *Tree) error {
	value, hasRef := node.Prop(PropLabelledBy)
	if hasRef && value.Kind == PropRef {
		id, _ := value.Ref.NodeID()
		target, ok := tree.Nodes[id]
		if !ok {
			return &BatchError{Op: -1, Problem: fmt.Sprintf("node %d is labelled by missing node %d", node.ID, id)}
		}
		if target.Text == "" && target.StringProp(PropLabel) == "" {
			return &BatchError{Op: -1, Problem: fmt.Sprintf("node %d is labelled by node %d, which has no text", node.ID, id)}
		}
	}
	if !node.Kind.Interactive() {
		return nil
	}
	if node.StringProp(PropLabel) != "" {
		return nil
	}
	if hasRef && value.Kind == PropRef {
		return nil
	}
	return &BatchError{Op: -1, Problem: fmt.Sprintf("%s node %d requires a label or labelled_by reference", node.Kind, node.ID)}
}

// dataSurfaceKind reports whether kind is one of the host-laid-out widgets
// whose geometry comes from explicit properties rather than intrinsic text
// size: the data surfaces added in ABI 1.8.0 and the rich content added in
// ABI 1.9.0.
func dataSurfaceKind(kind NodeKind) bool {
	switch kind {
	case NodeDataGrid, NodeDataColumn, NodeDataRow, NodeGraph, NodeGraphNode,
		NodeGraphEdge, NodePlanGraph, NodePlanNode, NodeCanvas, NodeCanvasShape,
		NodeRichText, NodeTextSpan, NodeVirtualList, NodeVirtualItem, NodeWebview:
		return true
	}
	return false
}

func kindIn(kinds []NodeKind, kind NodeKind) bool {
	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func insertAt(values []NodeID, index int, value NodeID) []NodeID {
	if index >= len(values) {
		return append(values, value)
	}
	values = append(values, 0)
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}

func removeValue(values []NodeID, value NodeID) []NodeID {
	for i, candidate := range values {
		if candidate == value {
			return append(values[:i], values[i+1:]...)
		}
	}
	return values
}

func isAncestor(tree *Tree, ancestor, node NodeID) bool {
	for current := node; current != 0; {
		value := tree.Nodes[current]
		if value == nil {
			return false
		}
		if value.Parent == ancestor {
			return true
		}
		current = value.Parent
	}
	return false
}
