package nativeui

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// testResolver is a minimal generation-tagged table. It mirrors the
// application HandleTable contract without importing another layer.
type testResolver struct {
	next    uint64
	handles map[uint64]NodeID
	byNode  map[NodeID]uint64
}

func newTestResolver() *testResolver {
	return &testResolver{next: 1, handles: make(map[uint64]NodeID), byNode: make(map[NodeID]uint64)}
}

func (r *testResolver) NodeForHandle(handle uint64) (NodeID, bool) {
	id, ok := r.handles[handle]
	return id, ok
}

func (r *testResolver) IssueHandle(id NodeID) (uint64, error) {
	handle := r.next<<32 | r.next
	r.next++
	r.handles[handle] = id
	r.byNode[id] = handle
	return handle, nil
}

func (r *testResolver) ReleaseHandle(handle uint64) error {
	id, ok := r.handles[handle]
	if !ok {
		return errors.New("unknown handle")
	}
	delete(r.handles, handle)
	delete(r.byNode, id)
	return nil
}

func (r *testResolver) ReleaseNode(id NodeID) {
	if handle, ok := r.byNode[id]; ok {
		_ = r.ReleaseHandle(handle)
	}
}

// buildCounterTree returns the tree the sample plugin builds: a column with a
// label, an input, a button, and a status text.
func buildCounterTree(t *testing.T, tree *Tree, resolver *testResolver) map[string]uint64 {
	t.Helper()
	batch := Batch{
		Root: IDFromNode(1),
		Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
			{Kind: OpCreate, ID: 2, NodeKind: NodeText},
			{Kind: OpCreate, ID: 3, NodeKind: NodeInput},
			{Kind: OpCreate, ID: 4, NodeKind: NodeButton},
			{Kind: OpCreate, ID: 5, NodeKind: NodeText},
			{Kind: OpSetProp, Node: IDFromNode(1), Prop: PropLayout, Value: IntValue(int64(LayoutColumn))},
			{Kind: OpSetProp, Node: IDFromNode(1), Prop: PropGap, Value: FloatValue(8)},
			{Kind: OpSetText, Node: IDFromNode(2), Text: "Count: 0"},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropLabel, Value: StringValue("Name")},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropLabel, Value: StringValue("Increment")},
			{Kind: OpSetText, Node: IDFromNode(5), Text: "typed: "},
			{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(3), IDFromNode(4), IDFromNode(5)}},
		},
	}
	result, err := tree.Apply(batch, resolver)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	handles := make(map[string]uint64)
	for _, created := range result.Created {
		handles[NodeID(created.ID).String()] = created.Handle
	}
	return handles
}

// String exists on NodeID for readable test failures.
func (id NodeID) String() string {
	return string(rune('0'+id%10)) + "-node"
}

func TestApplyBuildsTreeAndIssuesGenerationTaggedHandles(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	handles := buildCounterTree(t, tree, resolver)

	if tree.Root != 1 || tree.Revision != 1 || tree.Len() != 5 {
		t.Fatalf("tree = root %d revision %d len %d", tree.Root, tree.Revision, tree.Len())
	}
	if len(handles) != 5 {
		t.Fatalf("handles = %v", handles)
	}
	for id, handle := range handles {
		if handle>>32 == 0 {
			t.Fatalf("handle for %s is not generation tagged: %#x", id, handle)
		}
		if resolved, ok := resolver.NodeForHandle(handle); !ok || NodeID(handle&0xffffffff) != resolved {
			t.Fatalf("handle %#x resolves to %d, want slot %d", handle, resolved, handle&0xffffffff)
		}
	}
	root := tree.Nodes[1]
	if got := root.StringProp(PropLayout); got != "" {
		t.Fatalf("layout is an int prop, got %q", got)
	}
	if got := root.IntProp(PropLayout, -1); got != int64(LayoutColumn) {
		t.Fatalf("layout = %d", got)
	}
	if got := tree.Nodes[2].Text; got != "Count: 0" {
		t.Fatalf("text = %q", got)
	}
}

func TestApplyRejectsInvalidBatchAndKeepsLastGoodTree(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	handles := buildCounterTree(t, tree, resolver)
	before := tree.Clone()

	// The last op is invalid: a text node has no selected property.
	batch := Batch{Ops: []Op{
		{Kind: OpSetText, Node: HandleFromNode(handles["2-node"]), Text: "Count: 1"},
		{Kind: OpSetProp, Node: HandleFromNode(handles["2-node"]), Prop: PropSelected, Value: IntValue(1)},
	}}
	if _, err := tree.Apply(batch, resolver); err == nil {
		t.Fatal("invalid batch was accepted")
	}
	if !reflect.DeepEqual(tree.Nodes, before.Nodes) || tree.Revision != before.Revision {
		t.Fatal("a rejected batch changed the tree")
	}
	if got := tree.Nodes[2].Text; got != "Count: 0" {
		t.Fatalf("text after rejection = %q", got)
	}
}

func TestAppliedPublishesANewSnapshotWithoutChangingTheOldOne(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	handles := buildCounterTree(t, tree, resolver)
	before := tree

	next, result, err := tree.Applied(Batch{Ops: []Op{
		{Kind: OpSetText, Node: HandleFromNode(handles["2-node"]), Text: "Count: 1"},
	}}, resolver)
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if result.Revision != 2 || next.Revision != 2 {
		t.Fatalf("revisions = result %d next %d", result.Revision, next.Revision)
	}
	if before.Nodes[2].Text != "Count: 0" || before.Revision != 1 {
		t.Fatalf("prior snapshot changed: text %q revision %d", before.Nodes[2].Text, before.Revision)
	}
	if next.Nodes[2].Text != "Count: 1" || tree.Nodes[2].Text != "Count: 0" {
		t.Fatalf("snapshots = next %q live %q", next.Nodes[2].Text, tree.Nodes[2].Text)
	}
}

func TestApplyRejectsStaleHandles(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	handles := buildCounterTree(t, tree, resolver)
	stale := handles["2-node"]
	if err := resolver.ReleaseHandle(stale); err != nil {
		t.Fatal(err)
	}
	_, err := tree.Apply(Batch{Ops: []Op{
		{Kind: OpSetText, Node: HandleFromNode(stale), Text: "changed"},
	}}, resolver)
	if err == nil {
		t.Fatal("stale handle was accepted")
	}
	var batchErr *BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error = %T, want BatchError", err)
	}
	if BatchErrorStatus(err) != StatusStaleHandle {
		t.Fatalf("status = %v, want stale-handle", BatchErrorStatus(err))
	}
}

func TestApplyRejectsOrphansAndCycles(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	buildCounterTree(t, tree, resolver)

	tests := []struct {
		name  string
		batch Batch
	}{
		{
			name: "unreachable node",
			batch: Batch{Ops: []Op{
				{Kind: OpCreate, ID: 9, NodeKind: NodeText},
			}},
		},
		{
			name: "cycle through move",
			batch: Batch{Ops: []Op{
				{Kind: OpCreate, ID: 9, NodeKind: NodeContainer},
				{Kind: OpSetChildren, Node: IDFromNode(9), Children: []NodeRef{IDFromNode(1)}},
				{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(3), IDFromNode(4), IDFromNode(5), IDFromNode(9)}},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tree.Apply(test.batch, resolver); err == nil {
				t.Fatal("invalid batch was accepted")
			}
			if tree.Revision != 1 {
				t.Fatalf("revision changed to %d", tree.Revision)
			}
		})
	}
}

func TestApplyMoveAndDestroyMaintainParentLinks(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	handles := buildCounterTree(t, tree, resolver)

	batch := Batch{Ops: []Op{
		{Kind: OpMove, Node: IDFromNode(4), Parent: IDFromNode(1), Index: 0},
	}}
	if _, err := tree.Apply(batch, resolver); err != nil {
		t.Fatalf("move: %v", err)
	}
	if got := tree.Nodes[1].Children; !reflect.DeepEqual(got, []NodeID{4, 2, 3, 5}) {
		t.Fatalf("children after move = %v", got)
	}

	batch = Batch{Ops: []Op{{Kind: OpDestroy, Node: HandleFromNode(handles["4-node"])}}}
	result, err := tree.Apply(batch, resolver)
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if !reflect.DeepEqual(result.Destroyed, []NodeID{4}) {
		t.Fatalf("destroyed = %v, want [4]", result.Destroyed)
	}
	if _, ok := tree.Nodes[4]; ok {
		t.Fatal("destroyed node is still live")
	}
	if got := tree.Nodes[1].Children; !reflect.DeepEqual(got, []NodeID{2, 3, 5}) {
		t.Fatalf("children after destroy = %v", got)
	}
}

func TestDestroyReportsTheWholeSubtreeAndForbidsSameBatchReuse(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	handles := buildCounterTree(t, tree, resolver)

	result, err := tree.Apply(Batch{Ops: []Op{
		{Kind: OpDestroy, Node: IDFromNode(1)},
		{Kind: OpCreate, ID: 2, NodeKind: NodeText},
	}}, resolver)
	if err == nil {
		t.Fatalf("re-creating a destroyed id in the same batch was accepted: %+v", result)
	}
	if tree.Revision != 1 {
		t.Fatalf("revision changed to %d", tree.Revision)
	}

	result, err = tree.Apply(Batch{Ops: []Op{{Kind: OpDestroy, Node: IDFromNode(1)}}}, resolver)
	if err != nil {
		t.Fatalf("destroy root: %v", err)
	}
	destroyed := append([]NodeID(nil), result.Destroyed...)
	sortIDs(destroyed)
	if !reflect.DeepEqual(destroyed, []NodeID{1, 2, 3, 4, 5}) {
		t.Fatalf("destroyed = %v", result.Destroyed)
	}
	if tree.Len() != 0 || tree.Root != 0 {
		t.Fatalf("tree = root %d len %d", tree.Root, tree.Len())
	}

	// After the committed destroy the id may be reused; the new node gets a
	// fresh handle and the old handle stays stale.
	result, err = tree.Apply(Batch{Root: IDFromNode(2), Ops: []Op{
		{Kind: OpCreate, ID: 2, NodeKind: NodeText},
		{Kind: OpSetText, Node: IDFromNode(2), Text: "again"},
	}}, resolver)
	if err != nil {
		t.Fatalf("reuse id after commit: %v", err)
	}
	if len(result.Created) != 1 || result.Created[0].ID != 2 {
		t.Fatalf("created = %+v", result.Created)
	}
	if _, err := tree.Apply(Batch{Ops: []Op{
		{Kind: OpSetText, Node: HandleFromNode(handles["2-node"]), Text: "stale"},
	}}, resolver); err == nil {
		t.Fatal("stale handle from the destroyed node resolved")
	}
}

// counterFixture is the sample tree and the handle table that built it.
func counterFixture(t *testing.T) (*Tree, *testResolver) {
	t.Helper()
	tree := NewTree()
	resolver := newTestResolver()
	buildCounterTree(t, tree, resolver)
	return tree, resolver
}

// resolverSnapshot captures the handle table so a rejected batch can be
// proven not to have issued or released anything.
type resolverSnapshot struct {
	next    uint64
	handles map[uint64]NodeID
}

func snapshotResolver(resolver *testResolver) resolverSnapshot {
	handles := make(map[uint64]NodeID, len(resolver.handles))
	for handle, id := range resolver.handles {
		handles[handle] = id
	}
	return resolverSnapshot{next: resolver.next, handles: handles}
}

func requireTreeUntouched(t *testing.T, tree *Tree, resolver *testResolver,
	before *Tree, handles resolverSnapshot) {
	t.Helper()
	if tree.Root != before.Root || tree.Revision != before.Revision {
		t.Fatalf("rejected batch changed root/revision: root %d revision %d, want %d/%d",
			tree.Root, tree.Revision, before.Root, before.Revision)
	}
	if !reflect.DeepEqual(tree.Nodes, before.Nodes) {
		t.Fatalf("rejected batch changed the nodes")
	}
	if resolver.next != handles.next || !reflect.DeepEqual(resolver.handles, handles.handles) {
		t.Fatalf("rejected batch changed the handle table: next %d, handles %v",
			resolver.next, resolver.handles)
	}
}

// TestApplyIsAtomicForEveryRejectionShape proves a rejected batch leaves the
// tree, revision, and handle table exactly as they were, whichever op failed:
// the first, a middle op with valid ops after it, the last, the root, or an
// insert index outside the parent.
func TestApplyIsAtomicForEveryRejectionShape(t *testing.T) {
	valid := Op{Kind: OpSetText, Node: IDFromNode(2), Text: "Count: 1"}
	invalid := Op{Kind: OpSetProp, Node: IDFromNode(2),
		Prop: PropSelected, Value: IntValue(1)}

	cases := []struct {
		name  string
		batch Batch
	}{
		{"invalid first op", Batch{Ops: []Op{invalid, valid}}},
		{"invalid middle op", Batch{Ops: []Op{valid, invalid, valid}}},
		{"invalid last op", Batch{Ops: []Op{valid, invalid}}},
		{"unknown root", Batch{Root: IDFromNode(99), Ops: []Op{valid}}},
		{"root destroyed in the same batch", Batch{Root: IDFromNode(2), Ops: []Op{
			{Kind: OpDestroy, Node: IDFromNode(2)},
		}}},
		{"out-of-range insert", Batch{Ops: []Op{
			{Kind: OpCreate, ID: 6, NodeKind: NodeText},
			{Kind: OpInsert, Parent: IDFromNode(1), Child: IDFromNode(6), Index: 5},
		}}},
		{"destroy before recreating the id", Batch{Ops: []Op{
			{Kind: OpDestroy, Node: IDFromNode(2)},
			{Kind: OpCreate, ID: 2, NodeKind: NodeText},
		}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tree, resolver := counterFixture(t)
			before := tree.Clone()
			handles := snapshotResolver(resolver)
			result, err := tree.Apply(test.batch, resolver)
			if err == nil {
				t.Fatalf("invalid batch was accepted: %+v", result)
			}
			requireTreeUntouched(t, tree, resolver, before, handles)
		})
	}
}

// TestApplyCommitsInsertAtomically proves the OpInsert path commits with the
// rest of its batch: the created node is reachable, parent links agree, one
// handle is issued, and the revision advances once.
func TestApplyCommitsInsertAtomically(t *testing.T) {
	tree, resolver := counterFixture(t)
	before := tree.Revision

	result, err := tree.Apply(Batch{Ops: []Op{
		{Kind: OpCreate, ID: 6, NodeKind: NodeText},
		{Kind: OpSetText, Node: IDFromNode(6), Text: "Inserted"},
		{Kind: OpInsert, Parent: IDFromNode(1), Child: IDFromNode(6), Index: 1},
	}}, resolver)
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if tree.Revision != before+1 || result.Revision != before+1 {
		t.Fatalf("revisions = %d/%d, want %d", tree.Revision, result.Revision, before+1)
	}
	if got := tree.Nodes[1].Children; !reflect.DeepEqual(got, []NodeID{2, 6, 3, 4, 5}) {
		t.Fatalf("children after insert = %v", got)
	}
	if tree.Nodes[6].Parent != 1 || tree.Nodes[6].Text != "Inserted" {
		t.Fatalf("inserted node = %+v", tree.Nodes[6])
	}
	if len(result.Created) != 1 || result.Created[0].ID != 6 {
		t.Fatalf("created = %+v", result.Created)
	}
	if resolved, ok := resolver.NodeForHandle(result.Created[0].Handle); !ok || resolved != 6 {
		t.Fatalf("inserted handle resolves to %d, ok = %v", resolved, ok)
	}
}

// TestApplyCreateThenDestroyInOneBatch proves a batch that creates a node and
// destroys it again commits once, leaves the tree as it was, and releases the
// transient handle; the reverse order is rejected and changes nothing.
func TestApplyCreateThenDestroyInOneBatch(t *testing.T) {
	tree, resolver := counterFixture(t)
	before := tree.Clone()
	beforeHandles := snapshotResolver(resolver)

	result, err := tree.Apply(Batch{Ops: []Op{
		{Kind: OpCreate, ID: 6, NodeKind: NodeText},
		{Kind: OpSetText, Node: IDFromNode(6), Text: "scratch"},
		{Kind: OpDestroy, Node: IDFromNode(6)},
	}}, resolver)
	if err != nil {
		t.Fatalf("create then destroy: %v", err)
	}
	if tree.Revision != before.Revision+1 || tree.Root != before.Root {
		t.Fatalf("tree after commit = root %d revision %d", tree.Root, tree.Revision)
	}
	if !reflect.DeepEqual(tree.Nodes, before.Nodes) {
		t.Fatal("create then destroy left nodes behind")
	}
	if len(result.Created) != 1 || result.Created[0].ID != 6 {
		t.Fatalf("created = %+v", result.Created)
	}
	if !reflect.DeepEqual(result.Destroyed, []NodeID{6}) {
		t.Fatalf("destroyed = %v", result.Destroyed)
	}
	if _, live := resolver.NodeForHandle(result.Created[0].Handle); live {
		t.Fatal("the transient node handle stayed live")
	}
	// The same live handles remain; only the slot counter advanced.
	if !reflect.DeepEqual(resolver.handles, beforeHandles.handles) {
		t.Fatalf("live handles = %v", resolver.handles)
	}
}

func sortIDs(ids []NodeID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func TestApplyRequiresLabelsForInteractiveNodes(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	batch := Batch{
		Root: IDFromNode(1),
		Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
			{Kind: OpCreate, ID: 2, NodeKind: NodeButton},
			{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
		},
	}
	if _, err := tree.Apply(batch, resolver); err == nil {
		t.Fatal("a button without a label was accepted")
	}

	// A label reference to a text node satisfies the rule.
	batch.Ops = append(batch.Ops,
		Op{Kind: OpCreate, ID: 3, NodeKind: NodeText},
		Op{Kind: OpSetText, Node: IDFromNode(3), Text: "Increment"},
		Op{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropLabelledBy, Value: RefValue(IDFromNode(3))},
		Op{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(3)}},
	)
	if _, err := tree.Apply(batch, resolver); err != nil {
		t.Fatalf("labelled button rejected: %v", err)
	}
}

func TestApplyRejectsUnknownPropsAndKinds(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	tests := []Batch{
		{Root: IDFromNode(1), Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeText},
			{Kind: OpSetProp, Node: IDFromNode(1), Prop: PropChecked, Value: BoolValue(true)},
		}},
		{Root: IDFromNode(1), Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeKind(99)},
		}},
		{Root: IDFromNode(1), Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeText},
			{Kind: OpSetProp, Node: IDFromNode(1), Prop: PropLabel, Value: IntValue(1)},
		}},
	}
	for i, batch := range tests {
		if _, err := tree.Apply(batch, resolver); err == nil {
			t.Fatalf("case %d was accepted", i)
		}
	}
}

func TestFocusOrderHonorsTabIndexAndDocumentOrder(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	batch := Batch{
		Root: IDFromNode(1),
		Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
			{Kind: OpCreate, ID: 2, NodeKind: NodeButton},
			{Kind: OpCreate, ID: 3, NodeKind: NodeButton},
			{Kind: OpCreate, ID: 4, NodeKind: NodeButton},
			{Kind: OpCreate, ID: 5, NodeKind: NodeText},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropLabel, Value: StringValue("two")},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropLabel, Value: StringValue("three")},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropLabel, Value: StringValue("four")},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropTabIndex, Value: IntValue(2)},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropTabIndex, Value: IntValue(1)},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropTabIndex, Value: IntValue(1)},
			{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropFocusable, Value: BoolValue(true)},
			{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropTabIndex, Value: IntValue(-1)},
			{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(3), IDFromNode(4), IDFromNode(5)}},
		},
	}
	if _, err := tree.Apply(batch, resolver); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := FocusOrder(tree), []NodeID{3, 4, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("focus order = %v, want %v", got, want)
	}
	if Focusable(tree, 5) != true {
		t.Fatal("explicitly focusable node with negative tab index is not focusable")
	}
}

func TestQueueCoalescesInPlaceAndKeepsPerNodeOrder(t *testing.T) {
	queue := NewQueue(16)
	queue.Enqueue(Event{Kind: EventInput, Node: IDFromNode(1), Text: "a"})
	queue.Enqueue(Event{Kind: EventClick, Node: IDFromNode(2)})
	queue.Enqueue(Event{Kind: EventInput, Node: IDFromNode(1), Text: "ab"})
	queue.Enqueue(Event{Kind: EventScroll, Node: IDFromNode(2), DX: 1})
	queue.Enqueue(Event{Kind: EventScroll, Node: IDFromNode(2), DX: 2})

	got := queue.Drain(0)
	if len(got) != 3 {
		t.Fatalf("drained %d events: %+v", len(got), got)
	}
	if got[0].Kind != EventInput || got[0].Text != "ab" {
		t.Fatalf("coalesced input = %+v", got[0])
	}
	if got[1].Kind != EventClick || got[2].Kind != EventScroll || got[2].DX != 2 {
		t.Fatalf("order = %+v", got)
	}
}

func TestQueueBoundsOverflow(t *testing.T) {
	queue := NewQueue(3)
	for i := 0; i < 10; i++ {
		queue.Enqueue(Event{Kind: EventClick, Node: IDFromNode(NodeID(i + 1))})
	}
	if queue.Len() != 3 || queue.Dropped() != 7 {
		t.Fatalf("len = %d dropped = %d", queue.Len(), queue.Dropped())
	}
	if got := queue.Drain(0); got[0].Node != IDFromNode(8) {
		t.Fatalf("oldest surviving node = %d, want 8", got[0].Node)
	}
}

func TestRouteKeyHostShortcutWinsUnlessNodeCaptures(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	buildCounterTree(t, tree, resolver)
	shortcuts := []Shortcut{{Mods: ModMeta, Key: KeyS, Command: "save"}}

	route := RouteKey(tree, 3, ModMeta, KeyS, shortcuts)
	if route.HostShortcut != "save" || route.Deliver {
		t.Fatalf("route = %+v", route)
	}

	tree.Nodes[3].Props[PropKeyCapture] = BoolValue(true)
	route = RouteKey(tree, 3, ModMeta, KeyS, shortcuts)
	if route.HostShortcut != "" || !route.Deliver || !route.Capture {
		t.Fatalf("capturing route = %+v", route)
	}
}

func TestWireRoundTripsBatchAndEvents(t *testing.T) {
	batch := Batch{Root: HandleFromNode(0x100000001), Ops: []Op{
		{Kind: OpCreate, ID: 7, NodeKind: NodeButton},
		{Kind: OpSetProp, Node: IDFromNode(7), Prop: PropLabel, Value: StringValue("Go")},
		{Kind: OpSetProp, Node: IDFromNode(7), Prop: PropChecked, Value: BoolValue(true)},
		{Kind: OpSetChildren, Node: HandleFromNode(0x100000001), Children: []NodeRef{IDFromNode(7)}},
		{Kind: OpSetText, Node: IDFromNode(7), Text: "hello"},
		{Kind: OpMove, Node: IDFromNode(7), Parent: HandleFromNode(0x100000001), Index: 0},
		{Kind: OpDestroy, Node: IDFromNode(7)},
	}}
	decoded, err := DecodeBatch(EncodeBatch(batch))
	if err != nil {
		t.Fatalf("DecodeBatch: %v", err)
	}
	if !reflect.DeepEqual(decoded, batch) {
		t.Fatalf("decoded = %+v\nwant %+v", decoded, batch)
	}

	events := []Event{
		{Kind: EventInput, Node: HandleFromNode(0x300000003), Time: 99, Text: "typed", Key: KeyEnter, Mods: ModShift, Button: 1, Index: 2, X: 1.5, Y: 2.5, DX: 3, DY: 4, Width: 5, Height: 6, Related: HandleFromNode(0x400000004)},
		{Kind: EventLocaleChanged, Time: 100},
	}
	decodedEvents, err := DecodeEvents(EncodeEvents(events))
	if err != nil {
		t.Fatalf("DecodeEvents: %v", err)
	}
	if !reflect.DeepEqual(decodedEvents, events) {
		t.Fatalf("decoded events = %+v\nwant %+v", decodedEvents, events)
	}
}

func TestPollAndListenWireRoundTrips(t *testing.T) {
	events := []Event{{Kind: EventClick, Node: HandleFromNode(0x100000001), Time: 7}}
	response := PollResponse{Status: StatusOK, Events: events}
	decoded, err := DecodePollResponse(EncodePollResponse(response))
	if err != nil || !reflect.DeepEqual(decoded, response) {
		t.Fatalf("poll response = %+v, %v", decoded, err)
	}

	request, err := DecodeListenRequest(EncodeListenRequest(ListenRequest{
		Node: HandleFromNode(0x100000001), Event: EventInput,
	}))
	if err != nil || request.Node != HandleFromNode(0x100000001) || request.Event != EventInput {
		t.Fatalf("listen request = %+v, %v", request, err)
	}

	handle, err := DecodeUnlistenRequest(EncodeUnlistenRequest(0x200000002))
	if err != nil || handle != 0x200000002 {
		t.Fatalf("unlisten = %#x, %v", handle, err)
	}

	max, err := DecodePollRequest(EncodePollRequest(32))
	if err != nil || max != 32 {
		t.Fatalf("poll max = %d, %v", max, err)
	}
	if _, err := DecodePollRequest(EncodePollRequest(MaxPollEvents + 1)); err == nil {
		t.Fatal("poll above the cap was accepted")
	}
}

func TestDecodeBatchRejectsOversizedPayloads(t *testing.T) {
	oversized := make([]byte, MaxBatchBytes+1)
	if _, err := DecodeBatch(oversized); err == nil {
		t.Fatal("oversized batch was accepted")
	}
	var count [4]byte
	count[0] = 1
	// A create header claiming 5000 ops, above MaxBatchOps.
	payload := append([]byte{0, 0, 0, 0, 0, 0, 0, 0}, count[:]...)
	payload[8] = 0x88
	payload[9] = 0x13
	if _, err := DecodeBatch(payload); err == nil {
		t.Fatal("batch above the op cap was accepted")
	}
}

func TestStatusRoundTripAndBatchErrorStatus(t *testing.T) {
	response := StatusResponse{
		Status:   StatusOK,
		Revision: 3,
		Created:  []Created{{ID: 1, Handle: 0x200000002}},
	}
	decoded, err := DecodeStatus(EncodeStatus(response))
	if err != nil || !reflect.DeepEqual(decoded, response) {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	err = &BatchError{Op: 0, Cause: &StaleHandleError{Handle: 1}}
	if got := BatchErrorStatus(err); got != StatusStaleHandle {
		t.Fatalf("status = %v", got)
	}
}

// buildDataSurfaceTree builds one of every data-oriented widget with the
// props the gallery View uses: a paginated grid whose row count is far above
// the rows in the patch, a graph with explicit layout and an edge, a plan tree
// with annotations, and a canvas with drawing primitives and an image.
func buildDataSurfaceTree(t *testing.T, tree *Tree, resolver *testResolver) {
	t.Helper()
	batch := Batch{
		Root: IDFromNode(1),
		Ops: []Op{
			{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
			{Kind: OpCreate, ID: 2, NodeKind: NodeDataGrid},
			{Kind: OpCreate, ID: 3, NodeKind: NodeDataColumn},
			{Kind: OpCreate, ID: 4, NodeKind: NodeDataColumn},
			{Kind: OpCreate, ID: 5, NodeKind: NodeDataRow},
			{Kind: OpCreate, ID: 6, NodeKind: NodeText},
			{Kind: OpCreate, ID: 7, NodeKind: NodeText},
			{Kind: OpCreate, ID: 8, NodeKind: NodeGraph},
			{Kind: OpCreate, ID: 9, NodeKind: NodeGraphNode},
			{Kind: OpCreate, ID: 10, NodeKind: NodeGraphNode},
			{Kind: OpCreate, ID: 11, NodeKind: NodeGraphEdge},
			{Kind: OpCreate, ID: 12, NodeKind: NodePlanGraph},
			{Kind: OpCreate, ID: 13, NodeKind: NodePlanNode},
			{Kind: OpCreate, ID: 14, NodeKind: NodePlanNode},
			{Kind: OpCreate, ID: 15, NodeKind: NodeCanvas},
			{Kind: OpCreate, ID: 16, NodeKind: NodeCanvasShape},
			{Kind: OpCreate, ID: 17, NodeKind: NodeCanvasShape},

			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropLabel, Value: StringValue("Files")},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropRowCount, Value: IntValue(250000)},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSortColumn, Value: IntValue(1)},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSortDescending, Value: BoolValue(true)},
			{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSelected, Value: IntValue(3)},

			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropLabel, Value: StringValue("Name")},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropWidth, Value: FloatValue(160)},
			{Kind: OpSetProp, Node: IDFromNode(3), Prop: PropSortable, Value: BoolValue(true)},
			{Kind: OpSetProp, Node: IDFromNode(4), Prop: PropLabel, Value: StringValue("Size")},
			{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropRowIndex, Value: IntValue(3)},
			{Kind: OpSetText, Node: IDFromNode(6), Text: "alpha.txt"},
			{Kind: OpSetText, Node: IDFromNode(7), Text: "1200"},

			{Kind: OpSetProp, Node: IDFromNode(8), Prop: PropLabel, Value: StringValue("Build graph")},
			{Kind: OpSetProp, Node: IDFromNode(8), Prop: PropLayout, Value: IntValue(int64(LayoutRow))},
			{Kind: OpSetProp, Node: IDFromNode(9), Prop: PropLabel, Value: StringValue("start")},
			{Kind: OpSetProp, Node: IDFromNode(9), Prop: PropX, Value: FloatValue(20)},
			{Kind: OpSetProp, Node: IDFromNode(9), Prop: PropY, Value: FloatValue(30)},
			{Kind: OpSetProp, Node: IDFromNode(9), Prop: PropWidth, Value: FloatValue(100)},
			{Kind: OpSetProp, Node: IDFromNode(9), Prop: PropHeight, Value: FloatValue(40)},
			{Kind: OpSetProp, Node: IDFromNode(10), Prop: PropLabel, Value: StringValue("done")},
			{Kind: OpSetProp, Node: IDFromNode(10), Prop: PropX, Value: FloatValue(180)},
			{Kind: OpSetProp, Node: IDFromNode(10), Prop: PropY, Value: FloatValue(90)},
			{Kind: OpSetProp, Node: IDFromNode(10), Prop: PropSelected, Value: IntValue(1)},
			{Kind: OpSetProp, Node: IDFromNode(11), Prop: PropFrom, Value: RefValue(IDFromNode(9))},
			{Kind: OpSetProp, Node: IDFromNode(11), Prop: PropTo, Value: RefValue(IDFromNode(10))},
			{Kind: OpSetProp, Node: IDFromNode(11), Prop: PropDirected, Value: BoolValue(true)},
			{Kind: OpSetProp, Node: IDFromNode(11), Prop: PropLineWidth, Value: FloatValue(2)},

			{Kind: OpSetProp, Node: IDFromNode(12), Prop: PropLabel, Value: StringValue("Plan")},
			{Kind: OpSetText, Node: IDFromNode(13), Text: "Seq Scan"},
			{Kind: OpSetProp, Node: IDFromNode(13), Prop: PropPlanStatus, Value: IntValue(int64(PlanRunning))},
			{Kind: OpSetProp, Node: IDFromNode(13), Prop: PropCost, Value: FloatValue(12.5)},
			{Kind: OpSetProp, Node: IDFromNode(13), Prop: PropExpanded, Value: BoolValue(true)},
			{Kind: OpSetText, Node: IDFromNode(14), Text: "Index Scan"},
			{Kind: OpSetProp, Node: IDFromNode(14), Prop: PropAnnotation, Value: StringValue("actual rows 42")},
			{Kind: OpSetProp, Node: IDFromNode(14), Prop: PropPlanStatus, Value: IntValue(int64(PlanDone))},

			{Kind: OpSetProp, Node: IDFromNode(15), Prop: PropLabel, Value: StringValue("Sketch")},
			{Kind: OpSetProp, Node: IDFromNode(15), Prop: PropWidth, Value: FloatValue(320)},
			{Kind: OpSetProp, Node: IDFromNode(15), Prop: PropHeight, Value: FloatValue(200)},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropShape, Value: IntValue(int64(CanvasRect))},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropX, Value: FloatValue(10)},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropY, Value: FloatValue(12)},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropWidth, Value: FloatValue(80)},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropHeight, Value: FloatValue(24)},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropFillToken, Value: IntValue(int64(TokenAccent))},
			{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropLineWidth, Value: FloatValue(3)},
			{Kind: OpSetProp, Node: IDFromNode(17), Prop: PropShape, Value: IntValue(int64(CanvasImage))},
			{Kind: OpSetProp, Node: IDFromNode(17), Prop: PropSrc, Value: StringValue("assets/logo.png")},
			{Kind: OpSetProp, Node: IDFromNode(17), Prop: PropAlt, Value: StringValue("logo")},
			{Kind: OpSetProp, Node: IDFromNode(17), Prop: PropWidth, Value: FloatValue(64)},
			{Kind: OpSetProp, Node: IDFromNode(17), Prop: PropHeight, Value: FloatValue(64)},

			{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2), IDFromNode(8), IDFromNode(12), IDFromNode(15)}},
			{Kind: OpSetChildren, Node: IDFromNode(2), Children: []NodeRef{IDFromNode(3), IDFromNode(4), IDFromNode(5)}},
			{Kind: OpSetChildren, Node: IDFromNode(5), Children: []NodeRef{IDFromNode(6), IDFromNode(7)}},
			{Kind: OpSetChildren, Node: IDFromNode(8), Children: []NodeRef{IDFromNode(9), IDFromNode(10), IDFromNode(11)}},
			{Kind: OpSetChildren, Node: IDFromNode(12), Children: []NodeRef{IDFromNode(13)}},
			{Kind: OpSetChildren, Node: IDFromNode(13), Children: []NodeRef{IDFromNode(14)}},
			{Kind: OpSetChildren, Node: IDFromNode(15), Children: []NodeRef{IDFromNode(16), IDFromNode(17)}},
		},
	}
	if _, err := tree.Apply(batch, resolver); err != nil {
		t.Fatalf("Apply data surface tree: %v", err)
	}
}

func TestApplyBuildsDataSurfaceNodesAndResolvesRefs(t *testing.T) {
	tree := NewTree()
	buildDataSurfaceTree(t, tree, newTestResolver())

	grid := tree.Nodes[2]
	if grid.Kind != NodeDataGrid || grid.IntProp(PropRowCount, 0) != 250000 {
		t.Fatalf("grid = %+v", grid)
	}
	if !grid.BoolProp(PropSortDescending, false) || grid.IntProp(PropSortColumn, -1) != 1 ||
		grid.IntProp(PropSelected, -1) != 3 {
		t.Fatalf("grid state = sort %d desc %v selected %d", grid.IntProp(PropSortColumn, -1),
			grid.BoolProp(PropSortDescending, false), grid.IntProp(PropSelected, -1))
	}
	if row := tree.Nodes[5]; row.Kind != NodeDataRow || row.IntProp(PropRowIndex, -1) != 3 {
		t.Fatalf("row = %+v", row)
	}
	edge := tree.Nodes[11]
	from, ok := edge.Prop(PropFrom)
	if !ok || from.Kind != PropRef {
		t.Fatalf("edge from = %+v", from)
	}
	if id, ok := from.Ref.NodeID(); !ok || id != 9 {
		t.Fatalf("edge from ref = %#x", from.Ref)
	}
	to, _ := edge.Prop(PropTo)
	if id, ok := to.Ref.NodeID(); !ok || id != 10 {
		t.Fatalf("edge to ref = %#x", to.Ref)
	}
	if !edge.BoolProp(PropDirected, false) {
		t.Fatal("edge is not directed")
	}
	plan := tree.Nodes[13]
	if plan.IntProp(PropPlanStatus, -1) != int64(PlanRunning) || plan.FloatProp(PropCost, 0) != 12.5 {
		t.Fatalf("plan node = %+v", plan)
	}
	if tree.Nodes[14].StringProp(PropAnnotation) != "actual rows 42" {
		t.Fatalf("annotation = %q", tree.Nodes[14].StringProp(PropAnnotation))
	}
	shape := tree.Nodes[16]
	if shape.IntProp(PropShape, -1) != int64(CanvasRect) || shape.IntProp(PropFillToken, -1) != int64(TokenAccent) {
		t.Fatalf("shape = %+v", shape)
	}
	if tree.Nodes[17].StringProp(PropSrc) != "assets/logo.png" {
		t.Fatalf("canvas image src = %q", tree.Nodes[17].StringProp(PropSrc))
	}
}

func TestApplyRejectsInvalidDataSurfacePropsAndKeepsTheTree(t *testing.T) {
	tree := NewTree()
	buildDataSurfaceTree(t, tree, newTestResolver())
	before := tree.Clone()

	cases := []struct {
		name string
		ops  []Op
	}{
		{"negative row count", []Op{{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropRowCount, Value: IntValue(-1)}}},
		{"negative row index", []Op{{Kind: OpSetProp, Node: IDFromNode(5), Prop: PropRowIndex, Value: IntValue(-4)}}},
		{"unknown canvas shape", []Op{{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropShape, Value: IntValue(99)}}},
		{"wrapped canvas shape", []Op{{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropShape, Value: IntValue(1 << 32)}}},
		{"unknown plan status", []Op{{Kind: OpSetProp, Node: IDFromNode(13), Prop: PropPlanStatus, Value: IntValue(99)}}},
		{"wrapped plan status", []Op{{Kind: OpSetProp, Node: IDFromNode(13), Prop: PropPlanStatus, Value: IntValue(1<<32 + 1)}}},
		{"sort column below none", []Op{{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSortColumn, Value: IntValue(-2)}}},
		{"wrong property type", []Op{{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropShape, Value: StringValue("rect")}}},
		{"property on the wrong kind", []Op{{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropX, Value: FloatValue(1)}}},
		{"unknown node kind", []Op{{Kind: OpCreate, ID: 99, NodeKind: NodeKind(99)}}},
		{"non-finite geometry", []Op{{Kind: OpSetProp, Node: IDFromNode(16), Prop: PropX, Value: FloatValue(math.Inf(1))}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tree.Apply(Batch{Root: IDFromNode(1), Ops: tc.ops}, newTestResolver()); err == nil {
				t.Fatal("invalid batch was accepted")
			}
			if tree.Revision != before.Revision || !reflect.DeepEqual(tree.Nodes, before.Nodes) {
				t.Fatal("a rejected batch changed the tree")
			}
		})
	}
}

func TestDataSurfaceKindNamesAndQueueCoalescing(t *testing.T) {
	for kind, want := range map[NodeKind]string{
		NodeDataGrid: "data_grid", NodeDataColumn: "data_column", NodeDataRow: "data_row",
		NodeGraph: "graph", NodeGraphNode: "graph_node", NodeGraphEdge: "graph_edge",
		NodePlanGraph: "plan_graph", NodePlanNode: "plan_node",
		NodeCanvas: "canvas", NodeCanvasShape: "canvas_shape",
		NodeRichText: "rich_text", NodeTextSpan: "text_span",
		NodeVirtualList: "virtual_list", NodeVirtualItem: "virtual_item",
		NodeWebview: "webview",
	} {
		if !kind.Valid() || kind.String() != want {
			t.Fatalf("kind %d = %q, valid=%v", kind, kind.String(), kind.Valid())
		}
	}
	if NodeKind(35).Valid() {
		t.Fatal("kind 35 is not declared")
	}
	if !PlanRunning.Valid() || PlanStatus(99).Valid() || !CanvasRect.Valid() || CanvasShape(99).Valid() {
		t.Fatal("new enum validity is wrong")
	}

	queue := NewQueue(16)
	queue.Enqueue(Event{Kind: EventItemRequest, Node: 1, Index: 0})
	queue.Enqueue(Event{Kind: EventItemRequest, Node: 1, Index: 40})
	if queue.Len() != 1 {
		t.Fatalf("item requests did not coalesce: %+v", queue.Drain(0))
	}
	if events := queue.Drain(0); events[0].Index != 40 {
		t.Fatalf("coalesced item request = %+v", events[0])
	}
}

func TestApplyAcceptsAnEmbeddedWebviewNode(t *testing.T) {
	tree := NewTree()
	resolver := newTestResolver()
	batch := Batch{Root: IDFromNode(1), Ops: []Op{
		{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
		{Kind: OpCreate, ID: 2, NodeKind: NodeWebview},
		{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("ui/embed.html")},
		{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropGrow, Value: FloatValue(1)},
		{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
	}}
	applied, _, err := tree.Applied(batch, resolver)
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	node := applied.Nodes[2]
	if node.Kind != NodeWebview || node.Kind.String() != "webview" {
		t.Fatalf("kind = %v (%q)", node.Kind, node.Kind)
	}
	if got := node.StringProp(PropSrc); got != "ui/embed.html" {
		t.Fatalf("src = %q", got)
	}
	if !Focusable(applied, 2) {
		t.Fatal("embedded webview is not focusable")
	}
	if !EventWebview.SessionEvent() {
		t.Fatal("webview events must be session-wide")
	}
}

func TestApplyRejectsUnsafeEmbeddedWebviewShapes(t *testing.T) {
	tests := []struct {
		name  string
		ops   []Op
		apply func(*Tree, *testResolver) error
	}{
		{
			name: "remote source",
			ops: []Op{
				{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
				{Kind: OpCreate, ID: 2, NodeKind: NodeWebview},
				{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("https://evil.example/")},
				{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
			},
		},
		{
			name: "escaping source",
			ops: []Op{
				{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
				{Kind: OpCreate, ID: 2, NodeKind: NodeWebview},
				{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("ui/../plugin.wasm")},
				{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
			},
		},
		{
			name: "non-html source",
			ops: []Op{
				{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
				{Kind: OpCreate, ID: 2, NodeKind: NodeWebview},
				{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("ui/app.js")},
				{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
			},
		},
		{
			name: "source outside ui",
			ops: []Op{
				{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
				{Kind: OpCreate, ID: 2, NodeKind: NodeWebview},
				{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("plugin.wasm")},
				{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
			},
		},
		{
			name: "children",
			ops: []Op{
				{Kind: OpCreate, ID: 1, NodeKind: NodeContainer},
				{Kind: OpCreate, ID: 2, NodeKind: NodeWebview},
				{Kind: OpSetProp, Node: IDFromNode(2), Prop: PropSrc, Value: StringValue("ui/embed.html")},
				{Kind: OpCreate, ID: 3, NodeKind: NodeText},
				{Kind: OpSetChildren, Node: IDFromNode(1), Children: []NodeRef{IDFromNode(2)}},
				{Kind: OpSetChildren, Node: IDFromNode(2), Children: []NodeRef{IDFromNode(3)}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := NewTree().Applied(Batch{Root: IDFromNode(1), Ops: test.ops}, newTestResolver())
			if err == nil {
				t.Fatal("Applied accepted an unsafe embedded webview")
			}
		})
	}
}

func TestWebviewReplyWireRoundTrips(t *testing.T) {
	reply := WebviewReply{Handle: 0x700000007, Request: 42, Status: StatusOK, Payload: `{"ok":true}`}
	decoded, err := DecodeWebviewReply(EncodeWebviewReply(reply))
	if err != nil || !reflect.DeepEqual(decoded, reply) {
		t.Fatalf("reply = %+v, %v", decoded, err)
	}
	bad := WebviewReply{Handle: 1, Request: 2, Status: StatusDenied, Payload: "no"}
	decoded, err = DecodeWebviewReply(EncodeWebviewReply(bad))
	if err != nil || decoded.Status != StatusDenied || decoded.Payload != "no" {
		t.Fatalf("denied reply = %+v, %v", decoded, err)
	}
	if _, err := DecodeWebviewReply([]byte{1, 2, 3}); err == nil {
		t.Fatal("short reply was accepted")
	}
	over := WebviewReply{Payload: string(make([]byte, MaxWebviewBridgeBytes+1))}
	if _, err := DecodeWebviewReply(EncodeWebviewReply(over)); err == nil {
		t.Fatal("oversized reply payload was accepted")
	}
}
